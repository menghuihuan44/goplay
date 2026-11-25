package com.goplay.mobile

import android.content.Context
import android.os.Handler
import android.os.Looper
import com.goplay.mobile.accessibility.ActionRouter
import com.goplay.mobile.accessibility.GameAccessibilityService
import com.goplay.mobile.capture.ScreenCaptureService
import com.goplay.mobile.data.DeviceInfo
import com.goplay.mobile.data.DeviceProfile
import com.goplay.mobile.data.LogBus
import com.goplay.mobile.data.Metrics
import com.goplay.mobile.data.SessionState
import com.goplay.mobile.data.SettingsStore
import com.goplay.mobile.local.LocalCore
import com.goplay.mobile.net.CoreClient
import com.goplay.mobile.net.CoreRest
import com.goplay.mobile.voice.VoiceBridge
import com.goplay.mobile.voice.VoiceState
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.launch
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.booleanOrNull
import kotlinx.serialization.json.contentOrNull
import kotlinx.serialization.json.doubleOrNull
import kotlinx.serialization.json.longOrNull

/**
 * Process-wide object graph. Created once by [GoPlayApp] and shared by the
 * activity, the capture foreground service and the accessibility service.
 */
class AppGraph(context: Context) {
    val appContext: Context = context.applicationContext

    val logs = LogBus()
    val metrics = Metrics()
    val settingsStore = SettingsStore(appContext)
    val device: DeviceProfile = DeviceInfo.read(appContext)

    /** IO scope for the WebSocket supervisor and REST calls made by the app. */
    val scope = CoroutineScope(SupervisorJob() + Dispatchers.IO)
    private val mainHandler = Handler(Looper.getMainLooper())

    val client = CoreClient(
        settingsStore = settingsStore,
        metrics = metrics,
        logs = logs,
        scope = scope,
        deviceSummary = device.summary,
    )
    val rest = CoreRest(settingsStore = settingsStore, logs = logs)
    val actions = ActionRouter(client = client, metrics = metrics, logs = logs)

    /** Bundled Go core; starts as a child process on first launch. */
    val localCore = LocalCore(appContext, settingsStore, logs)

    private val _session = MutableStateFlow(SessionState())
    val session: StateFlow<SessionState> = _session.asStateFlow()

    /** The WebView that should receive JS callbacks; null while no Activity is attached. */
    @Volatile
    var webViewEval: ((String) -> Unit)? = null

    /**
     * Process-wide voice pipeline. It used to live in MainActivity, so leaving
     * the activity for the game could let the recognizer die with it. Owning it
     * here means an active conversation survives while the game is foreground.
     */
    val voice = VoiceBridge(
        context = appContext,
        log = { message -> logs.log("Voice: " + message) },
        evaluate = { script -> webViewEval?.invoke(script) },
        onState = { state -> onVoiceState(state) },
        onUtterance = { text -> sendVoiceUtterance(text) },
    )

    init {
        client.actionHandler = { spec -> actions.handle(spec) }
        client.onServerMessage = { message ->
            when (message.type) {
                "snapshot" -> (message.instance as? JsonObject)?.let { applySnapshot(it) }

                "stats" -> message.stats?.let { stats ->
                    metrics.updateFps(stats.captureFps)
                    _session.update { it.copy(fps = stats.captureFps, instanceStatus = "running") }
                }

                "chat" -> {
                    if (!message.text.isNullOrBlank()) logs.log("AI：" + message.text)
                }

                "error" -> {
                    _session.update { it.copy(lastError = message.error) }
                }

                // The core asks the app to recover a revoked/blank projection
                // (see ScreenCaptureService.ACTION_RECOVER).
                "recover_capture" -> {
                    logs.log("核心请求恢复截屏（recover_capture）")
                    ScreenCaptureService.requestRecover(appContext)
                }

                else -> {
                    message.status?.let { status ->
                        _session.update { it.copy(instanceStatus = status) }
                    }
                }
            }
        }

        scope.launch {
            client.state.collect { state ->
                _session.update {
                    it.copy(
                        connected = state == CoreClient.State.CONNECTED,
                        wsState = state.name.lowercase(),
                    )
                }
            }
        }
        scope.launch {
            client.lastError.collect { error ->
                if (!error.isNullOrBlank()) {
                    _session.update { it.copy(lastError = error) }
                }
            }
        }
    }

    /** Re-reads cheap runtime flags after returning to the activity. */
    fun refreshRuntimeState() {
        val capture = ScreenCaptureService.state.value
        _session.update {
            it.copy(
                accessibilityReady = GameAccessibilityService.instance != null,
                running = capture.running,
                paused = capture.paused,
                lastError = capture.error ?: it.lastError,
            )
        }
    }

