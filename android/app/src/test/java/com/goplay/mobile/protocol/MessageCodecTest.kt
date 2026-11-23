package com.goplay.mobile.protocol

import kotlinx.serialization.SerializationException
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

class MessageCodecTest {

    @Test
    fun helloRoundTripsWithProtocolConstants() {
        val hello = ClientHello(instanceId = "android-7", device = "Pixel 8 / arm64-v8a / 8192 MB")
        val raw = MessageCodec.encodeHello(hello)
        val decoded = MessageCodec.json.decodeFromString(ClientHello.serializer(), raw)

        assertEquals("hello", decoded.type)
        assertEquals(Protocol.VERSION, decoded.protocol)
        assertEquals(Protocol.CLIENT_NAME, decoded.client)
        assertEquals("android-7", decoded.instanceId)
        assertEquals("jpeg", decoded.capabilities.screenshot)
        assertTrue(decoded.capabilities.gestures.contains("tap"))
        assertTrue(raw.contains("\"type\":\"hello\""))
    }

    @Test
    fun parsesWrappedActionMessage() {
        val raw = """{"type":"action","id":"a-1","action":{"id":"a-1","type":"tap","x":0.5,"y":0.25,"note":"menu"}}"""
        val message = MessageCodec.parseServer(raw)

        assertEquals("action", message.type)
        assertEquals("a-1", message.id)
        val action = message.action
        assertNotNull(action)
        assertEquals("tap", action!!.type)
        assertEquals(0.5, action.x!!, 1e-9)
        assertEquals(0.25, action.y!!, 1e-9)
        assertEquals("menu", action.note)
    }

    @Test
    fun wrappedActionInheritsWrapperIdForAckCorrelation() {
        val raw = """{"type":"action","id":"outer-7","action":{"type":"tap","x":0.5,"y":0.25}}"""
        val action = MessageCodec.parseAction(raw)

        assertNotNull(action)
        assertEquals("outer-7", action!!.id)
    }

    @Test
    fun parsesDirectActionForm() {
        val raw = """{"type":"swipe","id":"s-2","x":0.1,"y":0.2,"x2":0.9,"y2":0.8,"durationMs":420}"""
        val message = MessageCodec.parseServer(raw)

        assertEquals("action", message.type)
        assertEquals("s-2", message.id)
        val action = message.action!!
        assertEquals("swipe", action.type)
        assertEquals(0.9, action.x2!!, 1e-9)
        assertEquals(420L, action.durationMs)
    }

    @Test
    fun wrappedActionWithoutActionObjectFails() {
        try {
            MessageCodec.parseServer("""{"type":"action"}""")
            throw AssertionError("expected SerializationException")
        } catch (expected: SerializationException) {
            assertTrue(expected.message!!.contains("missing"))
        }
    }

    @Test
    fun parsesStatsMessage() {
        val raw = """{"type":"stats","stats":{"framesSent":12,"framesDropped":2,"actionsExecuted":5,"actionsErrors":0,"wsReconnects":1,"captureFps":9.5,"lastFrameAtMs":1700000000000}}"""
        // "actionsErrors" is an unknown key and must be ignored thanks to ignoreUnknownKeys.
        val message = MessageCodec.parseServer(raw)
        assertEquals("stats", message.type)
        assertEquals(12L, message.stats!!.framesSent)
        assertEquals(9.5, message.stats!!.captureFps, 1e-9)
    }

    @Test
    fun ackRoundTrips() {
        val ack = ActionAck(id = "a-9", ok = false, actionType = "tap", errorCode = "accessibility_unavailable", error = "service not enabled", executedMs = 3)
        val decoded = MessageCodec.json.decodeFromString(ActionAck.serializer(), MessageCodec.encodeAck(ack))
        assertEquals("ack", decoded.type)
        assertEquals("a-9", decoded.id)
        assertTrue(!decoded.ok)
        assertEquals("accessibility_unavailable", decoded.errorCode)
        assertEquals(3L, decoded.executedMs)
    }

    @Test
    fun humanSignalEncodesRequiredFields() {
        val raw = MessageCodec.encodeHuman(active = true)
        val decoded = MessageCodec.json.decodeFromString(HumanSignal.serializer(), raw)

        assertEquals("human", decoded.type)
        assertTrue(decoded.active)
        assertEquals("accessibility", decoded.reason)
        assertTrue(raw.contains("\"type\":\"human\""))
        assertTrue(raw.contains("\"active\":true"))
        assertFalse(raw.contains("\"active\":false"))
    }

    @Test
    fun pongCarriesTimestamp() {
        val pong = MessageCodec.json.decodeFromString(Pong.serializer(), MessageCodec.encodePong(Pong(ts = 1234L)))
        assertEquals("pong", pong.type)
        assertEquals(1234L, pong.ts)
    }

    @Test
    fun parseActionReturnsNullForNonActionMessage() {
        assertNull(MessageCodec.parseAction("""{"type":"ping"}"""))
    }

    @Test
    fun invalidJsonFails() {
        try {
            MessageCodec.parseServer("{not json")
            throw AssertionError("expected a parsing exception")
        } catch (expected: Exception) {
            assertTrue(expected is SerializationException || expected is IllegalArgumentException)
        }
    }
}
