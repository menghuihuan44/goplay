package com.goplay.mobile.protocol

import java.nio.ByteBuffer
import java.nio.ByteOrder
import kotlinx.serialization.SerializationException

data class DecodedFrame(val header: FrameHeader, val jpeg: ByteArray)

/**
 * Binary WebSocket frame envelope:
 *
 *   offset 0  4 bytes  ASCII "GPF1"
 *   offset 4  4 bytes  unsigned big-endian header length N (max 64 KiB)
 *   offset 8  N bytes  UTF-8 JSON FrameHeader
 *   offset 8+N rest    raw JPEG bytes
 *
 * A frame is sent as one binary WebSocket message. This avoids base64 overhead
 * and keeps the image dimensions/quality attached to the exact JPEG.
 */
object FrameCodec {
    private val MAGIC = Protocol.FRAME_MAGIC.toByteArray(Charsets.US_ASCII)

    fun encode(header: FrameHeader, jpeg: ByteArray): ByteArray {
        require(jpeg.isNotEmpty()) { "jpeg payload is empty" }
        require(jpeg.size <= Protocol.MAX_JPEG_BYTES) {
            "jpeg payload too large: " + jpeg.size + " > " + Protocol.MAX_JPEG_BYTES
        }
        val effective = header.copy(bytes = jpeg.size)
        val headerBytes = MessageCodec.json
            .encodeToString(FrameHeader.serializer(), effective)
            .toByteArray(Charsets.UTF_8)
        require(headerBytes.size <= Protocol.MAX_FRAME_HEADER_BYTES) {
            "frame header too large: " + headerBytes.size
        }
        val buffer = ByteBuffer
            .allocate(MAGIC.size + 4 + headerBytes.size + jpeg.size)
            .order(ByteOrder.BIG_ENDIAN)
        buffer.put(MAGIC)
        buffer.putInt(headerBytes.size)
        buffer.put(headerBytes)
        buffer.put(jpeg)
        return buffer.array()
    }

    fun decode(bytes: ByteArray): DecodedFrame {
        require(bytes.size >= MAGIC.size + 4) { "frame too short: " + bytes.size }
        for (i in MAGIC.indices) {
            require(bytes[i] == MAGIC[i]) { "bad frame magic" }
        }
        val headerLength = ByteBuffer
            .wrap(bytes, MAGIC.size, 4)
            .order(ByteOrder.BIG_ENDIAN)
            .int
        require(headerLength in 0..Protocol.MAX_FRAME_HEADER_BYTES) {
            "invalid frame header length: " + headerLength
        }
        val payloadStart = MAGIC.size + 4 + headerLength
        require(payloadStart < bytes.size) { "frame has no jpeg payload" }
        require(bytes.size - payloadStart <= Protocol.MAX_JPEG_BYTES) {
            "jpeg payload too large: " + (bytes.size - payloadStart)
        }
        val headerJson = String(bytes, MAGIC.size + 4, headerLength, Charsets.UTF_8)
        val header = try {
            MessageCodec.json.decodeFromString(FrameHeader.serializer(), headerJson)
        } catch (e: SerializationException) {
            throw IllegalArgumentException("invalid frame header JSON", e)
        }
        val jpeg = bytes.copyOfRange(payloadStart, bytes.size)
        require(jpeg.isNotEmpty()) { "frame has no jpeg payload" }
        return DecodedFrame(header, jpeg)
    }
}
