package com.goplay.mobile.overlay

/**
 * Live view of what the AI is doing, mirrored from the core snapshot so the
 * floating island can answer "is it actually playing, and how is it going?"
 * instead of only showing that the socket is connected.
 */
data class AiRuntime(
    val status: String = "unknown",
    val action: String = "",
    val reason: String = "",
    val source: String = "",
    val model: String = "",
    val summary: String = "",
    val outcome: String? = null,
    val progress: Double? = null,
    val progressLabel: String = "",
    val decisions: Long = 0,
    val actions: Long = 0,
    val errors: Long = 0,
    val waiting: Boolean = false,
    val waitReason: String = "",
    val human: Boolean = false,
    // Real voice facts from VoiceBridge; empty/false means "no data", never a guess.
    val listening: Boolean = false,
    val speaking: Boolean = false,
    val lastHeard: String = "",
    val lastSpoken: String = "",
)
