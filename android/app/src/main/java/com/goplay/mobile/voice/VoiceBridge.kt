package com.goplay.mobile.voice

import android.content.Context
import android.content.Intent
import android.media.AudioAttributes
import android.os.Bundle
import android.os.Handler
import android.os.Looper
import android.speech.RecognitionListener
import android.speech.RecognizerIntent
import android.speech.SpeechRecognizer
import android.speech.tts.TextToSpeech
import android.speech.tts.UtteranceProgressListener
import android.util.Log
import org.json.JSONObject
import java.util.Locale

/**
 * One immutable snapshot of the voice pipeline, published to the UI.
 * [error] is the last recognition/TTS error, kept until the next success.
 */
data class VoiceState(
    val listening: Boolean = false,
    val speaking: Boolean = false,
    val lastHeard: String = "",
    val lastSpoken: String = "",
    val error: String? = null,
)

/**
 * Continuous speech input plus spoken replies for the WebView console.
 *
 * All methods and callbacks run on the main thread: SpeechRecognizer requires a
 * main-thread client, and evaluateJavascript must also happen there.
 *
 * JS contract (the console defines these; missing functions are simply no-ops
 * because evaluateJavascript just logs the ReferenceError):
 *   window.goplayVoiceFinal("<text>")   one final utterance
 *   window.goplayVoicePartial("<text>") interim text while the user speaks
 *   window.goplayVoiceError("<text>")   recognition failed / mic denied
 *   window.goplaySpeaking(true|false)   TTS started / finished
 */
