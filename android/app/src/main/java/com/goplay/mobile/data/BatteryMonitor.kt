package com.goplay.mobile.data

import android.content.Context
import android.os.BatteryManager

data class BatteryStatus(
    val percent: Int,
    val charging: Boolean,
) {
    val known: Boolean get() = percent in 0..100

    val label: String
        get() = when {
            !known && charging -> "电量未知 · 充电中"
            !known -> "电量未知"
            charging -> "$percent% · 充电中"
            else -> "$percent%"
        }
}

/** Reads the battery level/charging flag without registering a receiver. */
class BatteryMonitor(context: Context) {
    private val appContext = context.applicationContext

    fun status(): BatteryStatus {
        val manager = appContext.getSystemService(Context.BATTERY_SERVICE) as? BatteryManager
        val raw = manager?.getIntProperty(BatteryManager.BATTERY_PROPERTY_CAPACITY) ?: -1
        val percent = if (raw in 0..100) raw else -1
        val charging = try {
            manager?.isCharging == true
        } catch (_: Exception) {
            false
        }
        return BatteryStatus(percent = percent, charging = charging)
    }
}
