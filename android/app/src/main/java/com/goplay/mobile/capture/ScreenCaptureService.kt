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
import com.goplay.mobile.MainActivity
import com.goplay.mobile.data.AppSettings
import com.goplay.mobile.data.BatteryMonitor
import com.goplay.mobile.data.NetworkMonitor
import com.goplay.mobile.data.SessionState
import com.goplay.mobile.net.CoreClient
import com.goplay.mobile.notifications.Notifications
import com.goplay.mobile.overlay.AiRuntime
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
    /** Non-null after capture stopped on its own; the pill stays on screen. */
    val error: String? = null,
    /** True while the floating pill is attached, including the degraded state. */
    val overlayVisible: Boolean = false,
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
    private var lastConnectAttempt = 0L
    private var fpsWindowStart = 0L
    private var fpsFrames = 0
    private var stopping = false
    private var lastDisplayRestart = 0L
    private var displayListenerRegistered = false

    /** True after the projection died but the service + pill were kept alive. */
    private var degraded = false

    /** Human-readable reason shown on the pill and in the notification. */
    private var lastStopReason: String? = null

    private val projectionCallback = object : MediaProjection.Callback() {
        override fun onStop() {
            // The system (or the user via the system UI) revoked the projection.
            // Keep the service, the WebSocket and the pill: destroying everything
            // made the island vanish with no way back short of reopening the app.
            if (!stopping) onProjectionLost("系统结束了投屏")
        }

        /**
         * Android 14+ tells us the captured content changed size. This is the
         * authoritative signal for rotation/foldables; handle it the same way
         * as a display change instead of creating a second VirtualDisplay.
         */
        override fun onCapturedContentResize(width: Int, height: Int) {
            if (!stopping) scheduleDisplayReconfigure("投屏内容尺寸变化 " + width + "x" + height)
        }
    }

    private val displayListener = object : DisplayManager.DisplayListener {
        override fun onDisplayAdded(displayId: Int) = Unit
        override fun onDisplayRemoved(displayId: Int) = Unit

        override fun onDisplayChanged(displayId: Int) {
            if (displayId != Display.DEFAULT_DISPLAY) return
            scheduleDisplayReconfigure("显示尺寸/旋转变化")
        }
    }

    /**
     * Coalesces the burst of display callbacks that arrives during a rotation
     * animation, then reconfigures the *existing* VirtualDisplay.
     *
     * A MediaProjection consent token may create only one VirtualDisplay. The
     * old code released that display and called createVirtualDisplay() again
     * with the same token, which always fails on Android 14 ("旋转后重建截屏
     * 失败") and made the recovery loop: consent -> game -> rotate -> fail.
     */
    private fun scheduleDisplayReconfigure(reason: String) {
        val now = SystemClock.elapsedRealtime()
        if (now - lastDisplayRestart < 800L) return
        lastDisplayRestart = now
        mainHandler.postDelayed({
            if (projection == null || stopping || paused) return@postDelayed
            if (reconfigureDisplay(reason)) {
                publishStatus()
                overlay?.setStatus(null, isError = false)
                updateOverlayState(connected = graph.client.isConnected)
            }
        }, 450L)
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
        // Snapshot-driven: the pill must reflect decisions even when the socket
        // state does not change (which is the common case while playing).
        serviceScope.launch {
            graph.session.collect { session ->
                overlay?.setAI(aiFrom(session))
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
            ACTION_EXPAND_OVERLAY -> expandOverlay()
            ACTION_RECOVER -> recoverCapture()
            // No action means a system restart (START_NOT_STICKY, so in practice
            // never) — only give up when there is nothing left worth keeping.
            else -> if (projection == null && overlay == null) stopSelf()
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
        degraded = false
        lastStopReason = null
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
        // Clear the "截屏已停止…" notice left over from a previous projection.
        overlay?.setStatus(null, isError = false)
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

    private data class DisplayTarget(val width: Int, val height: Int)

    /** Physical screen size, scaled down to maxEdge and aligned to even pixels. */
    private fun targetDisplaySize(): DisplayTarget? {
        val metrics = realDisplayMetrics()
        val screenWidth = metrics.widthPixels
        val screenHeight = metrics.heightPixels
        if (screenWidth < 16 || screenHeight < 16) return null
        val scale = min(1f, maxEdge.toFloat() / maxOf(screenWidth, screenHeight).toFloat())
        var width = (screenWidth * scale).roundToInt().coerceAtLeast(16)
        var height = (screenHeight * scale).roundToInt().coerceAtLeast(16)
        width = width - (width % 2)
        height = height - (height % 2)
        if (width < 16 || height < 16) return null
        return DisplayTarget(width, height)
    }

    private fun setupCapture(): Boolean {
        val projection = projection ?: return false
        val target = targetDisplaySize()
        if (target == null) {
            STATE.update { it.copy(error = "屏幕尺寸异常") }
            return false
        }
        rotation = currentRotation()
        val width = target.width
        val height = target.height

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

    /**
     * Resizes the running display without touching the MediaProjection token.
     *
     * Rotation must never call createVirtualDisplay() again: the consent token
     * is single-use. We resize the surface and, only if that works, swap in a
     * new ImageReader. Any failure keeps the previous reader and dimensions, so
     * the AI keeps receiving frames instead of stopping the capture entirely.
     */
    private fun reconfigureDisplay(reason: String): Boolean {
        val display = virtualDisplay ?: return false
        val target = targetDisplaySize() ?: return false
        val newRotation = currentRotation()
        if (target.width == captureWidth && target.height == captureHeight) {
            rotation = newRotation
            graph.logs.log(reason + "：尺寸未变 " + captureWidth + "x" + captureHeight + "，保持投屏")
            return true
        }

        val oldReader = imageReader
        val oldThread = captureThread
        val oldWidth = captureWidth
        val oldHeight = captureHeight
        val density = resources.displayMetrics.densityDpi.coerceAtLeast(160)
        val thread = HandlerThread("goplay-capture").also { it.start() }
        val handler = Handler(thread.looper)
        val reader = try {
            ImageReader.newInstance(target.width, target.height, PixelFormat.RGBA_8888, 2)
        } catch (e: Exception) {
            graph.logs.error(reason + "：创建新 ImageReader 失败，保持旧画面", e)
            thread.quitSafely()
            return false
        }
        reader.setOnImageAvailableListener({ r -> onImageAvailable(r) }, handler)

        val resized = try {
            display.resize(target.width, target.height, density)
            true
        } catch (e: Exception) {
            graph.logs.error(reason + "：调整虚拟显示尺寸失败，保持旧画面", e)
            false
        }
        if (!resized) {
            try {
                reader.close()
            } catch (_: Exception) {
            }
            thread.quitSafely()
            return false
        }

        val surfaceSet = try {
            display.setSurface(reader.surface)
            true
        } catch (e: Exception) {
            graph.logs.error(reason + "：切换画面 Surface 失败，回退旧尺寸", e)
            false
        }
        if (!surfaceSet) {
            try {
                display.resize(oldWidth, oldHeight, density)
            } catch (_: Exception) {
            }
            try {
                reader.close()
            } catch (_: Exception) {
            }
            thread.quitSafely()
            return false
        }

        imageReader = reader
        captureThread = thread
        captureHandler = handler
        captureWidth = target.width
        captureHeight = target.height
        rotation = newRotation
        lastSentAt = 0L
        fpsWindowStart = SystemClock.elapsedRealtime()
        fpsFrames = 0
        try {
            oldReader?.close()
        } catch (_: Exception) {
        }
        oldThread?.quitSafely()
        graph.logs.log(
            reason + "：投屏已调整为 " + target.width + "x" + target.height +
                " rotation=" + rotation,
        )
        return true
    }

    private fun onImageAvailable(reader: ImageReader) {
        val image = try {
            reader.acquireLatestImage()
        } catch (e: Exception) {
            return
        } ?: return
        try {
            val now = SystemClock.elapsedRealtime()
            // A long game session can outlive Wi-Fi, Doze or a WebSocket ping
            // timeout. Frames are the only reliable heartbeat we have, so use
            // them to nudge the client back instead of silently dropping every
            // frame for the rest of the session.
            if (!graph.client.isConnected && now - lastConnectAttempt >= 3_000L) {
                lastConnectAttempt = now
                graph.client.ensureConnected()
            }
            if (paused || !graph.client.isConnected) {
                graph.metrics.onFrameDropped()
                return
            }
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
            overlayVisible = overlay?.isShown == true,
        )
        graph.updateSession {
            it.copy(running = true, paused = paused, lastError = null)
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
            graph.logs.log("截图长边变更 " + oldMaxEdge + " -> " + maxEdge + "，调整虚拟显示")
            if (reconfigureDisplay("应用截图设置")) {
                publishStatus()
            }
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
        // A degraded service already owns a pill; re-adding the window would
        // leak the old one (two overlapping bubbles). Reuse it instead.
        val existing = overlay
        if (existing != null) {
            existing.setStatus(null, isError = false)
            updateOverlayState(connected = graph.client.isConnected)
            return
        }
        val controller = OverlayController(
            context = this,
            callbacks = object : OverlayController.Callbacks {
                override fun onPauseToggle() {
                    if (paused) resumeCapture() else pauseCapture()
                }

                override fun onStop() {
                    // Only a deliberate user stop tears everything down.
                    graph.logs.log("悬浮球停止（用户主动）")
                    stopCapture()
                    stopSelf()
                }

                override fun onRecoverCapture() {
                    mainHandler.post { recoverCapture() }
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

                override fun onOpenApp() {
                    try {
                        startActivity(
                            Intent(this@ScreenCaptureService, MainActivity::class.java).apply {
                                addFlags(
                                    Intent.FLAG_ACTIVITY_NEW_TASK or
                                        Intent.FLAG_ACTIVITY_SINGLE_TOP,
                                )
                                // The console's "面板" button is the natural entry
                                // point for fixing permissions.
                                putExtra(MainActivity.EXTRA_FOCUS, MainActivity.FOCUS_PERMISSIONS)
                            },
                        )
                    } catch (e: Exception) {
                        graph.logs.log("从悬浮球打开主界面失败: " + e.message)
                    }
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

    /**
     * Pill "recover" action. If the projection is still alive we can rebuild
     * the surface in place; only a genuinely revoked projection needs the
     * system consent dialog again.
     */
    private fun recoverCapture() {
        if (stopping) return
        if (projection != null && virtualDisplay != null) {
            if (reconfigureDisplay("手动恢复截屏")) {
                degraded = false
                lastStopReason = null
                paused = false
                publishStatus()
                overlay?.setStatus(null, isError = false)
                updateOverlayState(connected = graph.client.isConnected)
                updateNotification(describeCapture(), paused = false)
                graph.logs.log("截屏已原位恢复")
                return
            }
        }
        graph.logs.log("投屏授权已失效，打开主界面重新选择录屏范围")
        openMainForCapture()
    }

    private fun openMainForCapture() {
        try {
            startActivity(
                Intent(this, MainActivity::class.java).apply {
                    addFlags(
                        Intent.FLAG_ACTIVITY_NEW_TASK or
                            Intent.FLAG_ACTIVITY_SINGLE_TOP,
                    )
                    putExtra(MainActivity.EXTRA_START_CAPTURE, true)
                },
            )
        } catch (e: Exception) {
            graph.logs.log("从悬浮球恢复截屏失败: " + (e.message ?: e.javaClass.simpleName))
        }
    }

    private fun expandOverlay() {
        // Also honoured while degraded: the pill is exactly where the user has to
        // tap to restart capture, so recreating it here is required.
        if (overlay == null) showOverlay()
        if (overlay == null) {
            graph.logs.log("悬浮球不存在且无法创建，展开失败")
            return
        }
        overlay?.expand()
    }

    private fun updateOverlayState(connected: Boolean) {
        overlay?.setRuntime(
            connected = connected,
            paused = paused,
            running = projection != null,
            accessibilityReady = com.goplay.mobile.accessibility.GameAccessibilityService.instance != null,
        )
        overlay?.setAI(aiFrom(graph.session.value))
    }

    private fun aiFrom(session: SessionState): AiRuntime = AiRuntime(
        status = session.aiStatus,
        action = session.aiAction,
        reason = session.aiReason,
        source = session.aiSource,
        model = session.aiModel,
        summary = session.aiSummary,
        outcome = session.aiOutcome,
        progress = session.aiProgress,
        progressLabel = session.aiProgressLabel,
        decisions = session.aiDecisions,
        actions = session.aiActions,
        errors = session.aiErrors,
        waiting = session.aiWaiting,
        waitReason = session.aiWaitReason,
        human = session.aiHuman,
    )

    private fun updateNotification(
        text: String,
        paused: Boolean,
        titleOverride: String? = null,
    ) {
        Notifications.update(this, text, paused, titleOverride)
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

    /**
     * Capture failed (bad consent data, virtual display error, rotation rebuild
     * failure…). It deliberately does NOT stop the foreground service or remove
     * the overlay: the user keeps a "截屏已停止：<原因>，点开恢复" pill to retry from.
     * The name is kept because every failure call site already used it.
     */
    private fun failAndStop(message: String) {
        onProjectionLost(message)
    }

    /**
     * Tears down the projection but keeps the service, the WebSocket and the
     * floating pill alive.
     *
     * Android 14 checks the mediaProjection foreground-service type when
     * startForeground() runs, not continuously, so an already-started
     * mediaProjection FGS remains legal after the projection is revoked. Keeping
     * that one service (with a rewritten notification) is the smallest stable
     * recovery path: no second service, no orphaned window, no silent vanish.
     */
    private fun onProjectionLost(reason: String) {
        if (stopping) return
        if (projection == null && degraded) return
        degraded = true
        lastStopReason = reason
        graph.logs.log("截屏中断：" + reason + "（保留悬浮球与前台服务）")
        releaseProjection()
        graph.metrics.updateFps(0.0)
        STATE.value = CaptureStatus(
            running = false,
            paused = false,
            error = reason,
            overlayVisible = overlay?.isShown == true,
        )
        graph.updateSession {
            it.copy(running = false, paused = false, fps = 0.0, lastError = reason)
        }
        if (overlay == null) showOverlay()
        // overlayVisible was captured before the pill could be (re)created.
        STATE.update { it.copy(overlayVisible = overlay?.isShown == true) }
        val notice = "截屏已停止：" + reason + "，点开恢复"
        overlay?.setStatus(notice, isError = true)
        updateOverlayState(connected = graph.client.isConnected)
        updateNotification(
            notice + " · 点悬浮球恢复截屏",
            paused = false,
            titleOverride = "GoPlay · 截屏已停止",
        )
    }

    /** Drops the projection + display without touching the service/overlay. */
    private fun releaseProjection() {
        teardownDisplay()
        if (displayListenerRegistered) {
            try {
                getSystemService(DisplayManager::class.java).unregisterDisplayListener(displayListener)
            } catch (_: Exception) {
            }
            displayListenerRegistered = false
        }
        val old = projection
        projection = null
        paused = false
        if (old != null) {
            try {
                old.unregisterCallback(projectionCallback)
            } catch (_: Exception) {
            }
            // Harmless when the system already stopped it (that is the common
            // degraded path); still required when we abort during start-up.
            try {
                old.stop()
            } catch (_: Exception) {
            }
        }
    }

    /** Full teardown. Only a user-initiated stop or onDestroy calls this. */
    private fun stopCapture() {
        if (stopping) return
        stopping = true
        graph.logs.log("正在停止截屏与悬浮球")
        try {
            overlay?.destroy()
        } catch (_: Exception) {
        }
        overlay = null
        releaseProjection()
        degraded = false
        lastStopReason = null
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
        const val ACTION_EXPAND_OVERLAY = "com.goplay.mobile.action.EXPAND_OVERLAY"
        const val ACTION_RECOVER = "com.goplay.mobile.action.RECOVER_CAPTURE"

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