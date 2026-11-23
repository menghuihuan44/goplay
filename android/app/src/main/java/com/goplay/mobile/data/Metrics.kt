package com.goplay.mobile.data

import java.util.concurrent.atomic.AtomicLong

data class MetricsSnapshot(
    val framesCaptured: Long,
    val framesSent: Long,
    val framesDropped: Long,
    val bytesCaptured: Long,
    val actionsExecuted: Long,
    val actionErrors: Long,
    val wsReconnects: Long,
    val captureFps: Double,
)

/** Thread-safe counters shared by the capture service, WebSocket client and UI. */
class Metrics {
    private val framesCaptured = AtomicLong()
    private val framesSent = AtomicLong()
    private val framesDropped = AtomicLong()
    private val bytesCaptured = AtomicLong()
    private val actionsExecuted = AtomicLong()
    private val actionErrors = AtomicLong()
    private val wsReconnects = AtomicLong()

    @Volatile
    var captureFps: Double = 0.0
        private set

    fun onFrameCaptured(bytes: Int) {
        framesCaptured.incrementAndGet()
        if (bytes > 0) bytesCaptured.addAndGet(bytes.toLong())
    }

    fun onFrameSent() {
        framesSent.incrementAndGet()
    }

    fun onFrameDropped() {
        framesDropped.incrementAndGet()
    }

    fun onAction(ok: Boolean) {
        if (ok) actionsExecuted.incrementAndGet() else actionErrors.incrementAndGet()
    }

    fun onReconnect() {
        wsReconnects.incrementAndGet()
    }

    fun updateFps(fps: Double) {
        captureFps = if (fps.isFinite() && fps >= 0.0) fps else 0.0
    }

    fun snapshot(): MetricsSnapshot = MetricsSnapshot(
        framesCaptured = framesCaptured.get(),
        framesSent = framesSent.get(),
        framesDropped = framesDropped.get(),
        bytesCaptured = bytesCaptured.get(),
        actionsExecuted = actionsExecuted.get(),
        actionErrors = actionErrors.get(),
        wsReconnects = wsReconnects.get(),
        captureFps = captureFps,
    )
}
