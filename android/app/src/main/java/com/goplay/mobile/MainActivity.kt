package com.goplay.mobile

import android.Manifest
import android.accessibilityservice.AccessibilityServiceInfo
import android.content.Intent
import android.content.pm.PackageManager
import android.media.projection.MediaProjectionManager
import android.net.Uri
import android.os.Build
import android.os.Bundle
import android.provider.Settings
import android.view.accessibility.AccessibilityManager
import android.widget.Button
import android.widget.EditText
import android.widget.SeekBar
import android.widget.TextView
import android.widget.Toast
import androidx.activity.result.contract.ActivityResultContracts
import androidx.appcompat.app.AppCompatActivity
import androidx.appcompat.widget.SwitchCompat
import androidx.core.app.NotificationManagerCompat
import androidx.core.content.ContextCompat
import androidx.lifecycle.Lifecycle
import androidx.lifecycle.lifecycleScope
import androidx.lifecycle.repeatOnLifecycle
import com.goplay.mobile.accessibility.GameAccessibilityService
import com.goplay.mobile.capture.CaptureStatus
import com.goplay.mobile.capture.ScreenCaptureService
import com.goplay.mobile.data.AppSettings
import com.goplay.mobile.data.BatteryMonitor
import com.goplay.mobile.data.NetworkMonitor
import com.goplay.mobile.data.SessionState
import com.goplay.mobile.net.CoreRest
import com.goplay.mobile.notifications.Notifications
import java.util.Locale
import kotlinx.coroutines.delay
import kotlinx.coroutines.isActive
import kotlinx.coroutines.launch

/**
 * Settings / permission / status shell. The heavy lifting happens in
 * [ScreenCaptureService] (projection + overlay) and [GameAccessibilityService]
 * (gestures). This activity never captures or injects by itself.
 */
class MainActivity : AppCompatActivity() {

    private lateinit var graph: AppGraph
    private val networkMonitor by lazy { NetworkMonitor(this) }
    private val batteryMonitor by lazy { BatteryMonitor(this) }

    private val hostInput: EditText by lazy { findViewById(R.id.hostInput) }
    private val portInput: EditText by lazy { findViewById(R.id.portInput) }
    private val instanceInput: EditText by lazy { findViewById(R.id.instanceInput) }
    private val qualityLabel: TextView by lazy { findViewById(R.id.qualityLabel) }
    private val qualitySeek: SeekBar by lazy { findViewById(R.id.qualitySeek) }
    private val fpsLabel: TextView by lazy { findViewById(R.id.fpsLabel) }
    private val fpsSeek: SeekBar by lazy { findViewById(R.id.fpsSeek) }
    private val maxEdgeLabel: TextView by lazy { findViewById(R.id.maxEdgeLabel) }
    private val maxEdgeSeek: SeekBar by lazy { findViewById(R.id.maxEdgeSeek) }
    private val wifiOnlySwitch: SwitchCompat by lazy { findViewById(R.id.wifiOnlySwitch) }
    private val autoReconnectSwitch: SwitchCompat by lazy { findViewById(R.id.autoReconnectSwitch) }
    private val saveButton: Button by lazy { findViewById(R.id.saveButton) }
    private val connectButton: Button by lazy { findViewById(R.id.connectButton) }

    private val overlayStatus: TextView by lazy { findViewById(R.id.overlayStatus) }
    private val overlayButton: Button by lazy { findViewById(R.id.overlayButton) }
    private val accessibilityStatus: TextView by lazy { findViewById(R.id.accessibilityStatus) }
    private val accessibilityButton: Button by lazy { findViewById(R.id.accessibilityButton) }
    private val notificationStatus: TextView by lazy { findViewById(R.id.notificationStatus) }
    private val notificationButton: Button by lazy { findViewById(R.id.notificationButton) }
    private val startCaptureButton: Button by lazy { findViewById(R.id.startCaptureButton) }
    private val stopCaptureButton: Button by lazy { findViewById(R.id.stopCaptureButton) }

