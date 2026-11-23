package com.goplay.mobile

import android.content.Context
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
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.launch

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

    init {
        client.actionHandler = { spec -> actions.handle(spec) }
        client.onServerMessage = { message ->
            when (message.type) {
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

    fun updateSession(transform: (SessionState) -> SessionState) {
        _session.update(transform)
    }
}
