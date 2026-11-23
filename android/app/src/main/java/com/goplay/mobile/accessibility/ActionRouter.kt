package com.goplay.mobile.accessibility

import android.os.Handler
import android.os.Looper
import android.os.SystemClock
import com.goplay.mobile.data.LogBus
import com.goplay.mobile.data.Metrics
import com.goplay.mobile.net.CoreClient
import com.goplay.mobile.protocol.ActionAck
import com.goplay.mobile.protocol.ActionMapper
import com.goplay.mobile.protocol.ActionSpec
import com.goplay.mobile.protocol.GestureCommand

/**
 * Validates each action, routes it to the accessibility service (or handles
 * "wait" locally), and always sends exactly one ack with the action id.
 */
class ActionRouter(
    private val client: CoreClient,
    private val metrics: Metrics,
    private val logs: LogBus,
) {
    private val mainHandler = Handler(Looper.getMainLooper())

    fun handle(spec: ActionSpec) {
        val actionId = spec.id ?: ("android-" + System.nanoTime())
        val started = SystemClock.elapsedRealtime()
        val mapping = ActionMapper.map(spec)
        val command = mapping.command
        if (command == null) {
            metrics.onAction(false)
            logs.log("拒绝动作 " + spec.type + "：" + mapping.error)
            client.sendAck(
                ActionAck(
                    id = actionId,
                    ok = false,
                    actionType = spec.type,
                    errorCode = mapping.errorCode ?: "invalid_action",
                    error = mapping.error,
                ),
            )
            return
        }

        if (command is GestureCommand.Wait) {
            mainHandler.postDelayed(
                {
                    val elapsed = SystemClock.elapsedRealtime() - started
                    metrics.onAction(true)
                    client.sendAck(
                        ActionAck(
                            id = actionId,
                            ok = true,
                            actionType = spec.type,
                            executedMs = elapsed,
                        ),
                    )
                },
                command.durationMs,
            )
            return
        }

        val service = GameAccessibilityService.instance
        if (service == null) {
            metrics.onAction(false)
            logs.log("动作失败：无障碍服务未连接（" + spec.type + "）")
            client.sendAck(
                ActionAck(
                    id = actionId,
                    ok = false,
                    actionType = spec.type,
                    errorCode = "accessibility_unavailable",
                    error = "GameAccessibilityService is not enabled/connected",
                ),
            )
            return
        }

        val timeoutMs = (spec.timeoutMs ?: DEFAULT_TIMEOUT_MS).coerceIn(1_000L, 15_000L)
        service.execute(command, timeoutMs) { result ->
            metrics.onAction(result.ok)
            if (result.ok) {
                logs.log(
                    "动作完成 " + spec.type + " " + result.executedMs + " ms",
                )
            } else {
                logs.log(
                    "动作失败 " + spec.type + " [" + result.errorCode + "] " + result.message,
                )
            }
            client.sendAck(
                ActionAck(
                    id = actionId,
                    ok = result.ok,
                    actionType = spec.type,
                    errorCode = result.errorCode,
                    error = result.message,
                    executedMs = result.executedMs,
                ),
            )
        }
    }

    companion object {
        private const val DEFAULT_TIMEOUT_MS = 5_000L
    }
}
