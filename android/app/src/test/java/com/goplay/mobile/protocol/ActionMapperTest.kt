package com.goplay.mobile.protocol

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class ActionMapperTest {

    @Test
    fun mapsTapAndClick() {
        val tap = ActionMapper.map(ActionSpec(type = "tap", x = 0.5, y = 0.25))
        assertTrue(tap.ok)
        assertEquals(GestureCommand.Tap(0.5f, 0.25f), tap.command)

        val click = ActionMapper.map(ActionSpec(type = "CLICK", x = 0.0, y = 1.0))
        assertTrue(click.ok)
        assertEquals(GestureCommand.Tap(0.0f, 1.0f), click.command)
    }

    @Test
    fun mapsSwipeWithDefaultAndClampedDuration() {
        val swipe = ActionMapper.map(ActionSpec(type = "swipe", x = 0.1, y = 0.2, x2 = 0.9, y2 = 0.8))
        assertEquals(GestureCommand.Swipe(0.1f, 0.2f, 0.9f, 0.8f, 300L), swipe.command)

        val tooSlow = ActionMapper.map(
            ActionSpec(type = "swipe", x = 0.1, y = 0.2, x2 = 0.9, y2 = 0.8, durationMs = 99_999L),
        )
        assertEquals(10_000L, (tooSlow.command as GestureCommand.Swipe).durationMs)
    }

    @Test
    fun mapsLongPress() {
        val press = ActionMapper.map(ActionSpec(type = "long_press", x = 0.5, y = 0.5))
        assertEquals(GestureCommand.LongPress(0.5f, 0.5f, 700L), press.command)

        val clamped = ActionMapper.map(ActionSpec(type = "longpress", x = 0.5, y = 0.5, durationMs = 10L))
        assertEquals(300L, (clamped.command as GestureCommand.LongPress).durationMs)
    }

    @Test
    fun mapsBackTextAndWait() {
        assertEquals(GestureCommand.Back, ActionMapper.map(ActionSpec(type = "back")).command)
        assertEquals(GestureCommand.Text("hello"), ActionMapper.map(ActionSpec(type = "text", text = "hello")).command)
        assertEquals(GestureCommand.Wait(1000L), ActionMapper.map(ActionSpec(type = "sleep")).command)
        assertEquals(GestureCommand.Wait(0L), ActionMapper.map(ActionSpec(type = "wait", durationMs = -5)).command)
        assertEquals(GestureCommand.Wait(60_000L), ActionMapper.map(ActionSpec(type = "wait", durationMs = 99_999)).command)
    }

    @Test
    fun rejectsMissingOrOutOfRangeCoordinates() {
        assertEquals("invalid_coords", ActionMapper.map(ActionSpec(type = "tap", x = 0.5)).errorCode)
        assertEquals("invalid_coords", ActionMapper.map(ActionSpec(type = "tap", x = 1.5, y = 0.5)).errorCode)
        assertEquals("invalid_coords", ActionMapper.map(ActionSpec(type = "tap", x = Double.NaN, y = 0.5)).errorCode)
        assertEquals("invalid_coords", ActionMapper.map(ActionSpec(type = "swipe", x = 0.1, y = 0.1, x2 = 0.2)).errorCode)
    }

    @Test
    fun rejectsMissingTextAndOverlongText() {
        val missing = ActionMapper.map(ActionSpec(type = "text"))
        assertFalse(missing.ok)
        assertEquals("missing_field", missing.errorCode)

        val tooLong = ActionMapper.map(ActionSpec(type = "text", text = "x".repeat(Protocol.MAX_TEXT_CHARS + 1)))
        assertEquals("text_too_long", tooLong.errorCode)
    }

    @Test
    fun rejectsUnknownAction() {
        val result = ActionMapper.map(ActionSpec(type = "launch_missiles"))
        assertFalse(result.ok)
        assertEquals("unsupported_action", result.errorCode)
    }

    @Test
    fun acceptsBoundaryCoordinates() {
        assertTrue(ActionMapper.map(ActionSpec(type = "tap", x = 0.0, y = 0.0)).ok)
        assertTrue(ActionMapper.map(ActionSpec(type = "tap", x = 1.0, y = 1.0)).ok)
    }
}
