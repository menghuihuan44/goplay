package com.goplay.mobile

import android.Manifest
import android.annotation.SuppressLint
import android.app.Activity
import android.content.ActivityNotFoundException
import android.content.Intent
import android.content.pm.ApplicationInfo
import android.content.pm.PackageManager
import android.media.projection.MediaProjectionManager
import android.net.Uri
import android.os.Build
import android.os.Bundle
import android.os.Looper
import android.provider.Settings
import android.speech.RecognizerIntent
import android.view.Gravity
import android.view.View
import android.view.ViewGroup
import android.webkit.JavascriptInterface
import android.webkit.WebChromeClient
import android.webkit.WebResourceRequest
import android.webkit.WebSettings
import android.webkit.WebView
import android.webkit.WebViewClient
import android.widget.Button
import android.widget.FrameLayout
import android.widget.LinearLayout
import android.widget.ProgressBar
import android.widget.TextView
import android.widget.Toast
import androidx.activity.OnBackPressedCallback
import androidx.activity.result.contract.ActivityResultContracts
import androidx.appcompat.app.AppCompatActivity
import androidx.core.app.NotificationManagerCompat
import androidx.core.content.ContextCompat
import androidx.lifecycle.Lifecycle
import androidx.lifecycle.lifecycleScope
import androidx.lifecycle.repeatOnLifecycle
import com.goplay.mobile.accessibility.GameAccessibilityService
import com.goplay.mobile.capture.ScreenCaptureService
import com.goplay.mobile.data.TakeoverSensitivity
import com.goplay.mobile.input.InputGate
import com.goplay.mobile.notifications.Notifications
import com.goplay.mobile.voice.VoiceBridge
import com.goplay.mobile.voice.VoiceState
import com.goplay.mobile.voice.VoiceKeepAliveService
import java.util.Locale
import java.util.concurrent.CountDownLatch
import java.util.concurrent.TimeUnit
import java.util.concurrent.atomic.AtomicReference
import kotlinx.coroutines.launch
import okhttp3.MediaType.Companion.toMediaType
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.RequestBody.Companion.toRequestBody
import org.json.JSONArray
import org.json.JSONObject

/**
 * Thin Android shell around the same five-page web console that ships with the
 * Go core. The core (libgoplaycore.so) runs as a child process on 127.0.0.1;
 * this activity only starts it, loads the console, and bridges the things a
 * WebView cannot do: installed apps, screen capture consent, overlay,
 * accessibility settings, permissions and continuous speech.
 *
 * Bridge contract (all methods return a JSON string, all UI work happens on the
 * main thread, and permission screens are only ever *opened* - nothing here
 * pretends a permission was granted):
 *
 *   statusJson()                 full snapshot, including a permissions object
 *   openPermissionCenter()       brings this activity up with focus=permissions
 *   ensureReadyForGame(id)       checks overlay/accessibility/notification/capture
 *   prepareGame(id, pkg)         activate -> permissions -> capture -> launch game
 *   startConversation()          continuous SpeechRecognizer
 *   stopConversation()           stop continuous recognition
 *   speak(text) / stopSpeaking() TextToSpeech
 *   setTakeoverSensitivity(l)    high | medium | low
 *   releaseHuman()               "I am done, give control back to the AI"
 */
class MainActivity : AppCompatActivity() {

    private lateinit var graph: AppGraph
    private lateinit var webView: WebView
    private lateinit var loading: LinearLayout
    private lateinit var loadingText: TextView
    private lateinit var loadingBar: ProgressBar
    private lateinit var retryButton: Button

    private var loadedPort = 0
    private var pendingExpandOverlay = false

    /** True once onPageFinished fired for the console, so JS callbacks are safe. */
    private var pageReady = false

    /** Console section to open (for example "permissions") once the page is ready. */
    private var pendingFocus: String? = null

    /** Guard against stacking two MediaProjection consent dialogs. */
    private var projectionRequestInFlight = false

    /** Set by EXTRA_START_CAPTURE (overlay restore button) until onResume runs. */
    private var pendingStartCapture = false

    /** Game package that waits for permissions + capture before it is launched. */
    private var pendingLaunchPackage: String? = null
    private var launchScheduled = false

    private var voice: VoiceBridge? = null
    private var pendingVoiceStart = false
    private var releaseHttp: OkHttpClient? = null

    private val projectionLauncher = registerForActivityResult(
        ActivityResultContracts.StartActivityForResult(),
    ) { result ->
        projectionRequestInFlight = false
        if (result.resultCode != Activity.RESULT_OK) {
            pendingExpandOverlay = false
            pendingLaunchPackage = null
            toast("需要录屏授权才能看到游戏画面")
            return@registerForActivityResult
        }
        val data = result.data
        if (data == null) {
            pendingExpandOverlay = false
            pendingLaunchPackage = null
            toast("录屏授权结果缺少数据，请重试")
            return@registerForActivityResult
        }
        try {
            val intent = ScreenCaptureService.startIntent(this, result.resultCode, data)
            ContextCompat.startForegroundService(this, intent)
            graph.client.ensureConnected()
            graph.logs.log("已获得录屏授权，正在启动截屏服务")
            if (pendingExpandOverlay) {
                pendingExpandOverlay = false
                webView.postDelayed({
                    runCatching {
                        startService(
                            ScreenCaptureService.commandIntent(
                                this,
                                ScreenCaptureService.ACTION_EXPAND_OVERLAY,
                            ),
                        )
                    }
                }, 1200L)
            }
            toast("已开始截屏；悬浮球可随时打开")
            schedulePendingLaunchIfReady(1200L)
        } catch (e: Exception) {
            graph.logs.log("启动截屏失败: " + (e.message ?: e.javaClass.simpleName))
            toast("启动截屏失败：" + (e.message ?: e.javaClass.simpleName))
        }
    }

    private val notificationPermissionLauncher = registerForActivityResult(
        ActivityResultContracts.RequestPermission(),
    ) { granted ->
        if (granted) {
            toast("通知权限已授予")
        } else {
            graph.logs.log("通知权限被拒绝：前台服务通知可能不可见")
            toast("通知权限被拒绝；前台服务通知可能不可见")
        }
    }

