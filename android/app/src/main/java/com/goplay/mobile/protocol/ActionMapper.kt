package com.goplay.mobile.protocol

import java.util.Locale

/** A validated, screen-independent gesture request handled by the accessibility service. */
sealed class GestureCommand {
    data class Tap(val x: Float, val y: Float) : GestureCommand()
    data class Swipe(
        val x1: Float,
        val y1: Float,
        val x2: Float,
        val y2: Float,
        val durationMs: Long,
    ) : GestureCommand()

    data class LongPress(val x: Float, val y: Float, val durationMs: Long) : GestureCommand()
    data class Text(val text: String) : GestureCommand()
    data object Back : GestureCommand()
    data class Wait(val durationMs: Long) : GestureCommand()
}

data class ActionMapping(
    val command: GestureCommand? = null,
    val errorCode: String? = null,
    val error: String? = null,
) {
    val ok: Boolean get() = command != null
}

/**
 * Maps protocol actions to validated commands. Coordinates are normalized to
 * [0,1] relative to the current screen. Rejecting bad coordinates here keeps the
 * accessibility service from dispatching gestures at arbitrary pixels.
 */
object ActionMapper {
    fun map(spec: ActionSpec): ActionMapping {
        return when (spec.type.lowercase(Locale.US)) {
            "tap", "click" -> {
                val x = coord(spec.x) ?: return badCoord("x")
                val y = coord(spec.y) ?: return badCoord("y")
                ActionMapping(GestureCommand.Tap(x, y))
            }

            "swipe" -> {
                val x1 = coord(spec.x) ?: return badCoord("x")
                val y1 = coord(spec.y) ?: return badCoord("y")
                val x2 = coord(spec.x2) ?: return badCoord("x2")
                val y2 = coord(spec.y2) ?: return badCoord("y2")
                val duration = (spec.durationMs ?: 300L).coerceIn(50L, 10_000L)
                ActionMapping(GestureCommand.Swipe(x1, y1, x2, y2, duration))
            }

            "long_press", "longpress" -> {
                val x = coord(spec.x) ?: return badCoord("x")
                val y = coord(spec.y) ?: return badCoord("y")
                val duration = (spec.durationMs ?: 700L).coerceIn(300L, 5_000L)
                ActionMapping(GestureCommand.LongPress(x, y, duration))
            }

            "back" -> ActionMapping(GestureCommand.Back)

            "text" -> {
                val text = spec.text
                if (text == null) {
                    return ActionMapping(errorCode = "missing_field", error = "text is required")
                }
                if (text.length > Protocol.MAX_TEXT_CHARS) {
                    return ActionMapping(
                        errorCode = "text_too_long",
                        error = "text length " + text.length + " exceeds " + Protocol.MAX_TEXT_CHARS,
                    )
                }
                ActionMapping(GestureCommand.Text(text))
            }

            "wait", "sleep" -> {
                val duration = (spec.durationMs ?: 1_000L).coerceIn(0L, 60_000L)
                ActionMapping(GestureCommand.Wait(duration))
            }

            else -> ActionMapping(
                errorCode = "unsupported_action",
                error = "unsupported action type: " + spec.type,
            )
        }
    }

    private fun coord(value: Double?): Float? {
        if (value == null || !value.isFinite()) return null
        if (value < 0.0 || value > 1.0) return null
        return value.toFloat()
    }

    private fun badCoord(name: String) = ActionMapping(
        errorCode = "invalid_coords",
        error = name + " must be a normalized finite coordinate in [0,1]",
    )
}
