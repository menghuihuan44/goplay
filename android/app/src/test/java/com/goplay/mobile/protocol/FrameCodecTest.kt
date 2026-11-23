package com.goplay.mobile.protocol

import java.nio.ByteBuffer
import java.nio.ByteOrder
import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

class FrameCodecTest {

    private fun header(sequence: Long = 1L) = FrameHeader(
        capturedAtMs = 1_700_000_000_000L,
        width = 720,
        height = 1600,
        quality = 70,
        rotation = 0,
        sequence = sequence,
        instanceId = "android-1",
    )

    private fun jpeg(): ByteArray = byteArrayOf(
        0xFF.toByte(), 0xD8.toByte(), 0xFF.toByte(), 0xE0.toByte(),
        0x00, 0x10, 0x4A, 0x46, 0x49, 0x46, 0x00, 0x01,
        0xFF.toByte(), 0xD9.toByte(),
    )

    @Test
    fun roundTripPreservesHeaderAndJpeg() {
        val jpeg = jpeg()
        val encoded = FrameCodec.encode(header(7L), jpeg)

        assertEquals("GPF1", String(encoded, 0, 4, Charsets.US_ASCII))
        val decoded = FrameCodec.decode(encoded)
        assertEquals(7L, decoded.header.sequence)
        assertEquals(jpeg.size, decoded.header.bytes)
        assertEquals(header(7L).copy(bytes = jpeg.size), decoded.header)
        assertArrayEquals(jpeg, decoded.jpeg)
    }

    @Test
    fun emptyJpegIsRejected() {
        try {
            FrameCodec.encode(header(), ByteArray(0))
            throw AssertionError("expected IllegalArgumentException")
        } catch (expected: IllegalArgumentException) {
            assertTrue(expected.message!!.contains("empty"))
        }
    }

    @Test
    fun badMagicIsRejected() {
        val encoded = FrameCodec.encode(header(), jpeg())
        encoded[0] = 'X'.code.toByte()
        try {
            FrameCodec.decode(encoded)
            throw AssertionError("expected IllegalArgumentException")
        } catch (expected: IllegalArgumentException) {
            assertTrue(expected.message!!.contains("magic"))
        }
    }

    @Test
    fun hugeHeaderLengthIsRejected() {
        val malformed = ByteBuffer.allocate(9).order(ByteOrder.BIG_ENDIAN)
            .put(Protocol.FRAME_MAGIC.toByteArray(Charsets.US_ASCII))
            .putInt(Protocol.MAX_FRAME_HEADER_BYTES + 1)
            .put(0)
            .array()
        try {
            FrameCodec.decode(malformed)
            throw AssertionError("expected IllegalArgumentException")
        } catch (expected: IllegalArgumentException) {
            assertTrue(expected.message!!.contains("header length"))
        }
    }

    @Test
    fun missingPayloadIsRejected() {
        val headerJson = MessageCodec.json.encodeToString(FrameHeader.serializer(), header())
            .toByteArray(Charsets.UTF_8)
        val noPayload = ByteBuffer.allocate(8 + headerJson.size).order(ByteOrder.BIG_ENDIAN)
            .put(Protocol.FRAME_MAGIC.toByteArray(Charsets.US_ASCII))
            .putInt(headerJson.size)
            .put(headerJson)
            .array()
        try {
            FrameCodec.decode(noPayload)
            throw AssertionError("expected IllegalArgumentException")
        } catch (expected: IllegalArgumentException) {
            assertTrue(expected.message!!.contains("no jpeg"))
        }
    }

    @Test
    fun corruptedHeaderJsonIsRejected() {
        val badHeader = "{\"width\":".toByteArray(Charsets.UTF_8)
        val malformed = ByteBuffer.allocate(8 + badHeader.size + 1).order(ByteOrder.BIG_ENDIAN)
            .put(Protocol.FRAME_MAGIC.toByteArray(Charsets.US_ASCII))
            .putInt(badHeader.size)
            .put(badHeader)
            .put(0xFF.toByte())
            .array()
        try {
            FrameCodec.decode(malformed)
            throw AssertionError("expected IllegalArgumentException")
        } catch (expected: IllegalArgumentException) {
            assertTrue(expected.message!!.contains("header JSON"))
        }
    }
}
