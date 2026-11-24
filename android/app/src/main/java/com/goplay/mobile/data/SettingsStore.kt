package com.goplay.mobile.data

import android.content.Context
import android.content.SharedPreferences
import com.goplay.mobile.protocol.Protocol

data class SaveResult(
    val ok: Boolean,
    val error: String? = null,
    val settings: AppSettings? = null,
)

/**
 * Persists [AppSettings]. All values are clamped/validated here so no other
 * component has to trust raw user input.
 */
class SettingsStore(context: Context) {
    private val prefs: SharedPreferences =
        context.applicationContext.getSharedPreferences(PREFS_NAME, Context.MODE_PRIVATE)

    fun load(): AppSettings = AppSettings(
        host = prefs.getString(KEY_HOST, DEFAULT_HOST) ?: DEFAULT_HOST,
        port = prefs.getInt(KEY_PORT, Protocol.DEFAULT_PORT).coerceIn(1, 65535),
        instanceId = prefs.getString(KEY_INSTANCE, Protocol.DEFAULT_INSTANCE_ID)
            ?.takeIf { INSTANCE_ID.matches(it) } ?: Protocol.DEFAULT_INSTANCE_ID,
        jpegQuality = prefs.getInt(KEY_QUALITY, 70).coerceIn(10, 95),
        targetFps = prefs.getInt(KEY_FPS, 10).coerceIn(1, 30),
        maxEdge = prefs.getInt(KEY_MAX_EDGE, 960).coerceIn(320, 3840),
        wifiOnlyDownloads = prefs.getBoolean(KEY_WIFI_ONLY, true),
        autoReconnect = prefs.getBoolean(KEY_AUTO_RECONNECT, true),
        takeoverSensitivity = TakeoverSensitivity.normalize(
            prefs.getString(TakeoverSensitivity.PREFS_KEY, TakeoverSensitivity.HIGH),
        ),
    )

    /**
     * Writes one takeover sensitivity level. Returns the stored (normalized)
     * level, or null when [level] is not a known value, so the bridge can
     * reject bad input instead of silently downgrading it.
     */
    fun setTakeoverSensitivity(level: String): String? {
        if (!TakeoverSensitivity.isValid(level)) return null
        val normalized = TakeoverSensitivity.normalize(level)
        prefs.edit().putString(TakeoverSensitivity.PREFS_KEY, normalized).apply()
        return normalized
    }

    fun save(input: AppSettings): SaveResult {
        val host = normalizeHost(input.host)
            ?: return SaveResult(false, "核心地址无效：只填 IP 或主机名，不要带协议、路径和空格")
        val instanceId = input.instanceId.trim()
        if (!INSTANCE_ID.matches(instanceId)) {
            return SaveResult(
                false,
                "实例 ID 只能包含字母、数字、点、下划线和连字符，长度 1-64",
            )
        }
        val normalized = input.copy(
            host = host,
            port = input.port.coerceIn(1, 65535),
            instanceId = instanceId,
            jpegQuality = input.jpegQuality.coerceIn(10, 95),
            targetFps = input.targetFps.coerceIn(1, 30),
            maxEdge = input.maxEdge.coerceIn(320, 3840),
            takeoverSensitivity = TakeoverSensitivity.normalize(input.takeoverSensitivity),
        )
        prefs.edit()
            .putString(KEY_HOST, normalized.host)
            .putInt(KEY_PORT, normalized.port)
            .putString(KEY_INSTANCE, normalized.instanceId)
            .putInt(KEY_QUALITY, normalized.jpegQuality)
            .putInt(KEY_FPS, normalized.targetFps)
            .putInt(KEY_MAX_EDGE, normalized.maxEdge)
            .putBoolean(KEY_WIFI_ONLY, normalized.wifiOnlyDownloads)
            .putBoolean(KEY_AUTO_RECONNECT, normalized.autoReconnect)
            .putString(TakeoverSensitivity.PREFS_KEY, TakeoverSensitivity.normalize(normalized.takeoverSensitivity))
            .apply()
        return SaveResult(true, settings = normalized)
    }

    companion object {
        private const val PREFS_NAME = "goplay_settings"
        private const val KEY_HOST = "host"
        private const val KEY_PORT = "port"
        private const val KEY_INSTANCE = "instance"
        private const val KEY_QUALITY = "quality"
        private const val KEY_FPS = "fps"
        private const val KEY_MAX_EDGE = "max_edge"
        private const val KEY_WIFI_ONLY = "wifi_only"
        private const val KEY_AUTO_RECONNECT = "auto_reconnect"
        private const val DEFAULT_HOST = "192.168.1.100"

        private val HOST = Regex("^[A-Za-z0-9](?:[A-Za-z0-9._-]{0,252})$")
        private val INSTANCE_ID = Regex("^[A-Za-z0-9._-]{1,64}$")

        fun normalizeHost(raw: String): String? {
            var value = raw.trim()
            if (value.isEmpty()) return null
            for (prefix in listOf("wss://", "ws://", "https://", "http://")) {
                if (value.startsWith(prefix, ignoreCase = true)) {
                    value = value.substring(prefix.length)
                    break
                }
            }
            value = value.substringBefore('/').substringBefore('?').trim()
            if (!HOST.matches(value)) return null
            return value
        }
    }
}
