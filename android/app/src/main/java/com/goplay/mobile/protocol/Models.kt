package com.goplay.mobile.protocol

import kotlinx.serialization.Serializable
import kotlinx.serialization.json.JsonElement

/**
 * Wire protocol v1 constants shared by the Android client, the Go core and
 * android/PROTOCOL.md. Keep this object small: it is the single source of
 * truth for paths, headers and frame envelopes.
 */
object Protocol {
    const val VERSION = 1
    const val DEFAULT_PORT = 8420
    const val DEFAULT_INSTANCE_ID = "android-1"
    const val WS_PATH_PREFIX = "/ws/instance/"
    const val CLIENT_NAME = "android"

    const val HEADER_INSTANCE = "X-GoPlay-Instance"
    const val HEADER_CLIENT = "X-GoPlay-Client"
    const val HEADER_PROTOCOL = "X-GoPlay-Protocol"
    const val HEADER_WIFI_ONLY = "X-GoPlay-Wifi-Only"

    /** Binary frame magic, ASCII "GPF1" = GoPlay Frame v1. */
    const val FRAME_MAGIC = "GPF1"
    const val MAX_FRAME_HEADER_BYTES = 64 * 1024
    const val MAX_JPEG_BYTES = 8 * 1024 * 1024
    const val MAX_TEXT_CHARS = 2000
}

@Serializable
data class Capabilities(
    val screenshot: String = "jpeg",
    val gestures: List<String> = listOf("tap", "swipe", "long_press", "back", "text", "wait"),
    val maxFps: Int = 30,
)

/** First text message after the WebSocket opens. */
@Serializable
data class ClientHello(
    val type: String = "hello",
    val protocol: Int = Protocol.VERSION,
    val client: String = Protocol.CLIENT_NAME,
    val clientVersion: String = "0.1.0",
    val instanceId: String,
    val device: String,
    val capabilities: Capabilities = Capabilities(),
)

@Serializable
data class ActionSpec(
    val id: String? = null,
    val type: String,
    val x: Double? = null,
    val y: Double? = null,
    val x2: Double? = null,
    val y2: Double? = null,
    val durationMs: Long? = null,
    val text: String? = null,
    val timeoutMs: Long? = null,
    val note: String? = null,
)

@Serializable
data class ActionStats(
    val framesSent: Long = 0,
    val framesDropped: Long = 0,
    val actionsExecuted: Long = 0,
    val actionErrors: Long = 0,
    val wsReconnects: Long = 0,
    val captureFps: Double = 0.0,
    val lastFrameAtMs: Long = 0,
)

/**
 * A message received from the core. The core may send:
 * - {"type":"action","action":{...}}
 * - {"type":"tap", ...ActionSpec fields...} (permissive direct form)
 * - {"type":"ping"} / {"type":"stats", ...} / {"type":"chat", ...}
 */
@Serializable
data class ServerMessage(
    val type: String,
    val id: String? = null,
    val action: ActionSpec? = null,
    val stats: ActionStats? = null,
    val text: String? = null,
    val status: String? = null,
    val intervalMs: Long? = null,
    val error: String? = null,
    /** Full instance snapshot, present on type="snapshot" (schema owned by Go). */
    val instance: JsonElement? = null,
)

@Serializable
data class ActionAck(
    val type: String = "ack",
    val id: String,
    val ok: Boolean,
    val actionType: String? = null,
    val errorCode: String? = null,
    val error: String? = null,
    val executedMs: Long? = null,
)

@Serializable
data class Pong(
    val type: String = "pong",
    val ts: Long = System.currentTimeMillis(),
)

/**
 * Client -> core signal that a real user is interacting with the device. The
 * accessibility service sends active=true while user input is observed and
 * active=false after roughly two seconds of quiet. AI-dispatched gestures are
 * deliberately excluded so the core does not mistake them for human takeover.
 */
@Serializable
data class HumanSignal(
    val type: String = "human",
    val active: Boolean,
    val reason: String = "accessibility",
)

@Serializable
data class FrameHeader(
    val capturedAtMs: Long,
    val width: Int,
    val height: Int,
    val quality: Int,
    val rotation: Int,
    val sequence: Long,
    val source: String = "media_projection",
    val instanceId: String? = null,
    val bytes: Int? = null,
)

/** Request body for POST /api/message. */
@Serializable
data class MessageRequest(
    val instanceId: String,
    val text: String,
    val kind: String = "user",
)

/** Request body for POST /api/local/recommend. */
@Serializable
data class LocalRecommendRequest(
    val device: String,
    val ramMb: Long,
    val abis: List<String>,
)
