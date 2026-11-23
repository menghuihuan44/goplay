package com.goplay.mobile.net

import com.goplay.mobile.data.AppSettings
import com.goplay.mobile.data.DeviceProfile
import com.goplay.mobile.data.LogBus
import com.goplay.mobile.data.SettingsStore
import com.goplay.mobile.protocol.LocalRecommendRequest
import com.goplay.mobile.protocol.MessageCodec
import com.goplay.mobile.protocol.MessageRequest
import com.goplay.mobile.protocol.Protocol
import java.util.concurrent.TimeUnit
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext
import kotlinx.serialization.Serializable
import okhttp3.MediaType.Companion.toMediaType
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.RequestBody.Companion.toRequestBody

@Serializable
data class ControlRequest(val instanceId: String, val reason: String? = null)

/**
 * HTTP REST side of the core API. WebSocket carries frames/actions; REST is used
 * for pause/resume, chat messages and local-model queries. Errors are returned
 * instead of thrown so the UI can render them.
 */
class CoreRest(
    private val settingsStore: SettingsStore,
    private val logs: LogBus,
) {
    data class ApiResult(
        val ok: Boolean,
        val status: Int = 0,
        val body: String? = null,
        val error: String? = null,
    ) {
        fun display(): String = when {
            ok -> body?.takeIf { it.isNotBlank() } ?: "OK"
            else -> "失败（HTTP " + status + "）：" + (error ?: "unknown error")
        }
    }

    private val client = OkHttpClient.Builder()
        .connectTimeout(5, TimeUnit.SECONDS)
        .readTimeout(10, TimeUnit.SECONDS)
        .callTimeout(15, TimeUnit.SECONDS)
        .retryOnConnectionFailure(true)
        .build()

    private val jsonMedia = "application/json; charset=utf-8".toMediaType()

    private fun settings(): AppSettings = settingsStore.load()

    suspend fun pause(reason: String = "overlay"): ApiResult {
        val instanceId = settings().instanceId
        val body = MessageCodec.json.encodeToString(
            ControlRequest.serializer(),
            ControlRequest(instanceId = instanceId, reason = reason),
        )
        return postJson("/api/control/pause", body)
    }

    suspend fun resume(): ApiResult {
        val body = MessageCodec.json.encodeToString(
            ControlRequest.serializer(),
            ControlRequest(instanceId = settings().instanceId),
        )
        return postJson("/api/control/resume", body)
    }

    suspend fun sendMessage(text: String): ApiResult {
        val trimmed = text.trim()
        if (trimmed.isEmpty()) return ApiResult(false, error = "消息为空")
        val body = MessageCodec.encodeMessageRequest(
            MessageRequest(instanceId = settings().instanceId, text = trimmed),
        )
        return postJson("/api/message", body)
    }

    suspend fun localStatus(): ApiResult = get("/api/local/status")

    suspend fun localRecommend(profile: DeviceProfile): ApiResult {
        val body = MessageCodec.encodeLocalRecommendRequest(
            LocalRecommendRequest(
                device = profile.summary,
                ramMb = profile.ramMb,
                abis = profile.abis,
            ),
        )
        return postJson("/api/local/recommend", body)
    }

    suspend fun health(): ApiResult = get("/health")

    private suspend fun get(path: String): ApiResult = withContext(Dispatchers.IO) {
        val current = settings()
        val request = Request.Builder()
            .url(current.httpBaseUrl + path)
            .header(Protocol.HEADER_WIFI_ONLY, if (current.wifiOnlyDownloads) "1" else "0")
            .get()
            .build()
        execute(request)
    }

    private suspend fun postJson(path: String, body: String): ApiResult =
        withContext(Dispatchers.IO) {
            val current = settings()
            val request = Request.Builder()
                .url(current.httpBaseUrl + path)
                .header(Protocol.HEADER_WIFI_ONLY, if (current.wifiOnlyDownloads) "1" else "0")
                .post(body.toRequestBody(jsonMedia))
                .build()
            execute(request)
        }

    private fun execute(request: Request): ApiResult {
        val label = request.method + " " + request.url
        return try {
            client.newCall(request).execute().use { response ->
                val text = response.body?.string()
                if (response.isSuccessful) {
                    logs.log("REST " + label + " -> " + response.code)
                    ApiResult(true, response.code, text)
                } else {
                    logs.log("REST " + label + " -> HTTP " + response.code)
                    ApiResult(
                        ok = false,
                        status = response.code,
                        body = text,
                        error = text?.take(300) ?: response.message,
                    )
                }
            }
        } catch (e: Exception) {
            logs.error("REST " + label + " 失败", e)
            ApiResult(false, error = e.message ?: e.javaClass.simpleName)
        }
    }
}