    private val micPermissionLauncher = registerForActivityResult(
        ActivityResultContracts.RequestPermission(),
    ) { granted ->
        if (!granted) {
            pendingVoiceStart = false
            val message = "麦克风权限被拒绝，连续语音不可用"
            graph.logs.log(message)
            emitJs("window.goplayVoiceError(" + JSONObject.quote(message) + ")")
            toast(message)
            return@registerForActivityResult
        }
        if (!pendingVoiceStart) return@registerForActivityResult
        pendingVoiceStart = false
        VoiceKeepAliveService.start(this)
        val error = voiceBridge().start()
        if (error != null) {
            VoiceKeepAliveService.stop(this)
            emitJs("window.goplayVoiceError(" + JSONObject.quote(error) + ")")
        } else {
            emitJs("window.goplayVoiceStarted && window.goplayVoiceStarted()")
        }
    }

    private val speechLauncher = registerForActivityResult(
        ActivityResultContracts.StartActivityForResult(),
    ) { result ->
        if (result.resultCode != Activity.RESULT_OK) return@registerForActivityResult
        val text = result.data
            ?.getStringArrayListExtra(RecognizerIntent.EXTRA_RESULTS)
            ?.firstOrNull()
            ?.trim()
            .orEmpty()
        if (text.isNotEmpty()) {
            val quoted = JSONObject.quote(text)
            emitJs("window.goplayVoice && window.goplayVoice(" + quoted + ")")
        }
    }

