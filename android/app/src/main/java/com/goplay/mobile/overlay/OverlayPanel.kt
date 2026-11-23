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
    private val stopButton: Button

    private var expanded = false
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
        column = LinearLayout(context).apply {
            orientation = LinearLayout.VERTICAL
            setPadding(dp(12), dp(8), dp(12), dp(8))
        }

        header = LinearLayout(context).apply {
            orientation = LinearLayout.HORIZONTAL
            gravity = Gravity.CENTER_VERTICAL
            minimumHeight = dp(34)
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
            maxLines = 1
            ellipsize = TextUtils.TruncateAt.END
            gravity = Gravity.END
            setPadding(dp(10), 0, 0, 0)
            layoutParams = LinearLayout.LayoutParams(
                0,
                ViewGroup.LayoutParams.WRAP_CONTENT,
                1f,
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
            inputType = InputType.TYPE_CLASS_TEXT
            maxLines = 1
            setSingleLine(true)
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
        actions.addView(
            pauseButton,
            LinearLayout.LayoutParams(0, ViewGroup.LayoutParams.WRAP_CONTENT, 1f),
        )
        actions.addView(
            stopButton,
            LinearLayout.LayoutParams(0, ViewGroup.LayoutParams.WRAP_CONTENT, 1f).apply {
                marginStart = dp(8)
            },
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
        onExpandedChanged?.invoke(value)
        if (!value) hideKeyboard()
    }

    fun setRuntime(
        connected: Boolean,
        paused: Boolean,
        running: Boolean,
        accessibilityReady: Boolean,
    ) {
        val color = when {
            !running -> R.color.text_muted
            paused -> R.color.state_warn
            !accessibilityReady -> R.color.state_warn
            connected -> R.color.state_ok
            else -> R.color.state_bad
        }
        dot.background = dotBackground(ContextCompat.getColor(context, color))
        stateText.text = when {
            !running -> "未运行"
            paused -> "已暂停"
            !accessibilityReady -> "无障碍未开启"
            connected -> "已连接"
            else -> "重连中"
        }
        pauseButton.text = if (paused) "继续" else "暂停"
        pauseButton.isEnabled = running
        stopButton.isEnabled = running
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
