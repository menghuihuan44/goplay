package com.goplay.mobile.accessibility

import android.accessibilityservice.AccessibilityService
import android.accessibilityservice.GestureDescription
import android.graphics.Path
import android.graphics.Point
import android.os.Build
import android.os.Bundle
import android.os.Handler
import android.os.Looper
import android.os.SystemClock
import android.util.DisplayMetrics
import android.view.WindowManager
import android.view.accessibility.AccessibilityEvent
import android.view.accessibility.AccessibilityNodeInfo
import com.goplay.mobile.GoPlayApp
import com.goplay.mobile.protocol.GestureCommand
import java.util.concurrent.atomic.AtomicBoolean

data class ExecutionResult(
    val ok: Boolean,
    val errorCode: String? = null,
    val message: String? = null,
    val executedMs: Long = 0L,
)

/**
 * Performs gestures only through the Android accessibility API:
 * dispatchGesture for taps/swipes/long-presses, GLOBAL_ACTION_BACK for back and
 * ACTION_SET_TEXT on the focused editable node for text.
 *
 * It deliberately never walks the node tree to read other apps' content and
 * never injects code or touches another process's memory.
 */
class GameAccessibilityService : AccessibilityService() {

    private val mainHandler = Handler(Looper.getMainLooper())

    private val graph get() = (application as? GoPlayApp)?.graph

    /** Number of AI commands currently being dispatched. */
    private var aiActionsInFlight = 0

    /** Last AI action start/end, used to ignore injected-gesture echo events. */
    private var lastAiActionAt = 0L

    private var humanActive = false

    private val humanIdle = Runnable {
        if (!humanActive) return@Runnable
        humanActive = false
        mainHandler.removeCallbacks(humanHeartbeat)
        graph?.client?.sendHuman(false)
        graph?.logs?.log("用户操作停止约 2s，AI 可恢复控制")
    }

    /**
     * Keeps refreshing human=true while the user keeps interacting, so the core's
     * 2.5 s quiet timer cannot hand control back to the AI mid-interaction.
     */
    private val humanHeartbeat = object : Runnable {
        override fun run() {
            if (!humanActive) return
            graph?.client?.sendHuman(true)
            mainHandler.postDelayed(this, HUMAN_HEARTBEAT_MS)
        }
    }

    override fun onServiceConnected() {
        super.onServiceConnected()
        instance = this
        logs()?.log("无障碍服务已连接（手势能力=" + (serviceInfo?.canPerformGestures == true) + "）")
    }

    override fun onAccessibilityEvent(event: AccessibilityEvent?) {
        // We only look at the event type to detect real user input. We never read
        // event text/content or walk the node tree: frames come from MediaProjection.
        val eventType = event?.eventType ?: return
        if (!isHumanInteraction(eventType)) return
        if (aiActionsInFlight > 0) return
        if (SystemClock.elapsedRealtime() - lastAiActionAt < AI_ECHO_GRACE_MS) return
        onHumanInteraction()
    }

    override fun onInterrupt() {
        // Nothing to interrupt: gestures are short-lived.
        releaseHumanSignal()
    }

    override fun onUnbind(intent: android.content.Intent?): Boolean {
        releaseHumanSignal()
        instance = null
        logs()?.log("无障碍服务已断开")
        return super.onUnbind(intent)
    }

    override fun onDestroy() {
        releaseHumanSignal()
        instance = null
        super.onDestroy()
    }

    private fun isHumanInteraction(eventType: Int): Boolean = when (eventType) {
        AccessibilityEvent.TYPE_TOUCH_INTERACTION_START,
        AccessibilityEvent.TYPE_TOUCH_INTERACTION_END,
        AccessibilityEvent.TYPE_GESTURE_DETECTION_START,
        AccessibilityEvent.TYPE_GESTURE_DETECTION_END,
        AccessibilityEvent.TYPE_VIEW_CLICKED,
        AccessibilityEvent.TYPE_VIEW_LONG_CLICKED,
        AccessibilityEvent.TYPE_VIEW_SCROLLED,
        AccessibilityEvent.TYPE_VIEW_TEXT_CHANGED -> true

        else -> false
    }

