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

    // Live AI evidence parsed from the core's periodic WebSocket snapshot. The
    // overlay must show what the AI is doing, not merely that sockets connect.
    val aiStatus: String = "unknown",
    val aiAction: String = "",
    val aiReason: String = "",
    val aiSource: String = "",
    val aiModel: String = "",
    val aiSummary: String = "",
    val aiOutcome: String? = null,
    val aiProgress: Double? = null,
    val aiProgressLabel: String = "",
    val aiDecisions: Long = 0,
    val aiActions: Long = 0,
    val aiErrors: Long = 0,
    val aiWaiting: Boolean = false,
    val aiWaitReason: String = "",
    val aiAt: Long = 0,
    val aiHuman: Boolean = false,
)
