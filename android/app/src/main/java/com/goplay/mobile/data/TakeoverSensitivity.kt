package com.goplay.mobile.data

/**
 * Human-takeover sensitivity. It lives in SharedPreferences under
 * [PREFS_KEY] so the accessibility service (a different process entry point)
 * can read the same value as the activity without extra IPC.
 *
 * high   — legacy behaviour: the first touch handed control to the user.
 * medium — two interactions, or one continuous gesture of 1.5 s.
 * low    — three interactions, or one continuous gesture of 3 s.
 *
 * [Config.echoGraceMs] is the window in which accessibility events are treated
 * as echoes of our own injected gestures: a less sensitive level needs a wider
 * window because a human press is harder to distinguish from our tap.
 */
object TakeoverSensitivity {
    const val HIGH = "high"
    const val MEDIUM = "medium"
    const val LOW = "low"

    /** SharedPreferences key, kept stable for the JS bridge contract. */
    const val PREFS_KEY = "takeover_sensitivity"

    val LEVELS = listOf(HIGH, MEDIUM, LOW)

    fun normalize(raw: String?): String =
        raw?.trim()?.lowercase()?.takeIf { it in LEVELS } ?: HIGH

    fun isValid(raw: String?): Boolean =
        raw != null && raw.trim().lowercase() in LEVELS

    data class Config(
        val level: String,
        /** Interactions needed inside one burst before the user takes over. */
        val interactions: Int,
        /** Continuous gesture duration that counts as a takeover. */
        val holdMs: Long,
        /** Accessibility events within this window after an AI gesture are echoes. */
        val echoGraceMs: Long,
        /** A gap longer than this starts a new interaction burst. */
        val burstWindowMs: Long,
    )

    fun config(level: String?): Config = when (normalize(level)) {
        LOW -> Config(
            level = LOW,
            interactions = 3,
            holdMs = 3_000L,
            echoGraceMs = 900L,
            burstWindowMs = 3_000L,
        )

        MEDIUM -> Config(
            level = MEDIUM,
            interactions = 2,
            holdMs = 1_500L,
            echoGraceMs = 650L,
            burstWindowMs = 2_500L,
        )

        else -> Config(
            level = HIGH,
            interactions = 1,
            holdMs = 0L,
            echoGraceMs = 400L,
            burstWindowMs = 2_000L,
        )
    }
}