    private val sessionStatus: TextView by lazy { findViewById(R.id.sessionStatus) }
    private val captureStatus: TextView by lazy { findViewById(R.id.captureStatus) }
    private val metricsStatus: TextView by lazy { findViewById(R.id.metricsStatus) }
    private val errorStatus: TextView by lazy { findViewById(R.id.errorStatus) }
    private val deviceInfoView: TextView by lazy { findViewById(R.id.deviceInfoView) }
    private val queryLocalButton: Button by lazy { findViewById(R.id.queryLocalButton) }
    private val localResultView: TextView by lazy { findViewById(R.id.localResultView) }
    private val messageInput: EditText by lazy { findViewById(R.id.messageInput) }
    private val sendButton: Button by lazy { findViewById(R.id.sendButton) }
    private val logView: TextView by lazy { findViewById(R.id.logView) }

    private val projectionLauncher = registerForActivityResult(
        ActivityResultContracts.StartActivityForResult(),
    ) { result ->
        if (result.resultCode != RESULT_OK) {
            toast(getString(R.string.toast_projection_denied))
            return@registerForActivityResult
        }
        val data = result.data
        if (data == null) {
            showError("录屏授权结果缺少 data，无法启动截屏")
            return@registerForActivityResult
        }
        try {
            val serviceIntent = ScreenCaptureService.startIntent(this, result.resultCode, data)
            ContextCompat.startForegroundService(this, serviceIntent)
            graph.client.ensureConnected()
            toast(getString(R.string.toast_started))
        } catch (e: Exception) {
            showError("启动截屏服务失败：" + (e.message ?: e.javaClass.simpleName))
        }
    }

    private val notificationPermissionLauncher = registerForActivityResult(
        ActivityResultContracts.RequestPermission(),
    ) { granted ->
        renderPermissions()
        if (granted) {
            toast("通知权限已授予")
        } else {
            toast("通知权限被拒绝；前台服务仍会运行，但系统通知可能不可见")
        }
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        graph = (application as GoPlayApp).graph
        Notifications.ensureChannel(this)
        setContentView(R.layout.activity_main)
        bindSeekBars()
        loadSettings(graph.settingsStore.load())
        bindButtons()
        deviceInfoView.text = graph.device.summary
        graph.refreshRuntimeState()
        renderPermissions()
        renderMetrics()
        observeState()
    }

    override fun onResume() {
        super.onResume()
        if (this::graph.isInitialized) {
            graph.refreshRuntimeState()
            renderPermissions()
        }
    }

    private fun bindSeekBars() {
        qualitySeek.setOnSeekBarChangeListener(object : SeekBar.OnSeekBarChangeListener {
            override fun onProgressChanged(seekBar: SeekBar?, progress: Int, fromUser: Boolean) {
                renderSeekLabels()
            }

            override fun onStartTrackingTouch(seekBar: SeekBar?) = Unit
            override fun onStopTrackingTouch(seekBar: SeekBar?) = Unit
        })
        fpsSeek.setOnSeekBarChangeListener(object : SeekBar.OnSeekBarChangeListener {
            override fun onProgressChanged(seekBar: SeekBar?, progress: Int, fromUser: Boolean) {
                renderSeekLabels()
            }

            override fun onStartTrackingTouch(seekBar: SeekBar?) = Unit
            override fun onStopTrackingTouch(seekBar: SeekBar?) = Unit
        })
        maxEdgeSeek.setOnSeekBarChangeListener(object : SeekBar.OnSeekBarChangeListener {
            override fun onProgressChanged(seekBar: SeekBar?, progress: Int, fromUser: Boolean) {
                renderSeekLabels()
            }

            override fun onStartTrackingTouch(seekBar: SeekBar?) = Unit
            override fun onStopTrackingTouch(seekBar: SeekBar?) = Unit
        })
    }

