package com.goplay.mobile.overlay

import android.content.Context
import android.content.SharedPreferences
import android.graphics.PixelFormat
import android.os.Build
import android.provider.Settings
import android.util.DisplayMetrics
import android.view.Gravity
import android.view.WindowManager
import com.goplay.mobile.input.InputGate
import kotlin.math.max

/**
 * Adds/removes the overlay window with TYPE_APPLICATION_OVERLAY and manages
 * focusability, dragging and persisted position.
 */
class OverlayController(
    private val context: Context,
    private val callbacks: Callbacks,
) : InputGate.Overlay {
    interface Callbacks {
        fun onPauseToggle()
        fun onStop()
        fun onMessage(text: String)
        fun onExpandedChanged(expanded: Boolean)
        fun onOpenApp()

        /** "恢复截屏": reopen the app so it can request MediaProjection again. */
        fun onRecoverCapture()
    }

    private val windowManager =
        context.getSystemService(WindowManager::class.java)
    private val prefs: SharedPreferences =
        context.getSharedPreferences(PREFS, Context.MODE_PRIVATE)
    private val panel = OverlayPanel(context, callbacks)
    private var params: WindowManager.LayoutParams? = null
    private var shown = false

    /** Mirrors [OverlayPanel.isExpanded]; read by [InputGate] from the AI side. */
    private var expanded = false

    /** True while an AI gesture is being dispatched; the window is not touchable. */
    private var aiGesture = false

    init {
        panel.onDrag = { dx, dy -> moveBy(dx, dy) }
        panel.onExpandedChanged = { expanded -> onExpanded(expanded) }
    }

    fun show(): Boolean {
        if (shown) return true
        if (!Settings.canDrawOverlays(context)) return false
        val layoutParams = WindowManager.LayoutParams(
            WindowManager.LayoutParams.WRAP_CONTENT,
            WindowManager.LayoutParams.WRAP_CONTENT,
            WindowManager.LayoutParams.TYPE_APPLICATION_OVERLAY,
            collapsedFlags(),
            PixelFormat.TRANSLUCENT,
        ).apply {
            gravity = Gravity.TOP or Gravity.START
            x = prefs.getInt(KEY_X, dp(14))
            y = prefs.getInt(KEY_Y, dp(110))
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.P) {
                layoutInDisplayCutoutMode =
                    WindowManager.LayoutParams.LAYOUT_IN_DISPLAY_CUTOUT_MODE_SHORT_EDGES
            }
            softInputMode = WindowManager.LayoutParams.SOFT_INPUT_STATE_ALWAYS_HIDDEN
        }
        return try {
            windowManager.addView(panel, layoutParams)
            params = layoutParams
            shown = true
            InputGate.attach(this)
            true
        } catch (e: Exception) {
            false
        }
    }

    fun setRuntime(
        connected: Boolean,
        paused: Boolean,
        running: Boolean,
        accessibilityReady: Boolean,
    ) {
        panel.setRuntime(connected, paused, running, accessibilityReady)
    }

    /** Mirrors the live AI snapshot into the island (state/action/progress). */
    fun setAI(info: AiRuntime) {
        panel.setAI(info)
    }

    /** Shows a notice (null clears it) such as "截屏已停止：<原因>，点开恢复". */
    fun setStatus(text: String?, isError: Boolean) {
        panel.setStatus(text, isError)
    }

    val isShown: Boolean get() = shown

    /** True while the island is expanded (the user may be typing there). */
    override val isExpanded: Boolean get() = expanded

    /**
     * Marks the short window in which the AI dispatches a gesture.
     *
     * While active the island window gets FLAG_NOT_TOUCHABLE so a tap/swipe that
     * the AI aimed at the game cannot land on the island (or its keyboard).
     * Ending the window restores exactly the flags that collapsible/expanded
     * state requires: expanded stays touchable and focusable, collapsed stays
     * NOT_FOCUSABLE.
     */
    override fun setAiGesture(active: Boolean) {
        if (aiGesture == active) return
        aiGesture = active
        applyWindowFlags()
    }

    /** Expand the pill into the compact console, e.g. after tapping 灵动岛. */
    fun expand() {
        if (!shown) return
        panel.expand()
    }

    fun destroy() {
        if (!shown) return
        shown = false
        InputGate.detach(this)
        aiGesture = false
        expanded = false
        savePosition()
        try {
            windowManager.removeView(panel)
        } catch (_: Exception) {
        }
        params = null
    }

    private fun moveBy(dx: Float, dy: Float) {
        val p = params ?: return
        val metrics = displayMetrics()
        val maxX = max(0, metrics.widthPixels - panel.width)
        val maxY = max(0, metrics.heightPixels - panel.height)
        p.x = (p.x + dx.toInt()).coerceIn(0, maxX)
        p.y = (p.y + dy.toInt()).coerceIn(0, maxY)
        try {
            windowManager.updateViewLayout(panel, p)
        } catch (_: Exception) {
        }
    }

    private fun onExpanded(expanded: Boolean) {
        this.expanded = expanded
        val p = params ?: return
        p.flags = flagsFor(expanded, aiGesture)
        p.softInputMode = if (expanded) {
            WindowManager.LayoutParams.SOFT_INPUT_ADJUST_RESIZE
        } else {
            WindowManager.LayoutParams.SOFT_INPUT_STATE_ALWAYS_HIDDEN
        }
        try {
            windowManager.updateViewLayout(panel, p)
        } catch (_: Exception) {
        }
        if (expanded) {
            // The flag change is applied asynchronously by WindowManager; focus
            // after it lands so the EditText can actually receive key events.
            panel.post { if (panel.isExpanded) panel.focusInput() }
        } else {
            panel.hideKeyboard()
        }
        callbacks.onExpandedChanged(expanded)
    }

    /**
     * Re-applies the current [expanded] + [aiGesture] flags to the window.
     * Called from the AI gesture window (main thread) and from expansion
     * changes, always deriving the flags from scratch so a missed end cannot
     * leave the island permanently untouchable.
     */
    private fun applyWindowFlags() {
        val p = params ?: return
        p.flags = flagsFor(expanded, aiGesture)
        try {
            windowManager.updateViewLayout(panel, p)
        } catch (_: Exception) {
        }
    }

    private fun flagsFor(expanded: Boolean, aiGesture: Boolean): Int {
        val base = if (expanded) expandedFlags() else collapsedFlags()
        return if (aiGesture) {
            base or WindowManager.LayoutParams.FLAG_NOT_TOUCHABLE
        } else {
            base
        }
    }

    private fun collapsedFlags(): Int =
        WindowManager.LayoutParams.FLAG_NOT_FOCUSABLE or
            WindowManager.LayoutParams.FLAG_NOT_TOUCH_MODAL or
            WindowManager.LayoutParams.FLAG_LAYOUT_IN_SCREEN

    private fun expandedFlags(): Int =
        WindowManager.LayoutParams.FLAG_NOT_TOUCH_MODAL or
            WindowManager.LayoutParams.FLAG_LAYOUT_IN_SCREEN

    private fun displayMetrics(): DisplayMetrics {
        val metrics = DisplayMetrics()
        @Suppress("DEPRECATION")
        windowManager.defaultDisplay.getRealMetrics(metrics)
        return metrics
    }

    private fun savePosition() {
        val p = params ?: return
        prefs.edit().putInt(KEY_X, p.x).putInt(KEY_Y, p.y).apply()
    }

    private fun dp(value: Int): Int =
        (value * context.resources.displayMetrics.density).toInt()

    companion object {
        private const val PREFS = "goplay_overlay"
        private const val KEY_X = "overlay_x"
        private const val KEY_Y = "overlay_y"
    }
}
