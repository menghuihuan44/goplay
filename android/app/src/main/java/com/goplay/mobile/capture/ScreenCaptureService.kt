package com.goplay.mobile.capture

import android.app.Service
import android.content.Context
import android.content.Intent
import android.content.pm.ServiceInfo
import android.graphics.PixelFormat
import android.hardware.display.DisplayManager
import android.hardware.display.VirtualDisplay
import android.media.ImageReader
import android.media.projection.MediaProjection
import android.media.projection.MediaProjectionManager
import android.os.Build
import android.os.Handler
import android.os.HandlerThread
import android.os.IBinder
import android.os.Looper
import android.os.SystemClock
import android.provider.Settings
import android.util.DisplayMetrics
import android.view.Display
import android.view.Surface
import android.view.WindowManager
import com.goplay.mobile.GoPlayApp
import com.goplay.mobile.data.AppSettings
import com.goplay.mobile.data.BatteryMonitor
import com.goplay.mobile.data.NetworkMonitor
import com.goplay.mobile.net.CoreClient
import com.goplay.mobile.notifications.Notifications
import com.goplay.mobile.overlay.OverlayController
import com.goplay.mobile.protocol.FrameHeader
import java.util.Locale
import kotlin.math.min
import kotlin.math.roundToInt
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.cancel
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.launch

data class CaptureStatus(
    val running: Boolean = false,
    val paused: Boolean = false,
    val width: Int = 0,
    val height: Int = 0,
    val quality: Int = 0,
    val targetFps: Int = 0,
    val error: String? = null,
)

/**
 * Foreground service owning MediaProjection, the ImageReader capture loop and
 * the overlay. It never asks for root and never injects into another app.
 *
 * Android shows a system consent dialog before this service can create the
 * projection; that token is single-use per consent.
 */
class ScreenCaptureService : Service() {

    private lateinit var graph: com.goplay.mobile.AppGraph
    private val serviceScope = CoroutineScope(SupervisorJob() + Dispatchers.Main.immediate)
    private val mainHandler = Handler(Looper.getMainLooper())
    private val batteryMonitor by lazy { BatteryMonitor(this) }
    private val networkMonitor by lazy { NetworkMonitor(this) }

    private var projection: MediaProjection? = null
    private var virtualDisplay: VirtualDisplay? = null
    private var imageReader: ImageReader? = null
    private var captureThread: HandlerThread? = null
    private var captureHandler: Handler? = null
    private var overlay: OverlayController? = null

    @Volatile private var paused = false
    @Volatile private var quality = 70
    @Volatile private var targetFps = 10
    @Volatile private var maxEdge = 960
    @Volatile private var captureWidth = 0
    @Volatile private var captureHeight = 0
    @Volatile private var rotation = 0

    private var lastSentAt = 0L
    private var lastNotifyAt = 0L
    private var fpsWindowStart = 0L
    private var fpsFrames = 0
    private var stopping = false
    private var lastDisplayRestart = 0L
    private var displayListenerRegistered = false

    private val projectionCallback = object : MediaProjection.Callback() {
        override fun onStop() {
            if (!stopping) {
                graph.logs.log("系统或用户结束了投屏，截屏服务停止")
                stopCapture()
                stopSelf()
            }
        }
    }

    private val displayListener = object : DisplayManager.DisplayListener {
        override fun onDisplayAdded(displayId: Int) = Unit
        override fun onDisplayRemoved(displayId: Int) = Unit

        override fun onDisplayChanged(displayId: Int) {
            if (displayId != Display.DEFAULT_DISPLAY) return
            val now = SystemClock.elapsedRealtime()
            if (now - lastDisplayRestart < 800L) return
            lastDisplayRestart = now
            mainHandler.postDelayed({
                if (projection != null && !stopping && !paused) {
                    graph.logs.log("显示尺寸/旋转变化，重建虚拟显示")
                    teardownDisplay()
                    if (!setupCapture()) {
                        failAndStop("旋转后重建截屏失败")
                    } else {
                        publishStatus()
                    }
                }
            }, 450L)
        }
    }

