package com.goplay.mobile.data

import com.goplay.mobile.protocol.Protocol

/** User-editable connection and capture settings, persisted in SharedPreferences. */
data class AppSettings(
    val host: String = "192.168.1.100",
    val port: Int = Protocol.DEFAULT_PORT,
    val instanceId: String = Protocol.DEFAULT_INSTANCE_ID,
    val jpegQuality: Int = 70,
    val targetFps: Int = 10,
    val maxEdge: Int = 960,
    val wifiOnlyDownloads: Boolean = true,
    val autoReconnect: Boolean = true,
    /** Human-takeover sensitivity, see [TakeoverSensitivity]. */
    val takeoverSensitivity: String = TakeoverSensitivity.HIGH,
    /**
     * Package of the game last launched from the console. The accessibility
     * service refuses to inject gestures into any other foreground package.
     * Empty = unknown (only GoPlay itself and keyboards are rejected).
     */
    val activeGamePackage: String = "",
) {
    val wsUrl: String
        get() = "ws://" + host + ":" + port + Protocol.WS_PATH_PREFIX + instanceId

    val httpBaseUrl: String
        get() = "http://" + host + ":" + port
}
