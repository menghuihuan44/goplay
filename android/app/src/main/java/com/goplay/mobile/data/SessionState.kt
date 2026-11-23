package com.goplay.mobile.data

/** Single app-wide session snapshot consumed by MainActivity and the overlay. */
data class SessionState(
    val running: Boolean = false,
    val paused: Boolean = false,
    val connected: Boolean = false,
    val wsState: String = "idle",
    val instanceStatus: String = "unknown",
    val fps: Double = 0.0,
    val lastError: String? = null,
    val accessibilityReady: Boolean = false,
)