    override fun onCreate() {
        super.onCreate()
        graph = (application as GoPlayApp).graph
        Notifications.ensureChannel(this)
        serviceScope.launch {
            graph.client.state.collect { state ->
                updateOverlayState(connected = state == CoreClient.State.CONNECTED)
            }
        }
    }

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        when (intent?.action) {
            ACTION_START -> handleStart(intent)
            ACTION_STOP -> {
                graph.logs.log("收到停止指令，关闭截屏")
                stopCapture()
                stopSelf()
            }
            ACTION_PAUSE -> pauseCapture()
            ACTION_RESUME -> resumeCapture()
            ACTION_RELOAD_SETTINGS -> reloadSettings()
            else -> if (projection == null) stopSelf()
        }
        return START_NOT_STICKY
    }

    override fun onBind(intent: Intent?): IBinder? = null

    override fun onDestroy() {
        stopCapture()
        serviceScope.cancel()
        super.onDestroy()
    }

    private fun handleStart(intent: Intent) {
        if (projection != null) {
            graph.logs.log("截屏服务已在运行，忽略重复启动")
            return
        }
        // The service was launched with startForegroundService(); call
        // startForeground() before any validation that could stopSelf() early.
        startForegroundCompat("正在启动截屏…", paused = false)
        stopping = false
        val resultCode = intent.getIntExtra(EXTRA_RESULT_CODE, 0)
        val data = projectionData(intent)
        if (data == null || resultCode == 0) {
            failAndStop("缺少录屏授权数据，请重新授权")
            return
        }

        val manager = getSystemService(MediaProjectionManager::class.java)
            ?: run {
                failAndStop("系统不支持 MediaProjection（服务不可用）")
                return
            }
        val newProjection = try {
            manager.getMediaProjection(resultCode, data)
        } catch (e: Exception) {
            graph.logs.error("获取 MediaProjection 失败", e)
            null
        }
        if (newProjection == null) {
            failAndStop("获取 MediaProjection 失败，请重新授权录屏")
            return
        }
        projection = newProjection
        newProjection.registerCallback(projectionCallback, mainHandler)

        reloadSettings(applyToDisplay = false)
        if (!setupCapture()) {
            failAndStop("创建虚拟显示失败")
            return
        }
        paused = false
        publishStatus()
        startForegroundCompat(describeCapture(), paused = false)
        showOverlay()
        updateOverlayState(connected = graph.client.isConnected)

        val displayManager = getSystemService(DisplayManager::class.java)
        if (!displayListenerRegistered && displayManager != null) {
            displayManager.registerDisplayListener(displayListener, mainHandler)
            displayListenerRegistered = true
        }
        graph.logs.log(
            "截屏开始 " + captureWidth + "×" + captureHeight + " q" + quality +
                " " + targetFps + "fps rotation=" + rotation,
        )
        graph.client.ensureConnected()
    }

    private fun projectionData(intent: Intent): Intent? {
        return if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU) {
            intent.getParcelableExtra(EXTRA_DATA, Intent::class.java)
        } else {
            @Suppress("DEPRECATION")
            intent.getParcelableExtra(EXTRA_DATA)
        }
    }

    private fun startForegroundCompat(text: String, paused: Boolean) {
        val notification = Notifications.buildCaptureNotification(this, text, paused)
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.Q) {
            startForeground(
                Notifications.NOTIFICATION_ID,
                notification,
                ServiceInfo.FOREGROUND_SERVICE_TYPE_MEDIA_PROJECTION,
            )
        } else {
            startForeground(Notifications.NOTIFICATION_ID, notification)
        }
    }

    private fun setupCapture(): Boolean {
        val projection = projection ?: return false
        val metrics = realDisplayMetrics()
        val screenWidth = metrics.widthPixels
        val screenHeight = metrics.heightPixels
        if (screenWidth < 16 || screenHeight < 16) {
            STATE.update { it.copy(error = "屏幕尺寸异常") }
            return false
        }
        rotation = currentRotation()
        val scale = min(1f, maxEdge.toFloat() / maxOf(screenWidth, screenHeight).toFloat())
        var width = (screenWidth * scale).roundToInt().coerceAtLeast(16)
        var height = (screenHeight * scale).roundToInt().coerceAtLeast(16)
        width = width - (width % 2)
        height = height - (height % 2)
        if (width < 16 || height < 16) return false

        val thread = HandlerThread("goplay-capture").also { it.start() }
        val handler = Handler(thread.looper)
        val reader = try {
            ImageReader.newInstance(width, height, PixelFormat.RGBA_8888, 2)
        } catch (e: Exception) {
            graph.logs.error("创建 ImageReader 失败", e)
            thread.quitSafely()
            return false
        }
        reader.setOnImageAvailableListener({ r -> onImageAvailable(r) }, handler)

        val density = resources.displayMetrics.densityDpi.coerceAtLeast(160)
        val display = try {
            projection.createVirtualDisplay(
                "GoPlayCapture",
                width,
                height,
                density,
                DisplayManager.VIRTUAL_DISPLAY_FLAG_AUTO_MIRROR,
                reader.surface,
                null,
                handler,
            )
        } catch (e: Exception) {
            graph.logs.error("创建 VirtualDisplay 失败", e)
            reader.close()
            thread.quitSafely()
            null
        }
        if (display == null) {
            STATE.update { it.copy(error = "创建虚拟显示失败") }
            return false
        }

        captureThread = thread
        captureHandler = handler
        imageReader = reader
        virtualDisplay = display
        captureWidth = width
        captureHeight = height
        lastSentAt = 0L
        fpsWindowStart = SystemClock.elapsedRealtime()
        fpsFrames = 0
        return true
    }

    private fun onImageAvailable(reader: ImageReader) {
        val image = try {
            reader.acquireLatestImage()
        } catch (e: Exception) {
            return
        } ?: return
        try {
            if (paused || !graph.client.isConnected) {
                graph.metrics.onFrameDropped()
                return
            }
            val now = SystemClock.elapsedRealtime()
            val interval = (1000L / targetFps.coerceAtLeast(1)).coerceAtLeast(1L)
            if (now - lastSentAt < interval) return
            val jpeg = FrameEncoder.encode(image, quality)
            if (jpeg == null) {
                graph.metrics.onFrameDropped()
                return
            }
            lastSentAt = now
            graph.metrics.onFrameCaptured(jpeg.size)
            val header = FrameHeader(
                capturedAtMs = System.currentTimeMillis(),
                width = captureWidth,
                height = captureHeight,
                quality = quality,
                rotation = rotationDegrees(rotation),
                sequence = 0L,
                source = "media_projection",
            )
            graph.client.sendFrame(jpeg, header)
            countFps(now)
            maybeUpdateNotification(now)
        } catch (e: Exception) {
            graph.metrics.onFrameDropped()
        } finally {
            try {
                image.close()
            } catch (_: Exception) {
                // reader already closed during shutdown
            }
        }
    }

    private fun countFps(now: Long) {
        fpsFrames++
        val elapsed = now - fpsWindowStart
        if (elapsed >= 1000L) {
            val fps = fpsFrames * 1000.0 / elapsed.toDouble()
            graph.metrics.updateFps(fps)
            fpsFrames = 0
            fpsWindowStart = now
        }
    }

    private fun maybeUpdateNotification(now: Long) {
        if (now - lastNotifyAt < 2000L) return
        lastNotifyAt = now
        updateNotification(describeCapture(), paused = false)
    }

    private fun describeCapture(): String {
        val fps = String.format(Locale.US, "%.1f", graph.metrics.snapshot().captureFps)
        return "AI 运行中 · " + captureWidth + "×" + captureHeight +
            " · JPEG q" + quality + " · " + fps + " fps" +
            " · " + batteryMonitor.status().label +
            " · " + networkMonitor.status().label
    }

    private fun publishStatus() {
        STATE.value = CaptureStatus(
            running = true,
            paused = paused,
            width = captureWidth,
            height = captureHeight,
            quality = quality,
            targetFps = targetFps,
        )
        graph.updateSession {
            it.copy(running = true, paused = paused)
        }
    }

    private fun pauseCapture() {
        if (projection == null || paused) return
        paused = true
        STATE.update { it.copy(paused = true) }
        graph.updateSession { it.copy(paused = true) }
        updateNotification(
            "AI 已暂停 · 截屏保留但不上传动作 · " +
                batteryMonitor.status().label + " · " + networkMonitor.status().label,
            paused = true,
        )
        updateOverlayState(connected = graph.client.isConnected)
        serviceScope.launch {
            val result = graph.rest.pause("overlay")
            graph.logs.log("REST pause: " + result.display())
        }
    }

    private fun resumeCapture() {
        if (projection == null || !paused) return
        paused = false
        lastSentAt = 0L
        STATE.update { it.copy(paused = false) }
        graph.updateSession { it.copy(paused = false) }
        updateNotification(describeCapture(), paused = false)
        updateOverlayState(connected = graph.client.isConnected)
        serviceScope.launch {
            val result = graph.rest.resume()
            graph.logs.log("REST resume: " + result.display())
        }
    }

    private fun reloadSettings(applyToDisplay: Boolean = true) {
        val settings: AppSettings = graph.settingsStore.load()
        val oldMaxEdge = maxEdge
        quality = settings.jpegQuality
        targetFps = settings.targetFps
        maxEdge = settings.maxEdge
        if (applyToDisplay && projection != null && oldMaxEdge != maxEdge) {
            graph.logs.log("截图长边变更 " + oldMaxEdge + " -> " + maxEdge + "，重建虚拟显示")
            teardownDisplay()
            if (!setupCapture()) {
                failAndStop("应用新设置时重建截屏失败")
                return
            }
            publishStatus()
        }
        if (projection != null) {
            updateOverlayState(connected = graph.client.isConnected)
            updateNotification(describeCapture(), paused = paused)
        }
    }

    private fun showOverlay() {
        if (!Settings.canDrawOverlays(this)) {
            graph.logs.log("未授予悬浮窗权限，截屏继续但不显示悬浮球")
            return
        }
        val controller = OverlayController(
            context = this,
            callbacks = object : OverlayController.Callbacks {
                override fun onPauseToggle() {
                    if (paused) resumeCapture() else pauseCapture()
                }

                override fun onStop() {
                    graph.logs.log("悬浮球停止")
                    stopCapture()
                    stopSelf()
                }

                override fun onMessage(text: String) {
                    serviceScope.launch {
                        val result = graph.rest.sendMessage(text)
                        graph.logs.log("悬浮球消息: " + result.display())
                    }
                }

                override fun onExpandedChanged(expanded: Boolean) {
                    if (expanded) graph.logs.log("悬浮球展开")
                }
            },
        )
        if (controller.show()) {
            overlay = controller
            updateOverlayState(connected = graph.client.isConnected)
        } else {
            graph.logs.log("悬浮球添加失败")
        }
    }

    private fun updateOverlayState(connected: Boolean) {
        overlay?.setRuntime(
            connected = connected,
            paused = paused,
            running = projection != null,
            accessibilityReady = com.goplay.mobile.accessibility.GameAccessibilityService.instance != null,
        )
    }

    private fun updateNotification(text: String, paused: Boolean) {
        Notifications.update(this, text, paused)
    }

    private fun realDisplayMetrics(): DisplayMetrics {
        val metrics = DisplayMetrics()
        @Suppress("DEPRECATION")
        getSystemService(WindowManager::class.java).defaultDisplay.getRealMetrics(metrics)
        return metrics
    }

    private fun currentRotation(): Int {
        @Suppress("DEPRECATION")
        return getSystemService(WindowManager::class.java).defaultDisplay.rotation
    }

    private fun rotationDegrees(surfaceRotation: Int): Int = when (surfaceRotation) {
        Surface.ROTATION_90 -> 90
        Surface.ROTATION_180 -> 180
        Surface.ROTATION_270 -> 270
        else -> 0
    }

    private fun failAndStop(message: String) {
        graph.logs.log(message)
        graph.updateSession { it.copy(lastError = message) }
        stopCapture()
        // stopCapture() resets CaptureStatus; keep the failure visible to the UI.
        STATE.update { it.copy(error = message) }
        stopSelf()
    }

    private fun stopCapture() {
        if (stopping) return
        stopping = true
        graph.logs.log("正在停止截屏与悬浮球")
        try {
            overlay?.destroy()
        } catch (_: Exception) {
        }
        overlay = null
        teardownDisplay()
        if (displayListenerRegistered) {
            try {
                getSystemService(DisplayManager::class.java).unregisterDisplayListener(displayListener)
            } catch (_: Exception) {
            }
            displayListenerRegistered = false
        }
        projection?.let { p ->
            try {
                p.unregisterCallback(projectionCallback)
            } catch (_: Exception) {
            }
            try {
                p.stop()
            } catch (_: Exception) {
            }
        }
        projection = null
        paused = false
        STATE.value = CaptureStatus()
        try {
            graph.client.disconnect()
        } catch (_: Exception) {
        }
        graph.updateSession {
            it.copy(
                running = false,
                paused = false,
                fps = 0.0,
            )
        }
        try {
            stopForeground(STOP_FOREGROUND_REMOVE)
        } catch (_: Exception) {
        }
    }

    private fun teardownDisplay() {
        try {
            virtualDisplay?.release()
        } catch (_: Exception) {
        }
        virtualDisplay = null
        try {
            imageReader?.close()
        } catch (_: Exception) {
        }
        imageReader = null
        captureThread?.quitSafely()
        captureThread = null
        captureHandler = null
        captureWidth = 0
        captureHeight = 0
    }

    companion object {
        const val ACTION_START = "com.goplay.mobile.action.START_CAPTURE"
        const val ACTION_STOP = "com.goplay.mobile.action.STOP_CAPTURE"
        const val ACTION_PAUSE = "com.goplay.mobile.action.PAUSE_CAPTURE"
        const val ACTION_RESUME = "com.goplay.mobile.action.RESUME_CAPTURE"
        const val ACTION_RELOAD_SETTINGS = "com.goplay.mobile.action.RELOAD_SETTINGS"

        const val EXTRA_RESULT_CODE = "result_code"
        const val EXTRA_DATA = "result_data"

        private val STATE = MutableStateFlow(CaptureStatus())
        val state: StateFlow<CaptureStatus> = STATE.asStateFlow()

        fun startIntent(context: Context, resultCode: Int, data: Intent): Intent =
            Intent(context, ScreenCaptureService::class.java).apply {
                action = ACTION_START
                putExtra(EXTRA_RESULT_CODE, resultCode)
                putExtra(EXTRA_DATA, data)
            }

        fun commandIntent(context: Context, action: String): Intent =
            Intent(context, ScreenCaptureService::class.java).apply { this.action = action }
    }
}
