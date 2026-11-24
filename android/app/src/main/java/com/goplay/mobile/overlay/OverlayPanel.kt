package com.goplay.mobile.overlay

import android.content.Context
import android.graphics.drawable.GradientDrawable
import android.text.InputType
import android.text.TextUtils
import android.view.Gravity
import android.view.MotionEvent
import android.view.View
import android.view.ViewConfiguration
import android.view.ViewGroup
import android.view.inputmethod.InputMethodManager
import android.widget.Button
import android.widget.EditText
import android.widget.FrameLayout
import android.widget.LinearLayout
import android.widget.TextView
import androidx.core.content.ContextCompat
import com.goplay.mobile.R
import kotlin.math.abs

/**
 * The floating control: a compact "dynamic island" pill that expands into a
 * small chat/control console. It is drawn entirely with normal Android views;
 * no game content is rendered or inspected here.
 */
class OverlayPanel(
    context: Context,
    private val callbacks: OverlayController.Callbacks,
) : FrameLayout(context) {

    private val collapsedWidth: Int = ViewGroup.LayoutParams.WRAP_CONTENT
    private val expandedWidthPx: Int = dp(300)

    private val column: LinearLayout
    private val header: LinearLayout
    private val dot: View
    private val title: TextView
    private val stateText: TextView
    private val chevron: TextView
    private val body: LinearLayout
    private val input: EditText
    private val pauseButton: Button
    private val panelButton: Button
    private val stopButton: Button

    /** Full-width "恢复截屏" action, only shown after the capture stopped by itself. */
    private val recoverButton: Button

    private var expanded = false

    /** Last runtime values, so [setStatus] can be undone without losing them. */
    private var lastConnected = false
    private var lastPaused = false
    private var lastRunning = false
    private var lastAccessibilityReady = false

    /** Non-null while an error/notice text replaces the runtime state line. */
    private var statusOverride: String? = null
    private var downRawX = 0f
    private var downRawY = 0f
    private var lastRawX = 0f
    private var lastRawY = 0f
    private var dragging = false
    private val touchSlop = ViewConfiguration.get(context).scaledTouchSlop

    /** Controller installs this; deltas are in screen pixels. */
    var onDrag: ((Float, Float) -> Unit)? = null

    /** Controller installs this after the window exists, to update flags/focus. */
    var onExpandedChanged: ((Boolean) -> Unit)? = null

    init {
        setBackground(panelBackground(expanded = false))
        clipToPadding = false
        // Never let the collapsed pill shrink to zero: a 0-sized overlay window is
        // invisible and untouchable, which used to make the island "blink and vanish".
        minimumWidth = dp(96)
        minimumHeight = dp(40)
        isClickable = true
        column = LinearLayout(context).apply {
            orientation = LinearLayout.VERTICAL
            setPadding(dp(12), dp(8), dp(12), dp(8))
        }

        header = LinearLayout(context).apply {
            orientation = LinearLayout.HORIZONTAL
            gravity = Gravity.CENTER_VERTICAL
            minimumHeight = dp(40)
            isClickable = true
        }
        dot = View(context).apply {
            layoutParams = LinearLayout.LayoutParams(dp(9), dp(9))
            background = dotBackground(ContextCompat.getColor(context, R.color.state_warn))
        }
        title = TextView(context).apply {
            text = "GoPlay"
            setTextColor(ContextCompat.getColor(context, R.color.text_primary))
            textSize = 13f
            typeface = android.graphics.Typeface.DEFAULT_BOLD
            setPadding(dp(7), 0, 0, 0)
        }
        stateText = TextView(context).apply {
            text = "准备中"
            setTextColor(ContextCompat.getColor(context, R.color.text_secondary))
            textSize = 10f
            maxLines = 2
            ellipsize = TextUtils.TruncateAt.END
            gravity = Gravity.END
            setPadding(dp(10), 0, 0, 0)
            // Collapsed: wrap content (weight 0) so the pill always shows its status
            // text — a weight-1 child inside a WRAP_CONTENT parent measured to 0 px.
            maxWidth = dp(170)
            layoutParams = LinearLayout.LayoutParams(
                ViewGroup.LayoutParams.WRAP_CONTENT,
                ViewGroup.LayoutParams.WRAP_CONTENT,
            )
        }
        chevron = TextView(context).apply {
            text = "⌄"
            setTextColor(ContextCompat.getColor(context, R.color.accent))
            textSize = 15f
            setPadding(dp(6), 0, 0, 0)
        }
        header.addView(dot)
        header.addView(title)
        header.addView(stateText)
        header.addView(chevron)
        column.addView(
            header,
            LinearLayout.LayoutParams(
                ViewGroup.LayoutParams.MATCH_PARENT,
                ViewGroup.LayoutParams.WRAP_CONTENT,
            ),
        )

        body = LinearLayout(context).apply {
            orientation = LinearLayout.VERTICAL
            visibility = GONE
            setPadding(0, dp(6), 0, 0)
        }
        val inputRow = LinearLayout(context).apply {
            orientation = LinearLayout.HORIZONTAL
            gravity = Gravity.CENTER_VERTICAL
        }
        input = EditText(context).apply {
            hint = "输入指令：稳一点 / 暂停 10 秒…"
            setHintTextColor(ContextCompat.getColor(context, R.color.text_muted))
            setTextColor(ContextCompat.getColor(context, R.color.text_primary))
            textSize = 12f
            inputType = InputType.TYPE_CLASS_TEXT or InputType.TYPE_TEXT_FLAG_MULTI_LINE
            maxLines = 3
            minLines = 2
            gravity = Gravity.TOP or Gravity.START
            setSingleLine(false)
            background = rounded(
                fill = ContextCompat.getColor(context, R.color.surface_2),
                radiusDp = 12f,
                stroke = ContextCompat.getColor(context, R.color.divider),
            )
            setPadding(dp(10), dp(7), dp(10), dp(7))
        }
        val send = Button(context).apply {
            text = "发送"
            isAllCaps = false
            textSize = 12f
            minHeight = dp(38)
            minimumHeight = dp(38)
            setTextColor(ContextCompat.getColor(context, R.color.on_accent))
            background = rounded(
                fill = ContextCompat.getColor(context, R.color.accent),
                radiusDp = 19f,
            )
            stateListAnimator = null
            setOnClickListener { sendMessage() }
        }
        inputRow.addView(
            input,
            LinearLayout.LayoutParams(0, ViewGroup.LayoutParams.WRAP_CONTENT, 1f),
        )
        inputRow.addView(
            send,
            LinearLayout.LayoutParams(
                ViewGroup.LayoutParams.WRAP_CONTENT,
                ViewGroup.LayoutParams.WRAP_CONTENT,
            ).apply { marginStart = dp(6) },
        )
        body.addView(
            inputRow,
            LinearLayout.LayoutParams(
                ViewGroup.LayoutParams.MATCH_PARENT,
                ViewGroup.LayoutParams.WRAP_CONTENT,
            ).apply { topMargin = dp(2) },
        )

        val actions = LinearLayout(context).apply {
            orientation = LinearLayout.HORIZONTAL
            gravity = Gravity.CENTER_VERTICAL
        }
        pauseButton = Button(context).apply {
            text = "暂停"
            isAllCaps = false
            textSize = 12f
            minHeight = dp(38)
            minimumHeight = dp(38)
            setTextColor(ContextCompat.getColor(context, R.color.accent))
            background = rounded(
                fill = ContextCompat.getColor(context, R.color.surface_2),
                radiusDp = 19f,
                stroke = ContextCompat.getColor(context, R.color.divider),
            )
            stateListAnimator = null
            setOnClickListener { callbacks.onPauseToggle() }
        }
        panelButton = Button(context).apply {
            text = "面板"
            isAllCaps = false
            textSize = 12f
            minHeight = dp(38)
            minimumHeight = dp(38)
            setTextColor(ContextCompat.getColor(context, R.color.text_primary))
            background = rounded(
                fill = ContextCompat.getColor(context, R.color.surface_2),
                radiusDp = 19f,
                stroke = ContextCompat.getColor(context, R.color.divider),
            )
            stateListAnimator = null
            setOnClickListener { callbacks.onOpenApp() }
        }
        stopButton = Button(context).apply {
            text = "停止"
            isAllCaps = false
            textSize = 12f
            minHeight = dp(38)
            minimumHeight = dp(38)
            setTextColor(ContextCompat.getColor(context, R.color.danger_text))
            background = rounded(
                fill = ContextCompat.getColor(context, R.color.danger),
                radiusDp = 19f,
            )
            stateListAnimator = null
            setOnClickListener { callbacks.onStop() }
        }
        recoverButton = Button(context).apply {
            text = "恢复截屏"
            isAllCaps = false
            textSize = 12f
            minHeight = dp(38)
            minimumHeight = dp(38)
            visibility = GONE
            setTextColor(ContextCompat.getColor(context, R.color.on_accent))
            background = rounded(
                fill = ContextCompat.getColor(context, R.color.accent),
                radiusDp = 19f,
            )
            stateListAnimator = null
            setOnClickListener { callbacks.onRecoverCapture() }
        }
        actions.addView(
            pauseButton,
            LinearLayout.LayoutParams(0, ViewGroup.LayoutParams.WRAP_CONTENT, 1f),
        )
        actions.addView(
            panelButton,
            LinearLayout.LayoutParams(0, ViewGroup.LayoutParams.WRAP_CONTENT, 1f).apply {
                marginStart = dp(6)
            },
        )
        actions.addView(
            stopButton,
            LinearLayout.LayoutParams(0, ViewGroup.LayoutParams.WRAP_CONTENT, 1f).apply {
                marginStart = dp(6)
            },
        )
        // Recovery lives on its own full-width row so it is impossible to miss
        // after the system killed the projection.
        body.addView(
            recoverButton,
            LinearLayout.LayoutParams(
                ViewGroup.LayoutParams.MATCH_PARENT,
                ViewGroup.LayoutParams.WRAP_CONTENT,
            ).apply { topMargin = dp(8) },
        )
        body.addView(
            actions,
            LinearLayout.LayoutParams(
                ViewGroup.LayoutParams.MATCH_PARENT,
                ViewGroup.LayoutParams.WRAP_CONTENT,
            ).apply { topMargin = dp(8) },
        )
        column.addView(
            body,
            LinearLayout.LayoutParams(
                ViewGroup.LayoutParams.MATCH_PARENT,
                ViewGroup.LayoutParams.WRAP_CONTENT,
            ),
        )
        addView(
            column,
            FrameLayout.LayoutParams(
                ViewGroup.LayoutParams.WRAP_CONTENT,
                ViewGroup.LayoutParams.WRAP_CONTENT,
            ),
        )
        installHeaderTouch()
    }

    private fun installHeaderTouch() {
        header.setOnTouchListener { _, event ->
            when (event.actionMasked) {
                MotionEvent.ACTION_DOWN -> {
                    downRawX = event.rawX
                    downRawY = event.rawY
                    lastRawX = event.rawX
                    lastRawY = event.rawY
                    dragging = false
                    true
                }

                MotionEvent.ACTION_MOVE -> {
                    val totalX = event.rawX - downRawX
                    val totalY = event.rawY - downRawY
                    if (!dragging && (abs(totalX) > touchSlop || abs(totalY) > touchSlop)) {
                        dragging = true
                    }
                    if (dragging) {
                        val dx = event.rawX - lastRawX
                        val dy = event.rawY - lastRawY
                        onDrag?.invoke(dx, dy)
                    }
                    lastRawX = event.rawX
                    lastRawY = event.rawY
                    true
                }

                MotionEvent.ACTION_UP -> {
                    if (!dragging) toggleExpanded()
                    true
                }

                MotionEvent.ACTION_CANCEL -> true
                else -> false
            }
        }
    }

    private fun sendMessage() {
        val text = input.text?.toString()?.trim().orEmpty()
        if (text.isEmpty()) return
        callbacks.onMessage(text)
        input.setText("")
    }

    fun toggleExpanded() {
        setExpanded(!expanded)
    }

    fun expand() {
        setExpanded(true)
    }

    fun setExpanded(value: Boolean) {
        if (expanded == value) return
        expanded = value
        body.visibility = if (value) VISIBLE else GONE
        title.text = if (value) "GoPlay 控制台" else "GoPlay"
        chevron.text = if (value) "⌃" else "⌄"
        background = panelBackground(value)
        val width = if (value) expandedWidthPx else collapsedWidth
        column.layoutParams = FrameLayout.LayoutParams(
            width,
            ViewGroup.LayoutParams.WRAP_CONTENT,
        )
        // Expanded: the status line shares the row with the title (weight 1).
        // Collapsed: it must size itself, otherwise the pill shows a 0 px label.
        stateText.layoutParams = if (value) {
            LinearLayout.LayoutParams(0, ViewGroup.LayoutParams.WRAP_CONTENT, 1f)
        } else {
            LinearLayout.LayoutParams(
                ViewGroup.LayoutParams.WRAP_CONTENT,
                ViewGroup.LayoutParams.WRAP_CONTENT,
            )
        }
        onExpandedChanged?.invoke(value)
        if (!value) hideKeyboard()
    }

    fun setRuntime(
        connected: Boolean,
        paused: Boolean,
        running: Boolean,
        accessibilityReady: Boolean,
    ) {
        lastConnected = connected
        lastPaused = paused
        lastRunning = running
        lastAccessibilityReady = accessibilityReady
        renderRuntime()
    }

    /**
     * Replaces the runtime state line with a notice such as
     * "截屏已停止：系统结束了投屏，点开恢复" and reveals the recovery action.
     * Pass text = null to go back to the normal runtime rendering.
     */
    fun setStatus(text: String?, isError: Boolean) {
        statusOverride = text
        if (text != null) {
            stateText.text = text
            stateText.setTextColor(
                ContextCompat.getColor(
                    context,
                    if (isError) R.color.state_bad else R.color.text_secondary,
                ),
            )
            recoverButton.visibility = if (isError || !lastRunning) VISIBLE else GONE
            // Nothing is capturing, so pausing is meaningless until recovery.
            pauseButton.isEnabled = false
            stopButton.isEnabled = true
            dot.background = dotBackground(
                ContextCompat.getColor(
                    context,
                    if (isError) R.color.state_bad else R.color.text_muted,
                ),
            )
        } else {
            recoverButton.visibility = GONE
            stateText.setTextColor(ContextCompat.getColor(context, R.color.text_secondary))
            renderRuntime()
        }
    }

    private fun renderRuntime() {
        if (statusOverride != null) return
        val color = when {
            !lastRunning -> R.color.text_muted
            lastPaused -> R.color.state_warn
            !lastAccessibilityReady -> R.color.state_warn
            lastConnected -> R.color.state_ok
            else -> R.color.state_bad
        }
        dot.background = dotBackground(ContextCompat.getColor(context, color))
        stateText.text = when {
            !lastRunning -> "未运行"
            lastPaused -> "已暂停"
            !lastAccessibilityReady -> "无障碍未开启"
            lastConnected -> "已连接"
            else -> "重连中"
        }
        pauseButton.text = if (lastPaused) "继续" else "暂停"
        pauseButton.isEnabled = lastRunning
        // Stop stays enabled even after the projection died: it is the only way to
        // dismiss the kept-alive pill.
        stopButton.isEnabled = true
        recoverButton.visibility = if (!lastRunning) VISIBLE else GONE
    }

    fun focusInput() {
        input.requestFocus()
        post {
            val imm = context.getSystemService(InputMethodManager::class.java) ?: return@post
            imm.showSoftInput(input, InputMethodManager.SHOW_IMPLICIT)
        }
    }

    fun hideKeyboard() {
        input.clearFocus()
        val imm = context.getSystemService(InputMethodManager::class.java) ?: return
        imm.hideSoftInputFromWindow(input.windowToken, 0)
    }

    val isExpanded: Boolean get() = expanded

    private fun panelBackground(expanded: Boolean): GradientDrawable = rounded(
        fill = ContextCompat.getColor(context, R.color.overlay_bg),
        radiusDp = if (expanded) 22f else 26f,
        stroke = ContextCompat.getColor(context, R.color.overlay_stroke),
    )

    private fun dotBackground(color: Int): GradientDrawable = GradientDrawable().apply {
        shape = GradientDrawable.OVAL
        setColor(color)
    }

    private fun rounded(
        fill: Int,
        radiusDp: Float,
        stroke: Int? = null,
    ): GradientDrawable = GradientDrawable().apply {
        shape = GradientDrawable.RECTANGLE
        cornerRadius = dp(radiusDp.toInt()).toFloat()
        setColor(fill)
        if (stroke != null) setStroke(dp(1).coerceAtLeast(1), stroke)
    }

    private fun dp(value: Int): Int =
        (value * resources.displayMetrics.density).toInt().coerceAtLeast(0)
}
