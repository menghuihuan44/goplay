package com.goplay.mobile.protocol

import kotlin.random.Random
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

class BackoffPolicyTest {

    @Test
    fun exactSequenceWithoutJitter() {
        val policy = BackoffPolicy(jitterRatio = 0.0)
        assertEquals(500L, policy.delayForAttempt(0))
        assertEquals(1_000L, policy.delayForAttempt(1))
        assertEquals(2_000L, policy.delayForAttempt(2))
        assertEquals(4_000L, policy.delayForAttempt(3))
        assertEquals(8_000L, policy.delayForAttempt(4))
        assertEquals(16_000L, policy.delayForAttempt(5))
        assertEquals(30_000L, policy.delayForAttempt(6))
        assertEquals(30_000L, policy.delayForAttempt(7))
        assertEquals(30_000L, policy.delayForAttempt(32))
        assertEquals(30_000L, policy.delayForAttempt(100))
    }

    @Test
    fun jitterStaysInExpectedWindow() {
        val policy = BackoffPolicy(jitterRatio = 0.20, random = Random(42))
        repeat(200) {
            val delay = policy.delayForAttempt(0)
            assertTrue("delay=" + delay, delay in 400L..600L)
        }
    }

    @Test
    fun maxDelayIsClampedEvenWithJitter() {
        val policy = BackoffPolicy(jitterRatio = 0.20, random = Random(7))
        repeat(200) {
            val delay = policy.delayForAttempt(10)
            assertTrue("delay=" + delay, delay in 24_000L..30_000L)
        }
    }

    @Test
    fun negativeAttemptBehavesLikeFirstRetry() {
        val policy = BackoffPolicy(jitterRatio = 0.0)
        assertEquals(policy.delayForAttempt(0), policy.delayForAttempt(-3))
    }

    @Test
    fun customBaseAndFactorAreHonored() {
        val policy = BackoffPolicy(baseMillis = 250L, factor = 3.0, maxMillis = 5_000L, jitterRatio = 0.0)
        assertEquals(250L, policy.delayForAttempt(0))
        assertEquals(750L, policy.delayForAttempt(1))
        assertEquals(2_250L, policy.delayForAttempt(2))
        assertEquals(5_000L, policy.delayForAttempt(3))
    }
}
