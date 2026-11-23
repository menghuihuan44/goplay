package com.goplay.mobile.net

import com.goplay.mobile.data.LogBus
import com.goplay.mobile.data.Metrics
import com.goplay.mobile.data.SettingsStore
import com.goplay.mobile.protocol.ActionAck
import com.goplay.mobile.protocol.ActionSpec
import com.goplay.mobile.protocol.BackoffPolicy
import com.goplay.mobile.protocol.ClientHello
import com.goplay.mobile.protocol.FrameCodec
import com.goplay.mobile.protocol.FrameHeader
import com.goplay.mobile.protocol.MessageCodec
import com.goplay.mobile.protocol.Protocol
import com.goplay.mobile.protocol.ServerMessage
import java.io.IOException
import java.util.concurrent.TimeUnit
import kotlinx.coroutines.CancellationException
import java.util.concurrent.atomic.AtomicBoolean
import java.util.concurrent.atomic.AtomicLong
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Job
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.isActive
import kotlinx.coroutines.launch
import kotlinx.coroutines.withTimeoutOrNull
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.Response
import okhttp3.WebSocket
import okhttp3.WebSocketListener
import okio.ByteString
import okio.ByteString.Companion.toByteString

/**
 * WebSocket client for one core instance.
 *
 * Lifecycle: [connect] starts a supervisor loop that keeps reconnecting with
 * exponential backoff until [disconnect]. JPEG frames are only sent while the
 * socket is open and the outgoing queue is empty; stale frames are dropped so a
 * slow LAN link cannot accumulate megabytes of images in memory.
 */