    private fun onHumanInteraction() {
        if (!humanActive) {
            humanActive = true
            graph?.client?.sendHuman(true)
            graph?.logs?.log("检测到用户操作，通知核心交还控制")
            mainHandler.removeCallbacks(humanHeartbeat)
            mainHandler.postDelayed(humanHeartbeat, HUMAN_HEARTBEAT_MS)
        }
        mainHandler.removeCallbacks(humanIdle)
        mainHandler.postDelayed(humanIdle, HUMAN_IDLE_MS)
    }

    private fun releaseHumanSignal() {
        mainHandler.removeCallbacks(humanIdle)
        mainHandler.removeCallbacks(humanHeartbeat)
        if (humanActive) {
            humanActive = false
            graph?.client?.sendHuman(false)
        }
    }

    private fun beginAiAction() {
        aiActionsInFlight++
        lastAiActionAt = SystemClock.elapsedRealtime()
        if (humanActive) {
            // An AI action is about to run; stop heartbeats and explicitly release
            // the human flag instead of leaving the core waiting for its timeout.
            humanActive = false
            mainHandler.removeCallbacks(humanIdle)
            mainHandler.removeCallbacks(humanHeartbeat)
            graph?.client?.sendHuman(false)
            graph?.logs?.log("AI 动作开始，释放人工接管信号")
        }
    }

    private fun endAiAction() {
        if (aiActionsInFlight > 0) aiActionsInFlight--
        lastAiActionAt = SystemClock.elapsedRealtime()
    }

    /**
     * Execute a validated command. Must be called on the main thread; calls from
     * other threads are re-posted. [callback] is always invoked exactly once.
     */
    fun execute(command: GestureCommand, timeoutMs: Long, callback: (ExecutionResult) -> Unit) {
        if (Looper.myLooper() != Looper.getMainLooper()) {
            mainHandler.post { execute(command, timeoutMs, callback) }
            return
        }
        beginAiAction()
        val finish: (ExecutionResult) -> Unit = { result ->
            endAiAction()
            callback(result)
        }
        when (command) {
            is GestureCommand.Tap -> dispatchTouch(
                path = singlePointPath(command.x, command.y),
                durationMs = TAP_DURATION_MS,
                timeoutMs = timeoutMs,
                callback = finish,
            )

            is GestureCommand.LongPress -> dispatchTouch(
                path = singlePointPath(command.x, command.y),
                durationMs = command.durationMs,
                timeoutMs = timeoutMs,
                callback = finish,
            )

            is GestureCommand.Swipe -> {
                val (w, h) = screenSize()
                val path = Path().apply {
                    moveTo(toPx(command.x1, w), toPx(command.y1, h))
                    lineTo(toPx(command.x2, w), toPx(command.y2, h))
                }
                dispatchGesturePath(path, command.durationMs, timeoutMs, finish)
            }

            is GestureCommand.Back -> {
                val started = SystemClock.elapsedRealtime()
                val ok = performGlobalAction(GLOBAL_ACTION_BACK)
                finish(
                    if (ok) {
                        ExecutionResult(true, executedMs = SystemClock.elapsedRealtime() - started)
                    } else {
                        ExecutionResult(
                            false,
                            "back_failed",
                            "performGlobalAction(GLOBAL_ACTION_BACK) returned false",
                            SystemClock.elapsedRealtime() - started,
                        )
                    },
                )
            }

            is GestureCommand.Text -> setText(command.text, finish)

            is GestureCommand.Wait -> finish(
                ExecutionResult(
                    false,
                    "unsupported_here",
                    "wait is handled by ActionRouter",
                ),
            )
        }
    }

    private fun dispatchTouch(
        path: Path,
        durationMs: Long,
        timeoutMs: Long,
        callback: (ExecutionResult) -> Unit,
    ) {
        dispatchGesturePath(path, durationMs, timeoutMs, callback)
    }