    private fun bindButtons() {
        saveButton.setOnClickListener { saveSettings(showSuccess = true) }
        connectButton.setOnClickListener {
            if (!saveSettings(showSuccess = false)) return@setOnClickListener
            graph.client.connect()
            graph.logs.log("请求连接 " + graph.settingsStore.load().wsUrl)
            toast("正在连接核心…")
        }
        startCaptureButton.setOnClickListener { beginCapture() }
        stopCaptureButton.setOnClickListener { stopCapture() }
        overlayButton.setOnClickListener { openOverlaySettings() }
        accessibilityButton.setOnClickListener { openAccessibilitySettings() }
        notificationButton.setOnClickListener { requestOrOpenNotificationPermission() }
        queryLocalButton.setOnClickListener { queryLocalModels() }
        sendButton.setOnClickListener { sendMessage() }
    }

    private fun loadSettings(settings: AppSettings) {
        hostInput.setText(settings.host)
        portInput.setText(settings.port.toString())
        instanceInput.setText(settings.instanceId)
        qualitySeek.progress = settings.jpegQuality.coerceIn(10, 95)
        fpsSeek.progress = settings.targetFps.coerceIn(1, 30)
        val index = MAX_EDGES.indexOfFirst { it >= settings.maxEdge }
            .let { if (it < 0) MAX_EDGES.lastIndex else it }
        maxEdgeSeek.progress = index
        wifiOnlySwitch.isChecked = settings.wifiOnlyDownloads
        autoReconnectSwitch.isChecked = settings.autoReconnect
        renderSeekLabels()
    }

    private fun renderSeekLabels() {
        val quality = qualitySeek.progress.coerceIn(10, 95)
        val fps = fpsSeek.progress.coerceIn(1, 30)
        val edge = MAX_EDGES[maxEdgeSeek.progress.coerceIn(0, MAX_EDGES.lastIndex)]
        qualityLabel.text = "JPEG 质量：" + quality + "（10–95，越高越清晰也越费流量）"
        fpsLabel.text = "目标截屏 FPS：" + fps + "（实际受设备和网络限制）"
        maxEdgeLabel.text = "截图长边：" + edge + " px"
    }

    private fun currentSettings(): AppSettings = AppSettings(
        host = hostInput.text?.toString().orEmpty(),
        port = portInput.text?.toString()?.toIntOrNull() ?: 0,
        instanceId = instanceInput.text?.toString().orEmpty(),
        jpegQuality = qualitySeek.progress.coerceIn(10, 95),
        targetFps = fpsSeek.progress.coerceIn(1, 30),
        maxEdge = MAX_EDGES[maxEdgeSeek.progress.coerceIn(0, MAX_EDGES.lastIndex)],
        wifiOnlyDownloads = wifiOnlySwitch.isChecked,
        autoReconnect = autoReconnectSwitch.isChecked,
    )

    private fun saveSettings(showSuccess: Boolean): Boolean {
        val raw = currentSettings()
        if (raw.port !in 1..65535) {
            showError("端口必须在 1–65535 之间")
            return false
        }
        val result = graph.settingsStore.save(raw)
        val saved = result.settings
        if (!result.ok || saved == null) {
            showError(result.error ?: "设置保存失败")
            return false
        }
        loadSettings(saved)
        if (showSuccess) {
            toast(getString(R.string.toast_saved))
            graph.logs.log("设置已保存：" + saved.wsUrl)
        }
        if (ScreenCaptureService.state.value.running) {
            try {
                startService(
                    ScreenCaptureService.commandIntent(
                        this,
                        ScreenCaptureService.ACTION_RELOAD_SETTINGS,
                    ),
                )
            } catch (e: Exception) {
                graph.logs.error("通知截屏服务重载设置失败", e)
            }
        }
        graph.client.reconnectIfActive()
        return true
    }

