package com.goplay.mobile.protocol

import kotlin.random.Random

/**
 * Exponential backoff with jitter:
 *
 *   raw(n)    = min(maxMs, baseMs * factor^n)
 *   delay(n)  = raw(n) +/- (raw(n) * jitterRatio), clamped to [0, maxMs]
 *
 * [random] is injectable so JVM tests are deterministic. Attempt 0 is the first
 * retry after the initial connection fails.
 */
class BackoffPolicy(
    val baseMillis: Long = 500L,
    val factor: Double = 2.0,
    val maxMillis: Long = 30_000L,
    val jitterRatio: Double = 0.20,
    private val random: Random = Random.Default,
) {
    init {
        require(baseMillis > 0) { "baseMillis must be > 0" }
        require(factor >= 1.0) { "factor must be >= 1.0" }
        require(maxMillis >= baseMillis) { "maxMillis must be >= baseMillis" }
        require(jitterRatio in 0.0..1.0) { "jitterRatio must be in [0,1]" }
    }

    fun delayForAttempt(attempt: Int): Long {
        val n = attempt.coerceIn(0, 32)
        var raw = baseMillis.toDouble()
        repeat(n) {
            if (raw < maxMillis) raw *= factor
        }
        val capped = raw.coerceAtMost(maxMillis.toDouble())
        if (jitterRatio == 0.0) return capped.toLong().coerceAtLeast(1L)
        val jitter = capped * jitterRatio
        val low = (capped - jitter).coerceAtLeast(0.0)
        val high = (capped + jitter).coerceAtMost(maxMillis.toDouble())
        if (high <= low) return low.toLong().coerceAtLeast(1L)
        return random.nextDouble(low, high).toLong().coerceAtLeast(1L)
    }
}