class VoiceBridge(
    private val context: Context,
    private val log: (String) -> Unit,
    private val evaluate: (String) -> Unit,
    /** Called with a fresh snapshot on every voice state change. */
    private val onState: ((VoiceState) -> Unit)? = null,
) {
    private val mainHandler = Handler(Looper.getMainLooper())

    private var recognizer: SpeechRecognizer? = null
    private var listening = false

    /** Latest snapshot surface (capture status, overlay, JS bridge all read it). */
    @Volatile
    private var current = VoiceState()

    /** Last snapshot handed to [onState], used to drop duplicate callbacks. */
    private var lastPublished: VoiceState? = null

    /** Current voice state; safe to read from any thread. */
    val state: VoiceState get() = current

    /** True between start() and stop(): the user wants an ongoing conversation. */
    private var conversation = false

    /** True while the microphone is muted because the AI is speaking. */
    private var pausedForSpeech = false
    private var restartDelayMs = BASE_RESTART_MS
    private var pendingRestart: Runnable? = null
    private var destroyed = false

    private var tts: TextToSpeech? = null
    private var ttsReady = false
    private var speaking = false

    /** Text requested before the TTS engine finished initialising. */
    private var pendingSpeech: String? = null

    /** True while continuous recognition should be running. */
    val isListening: Boolean get() = listening

    fun start(): String? {
        if (destroyed) return "语音已释放，请重新打开页面"
        if (!SpeechRecognizer.isRecognitionAvailable(context)) {
            val message = "系统没有可用的语音识别服务"
            emitError(message)
            return message
        }
        if (listening) return null
        conversation = true
        pausedForSpeech = false
        setListening(true)
        clearError()
        restartDelayMs = BASE_RESTART_MS
        startRecognizer()
        log("连续语音识别已启动")
        return null
    }

    fun stop() {
        conversation = false
        pausedForSpeech = false
        if (!listening) return
        stopListeningInternal()
        log("连续语音识别已停止")
    }

    private fun stopListeningInternal() {
        setListening(false)
        cancelPendingRestart()
        releaseRecognizer()
    }

    /**
     * Initialise TTS before the first narration. Calling speak() the first time
     * used to drop the opening line because the engine was still starting.
     */
    fun warmUp() {
        ensureTts()
    }

    fun speak(text: String): String? {
        val clean = text.trim()
        if (clean.isEmpty()) return "文本为空"
        ensureTts()
        // Half-duplex conversation: mute the microphone before the AI talks so
        // the recognizer cannot transcribe the app's own speech and loop.
        if (conversation && listening) {
            pausedForSpeech = true
            stopListeningInternal()
            log("AI 开始说话，暂停听音")
        }
        // Record what the AI is about to say before the engine plays it, so the
        // island shows the same text the user hears.
        current = current.copy(lastSpoken = clean)
        publishState()
        if (tts == null || !ttsReady) {
            // Queue instead of failing: the engine may still be initialising on
            // the first call. It will speak as soon as it is ready.
            pendingSpeech = clean
            log("TTS 尚未就绪，已排队：" + clean.take(40))
            return null
        }
        return speakNow(clean)
    }

    private fun speakNow(clean: String): String? {
        val engine = tts ?: return "TTS 不可用"
        if (!ttsReady) {
            pendingSpeech = clean
            return null
        }
        val clipped = if (clean.length > MAX_SPEAK_CHARS) {
            clean.substring(0, MAX_SPEAK_CHARS)
        } else {
            clean
        }
        val result = engine.speak(clipped, TextToSpeech.QUEUE_FLUSH, null, UTTERANCE_ID)
        if (result != TextToSpeech.SUCCESS) {
            recordError("TTS 播放失败（" + result + "）")
            resumeAfterSpeech()
            return "TTS 播放失败（" + result + "）"
        }
        return null
    }

    fun stopSpeaking() {
        pendingSpeech = null
        tts?.stop()
        markSpeaking(false)
        resumeAfterSpeech()
    }

    /**
     * Restarts listening after an utterance unless the user ended the call.
     * A denied/failed TTS also lands here, so a broken TTS never silences the call.
     */
    private fun resumeAfterSpeech() {
        if (!pausedForSpeech) return
        pausedForSpeech = false
        if (!conversation || destroyed || listening) return
        setListening(true)
        restartDelayMs = BASE_RESTART_MS
        startRecognizer()
        log("AI 说完，继续听音")
    }

    /** Stops listening (mic must not stay open in the background). TTS keeps going. */
    fun onPause() {
        stop()
    }

    fun release() {
        destroyed = true
        setListening(false)
        cancelPendingRestart()
        releaseRecognizer()
        val engine = tts
        tts = null
        ttsReady = false
        markSpeaking(false)
        try {
            engine?.stop()
            engine?.shutdown()
        } catch (e: Exception) {
            Log.w(TAG, "shutdown TTS failed: " + e.message)
        }
    }

    // ---- recognition -------------------------------------------------------

    private fun startRecognizer() {
        if (!listening || destroyed) return
        var current = recognizer
        if (current == null) {
            current = try {
                SpeechRecognizer.createSpeechRecognizer(context)
            } catch (e: Exception) {
                log("创建语音识别失败: " + e.message)
                emitError("创建语音识别失败")
                setListening(false)
                return
            }
            current.setRecognitionListener(listener)
            recognizer = current
        }
        val intent = Intent(RecognizerIntent.ACTION_RECOGNIZE_SPEECH).apply {
            putExtra(
                RecognizerIntent.EXTRA_LANGUAGE_MODEL,
                RecognizerIntent.LANGUAGE_MODEL_FREE_FORM,
            )
            putExtra(RecognizerIntent.EXTRA_PARTIAL_RESULTS, true)
            putExtra(RecognizerIntent.EXTRA_MAX_RESULTS, 3)
            putExtra(RecognizerIntent.EXTRA_LANGUAGE, Locale.getDefault().toLanguageTag())
            putExtra(RecognizerIntent.EXTRA_CALLING_PACKAGE, context.packageName)
            putExtra(RecognizerIntent.EXTRA_SPEECH_INPUT_COMPLETE_SILENCE_LENGTH_MILLIS, 1200)
            putExtra(RecognizerIntent.EXTRA_SPEECH_INPUT_POSSIBLY_COMPLETE_SILENCE_LENGTH_MILLIS, 800)
        }
        try {
            // callListen() on the same instance is supported for continuous use.
            current.startListening(intent)
        } catch (e: Exception) {
            log("启动语音识别失败: " + e.message)
            emitError("启动语音识别失败")
            scheduleRestart(restartDelayMs)
        }
    }

    private val listener = object : RecognitionListener {
        override fun onReadyForSpeech(params: Bundle?) = Unit
        override fun onBeginningOfSpeech() = Unit
        override fun onRmsChanged(rmsdB: Float) = Unit
        override fun onBufferReceived(buffer: ByteArray?) = Unit
        override fun onEndOfSpeech() = Unit

        override fun onPartialResults(partialResults: Bundle?) {
            val text = firstResult(partialResults) ?: return
            if (text.isBlank()) return
            emit("window.goplayVoicePartial(" + JSONObject.quote(text) + ")")
        }

        override fun onResults(results: Bundle?) {
            val text = firstResult(results)
            if (!text.isNullOrBlank()) {
                // Real heard text: the island may show it, the console receives it.
                current = current.copy(lastHeard = text, error = null)
                publishState()
                emit("window.goplayVoiceFinal(" + JSONObject.quote(text) + ")")
            }
            // Continuous conversation: speak again after every final utterance.
            restartDelayMs = BASE_RESTART_MS
            scheduleRestart(CONTINUE_RESTART_MS)
        }

        override fun onError(error: Int) {
            val message = describeError(error)
            val permanent = error == SpeechRecognizer.ERROR_INSUFFICIENT_PERMISSIONS ||
                error == SpeechRecognizer.ERROR_SERVER_DISCONNECTED
            log("语音识别错误 " + error + "：" + message)
            emitError(message)
            if (permanent) {
                setListening(false)
                releaseRecognizer()
                return
            }
            // Normal end-of-speech errors restart quickly; real failures back off
            // so a broken microphone or network cannot spin the recognizer forever.
            val quick = error == SpeechRecognizer.ERROR_NO_MATCH ||
                error == SpeechRecognizer.ERROR_SPEECH_TIMEOUT ||
                error == SpeechRecognizer.ERROR_CLIENT
            if (quick) {
                scheduleRestart(CONTINUE_RESTART_MS)
            } else {
                val delay = restartDelayMs
                restartDelayMs = (restartDelayMs * 2).coerceAtMost(MAX_RESTART_MS)
                scheduleRestart(delay)
            }
        }

        override fun onEvent(eventType: Int, params: Bundle?) = Unit
    }

    private fun firstResult(bundle: Bundle?): String? {
        val list = bundle?.getStringArrayList(SpeechRecognizer.RESULTS_RECOGNITION) ?: return null
        return list.firstOrNull()?.trim()
    }

    private fun scheduleRestart(delayMs: Long) {
        if (!listening || destroyed) return
        cancelPendingRestart()
        val runnable = Runnable {
            if (listening && !destroyed) startRecognizer()
        }
        pendingRestart = runnable
        mainHandler.postDelayed(runnable, delayMs)
    }

    private fun cancelPendingRestart() {
        pendingRestart?.let { mainHandler.removeCallbacks(it) }
        pendingRestart = null
    }

    private fun releaseRecognizer() {
        val current = recognizer ?: return
        recognizer = null
        try {
            current.cancel()
        } catch (_: Exception) {
            // Already gone.
        }
        try {
            current.destroy()
        } catch (_: Exception) {
            // Already gone.
        }
    }

    private fun describeError(error: Int): String = when (error) {
        SpeechRecognizer.ERROR_AUDIO -> "麦克风录音出错"
        SpeechRecognizer.ERROR_CLIENT -> "语音识别客户端出错"
        SpeechRecognizer.ERROR_INSUFFICIENT_PERMISSIONS -> "缺少录音权限，请在设置里允许麦克风"
        SpeechRecognizer.ERROR_NETWORK -> "语音识别网络错误"
        SpeechRecognizer.ERROR_NETWORK_TIMEOUT -> "语音识别网络超时"
        SpeechRecognizer.ERROR_NO_MATCH -> "没有听清，请再说一次"
        SpeechRecognizer.ERROR_RECOGNIZER_BUSY -> "语音识别服务忙"
        SpeechRecognizer.ERROR_SERVER -> "语音识别服务返回错误"
        SpeechRecognizer.ERROR_SPEECH_TIMEOUT -> "没有检测到语音"
        SpeechRecognizer.ERROR_TOO_MANY_REQUESTS -> "语音识别请求过于频繁"
        SpeechRecognizer.ERROR_SERVER_DISCONNECTED -> "语音识别服务已断开"
        else -> "语音识别失败（代码 " + error + "）"
    }

    // ---- speech output -----------------------------------------------------

    private fun ensureTts() {
        if (tts != null) return
        tts = try {
            TextToSpeech(context) { status ->
                if (status == TextToSpeech.SUCCESS) {
                    val engine = tts
                    val chosen = chooseChineseVoice(engine)
                    try {
                        engine?.setAudioAttributes(
                            AudioAttributes.Builder()
                                .setUsage(AudioAttributes.USAGE_ASSISTANT)
                                .setContentType(AudioAttributes.CONTENT_TYPE_SPEECH)
                                .build(),
                        )
                    } catch (e: Exception) {
                        Log.w(TAG, "set TTS audio attributes failed: " + e.message)
                    }
                    engine?.setOnUtteranceProgressListener(progressListener)
                    ttsReady = true
                    log("TTS 已就绪 lang=" + chosen.toLanguageTag())
                    val queued = pendingSpeech
                    pendingSpeech = null
                    if (queued != null) {
                        val error = speakNow(queued)
                        if (error != null) log("排队语音播放失败: " + error)
                    }
                } else {
                    ttsReady = false
                    log("TTS 初始化失败 status=" + status)
                    emitError("语音播报初始化失败；请检查系统 TTS 引擎")
                }
            }
        } catch (e: Exception) {
            log("创建 TTS 失败: " + e.message)
            emitError("语音播报不可用；请检查系统 TTS 引擎")
            null
        }
    }

    /**
     * Prefer a real Chinese voice. If the device only has an English engine the
     * Chinese text may be skipped entirely, which looked like the AI was mute.
     */
    private fun chooseChineseVoice(engine: TextToSpeech?): Locale {
        if (engine == null) return Locale.getDefault()
        val candidates = listOf(Locale.SIMPLIFIED_CHINESE, Locale.CHINESE, Locale.getDefault())
        for (locale in candidates) {
            try {
                val result = engine.setLanguage(locale)
                if (result >= TextToSpeech.LANG_AVAILABLE) {
                    return locale
                }
            } catch (e: Exception) {
                Log.w(TAG, "set TTS language " + locale + " failed: " + e.message)
            }
        }
        // Keep the default language set by the engine; speech may be inaccurate
        // but it is better than staying silent.
        return Locale.getDefault()
    }

    private val progressListener = object : UtteranceProgressListener() {
        override fun onStart(utteranceId: String?) = markSpeaking(true)
        override fun onDone(utteranceId: String?) {
            markSpeaking(false)
            resumeAfterSpeech()
        }

        @Deprecated("Deprecated in Java")
        override fun onError(utteranceId: String?) {
            markSpeaking(false)
            resumeAfterSpeech()
        }

        override fun onError(utteranceId: String?, errorCode: Int) {
            markSpeaking(false)
            resumeAfterSpeech()
        }

        override fun onStop(utteranceId: String?, interrupted: Boolean) {
            markSpeaking(false)
            resumeAfterSpeech()
        }
    }

    private fun markSpeaking(value: Boolean) {
        if (speaking == value) return
        speaking = value
        current = current.copy(speaking = value)
        publishState()
        emit("window.goplaySpeaking(" + value + ")")
    }

    private fun setListening(value: Boolean) {
        if (listening == value) return
        listening = value
        current = current.copy(listening = value)
        publishState()
    }

    /** Keeps the last error visible until the next successful utterance. */
    private fun recordError(message: String) {
        if (current.error == message) return
        current = current.copy(error = message)
        publishState()
    }

    private fun clearError() {
        if (current.error == null) return
        current = current.copy(error = null)
        publishState()
    }

    /** Hands a changed snapshot to [onState]; duplicates are dropped. */
    private fun publishState() {
        val snapshot = current
        if (snapshot == lastPublished) return
        lastPublished = snapshot
        val listener = onState ?: return
        if (Looper.myLooper() == Looper.getMainLooper()) {
            listener(snapshot)
        } else {
            mainHandler.post { listener(snapshot) }
        }
    }

    // ---- JS ----------------------------------------------------------------

    private fun emitError(message: String) {
        recordError(message)
        emit("window.goplayVoiceError(" + JSONObject.quote(message) + ")")
    }

    private fun emit(script: String) {
        // RecognitionListener callbacks arrive on the main thread already, but be
        // explicit: evaluateJavascript must never touch the WebView off-main.
        if (Looper.myLooper() == Looper.getMainLooper()) {
            evaluate(script)
        } else {
            mainHandler.post { evaluate(script) }
        }
    }

    companion object {
        private const val TAG = "GoPlayVoice"
        private const val UTTERANCE_ID = "goplay-reply"
        private const val MAX_SPEAK_CHARS = 600
        private const val BASE_RESTART_MS = 300L
        private const val CONTINUE_RESTART_MS = 350L
        private const val MAX_RESTART_MS = 8_000L
    }
}