    private fun beginCapture() {
        if (!saveSettings(showSuccess = false)) return
        if (!Settings.canDrawOverlays(this)) {
            toast(getString(R.string.toast_overlay_required))
        }
        if (GameAccessibilityService.instance == null) {
            toast(getString(R.string.toast_accessibility_missing))
        }
        val manager = getSystemService(MediaProjectionManager::class.java)
        if (manager == null) {
            showError("系统不支持 MediaProjection")
            return
        }
        val intent = try {
            manager.createScreenCaptureIntent()
        } catch (e: Exception) {
            showError("无法创建录屏授权请求：" + (e.message ?: e.javaClass.simpleName))
            return
        }
        try {
            projectionLauncher.launch(intent)
        } catch (e: Exception) {
            showError("打开录屏授权界面失败：" + (e.message ?: e.javaClass.simpleName))
        }
    }

    private fun stopCapture() {
        val wasRunning = ScreenCaptureService.state.value.running
        try {
            stopService(Intent(this, ScreenCaptureService::class.java))
        } catch (e: Exception) {
            graph.logs.error("停止截屏服务失败", e)
        }
        graph.client.disconnect()
        graph.refreshRuntimeState()
        toast(if (wasRunning) getString(R.string.toast_stopped) else "截屏未在运行")
    }

    private fun openOverlaySettings() {
        try {
            startActivity(
                Intent(
                    Settings.ACTION_MANAGE_OVERLAY_PERMISSION,
                    Uri.parse("package:" + packageName),
                ),
            )
        } catch (e: Exception) {
            try {
                startActivity(Intent(Settings.ACTION_MANAGE_OVERLAY_PERMISSION))
            } catch (inner: Exception) {
                showError("无法打开悬浮窗设置：" + (inner.message ?: inner.javaClass.simpleName))
            }
        }
    }

    private fun openAccessibilitySettings() {
        try {
            startActivity(Intent(Settings.ACTION_ACCESSIBILITY_SETTINGS))
        } catch (e: Exception) {
            showError("无法打开无障碍设置：" + (e.message ?: e.javaClass.simpleName))
        }
    }

