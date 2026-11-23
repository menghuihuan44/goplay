package com.goplay.mobile.protocol

import kotlinx.serialization.SerializationException
import kotlinx.serialization.json.Json
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.contentOrNull

/** JSON codec for protocol v1. Kotlin/JVM unit tests exercise these methods. */
object MessageCodec {
    val json: Json = Json {
        ignoreUnknownKeys = true
        encodeDefaults = true
        explicitNulls = false
        isLenient = false
    }

    private val ACTION_TYPE_ALIASES = setOf(
        "tap", "click", "swipe", "long_press", "longpress", "back", "text", "wait", "sleep",
    )

    fun encodeHello(hello: ClientHello): String =
        json.encodeToString(ClientHello.serializer(), hello)

    fun encodeAck(ack: ActionAck): String =
        json.encodeToString(ActionAck.serializer(), ack)

    fun encodePong(pong: Pong = Pong()): String =
        json.encodeToString(Pong.serializer(), pong)

    fun encodeHuman(active: Boolean): String =
        json.encodeToString(HumanSignal.serializer(), HumanSignal(active = active))

    fun encodeMessageRequest(request: MessageRequest): String =
        json.encodeToString(MessageRequest.serializer(), request)

    fun encodeLocalRecommendRequest(request: LocalRecommendRequest): String =
        json.encodeToString(LocalRecommendRequest.serializer(), request)

    /**
     * Parse a server text message, accepting both the wrapped action form and a
     * direct action object whose "type" is an action name. Throws
     * [SerializationException] or [IllegalArgumentException] when the message is
     * not valid JSON; callers decide whether to log or close the socket.
     */
    fun parseServer(raw: String): ServerMessage {
        val element = json.parseToJsonElement(raw)
        val obj = element as? JsonObject
            ?: throw SerializationException("server message must be a JSON object")
        val type = (obj["type"] as? JsonPrimitive)?.contentOrNull ?: ""
        if (type in ACTION_TYPE_ALIASES) {
            val action = json.decodeFromJsonElement(ActionSpec.serializer(), obj)
            return ServerMessage(type = "action", id = action.id, action = action)
        }
        val message = json.decodeFromJsonElement(ServerMessage.serializer(), obj)
        if (message.type == "action" && message.action == null) {
            throw SerializationException("action message is missing the action object")
        }
        val action = message.action
        if (message.type == "action" && action != null && action.id == null && message.id != null) {
            // The wrapper id is the correlation id; propagate it so the ack can
            // be matched even when the inner action object omitted its own id.
            return message.copy(action = action.copy(id = message.id))
        }
        return message
    }

    fun parseAction(raw: String): ActionSpec? = parseServer(raw).action
}
