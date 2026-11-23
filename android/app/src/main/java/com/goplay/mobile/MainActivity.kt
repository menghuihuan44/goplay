package com.goplay.mobile

import android.Manifest
import android.annotation.SuppressLint
import android.app.Activity
import android.content.ActivityNotFoundException
import android.content.Intent
import android.content.pm.ApplicationInfo
import android.content.pm.PackageManager
import android.graphics.Color
import android.media.projection.MediaProjectionManager
import android.net.Uri
import android.os.Build
import android.os.Bundle
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
import com.goplay.mobile.local.LocalCore
import com.goplay.mobile.notifications.Notifications
import java.util.Locale
import kotlinx.coroutines.launch
import org.json.JSONArray
import org.json.JSONObject

/**
 * Thin Android shell around the same five-page web console that ships with the
 * Go core. The core (libgoplaycore.so) runs as a child process on 127.0.0.1;
 * this activity only starts it, loads the console, and bridges the things a
 * WebView cannot do: installed apps, screen capture consent, overlay,
 * accessibility settings and speech recognition.
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

    private val projectionLauncher = registerForActivityResult(
        ActivityResultContracts.StartActivityForResult(),
    ) { result ->
        if (result.resultCode != Activity.RESULT_OK) {
            pendingExpandOverlay = false
            toast("需要录屏授权才能看到游戏画面")
            return@registerForActivityResult
        }
        val data = result.data
        if (data == null) {
            pendingExpandOverlay = false
            toast("录屏授权结果缺少数据，请重试")
            return@registerForActivityResult
        }
        try {
            val intent = ScreenCaptureService.startIntent(this, result.resultCode, data)
            ContextCompat.startForegroundService(this, intent)
            graph.client.ensureConnected()
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
        } catch (e: Exception) {
            toast("启动截屏失败：" + (e.message ?: e.javaClass.simpleName))
        }
    }

    private val notificationPermissionLauncher = registerForActivityResult(
        ActivityResultContracts.RequestPermission(),
    ) { granted ->
        toast(if (granted) "通知权限已授予" else "通知权限被拒绝；前台服务通知可能不可见")
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
            webView.evaluateJavascript("window.goplayVoice && window.goplayVoice(" + quoted + ")", null)
        }
    }

    @SuppressLint("SetJavaScriptEnabled")
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        graph = (application as GoPlayApp).graph
        Notifications.ensureChannel(this)
        buildContentView()
        configureWebView()
        observeCore()
        graph.localCore.start(lifecycleScope)
        maybeRequestNotificationPermission()
        onBackPressedDispatcher.addCallback(this, object : OnBackPressedCallback(true) {
            override fun handleOnBackPressed() {
                if (webView.canGoBack()) webView.goBack() else moveTaskToBack(true)
            }
        })
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

    private fun beginCapture() {
        val manager = getSystemService(MediaProjectionManager::class.java)
        if (manager == null) {
            toast("系统不支持 MediaProjection")
            return
        }
        if (!Settings.canDrawOverlays(this)) {
            toast("建议授予悬浮窗权限，否则没有悬浮球")
        }
        if (GameAccessibilityService.instance == null) {
            toast("建议开启无障碍服务，否则 AI 无法点击/滑动")
        }
        try {
            projectionLauncher.launch(manager.createScreenCaptureIntent())
        } catch (e: Exception) {
            toast("打开录屏授权失败：" + (e.message ?: e.javaClass.simpleName))
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

    private fun activateInstance(id: String): Boolean {
        val clean = id.trim()
        if (!Regex("^[A-Za-z0-9._-]{1,64}$").matches(clean)) return false
        val current = graph.settingsStore.load()
        val port = graph.localCore.port.takeIf { it > 0 } ?: current.port
        val saved = graph.settingsStore.save(
            current.copy(host = "127.0.0.1", port = port, instanceId = clean),
        )
        if (!saved.ok) {
            toast(saved.error ?: "保存当前游戏失败")
            return false
        }
        runOnUiThread {
            if (ScreenCaptureService.state.value.running) {
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
            } else {
                beginCapture()
            }
        }
        return true
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

    private fun statusJson(): String {
        val capture = ScreenCaptureService.state.value
        val settings = graph.settingsStore.load()
        val core = graph.localCore.state.value
        val notifications = NotificationManagerCompat.from(this).areNotificationsEnabled()
        return JSONObject()
            .put("coreReady", core.ready)
            .put("corePort", core.port)
            .put("coreError", core.error ?: JSONObject.NULL)
            .put("coreLog", core.logPath)
            .put("captureRunning", capture.running)
            .put("capturePaused", capture.paused)
            .put("captureWidth", capture.width)
            .put("captureHeight", capture.height)
            .put("accessibilityReady", GameAccessibilityService.instance != null)
            .put("overlayPermission", Settings.canDrawOverlays(this))
            .put("notificationPermission", notifications)
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

    private inner class HostBridge {
        @JavascriptInterface
        fun installedGames(): String = installedGamesJson()

        @JavascriptInterface
        fun statusJson(): String = statusJson()

        @JavascriptInterface
        fun setActiveInstance(id: String): String {
            val ok = activateInstance(id)
            return JSONObject().put("ok", ok).put("instanceId", id).toString()
        }

        @JavascriptInterface
        fun launchGame(pkg: String): String {
            val intent = packageManager.getLaunchIntentForPackage(pkg)
            if (intent == null) {
                toast("找不到可启动的游戏：" + pkg)
                return "{\"ok\":false}"
            }
            intent.addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)
            return try {
                startActivity(intent)
                "{\"ok\":true}"
            } catch (e: Exception) {
                toast("启动游戏失败：" + (e.message ?: e.javaClass.simpleName))
                "{\"ok\":false}"
            }
        }

        @JavascriptInterface
        fun startCapture(): String {
            runOnUiThread {
                if (!ScreenCaptureService.state.value.running) beginCapture()
            }
            return "{\"ok\":true}"
        }

        @JavascriptInterface
        fun stopCapture(): String {
            runOnUiThread { stopCapture() }
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
                    beginCapture()
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
                stopCapture()
                graph.localCore.stop()
                finishAndRemoveTask()
            }
            return "{\"ok\":true}"
        }
    }
}
