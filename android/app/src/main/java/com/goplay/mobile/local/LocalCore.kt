package com.goplay.mobile.local

import android.content.Context
import com.goplay.mobile.data.LogBus
import com.goplay.mobile.data.SettingsStore
import java.io.File
import java.net.HttpURLConnection
import java.net.InetAddress
import java.net.ServerSocket
import java.net.URL
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext

/**
 * Starts the bundled Go core (jniLibs/arm64-v8a/libgoplaycore.so) as a local
 * child process. The core binds to 127.0.0.1 on a free port and serves the
 * same HTTP/WebSocket API as the desktop daemon.
 *
 * The binary is a native library only because Android installs those with the
 * executable bit and keeps them in a read-only, executable location. It is not
 * loaded into this process; it runs as a separate process, like goplayd on PC.
 */
class LocalCore(
    private val context: Context,
    private val settingsStore: SettingsStore,
    private val logs: LogBus,
) {
    data class State(
        val starting: Boolean = false,
        val ready: Boolean = false,
        val port: Int = 0,
        val pid: Long = 0L,
        val error: String? = null,
        val logPath: String = "",
        val version: String = "",
    )

    private val _state = MutableStateFlow(State())
    val state: StateFlow<State> = _state.asStateFlow()

    @Volatile
    private var process: Process? = null
    private var job: Job? = null

    val isReady: Boolean get() = _state.value.ready
    val port: Int get() = _state.value.port

    @Synchronized
    fun start(scope: CoroutineScope) {
        if (_state.value.ready || job?.isActive == true) return
        job = scope.launch(Dispatchers.IO) { boot() }
    }

    private suspend fun boot() {
        val nativeDir = File(context.applicationInfo.nativeLibraryDir)
        val binary = File(nativeDir, "libgoplaycore.so")
        val llama = File(nativeDir, "libllama-server.so")
        val dataDir = File(context.filesDir, "core").also { it.mkdirs() }
        val logFile = File(context.filesDir, "core.log")

        _state.value = State(starting = true, logPath = logFile.absolutePath)
        if (!binary.isFile) {
            fail("找不到本机核心 " + binary.absolutePath)
            return
        }
        if (!binary.canExecute()) {
            fail("本机核心不可执行：" + binary.absolutePath + "（请用包含 extractNativeLibs 的 APK 安装）")
            return
        }

        var lastError = ""
        repeat(PORT_ATTEMPTS) { attempt ->
            val port = freePort()
            val command = mutableListOf(
                binary.absolutePath,
                "-addr", "127.0.0.1:" + port,
                "-data", dataDir.absolutePath,
                "-v",
            )
            val builder = ProcessBuilder(command)
                .directory(dataDir)
                .redirectErrorStream(true)
                .redirectOutput(ProcessBuilder.Redirect.appendTo(logFile))
            val env = builder.environment()
            if (llama.isFile) {
                env["GOPLAY_LLAMA_SERVER"] = llama.absolutePath
            }
            // Hugging Face is frequently unreachable from mainland China. The
            // core honours this override for model downloads; users can still
            // switch back by editing config.json.
            if (env["GOPLAY_HF_ENDPOINT"].isNullOrBlank()) {
                env["GOPLAY_HF_ENDPOINT"] = "https://hf-mirror.com"
            }
            env["HOME"] = context.filesDir.absolutePath
            env["TMPDIR"] = context.cacheDir.absolutePath

            val proc = try {
                builder.start()
            } catch (e: Exception) {
                fail("启动本机核心失败：" + (e.message ?: e.javaClass.simpleName))
                return
            }
            process = proc
            logs.log("本机核心已启动 port=" + port)
            _state.value = State(
                starting = true,
                port = port,
                logPath = logFile.absolutePath,
            )

            val ready = waitForHealth(port, proc)
            if (ready) {
                val current = settingsStore.load()
                val saved = settingsStore.save(
                    current.copy(host = "127.0.0.1", port = port),
                )
                if (!saved.ok) {
                    logs.error("保存本机核心地址失败：" + (saved.error ?: ""), null)
                }
                _state.value = State(
                    ready = true,
                    port = port,
                    logPath = logFile.absolutePath,
                    version = readVersion(port),
                )
                logs.log("本机核心就绪 127.0.0.1:" + port)
                return
            }

            lastError = "本机核心未在限定时间内响应（端口 " + port + "）"
            logs.log(lastError)
            try {
                proc.destroy()
            } catch (_: Exception) {
            }
            process = null
            if (attempt < PORT_ATTEMPTS - 1) delay(400L)
        }
        fail(lastError.ifBlank { "本机核心启动失败" })
    }

    private suspend fun waitForHealth(port: Int, proc: Process): Boolean {
        repeat(HEALTH_ATTEMPTS) {
            if (!proc.isAlive) return false
            if (health(port)) return true
            delay(HEALTH_INTERVAL_MS)
        }
        return false
    }

    private fun health(port: Int): Boolean = try {
        val conn = URL("http://127.0.0.1:" + port + "/health").openConnection() as HttpURLConnection
        conn.connectTimeout = 600
        conn.readTimeout = 600
        conn.requestMethod = "GET"
        val code = conn.responseCode
        try {
            conn.inputStream.close()
        } catch (_: Exception) {
        }
        conn.disconnect()
        code in 200..299
    } catch (_: Exception) {
        false
    }

    private fun readVersion(port: Int): String = try {
        val conn = URL("http://127.0.0.1:" + port + "/health").openConnection() as HttpURLConnection
        conn.connectTimeout = 800
        conn.readTimeout = 800
        val body = conn.inputStream.bufferedReader().use { it.readText() }
        conn.disconnect()
        val match = Regex("\"version\"\\s*:\\s*\"([^\"]+)\"").find(body)
        match?.groupValues?.getOrNull(1) ?: ""
    } catch (_: Exception) {
        ""
    }

    private fun freePort(): Int = ServerSocket(0, 1, InetAddress.getByName("127.0.0.1")).use {
        it.localPort
    }

    private fun fail(message: String) {
        logs.error(message, null)
        _state.value = _state.value.copy(starting = false, ready = false, error = message)
    }

    fun stop() {
        try {
            process?.destroy()
        } catch (_: Exception) {
        }
        process = null
        _state.value = _state.value.copy(starting = false, ready = false)
    }

    companion object {
        private const val PORT_ATTEMPTS = 2
        private const val HEALTH_ATTEMPTS = 75
        private const val HEALTH_INTERVAL_MS = 200L
    }
}