    /**
     * Mirrors real voice facts into the session snapshot and, when a WebView is
     * attached, into the console. Called from the main thread by VoiceBridge.
     */
    private fun onVoiceState(state: VoiceState) {
        _session.update {
            it.copy(
                aiListening = state.listening,
                aiSpeaking = state.speaking,
                aiLastHeard = state.lastHeard.ifBlank { it.aiLastHeard },
                aiLastSpoken = state.lastSpoken.ifBlank { it.aiLastSpoken },
            )
        }
        val json = org.json.JSONObject()
            .put("listening", state.listening)
            .put("speaking", state.speaking)
            .put("last_heard", state.lastHeard)
            .put("last_spoken", state.lastSpoken)
            .put("error", state.error ?: org.json.JSONObject.NULL)
            .toString()
        webViewEval?.invoke("window.goplayVoiceState && window.goplayVoiceState(" + json + ")")
    }

    /**
     * A final utterance must reach the core even when the WebView is paused
     * behind a fullscreen game. Sending over REST here (not from JS) keeps the
     * conversation alive; the reply is spoken through the same native TTS.
     */
    private fun sendVoiceUtterance(text: String) {
        val trimmed = text.trim()
        if (trimmed.isEmpty()) return
        logs.log("语音指令：" + trimmed)
        scope.launch {
            val result = rest.sendMessage(trimmed)
            logs.log("语音指令发送: " + result.display())
            val body = result.body ?: return@launch
            val reply = try {
                org.json.JSONObject(body).optString("reply", "")
            } catch (_: Exception) {
                ""
            }
            if (reply.isNotBlank()) {
                // TextToSpeech belongs to the main thread; this coroutine is IO.
                mainHandler.post { voice.speak(reply) }
            }
        }
    }

    fun updateSession(transform: (SessionState) -> SessionState) {
        _session.update(transform)
    }

    /**
     * Projects the core's periodic instance snapshot into the few facts the
     * floating panel can display. Missing fields stay empty: an unfamiliar game
     * may have no health, score, progress or outcome, and guessing one is worse
     * than showing "AI 正在观察".
     */
    private fun applySnapshot(obj: JsonObject) {
        val status = obj.str("status")
        val decision = obj.obj("decision")
        val display = obj.obj("display")
        val stats = obj.obj("stats")
        val action = decision?.obj("action")
        val kind = action?.str("kind").orEmpty()
        val target = action?.str("target").orEmpty()
        val waiting = obj.bool("waiting")
        val human = obj.bool("human_control")
        val paused = obj.bool("paused")
        val progress = (display?.get("progress") as? JsonPrimitive)?.doubleOrNull
        val outcome = display?.str("outcome")?.takeIf { it.isNotBlank() }
        val errorText = stats?.str("last_error").orEmpty()
        val aiStatus = when {
            status == "error" -> "error"
            human -> "human"
            waiting -> "waiting"
            paused -> "paused"
            status == "stopped" -> "stopped"
            status == "running" || status == "starting" -> "playing"
            else -> status.ifBlank { "unknown" }
        }
        _session.update {
            it.copy(
                instanceStatus = status.ifBlank { it.instanceStatus },
                running = status == "running" || status == "starting" || status == "paused" || status == "human_control",
                paused = paused,
                aiStatus = aiStatus,
                aiAction = listOf(kind, target).filter { p -> p.isNotBlank() }.joinToString(" "),
                aiReason = decision?.str("reason").orEmpty(),
                aiSource = decision?.str("source").orEmpty(),
                aiModel = listOf(
                    decision?.str("provider").orEmpty(),
                    decision?.str("model").orEmpty(),
                ).filter { p -> p.isNotBlank() }.joinToString(" / "),
                aiSummary = display?.str("summary").orEmpty(),
                aiOutcome = outcome,
                aiProgress = progress,
                aiProgressLabel = display?.str("progress_label").orEmpty(),
                aiDecisions = stats?.long("decisions") ?: it.aiDecisions,
                aiActions = stats?.long("actions") ?: it.aiActions,
                aiErrors = stats?.long("errors") ?: it.aiErrors,
                aiWaiting = waiting,
                aiWaitReason = obj.str("wait_reason"),
                aiAt = System.currentTimeMillis(),
                aiHuman = human,
                lastError = if (status == "error" && errorText.isNotBlank()) errorText else it.lastError,
            )
        }
    }

    private fun JsonObject.obj(key: String): JsonObject? = this[key] as? JsonObject

    private fun JsonObject.str(key: String): String =
        (this[key] as? JsonPrimitive)?.contentOrNull.orEmpty()

    private fun JsonObject.bool(key: String): Boolean =
        (this[key] as? JsonPrimitive)?.booleanOrNull ?: false

    private fun JsonObject.long(key: String): Long? =
        (this[key] as? JsonPrimitive)?.longOrNull
}