class CoreClient(
    private val settingsStore: SettingsStore,
    private val metrics: Metrics,
    private val logs: LogBus,
    private val scope: CoroutineScope,
    private val deviceSummary: String,
) {
    enum class State { IDLE, CONNECTING, CONNECTED, RECONNECTING, DISCONNECTED }

    private val _state = MutableStateFlow(State.IDLE)
    val state: StateFlow<State> = _state.asStateFlow()

    private val _lastError = MutableStateFlow<String?>(null)
    val lastError: StateFlow<String?> = _lastError.asStateFlow()

    /** Called on the OkHttp reader thread; handlers must be thread-safe. */
    @Volatile
    var actionHandler: ((ActionSpec) -> Unit)? = null

    /** Called for non-action server messages (chat/stats/errors). */
    @Volatile
    var onServerMessage: ((ServerMessage) -> Unit)? = null

    private val client = OkHttpClient.Builder()
        .connectTimeout(10, TimeUnit.SECONDS)
        .readTimeout(0, TimeUnit.MILLISECONDS)
        .pingInterval(15, TimeUnit.SECONDS)
        .retryOnConnectionFailure(false)
        .build()

    private val manualClose = AtomicBoolean(false)
    private val sequence = AtomicLong(0)
    private val backoff = BackoffPolicy()

    @Volatile
    private var webSocket: WebSocket? = null

    @Volatile
    private var ready = false

    @Volatile
    private var instanceId: String = Protocol.DEFAULT_INSTANCE_ID

    private var connectionJob: Job? = null

    val isConnected: Boolean get() = ready

    fun connect() {
        if (connectionJob?.isActive == true) {
            logs.log("WebSocket 已经在连接或运行中")
            return
        }
        manualClose.set(false)
        connectionJob = scope.launch { runLoop() }
    }

    fun disconnect() {
        manualClose.set(true)
        connectionJob?.cancel()
        connectionJob = null
        ready = false
        webSocket?.close(1000, "client stopped")
        webSocket = null
        _state.value = State.DISCONNECTED
        logs.log("WebSocket 已断开（手动）")
    }

    /** Reconnect only if a session is currently active; used after settings change. */
    fun reconnectIfActive() {
        val current = _state.value
        if (current == State.CONNECTING || current == State.CONNECTED || current == State.RECONNECTING) {
            disconnect()
            scope.launch {
                delay(150)
                connect()
            }
        }
    }

    fun ensureConnected() {
        if (!ready && connectionJob?.isActive != true) connect()
    }

    /**
     * Encode and enqueue one JPEG frame. Returns true when it was accepted by the
     * WebSocket. Encoding failures and a busy outgoing queue count as drops.
     */
    fun sendFrame(jpeg: ByteArray, header: FrameHeader): Boolean {
        if (!ready) {
            metrics.onFrameDropped()
            return false
        }
        val ws = webSocket
        if (ws == null || ws.queueSize() > 0) {
            metrics.onFrameDropped()
            return false
        }
        val payload = try {
            FrameCodec.encode(
                header.copy(
                    instanceId = instanceId,
                    sequence = sequence.incrementAndGet(),
                ),
                jpeg,
            )
        } catch (e: Exception) {
            logs.error("帧编码失败", e)
            metrics.onFrameDropped()
            return false
        }
        val accepted = try {
            ws.send(payload.toByteString())
        } catch (e: Exception) {
            logs.error("帧发送失败", e)
            false
        }
        if (accepted) {
            metrics.onFrameSent()
        } else {
            metrics.onFrameDropped()
        }
        return accepted
    }

    /**
     * Send a human-interaction signal. Returns false when the socket is not
     * ready; the accessibility service retries on the next observed event, and
     * the core also auto-resumes after its own quiet timeout.
     */
    fun sendHuman(active: Boolean): Boolean {
        val ws = webSocket
        if (ws == null || !ready) return false
        return try {
            ws.send(MessageCodec.encodeHuman(active))
        } catch (e: Exception) {
            logs.error("human 信号发送失败", e)
            false
        }
    }

    fun sendAck(ack: ActionAck) {
        val ws = webSocket
        if (ws == null || !ready) {
            logs.log("无法发送 ack（WebSocket 未连接）: " + ack.id)
            return
        }
        if (!ws.send(MessageCodec.encodeAck(ack))) {
            logs.log("ack 入队失败: " + ack.id)
        }
    }

    private fun sendPong() {
        webSocket?.send(MessageCodec.encodePong())
    }

    private suspend fun runLoop() {
        var attempt = 0
        while (scope.isActive && !manualClose.get()) {
            val settings = settingsStore.load()
            instanceId = settings.instanceId
            _state.value = if (attempt == 0) State.CONNECTING else State.RECONNECTING
            val openSignal = CompletableDeferred<Boolean>()
            val closeSignal = CompletableDeferred<String>()
            val hello = ClientHello(
                instanceId = settings.instanceId,
                device = deviceSummary,
            )
            val request = try {
                Request.Builder()
                    .url(settings.wsUrl)
                    .header(Protocol.HEADER_INSTANCE, settings.instanceId)
                    .header(Protocol.HEADER_CLIENT, Protocol.CLIENT_NAME)
                    .header(Protocol.HEADER_PROTOCOL, Protocol.VERSION.toString())
                    .header(Protocol.HEADER_WIFI_ONLY, if (settings.wifiOnlyDownloads) "1" else "0")
                    .build()
            } catch (e: IllegalArgumentException) {
                _lastError.value = "WebSocket URL 无效: " + settings.wsUrl
                logs.error("WebSocket URL 无效", e)
                _state.value = State.DISCONNECTED
                return
            }

            val listener = object : WebSocketListener() {
                override fun onOpen(webSocket: WebSocket, response: Response) {
                    logs.log("WebSocket 已连接: " + settings.wsUrl)
                    val helloText = MessageCodec.encodeHello(hello)
                    ready = webSocket.send(helloText)
                    if (!ready) {
                        _lastError.value = "hello 发送失败"
                        webSocket.close(1011, "hello failed")
                    }
                    if (!openSignal.isCompleted) openSignal.complete(ready)
                }

                override fun onMessage(webSocket: WebSocket, text: String) {
                    handleText(text)
                }

                override fun onMessage(webSocket: WebSocket, bytes: ByteString) {
                    logs.log("忽略服务器二进制消息：" + bytes.size + " bytes")
                }

                override fun onClosing(webSocket: WebSocket, code: Int, reason: String) {
                    webSocket.close(1000, null)
                    if (!closeSignal.isCompleted) {
                        closeSignal.complete("closing " + code + " " + reason)
                    }
                }

                override fun onClosed(webSocket: WebSocket, code: Int, reason: String) {
                    ready = false
                    if (!openSignal.isCompleted) openSignal.complete(false)
                    if (!closeSignal.isCompleted) {
                        closeSignal.complete("closed " + code + " " + reason)
                    }
                }

                override fun onFailure(webSocket: WebSocket, t: Throwable, response: Response?) {
                    ready = false
                    val message = t.message ?: t.javaClass.simpleName
                    _lastError.value = message
                    if (!openSignal.isCompleted) openSignal.complete(false)
                    if (!closeSignal.isCompleted) closeSignal.complete("failure: " + message)
                }
            }

            val ws = client.newWebSocket(request, listener)
            webSocket = ws
            val opened = try {
                withTimeoutOrNull(OPEN_TIMEOUT_MS) { openSignal.await() } ?: false
            } catch (e: CancellationException) {
                throw e
            } catch (e: Exception) {
                false
            }
            if (!opened) {
                ready = false
                ws.cancel()
                webSocket = null
                if (!closeSignal.isCompleted) closeSignal.complete("open timeout")
            } else {
                attempt = 0
                _state.value = State.CONNECTED
            }

            val closeReason = closeSignal.await()
            ready = false
            webSocket = null
            if (manualClose.get()) break
            if (!settingsStore.load().autoReconnect) {
                logs.log("断线自动重连已关闭，WebSocket 停止")
                _state.value = State.DISCONNECTED
                break
            }
            _lastError.value = closeReason
            logs.log("WebSocket 断开：" + closeReason)
            _state.value = State.RECONNECTING
            metrics.onReconnect()
            val waitMs = backoff.delayForAttempt(attempt)
            attempt++
            logs.log("将在 " + waitMs + " ms 后重连 WebSocket（第 " + attempt + " 次）")
            delay(waitMs)
        }
        ready = false
        webSocket = null
        _state.value = State.DISCONNECTED
    }

    private fun handleText(text: String) {
        val message = try {
            MessageCodec.parseServer(text)
        } catch (e: Exception) {
            logs.error("无法解析服务端消息", e)
            return
        }
        when (message.type) {
            "ping" -> sendPong()
            "action" -> {
                val action = message.action
                if (action == null) {
                    logs.error("action 消息缺少 action 对象")
                } else {
                    actionHandler?.invoke(action)
                }
            }

            "stats" -> onServerMessage?.invoke(message)
            "chat" -> {
                logs.log("核心消息：" + (message.text ?: ""))
                onServerMessage?.invoke(message)
            }

            "error" -> {
                _lastError.value = message.error ?: "core error"
                logs.error("核心错误：" + (message.error ?: ""))
                onServerMessage?.invoke(message)
            }

            else -> onServerMessage?.invoke(message)
        }
    }

    companion object {
        private const val OPEN_TIMEOUT_MS = 15_000L
    }
}