    private fun dispatchGesturePath(
        path: Path,
        durationMs: Long,
        timeoutMs: Long,
        callback: (ExecutionResult) -> Unit,
    ) {
        val started = SystemClock.elapsedRealtime()
        val done = AtomicBoolean(false)
        val duration = durationMs.coerceIn(1L, 10_000L)

        fun finish(result: ExecutionResult) {
            if (done.compareAndSet(false, true)) callback(result)
        }

        val gesture = try {
            GestureDescription.Builder()
                .addStroke(GestureDescription.StrokeDescription(path, 0, duration))
                .build()
        } catch (e: Exception) {
            finish(
                ExecutionResult(
                    false,
                    "gesture_build_failed",
                    e.message,
                    SystemClock.elapsedRealtime() - started,
                ),
            )
            return
        }

        val accepted = try {
            dispatchGesture(
                gesture,
                object : GestureResultCallback() {
                    override fun onCompleted(gestureDescription: GestureDescription?) {
                        finish(
                            ExecutionResult(
                                true,
                                executedMs = SystemClock.elapsedRealtime() - started,
                            ),
                        )
                    }

                    override fun onCancelled(gestureDescription: GestureDescription?) {
                        finish(
                            ExecutionResult(
                                false,
                                "gesture_cancelled",
                                "Android cancelled the gesture",
                                SystemClock.elapsedRealtime() - started,
                            ),
                        )
                    }
                },
                mainHandler,
            )
        } catch (e: Exception) {
            false
        }

        if (!accepted) {
            finish(
                ExecutionResult(
                    false,
                    "dispatch_failed",
                    "dispatchGesture returned false (another gesture in progress or service disabled)",
                    SystemClock.elapsedRealtime() - started,
                ),
            )
            return
        }
        mainHandler.postDelayed(
            {
                finish(
                    ExecutionResult(
                        false,
                        "gesture_timeout",
                        "gesture callback did not complete in time",
                        SystemClock.elapsedRealtime() - started,
                    ),
                )
            },
            timeoutMs.coerceIn(1_000L, 15_000L),
        )
    }

    private fun setText(text: String, callback: (ExecutionResult) -> Unit) {
        val started = SystemClock.elapsedRealtime()
        val root = rootInActiveWindow
        val focused = root?.findFocus(AccessibilityNodeInfo.FOCUS_INPUT)
        if (focused == null) {
            callback(
                ExecutionResult(
                    false,
                    "no_focus",
                    "no focused editable field; tap the field first",
                    SystemClock.elapsedRealtime() - started,
                ),
            )
            return
        }
        val args = Bundle().apply {
            putCharSequence(
                AccessibilityNodeInfo.ACTION_ARGUMENT_SET_TEXT_CHARSEQUENCE,
                text,
            )
        }
        val ok = focused.performAction(AccessibilityNodeInfo.ACTION_SET_TEXT, args)
        callback(
            if (ok) {
                ExecutionResult(true, executedMs = SystemClock.elapsedRealtime() - started)
            } else {
                ExecutionResult(
                    false,
                    "set_text_failed",
                    "ACTION_SET_TEXT was rejected by the focused node",
                    SystemClock.elapsedRealtime() - started,
                )
            },
        )
    }

    private fun singlePointPath(x: Float, y: Float): Path {
        val (w, h) = screenSize()
        return Path().apply { moveTo(toPx(x, w), toPx(y, h)) }
    }

    private fun toPx(normalized: Float, size: Int): Float {
        if (size <= 2) return 1f
        return (normalized * size).coerceIn(1f, size - 2f)
    }

    private fun screenSize(): Pair<Int, Int> {
        val wm = getSystemService(WindowManager::class.java)
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.R) {
            val bounds = wm.currentWindowMetrics.bounds
            return Pair(bounds.width(), bounds.height())
        }
        val metrics = DisplayMetrics()
        @Suppress("DEPRECATION")
        wm.defaultDisplay.getRealMetrics(metrics)
        return Pair(metrics.widthPixels, metrics.heightPixels)
    }

    private fun logs() = (application as? GoPlayApp)?.graph?.logs

    companion object {
        private const val TAP_DURATION_MS = 60L

        /** Send human=false after this much user idle time. */
        private const val HUMAN_IDLE_MS = 2_000L

        /** Refresh human=true at this cadence while the user keeps interacting. */
        private const val HUMAN_HEARTBEAT_MS = 1_000L

        /** Ignore accessibility echoes of our own injected gestures just after they end. */
        private const val AI_ECHO_GRACE_MS = 400L

        /** Null until the user enables the service in Settings. */
        @Volatile
        var instance: GameAccessibilityService? = null
            private set
    }
}