    private fun requestOrOpenNotificationPermission() {
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU &&
            ContextCompat.checkSelfPermission(this, Manifest.permission.POST_NOTIFICATIONS) !=
            PackageManager.PERMISSION_GRANTED
        ) {
            notificationPermissionLauncher.launch(Manifest.permission.POST_NOTIFICATIONS)
            return
        }
        openNotificationSettings()
    }

    private fun openNotificationSettings() {
        try {
            val intent = Intent(Settings.ACTION_APP_NOTIFICATION_SETTINGS)
                .putExtra(Settings.EXTRA_APP_PACKAGE, packageName)
            startActivity(intent)
        } catch (e: Exception) {
            try {
                startActivity(
                    Intent(
                        Settings.ACTION_APPLICATION_DETAILS_SETTINGS,
                        Uri.parse("package:" + packageName),
                    ),
                )
            } catch (inner: Exception) {
                showError("无法打开通知设置：" + (inner.message ?: inner.javaClass.simpleName))
            }
        }
    }

    private fun renderPermissions() {
        val overlay = Settings.canDrawOverlays(this)
        overlayStatus.text = "悬浮窗：" + if (overlay) {
            "已授权，录屏时显示可拖动悬浮球"
        } else {
            "未授权（仍可录屏，但没有悬浮球控制）"
        }

        val manager = getSystemService(AccessibilityManager::class.java)
        val enabledInSystem = manager
            ?.getEnabledAccessibilityServiceList(AccessibilityServiceInfo.FEEDBACK_ALL_MASK)
            ?.any { info ->
                info.resolveInfo?.serviceInfo?.packageName == packageName
            } == true
        val bound = GameAccessibilityService.instance != null
        accessibilityStatus.text = "无障碍服务：" + when {
            bound -> "已连接，可执行点击/滑动/长按/返回/文本"
            enabledInSystem -> "系统已启用，等待服务绑定"
            else -> "未开启（AI 动作无法执行）"
        }

        val notifications = NotificationManagerCompat.from(this).areNotificationsEnabled()
        notificationStatus.text = "通知：" + if (notifications) {
            "已开启，前台服务通知可见"
        } else {
            "未开启，前台服务通知可能不可见"
        }
    }

    private fun queryLocalModels() {
        queryLocalButton.isEnabled = false
        localResultView.text = "查询中…（核心未实现接口时会显示错误）"
        lifecycleScope.launch {
            try {
                val status = graph.rest.localStatus()
                val recommend = graph.rest.localRecommend(graph.device)
                localResultView.text = buildString {
                    appendLine("设备：" + graph.device.summary)
                    appendLine()
                    appendLine("GET /api/local/status")
                    appendLine(status.display().take(2000))
                    appendLine()
                    appendLine("POST /api/local/recommend")
                    append(recommend.display().take(2000))
                }
                graph.logs.log("本地模型查询 status=" + status.status + " recommend=" + recommend.status)
            } finally {
                queryLocalButton.isEnabled = true
            }
        }
    }

    private fun sendMessage() {
        val text = messageInput.text?.toString()?.trim().orEmpty()
        if (text.isEmpty()) {
            toast(getString(R.string.toast_message_required))
            return
        }
        messageInput.setText("")
        lifecycleScope.launch {
            val result = graph.rest.sendMessage(text)
            graph.logs.log("发送消息：" + result.display())
            toast(if (result.ok) "消息已发送" else "发送失败：" + (result.error ?: "HTTP " + result.status))
        }
    }

    private fun observeState() {
        lifecycleScope.launch {
            repeatOnLifecycle(Lifecycle.State.STARTED) {
                launch {
                    graph.session.collect { state -> renderSession(state) }
                }
                launch {
                    ScreenCaptureService.state.collect { capture -> renderCapture(capture) }
                }
                launch {
                    graph.logs.lines.collect { lines ->
                        logView.text = lines.joinToString("\n")
                    }
                }
                launch {
                    while (isActive) {
                        graph.refreshRuntimeState()
                        renderPermissions()
                        renderMetrics()
                        delay(1000L)
                    }
                }
            }
        }
    }

    private fun renderSession(state: SessionState) {
        val running = if (state.running) "运行中" else "未运行"
        val paused = if (state.paused) " · 已暂停" else ""
        val connection = if (state.connected) "已连接" else state.wsState
        sessionStatus.text = "服务：" + running + paused +
            " · WebSocket：" + connection +
            " · 实例：" + state.instanceStatus
        if (state.lastError.isNullOrBlank()) {
            errorStatus.text = "最近错误：无"
        } else {
            errorStatus.text = "最近错误：" + state.lastError
        }
    }

    private fun renderCapture(capture: CaptureStatus) {
        captureStatus.text = if (capture.running) {
            "截屏：" + capture.width + "×" + capture.height +
                " · JPEG q" + capture.quality +
                " · 目标 " + capture.targetFps + " fps" +
                (if (capture.paused) " · 已暂停" else "")
        } else {
            "截屏：未运行"
        }
        if (!capture.error.isNullOrBlank()) {
            errorStatus.text = "最近错误：" + capture.error
        }
        startCaptureButton.isEnabled = !capture.running
        stopCaptureButton.isEnabled = capture.running
    }

    private fun renderMetrics() {
        val metrics = graph.metrics.snapshot()
        val network = networkMonitor.status()
        val battery = batteryMonitor.status()
        metricsStatus.text = String.format(
            Locale.US,
            "截图 FPS %.1f · 已发送 %d 帧 · 丢弃 %d · 动作 %d（错误 %d） · 重连 %d\n网络：%s · 电量：%s",
            metrics.captureFps,
            metrics.framesSent,
            metrics.framesDropped,
            metrics.actionsExecuted,
            metrics.actionErrors,
            metrics.wsReconnects,
            network.label,
            battery.label,
        )
    }

    private fun showError(message: String) {
        errorStatus.text = "最近错误：" + message
        toast(message)
        if (this::graph.isInitialized) graph.logs.error(message, null)
    }

    private fun toast(message: String) {
        Toast.makeText(this, message, Toast.LENGTH_SHORT).show()
    }

    companion object {
        /** Selectable long-edge values; must match maxEdgeSeek's max=3. */
        private val MAX_EDGES = intArrayOf(640, 960, 1280, 1920)
    }
}