    @SuppressLint("SetJavaScriptEnabled")
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        graph = (application as GoPlayApp).graph
        // Restore the game the AI is allowed to act on. Empty means "unknown",
        // and the accessibility gate then still refuses GoPlay itself and IMEs.
        InputGate.setActiveGamePackage(graph.settingsStore.load().activeGamePackage)
        Notifications.ensureChannel(this)
        buildContentView()
        configureWebView()
        observeCore()
        graph.localCore.start(lifecycleScope)
        // Initialise TTS now, not on the first narration: the opening line was
        // previously dropped because the engine was still initialising.
        voiceBridge().warmUp()
        maybeRequestNotificationPermission()
        handleIntentExtras(intent)
        onBackPressedDispatcher.addCallback(this, object : OnBackPressedCallback(true) {
            override fun handleOnBackPressed() {
                if (webView.canGoBack()) webView.goBack() else moveTaskToBack(true)
            }
        })
    }

    override fun onNewIntent(intent: Intent) {
        super.onNewIntent(intent)
        setIntent(intent)
        handleIntentExtras(intent)
    }

    override fun onResume() {
        super.onResume()
        if (pendingStartCapture) {
            pendingStartCapture = false
            if (!ScreenCaptureService.state.value.running) {
                graph.logs.log("收到恢复截屏请求，重新申请录屏授权")
                beginCapture()
            }
        }
        // Returning from the permission screens: if the user granted everything
        // we asked for, continue the game launch that prepareGame() deferred.
        schedulePendingLaunchIfReady(400L)
    }

    override fun onPause() {
        super.onPause()
        // Do NOT stop the recognizer here. Switching to the game is exactly the
        // moment the user wants to keep talking to the AI. VoiceKeepAliveService
        // keeps the process in the foreground with the microphone type so the
        // recognizer is allowed to continue in the background.
    }

    override fun onDestroy() {
        VoiceKeepAliveService.stop(this)
        voice?.release()
        voice = null
        pendingLaunchPackage = null
        releaseHttp?.dispatcher?.executorService?.shutdown()
        releaseHttp?.connectionPool?.evictAll()
        releaseHttp = null
        super.onDestroy()
    }

    private fun handleIntentExtras(intent: Intent?) {
        if (intent == null) return
        val focus = intent.getStringExtra(EXTRA_FOCUS)
        if (!focus.isNullOrBlank()) {
            intent.removeExtra(EXTRA_FOCUS)
            dispatchFocus(focus)
        }
        if (intent.getBooleanExtra(EXTRA_START_CAPTURE, false)) {
            intent.removeExtra(EXTRA_START_CAPTURE)
            pendingStartCapture = true
            if (lifecycle.currentState.isAtLeast(Lifecycle.State.RESUMED) &&
                !ScreenCaptureService.state.value.running
            ) {
                pendingStartCapture = false
                beginCapture()
            }
        }
    }

    private fun buildContentView() {
        val root = FrameLayout(this).apply {
            setBackgroundColor(ContextCompat.getColor(this@MainActivity, R.color.bg))
        }
        webView = WebView(this).apply {
            layoutParams = FrameLayout.LayoutParams(
                ViewGroup.LayoutParams.MATCH_PARENT,
                ViewGroup.LayoutParams.MATCH_PARENT,
            )
        }
        root.addView(webView)

        loading = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            gravity = Gravity.CENTER
            setPadding(dp(28), dp(28), dp(28), dp(28))
            setBackgroundColor(ContextCompat.getColor(this@MainActivity, R.color.bg))
            layoutParams = FrameLayout.LayoutParams(
                ViewGroup.LayoutParams.MATCH_PARENT,
                ViewGroup.LayoutParams.MATCH_PARENT,
            )
        }
        val title = TextView(this).apply {
            text = "GoPlay"
            setTextColor(ContextCompat.getColor(this@MainActivity, R.color.accent))
            textSize = 30f
            gravity = Gravity.CENTER
        }
        loadingText = TextView(this).apply {
            text = "正在启动本机 AI 核心…"
            setTextColor(ContextCompat.getColor(this@MainActivity, R.color.text_secondary))
            textSize = 14f
            gravity = Gravity.CENTER
            setPadding(0, dp(12), 0, dp(14))
        }
        loadingBar = ProgressBar(this).apply {
            isIndeterminate = true
            indeterminateTintList = android.content.res.ColorStateList.valueOf(
                ContextCompat.getColor(this@MainActivity, R.color.accent),
            )
        }
        retryButton = Button(this).apply {
            text = "重试"
            isAllCaps = false
            visibility = View.GONE
            setOnClickListener {
                retryButton.visibility = View.GONE
                loadingBar.visibility = View.VISIBLE
                loadingText.text = "正在启动本机 AI 核心…"
                graph.localCore.start(lifecycleScope)
            }
        }
        loading.addView(title)
        loading.addView(loadingText)
        loading.addView(loadingBar)
        loading.addView(retryButton)
        root.addView(loading)
        setContentView(root)
    }

    private fun configureWebView() {
        WebView.setWebContentsDebuggingEnabled(
            (applicationInfo.flags and ApplicationInfo.FLAG_DEBUGGABLE) != 0,
        )
        webView.setBackgroundColor(ContextCompat.getColor(this, R.color.bg))
        webView.settings.apply {
            javaScriptEnabled = true
            domStorageEnabled = true
            allowFileAccess = false
            allowContentAccess = false
            mediaPlaybackRequiresUserGesture = false
            cacheMode = WebSettings.LOAD_DEFAULT
            userAgentString = userAgentString + " GoPlayAndroid/0.3"
        }
        webView.addJavascriptInterface(HostBridge(), "GoPlayHost")
        webView.webChromeClient = object : WebChromeClient() {
            override fun onConsoleMessage(message: android.webkit.ConsoleMessage): Boolean {
                val level = when (message.messageLevel()) {
                    android.webkit.ConsoleMessage.MessageLevel.ERROR -> "ERROR"
                    android.webkit.ConsoleMessage.MessageLevel.WARNING -> "WARN"
                    else -> "LOG"
                }
                graph.logs.log("WebView " + level + ": " + message.message())
                return true
            }
        }
        webView.webViewClient = object : WebViewClient() {
            override fun shouldOverrideUrlLoading(
                view: WebView,
                request: WebResourceRequest,
            ): Boolean {
                val url = request.url.toString()
                val local = graph.localCore.port > 0 &&
                    url.startsWith("http://127.0.0.1:" + graph.localCore.port)
                if (local) return false
                if (url.startsWith("http://") || url.startsWith("https://")) {
                    runCatching { startActivity(Intent(Intent.ACTION_VIEW, Uri.parse(url))) }
                    return true
                }
                return false
            }

            override fun onPageFinished(view: WebView, url: String) {
                loading.visibility = View.GONE
                pageReady = true
                flushPendingFocus()
            }
        }
    }

    private fun observeCore() {
        lifecycleScope.launch {
            repeatOnLifecycle(Lifecycle.State.STARTED) {
                graph.localCore.state.collect { state ->
                    when {
                        state.ready -> {
                            val port = state.port
                            if (port > 0 && port != loadedPort) {
                                loadedPort = port
                                pageReady = false
                                loading.visibility = View.VISIBLE
                                loadingBar.visibility = View.VISIBLE
                                retryButton.visibility = View.GONE
                                loadingText.text = "本机 AI 核心已就绪，正在打开控制台…"
                                webView.loadUrl("http://127.0.0.1:" + port + "/")
                            }
                        }

                        !state.error.isNullOrBlank() -> {
                            loading.visibility = View.VISIBLE
                            loadingBar.visibility = View.GONE
                            retryButton.visibility = View.VISIBLE
                            loadingText.text = "本机核心启动失败\n\n" + state.error +
                                "\n\n日志：" + state.logPath
                        }

                        state.starting -> {
                            loading.visibility = View.VISIBLE
                            loadingBar.visibility = View.VISIBLE
                            loadingText.text = "正在启动本机 AI 核心…"
                        }
                    }
                }
            }
        }
    }

    private fun maybeRequestNotificationPermission() {
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU &&
            ContextCompat.checkSelfPermission(this, Manifest.permission.POST_NOTIFICATIONS) !=
            PackageManager.PERMISSION_GRANTED
        ) {
            notificationPermissionLauncher.launch(Manifest.permission.POST_NOTIFICATIONS)
        }
    }

    // ---- permissions -------------------------------------------------------

    private data class PermissionSnapshot(
        val overlay: Boolean,
        val accessibility: Boolean,
        val notification: Boolean,
        val capture: Boolean,
    ) {
        fun missing(): List<String> {
            val out = ArrayList<String>(4)
            if (!overlay) out.add("overlay")
            if (!accessibility) out.add("accessibility")
            if (!notification) out.add("notification")
            if (!capture) out.add("capture")
            return out
        }
    }

    private fun notificationGranted(): Boolean {
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU &&
            ContextCompat.checkSelfPermission(this, Manifest.permission.POST_NOTIFICATIONS) !=
            PackageManager.PERMISSION_GRANTED
        ) {
            return false
        }
        return NotificationManagerCompat.from(this).areNotificationsEnabled()
    }

    private fun permissionSnapshot(): PermissionSnapshot {
        val capture = ScreenCaptureService.state.value
        return PermissionSnapshot(
            overlay = Settings.canDrawOverlays(this),
            accessibility = GameAccessibilityService.instance != null,
            notification = notificationGranted(),
            capture = capture.running,
        )
    }

    private fun missingMessage(item: String): String = when (item) {
        "overlay" -> "需要「显示在其他应用上层」权限，已打开系统设置"
        "accessibility" -> "需要开启 GoPlay 无障碍服务，已打开系统设置"
        "notification" -> "需要通知权限，已弹出系统授权"
        "capture" -> "需要录屏授权，已弹出系统授权"
        "instance" -> "实例 ID 不合法"
        else -> "需要授予权限：" + item
    }

    /**
     * Opens the system screen that grants [item]. Never grants anything itself:
     * the user must still confirm in the system UI, which is exactly what the
     * product spec asks for (no silent toast, no permission bypass).
     */
    private fun openPermissionScreen(item: String) {
        when (item) {
            "overlay" -> openOverlaySettings()
            "accessibility" -> openAccessibilitySettings()
            "notification" -> {
                if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU &&
                    ContextCompat.checkSelfPermission(
                        this,
                        Manifest.permission.POST_NOTIFICATIONS,
                    ) != PackageManager.PERMISSION_GRANTED
                ) {
                    notificationPermissionLauncher.launch(Manifest.permission.POST_NOTIFICATIONS)
                } else {
                    // Already allowed at the app level; the OS channel may be off.
                    runCatching {
                        startActivity(
                            Intent(Settings.ACTION_APP_NOTIFICATION_SETTINGS)
                                .putExtra(Settings.EXTRA_APP_PACKAGE, packageName),
                        )
                    }
                }
            }

            "capture" -> beginCapture()
        }
    }

    private fun ensureReadyJson(instanceId: String): String {
        val clean = instanceId.trim()
        if (!INSTANCE_ID.matches(clean)) {
            return JSONObject()
                .put("ok", false)
                .put("error", "invalid_instance_id")
                .put("missing", JSONArray().put("instance"))
                .put("message", missingMessage("instance"))
                .toString()
        }
        if (!applyActiveInstance(clean)) {
            return JSONObject()
                .put("ok", false)
                .put("error", "save_failed")
                .put("instanceId", clean)
                .put("missing", JSONArray().put("instance"))
                .toString()
        }
        val snapshot = permissionSnapshot()
        val missing = snapshot.missing()
        val out = JSONObject()
            .put("ok", missing.isEmpty())
            .put("instanceId", clean)
            .put("missing", JSONArray(missing))
            .put("accessibility", snapshot.accessibility)
            .put("overlay", snapshot.overlay)
            .put("notification", snapshot.notification)
            .put("captureRunning", snapshot.capture)
        if (missing.isEmpty()) {
            out.put("message", "权限齐全，可以开始游戏")
            return out.toString()
        }
        val first = missing.first()
        out.put("opened", first)
        out.put("message", missingMessage(first))
        openPermissionScreen(first)
        dispatchFocus(FOCUS_PERMISSIONS)
        return out.toString()
    }

    // ---- game preparation --------------------------------------------------

    /**
     * Activates the instance, requests what is missing and starts the game.
     *
     * Everything that must be exact happens on the main thread before this
     * returns, so the JS side gets real flags instead of optimistic guesses.
     * The projection consent dialog is asynchronous by design: the game launch
     * is deferred until the user finished granting (see schedulePendingLaunchIfReady).
     */
    private fun prepareGameJson(instanceId: String, pkg: String): String {
        val steps = JSONArray()
        val clean = instanceId.trim()
        if (!INSTANCE_ID.matches(clean)) {
            return JSONObject()
                .put("ok", false)
                .put("error", "invalid_instance_id")
                .put("missing", JSONArray().put("instance"))
                .put("steps", steps)
                .put("message", missingMessage("instance"))
                .toString()
        }
        if (!applyActiveInstance(clean)) {
            return JSONObject()
                .put("ok", false)
                .put("error", "save_failed")
                .put("instanceId", clean)
                .put("missing", JSONArray().put("instance"))
                .put("steps", steps)
                .toString()
        }
        steps.put("active_instance")

        val snapshot = permissionSnapshot()
        val blocking = ArrayList<String>(3)
        if (!snapshot.overlay) blocking.add("overlay")
        if (!snapshot.accessibility) blocking.add("accessibility")
        if (!snapshot.notification) blocking.add("notification")
        steps.put("permissions")

        val out = JSONObject()
            .put("instanceId", clean)
            .put("accessibility", snapshot.accessibility)
            .put("overlay", snapshot.overlay)
            .put("notification", snapshot.notification)
            .put("captureRunning", snapshot.capture)

        if (blocking.isNotEmpty()) {
            // Do not launch the game behind a system settings page the user is
            // about to read; remember it and continue on the next resume.
            pendingLaunchPackage = pkg.trim().ifBlank { null }
            out.put("ok", false)
                .put("missing", JSONArray(blocking))
                .put("opened", blocking.first())
                .put("message", missingMessage(blocking.first()))
            if (pendingLaunchPackage != null) out.put("launchPending", true)
            steps.put("open_settings")
            openPermissionScreen(blocking.first())
            dispatchFocus(FOCUS_PERMISSIONS)
            out.put("steps", steps)
            return out.toString()
        }

        var captureStarted = false
        if (!snapshot.capture) {
            pendingLaunchPackage = pkg.trim().ifBlank { null }
            steps.put("capture_request")
            captureStarted = beginCapture()
            out.put("captureStarted", captureStarted)
            if (!captureStarted) {
                out.put("ok", false)
                    .put("missing", JSONArray().put("capture"))
                    .put("message", "无法弹出录屏授权，请手动打开录屏")
                    .put("steps", steps)
                return out.toString()
            }
            if (pendingLaunchPackage != null) out.put("launchPending", true)
        } else {
            steps.put("capture_running")
            out.put("captureStarted", false)
        }

        var launched = false
        if (pkg.isNotBlank() && snapshot.capture) {
            steps.put("launch_game")
            launched = launchGamePackage(pkg)
        }
        out.put("ok", true)
            .put("missing", JSONArray())
            .put("launched", launched)
            .put("captureStarted", captureStarted)
            .put("steps", steps)
            .put(
                "message",
                when {
                    launched -> "已启动游戏"
                    captureStarted -> "已请求录屏授权；授权后将启动游戏"
                    pkg.isBlank() -> "准备完成"
                    else -> "等待录屏授权后启动游戏"
                },
            )
        return out.toString()
    }

    /**
     * Launches the deferred game once every prerequisite is really satisfied.
     * Guarded by launchScheduled so the projection callback and onResume can
     * both ask without starting the game twice.
     */
    private fun schedulePendingLaunchIfReady(delayMs: Long) {
        pendingLaunchPackage ?: return
        if (launchScheduled) return
        if (!::webView.isInitialized) return
        val snapshot = permissionSnapshot()
        if (!snapshot.capture || !snapshot.overlay || !snapshot.accessibility ||
            !snapshot.notification
        ) {
            return
        }
        launchScheduled = true
        webView.postDelayed({
            launchScheduled = false
            val target = pendingLaunchPackage ?: return@postDelayed
            pendingLaunchPackage = null
            if (launchGamePackage(target)) {
                graph.logs.log("权限与录屏已就绪，启动游戏 " + target)
            }
        }, delayMs)
    }

    /**
     * Remembers which game the AI may touch, in memory (InputGate, read by the
     * accessibility service) and on disk (so a process restart still knows).
     */
    private fun rememberActiveGame(pkg: String) {
        InputGate.setActiveGamePackage(pkg)
        graph.settingsStore.setActiveGamePackage(pkg)
    }

    private fun launchGamePackage(pkg: String): Boolean {
        val clean = pkg.trim()
        if (clean.isEmpty()) return false
        val intent = packageManager.getLaunchIntentForPackage(clean)
        if (intent == null) {
            toast("找不到可启动的游戏：" + clean)
            graph.logs.log("找不到可启动的游戏: " + clean)
            return false
        }
        intent.addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)
        return try {
            startActivity(intent)
            rememberActiveGame(clean)
            true
        } catch (e: Exception) {
            toast("启动游戏失败：" + (e.message ?: e.javaClass.simpleName))
            graph.logs.log("启动游戏失败: " + (e.message ?: e.javaClass.simpleName))
            false
        }
    }

    // ---- capture -----------------------------------------------------------

    private fun beginCapture(): Boolean {
        if (projectionRequestInFlight) {
            graph.logs.log("录屏授权已在等待中，忽略重复请求")
            return false
        }
        val manager = getSystemService(MediaProjectionManager::class.java)
        if (manager == null) {
            toast("系统不支持 MediaProjection")
            return false
        }
        if (!Settings.canDrawOverlays(this)) {
            graph.logs.log("开始录屏：悬浮窗权限未授予，灵动岛将不可见")
        }
        if (GameAccessibilityService.instance == null) {
            graph.logs.log("开始录屏：无障碍服务未开启，AI 无法操作游戏")
        }
        return try {
            projectionRequestInFlight = true
            projectionLauncher.launch(manager.createScreenCaptureIntent())
            true
        } catch (e: Exception) {
            projectionRequestInFlight = false
            toast("打开录屏授权失败：" + (e.message ?: e.javaClass.simpleName))
            false
        }
    }

    private fun stopCapture() {
        try {
            stopService(Intent(this, ScreenCaptureService::class.java))
        } catch (_: Exception) {
        }
        graph.client.disconnect()
        toast("已停止截屏")
    }

    // ---- instances ---------------------------------------------------------

    /**
     * Persists the active instance without tearing down anything that is
     * already running. Only a live capture service is told to reconnect/reload.
     */
    private fun applyActiveInstance(id: String): Boolean {
        val clean = id.trim()
        if (!INSTANCE_ID.matches(clean)) {
            graph.logs.log("实例 ID 不合法: " + clean.take(80))
            return false
        }
        val current = graph.settingsStore.load()
        val sameInstance = current.instanceId == clean
        val port = graph.localCore.port.takeIf { it > 0 } ?: current.port
        val saved = graph.settingsStore.save(
            current.copy(host = "127.0.0.1", port = port, instanceId = clean),
        )
        if (!saved.ok) {
            toast(saved.error ?: "保存当前游戏失败")
            return false
        }
        if (ScreenCaptureService.state.value.running) {
            if (sameInstance) {
                // Re-selecting the same card must not tear down a healthy socket;
                // only nudge it when a previous reconnect failed.
                graph.client.ensureConnected()
            } else {
                graph.client.reconnectIfActive()
                graph.client.ensureConnected()
                runCatching {
                    startService(
                        ScreenCaptureService.commandIntent(
                            this,
                            ScreenCaptureService.ACTION_RELOAD_SETTINGS,
                        ),
                    )
                }
            }
            graph.logs.log(
                (if (sameInstance) "继续使用活动实例：" else "已切换活动实例：") + clean,
            )
        }
        return true
    }

    // ---- voice -------------------------------------------------------------

    private fun voiceBridge(): VoiceBridge = voice ?: VoiceBridge(
        context = this,
        log = { message -> graph.logs.log("Voice: " + message) },
        evaluate = { script -> emitJs(script) },
        onState = { state -> onVoiceState(state) },
    ).also { voice = it }

    /**
     * Real voice facts. The island needs them to show whether the AI is hearing
     * or speaking, and the console mirrors them through window.goplayVoiceState.
     */
    private fun onVoiceState(state: VoiceState) {
        graph.updateSession {
            it.copy(
                aiListening = state.listening,
                aiSpeaking = state.speaking,
                aiLastHeard = state.lastHeard.ifBlank { it.aiLastHeard },
                aiLastSpoken = state.lastSpoken.ifBlank { it.aiLastSpoken },
            )
        }
        val json = JSONObject()
            .put("listening", state.listening)
            .put("speaking", state.speaking)
            .put("last_heard", state.lastHeard)
            .put("last_spoken", state.lastSpoken)
            .put("error", state.error ?: JSONObject.NULL)
            .toString()
        emitJs("window.goplayVoiceState && window.goplayVoiceState(" + json + ")")
    }

    private fun hasMicPermission(): Boolean =
        ContextCompat.checkSelfPermission(this, Manifest.permission.RECORD_AUDIO) ==
            PackageManager.PERMISSION_GRANTED

    private fun startConversationJson(): String {
        if (!hasMicPermission()) {
            pendingVoiceStart = true
            val message = "需要录音权限才能连续语音，请在弹窗中允许麦克风"
            emitJs("window.goplayVoiceError(" + JSONObject.quote(message) + ")")
            micPermissionLauncher.launch(Manifest.permission.RECORD_AUDIO)
            return JSONObject()
                .put("ok", false)
                .put("listening", false)
                .put("error", "permission_required")
                .put("message", message)
                .toString()
        }
        VoiceKeepAliveService.start(this)
        val error = voiceBridge().start()
        return if (error == null) {
            emitJs("window.goplayVoiceStarted && window.goplayVoiceStarted()")
            JSONObject().put("ok", true).put("listening", true)
                .put("message", "连续语音已开启").toString()
        } else {
            VoiceKeepAliveService.stop(this)
            JSONObject().put("ok", false).put("listening", false)
                .put("error", error).put("message", error).toString()
        }
    }

    private fun requestVoice() {
        val intent = Intent(RecognizerIntent.ACTION_RECOGNIZE_SPEECH).apply {
            putExtra(
                RecognizerIntent.EXTRA_LANGUAGE_MODEL,
                RecognizerIntent.LANGUAGE_MODEL_FREE_FORM,
            )
            putExtra(RecognizerIntent.EXTRA_LANGUAGE, Locale.getDefault().toLanguageTag())
            putExtra(RecognizerIntent.EXTRA_PROMPT, "说出你的指令，例如：先别打那个 Boss")
            putExtra(RecognizerIntent.EXTRA_MAX_RESULTS, 1)
        }
        try {
            speechLauncher.launch(intent)
        } catch (e: ActivityNotFoundException) {
            toast("这台设备没有语音识别服务，请用键盘输入")
        } catch (e: Exception) {
            toast("语音启动失败：" + (e.message ?: e.javaClass.simpleName))
        }
    }

    // ---- settings screens --------------------------------------------------

    private fun openAccessibilitySettings() {
        runCatching { startActivity(Intent(Settings.ACTION_ACCESSIBILITY_SETTINGS)) }
            .onFailure { toast("无法打开无障碍设置") }
    }

    private fun openOverlaySettings() {
        val intent = Intent(
            Settings.ACTION_MANAGE_OVERLAY_PERMISSION,
            Uri.parse("package:" + packageName),
        )
        runCatching { startActivity(intent) }.onFailure {
            runCatching { startActivity(Intent(Settings.ACTION_MANAGE_OVERLAY_PERMISSION)) }
                .onFailure { toast("无法打开悬浮窗设置") }
        }
    }

    private fun openAppSettings() {
        val intent = Intent(
            Settings.ACTION_APPLICATION_DETAILS_SETTINGS,
            Uri.parse("package:" + packageName),
        )
        runCatching { startActivity(intent) }.onFailure { toast("无法打开应用设置") }
    }

    /**
     * Brings this activity to the front and asks the console to open its
     * settings section. The intent extra is also the public contract for other
     * components (the overlay "panel" button uses it).
     */
    private fun openPermissionCenter() {
        val intent = Intent(this, MainActivity::class.java).apply {
            addFlags(
                Intent.FLAG_ACTIVITY_NEW_TASK or
                    Intent.FLAG_ACTIVITY_SINGLE_TOP or
                    Intent.FLAG_ACTIVITY_CLEAR_TOP,
            )
            putExtra(EXTRA_FOCUS, FOCUS_PERMISSIONS)
        }
        runCatching { startActivity(intent) }
            .onFailure { graph.logs.log("打开权限中心失败: " + it.message) }
        dispatchFocus(FOCUS_PERMISSIONS)
    }

    private fun dispatchFocus(section: String) {
        pendingFocus = section
        flushPendingFocus()
    }

    private fun flushPendingFocus() {
        val section = pendingFocus ?: return
        if (!pageReady) return
        pendingFocus = null
        emitJs("window.goplayFocus && window.goplayFocus(" + JSONObject.quote(section) + ");")
    }

    private fun emitJs(script: String) {
        if (!::webView.isInitialized) return
        runOnUiThread {
            if (!isFinishing) {
                runCatching { webView.evaluateJavascript(script, null) }
            }
        }
    }

    // ---- human hand-back ---------------------------------------------------

    /**
     * Sends human=false over both transports: the WebSocket bridge is
     * authoritative, and the REST endpoint exists for cores that only accept
     * HTTP. Never throws; failures come back as text in the JSON.
     */
    private fun releaseHumanJson(): String {
        val instanceId = graph.settingsStore.load().instanceId
        // Clear the accessibility latch first so its 1 s heartbeat cannot
        // immediately re-assert human=true after we release.
        GameAccessibilityService.instance?.releaseHuman()
        val wsSent = graph.client.sendHuman(false)
        if (wsSent) {
            graph.logs.log("交还 AI：已通过 WebSocket 发送 human=false")
            return JSONObject()
                .put("ok", true)
                .put("transport", "ws")
                .put("instanceId", instanceId)
                .put("message", "已交还 AI 控制")
                .toString()
        }
        val result = postRelease(instanceId)
        return if (result.first) {
            graph.logs.log("交还 AI：已通过 HTTP 发送 human=false")
            JSONObject()
                .put("ok", true)
                .put("transport", "http")
                .put("instanceId", instanceId)
                .put("message", "已交还 AI 控制")
                .toString()
        } else {
            graph.logs.log("交还 AI 失败：" + result.second)
            JSONObject()
                .put("ok", false)
                .put("transport", "none")
                .put("instanceId", instanceId)
                .put("error", result.second)
                .put("message", "交还失败：" + result.second)
                .toString()
        }
    }

    private fun postRelease(instanceId: String): Pair<Boolean, String> {
        val port = graph.localCore.port.takeIf { it > 0 } ?: graph.settingsStore.load().port
        if (port <= 0) return Pair(false, "本机核心尚未就绪")
        val url = "http://127.0.0.1:" + port + "/api/instances/" + instanceId + "/release"
        val client = releaseHttp ?: OkHttpClient.Builder()
            .connectTimeout(RELEASE_HTTP_TIMEOUT_MS, TimeUnit.MILLISECONDS)
            .readTimeout(RELEASE_HTTP_TIMEOUT_MS, TimeUnit.MILLISECONDS)
            .writeTimeout(RELEASE_HTTP_TIMEOUT_MS, TimeUnit.MILLISECONDS)
            .build()
            .also { releaseHttp = it }
        return try {
            val body = JSONObject().put("reason", "user_release").toString()
                .toRequestBody(JSON_MEDIA_TYPE)
            val request = Request.Builder().url(url).post(body).build()
            client.newCall(request).execute().use { response ->
                val text = response.body?.string().orEmpty()
                if (response.isSuccessful) {
                    Pair(true, text)
                } else {
                    Pair(
                        false,
                        "HTTP " + response.code +
                            (if (text.isBlank()) "" else " " + text.take(200)),
                    )
                }
            }
        } catch (e: Exception) {
            Pair(false, e.message ?: e.javaClass.simpleName)
        }
    }

    // ---- takeover sensitivity ---------------------------------------------

    private fun setTakeoverJson(level: String): String {
        val clean = level.trim().lowercase(Locale.ROOT)
        val applied = graph.settingsStore.setTakeoverSensitivity(clean)
        if (applied == null) {
            return JSONObject()
                .put("ok", false)
                .put("error", "invalid_sensitivity")
                .put("takeoverSensitivity", graph.settingsStore.load().takeoverSensitivity)
                .put("allowed", JSONArray(TakeoverSensitivity.LEVELS))
                .put("message", "灵敏度只能是 high / medium / low")
                .toString()
        }
        GameAccessibilityService.instance?.reloadSensitivity()
        graph.logs.log("接管灵敏度已设为 " + applied)
        return JSONObject()
            .put("ok", true)
            .put("takeoverSensitivity", applied)
            .toString()
    }

    // ---- misc --------------------------------------------------------------

    private fun installedGamesJson(): String {
        val manager = packageManager
        val intent = Intent(Intent.ACTION_MAIN).addCategory(Intent.CATEGORY_LAUNCHER)
        val resolved = try {
            @Suppress("DEPRECATION")
            manager.queryIntentActivities(intent, 0)
        } catch (_: Exception) {
            emptyList()
        }
        val seen = HashSet<String>()
        val entries = ArrayList<Triple<String, String, Boolean>>()
        for (info in resolved) {
            val pkg = info.activityInfo?.packageName ?: continue
            if (pkg == packageName || !seen.add(pkg)) continue
            val app = info.activityInfo.applicationInfo
            val label = try {
                info.loadLabel(manager).toString()
            } catch (_: Exception) {
                pkg
            }
            val system = (app.flags and ApplicationInfo.FLAG_SYSTEM) != 0
            entries.add(Triple(pkg, label, system))
        }
        entries.sortWith(compareBy<Triple<String, String, Boolean>> { it.third }.thenBy { it.second })
        val out = JSONArray()
        for ((pkg, label, system) in entries.take(300)) {
            out.put(
                JSONObject()
                    .put("package", pkg)
                    .put("label", label)
                    .put("system", system),
            )
        }
        return out.toString()
    }

    private fun buildStatusJson(): String {
        val capture = ScreenCaptureService.state.value
        val settings = graph.settingsStore.load()
        val core = graph.localCore.state.value
        val snapshot = permissionSnapshot()
        val voiceState = voice?.state ?: VoiceState()
        val voiceJson = JSONObject()
            .put("listening", voiceState.listening)
            .put("speaking", voiceState.speaking)
            .put("last_heard", voiceState.lastHeard)
            .put("last_spoken", voiceState.lastSpoken)
            .put("error", voiceState.error ?: JSONObject.NULL)
        val permissions = JSONObject()
            .put("overlay", snapshot.overlay)
            .put("accessibility", snapshot.accessibility)
            .put("notification", snapshot.notification)
            .put("capture", snapshot.capture)
            .put("missing", JSONArray(snapshot.missing()))
        return JSONObject()
            .put("coreReady", core.ready)
            .put("corePort", core.port)
            .put("coreError", core.error ?: JSONObject.NULL)
            .put("coreLog", core.logPath)
            .put("wsConnected", graph.client.isConnected)
            .put("wsState", graph.client.state.value.name.lowercase())
            .put("wsError", graph.client.lastError.value ?: JSONObject.NULL)
            .put("captureRunning", capture.running)
            .put("capturePaused", capture.paused)
            .put("captureWidth", capture.width)
            .put("captureHeight", capture.height)
            .put("captureError", capture.error ?: JSONObject.NULL)
            .put("overlayVisible", capture.overlayVisible)
            .put("permissions", permissions)
            .put("accessibilityReady", snapshot.accessibility)
            .put("overlayPermission", snapshot.overlay)
            .put("notificationPermission", snapshot.notification)
            .put("micPermission", hasMicPermission())
            .put("voice", voiceJson)
            .put("voiceListening", voiceState.listening)
            .put("voiceSpeaking", voiceState.speaking)
            .put("takeoverSensitivity", settings.takeoverSensitivity)
            .put("activeInstance", settings.instanceId)
            .put("device", graph.device.summary)
            .put("version", core.version)
            .toString()
    }

    private fun toast(message: String) {
        runOnUiThread { Toast.makeText(this, message, Toast.LENGTH_SHORT).show() }
    }

    private fun dp(value: Int): Int =
        (value * resources.displayMetrics.density).toInt()

    /**
     * Runs [block] on the main thread and waits briefly for its result.
     *
     * JavascriptInterface methods run on a WebView background thread, but the
     * values they report (permission snapshots, settings, service state) must be
     * read on the main thread. Blocking the bridge thread for a few ms is far
     * cheaper than returning optimistic lies to the console.
     */
    private fun <T> onMainNow(timeoutMs: Long = 1500L, block: () -> T): T? {
        if (Looper.myLooper() == Looper.getMainLooper()) return block()
        val holder = AtomicReference<T?>()
        val latch = CountDownLatch(1)
        runOnUiThread {
            try {
                holder.set(block())
            } catch (e: Exception) {
                graph.logs.log("主线程操作失败: " + (e.message ?: e.javaClass.simpleName))
            } finally {
                latch.countDown()
            }
        }
        val finished = try {
            latch.await(timeoutMs, TimeUnit.MILLISECONDS)
        } catch (_: InterruptedException) {
            Thread.currentThread().interrupt()
            false
        }
        if (!finished) {
            graph.logs.log("主线程操作超时（" + timeoutMs + "ms）")
            return null
        }
        return holder.get()
    }

    private fun uiJson(block: () -> String): String =
        onMainNow { block() } ?: JSONObject()
            .put("ok", false)
            .put("error", "main_thread_timeout")
            .put("message", "界面忙，请稍后再试")
            .toString()

    private inner class HostBridge {
        @JavascriptInterface
        fun installedGames(): String = installedGamesJson()

        @JavascriptInterface
        fun statusJson(): String = buildStatusJson()

        @JavascriptInterface
        fun setActiveInstance(id: String): String = uiJson {
            val ok = applyActiveInstance(id)
            JSONObject().put("ok", ok).put("instanceId", id.trim()).toString()
        }

        /** Force a fresh WebSocket handshake for the active instance. */
        @JavascriptInterface
        fun reconnectCore(): String = uiJson {
            // The user pressed reconnect: drop whatever half-open socket exists
            // instead of only reconnecting when the supervisor thinks it is live.
            graph.client.disconnect()
            graph.client.ensureConnected()
            JSONObject()
                .put("ok", true)
                .put("wsConnected", graph.client.isConnected)
                .put("wsState", graph.client.state.value.name.lowercase())
                .toString()
        }

        @JavascriptInterface
        fun prepareGame(instanceId: String, pkg: String): String =
            uiJson { prepareGameJson(instanceId, pkg) }

        @JavascriptInterface
        fun ensureReadyForGame(instanceId: String): String =
            uiJson { ensureReadyJson(instanceId) }

        @JavascriptInterface
        fun openPermissionCenter(): String {
            runOnUiThread { openPermissionCenter() }
            return "{\"ok\":true}"
        }

        @JavascriptInterface
        fun launchGame(pkg: String): String = uiJson {
            JSONObject().put("ok", launchGamePackage(pkg)).toString()
        }

        @JavascriptInterface
        fun startCapture(): String = uiJson {
            if (ScreenCaptureService.state.value.running) {
                JSONObject().put("ok", true).put("alreadyRunning", true).toString()
            } else {
                JSONObject().put("ok", beginCapture()).toString()
            }
        }

        @JavascriptInterface
        fun stopCapture(): String {
            runOnUiThread { this@MainActivity.stopCapture() }
            return "{\"ok\":true}"
        }

        @JavascriptInterface
        fun openOverlay(): String {
            runOnUiThread {
                if (!Settings.canDrawOverlays(this@MainActivity)) {
                    openOverlaySettings()
                    toast("先允许「显示在其他应用上层」，再点灵动岛")
                } else if (ScreenCaptureService.state.value.running) {
                    runCatching {
                        startService(
                            ScreenCaptureService.commandIntent(
                                this@MainActivity,
                                ScreenCaptureService.ACTION_EXPAND_OVERLAY,
                            ),
                        )
                    }
                } else {
                    pendingExpandOverlay = true
                    if (!beginCapture()) pendingExpandOverlay = false
                }
            }
            return "{\"ok\":true}"
        }

        @JavascriptInterface
        fun startVoice(): String {
            runOnUiThread { requestVoice() }
            return "{\"ok\":true}"
        }

        @JavascriptInterface
        fun startConversation(): String = uiJson { startConversationJson() }

        @JavascriptInterface
        fun stopConversation(): String = uiJson {
            voice?.stop()
            VoiceKeepAliveService.stop(this@MainActivity)
            JSONObject().put("ok", true).put("listening", false).toString()
        }

        @JavascriptInterface
        fun speak(text: String): String = uiJson {
            if (text.isBlank()) {
                JSONObject().put("ok", false).put("error", "文本为空").toString()
            } else {
                val error = voiceBridge().speak(text)
                if (error == null) {
                    JSONObject().put("ok", true).toString()
                } else {
                    JSONObject().put("ok", false).put("error", error).toString()
                }
            }
        }

        @JavascriptInterface
        fun stopSpeaking(): String = uiJson {
            voice?.stopSpeaking()
            JSONObject().put("ok", true).toString()
        }

        @JavascriptInterface
        fun setTakeoverSensitivity(level: String): String = uiJson { setTakeoverJson(level) }

        @JavascriptInterface
        fun releaseHuman(): String = releaseHumanJson()

        @JavascriptInterface
        fun openAccessibility(): String {
            runOnUiThread { openAccessibilitySettings() }
            return "{\"ok\":true}"
        }

        @JavascriptInterface
        fun openOverlaySettings(): String {
            runOnUiThread { this@MainActivity.openOverlaySettings() }
            return "{\"ok\":true}"
        }

        @JavascriptInterface
        fun requestNotification(): String {
            runOnUiThread {
                if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU &&
                    ContextCompat.checkSelfPermission(
                        this@MainActivity,
                        Manifest.permission.POST_NOTIFICATIONS,
                    ) != PackageManager.PERMISSION_GRANTED
                ) {
                    notificationPermissionLauncher.launch(Manifest.permission.POST_NOTIFICATIONS)
                } else {
                    toast("通知权限已授予")
                }
            }
            return "{\"ok\":true}"
        }

        @JavascriptInterface
        fun openAppSettings(): String {
            runOnUiThread { this@MainActivity.openAppSettings() }
            return "{\"ok\":true}"
        }

        @JavascriptInterface
        fun quit(): String {
            runOnUiThread {
                voice?.release()
                voice = null
                stopCapture()
                graph.localCore.stop()
                finishAndRemoveTask()
            }
            return "{\"ok\":true}"
        }
    }

    companion object {
        /** Extra consumed by handleIntentExtras: resume capture (overlay restore). */
        const val EXTRA_START_CAPTURE = "startCapture"

        /** Extra consumed by handleIntentExtras: console section to open. */
        const val EXTRA_FOCUS = "focus"

        /** Value of EXTRA_FOCUS that opens the console permission center. */
        const val FOCUS_PERMISSIONS = "permissions"

        private val INSTANCE_ID = Regex("^[A-Za-z0-9._-]{1,64}$")
        private const val RELEASE_HTTP_TIMEOUT_MS = 2_000L
        private val JSON_MEDIA_TYPE = "application/json; charset=utf-8".toMediaType()
    }
}
