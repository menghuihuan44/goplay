package com.goplay.mobile.data

import android.app.ActivityManager
import android.content.Context
import android.os.Build
import java.util.Locale

data class DeviceProfile(
    val androidVersion: String,
    val sdkInt: Int,
    val manufacturer: String,
    val model: String,
    val abis: List<String>,
    val ramMb: Long,
    val cores: Int,
) {
    val summary: String
        get() = "Android " + androidVersion + " (SDK " + sdkInt + ") · " +
            manufacturer + " " + model + " · " +
            abis.joinToString(",") + " · " +
            ramMb + " MB RAM · " + cores + " 核"

    val abiSummary: String get() = abis.joinToString(", ")
}

object DeviceInfo {
    fun read(context: Context): DeviceProfile {
        val am = context.getSystemService(Context.ACTIVITY_SERVICE) as? ActivityManager
        val mem = ActivityManager.MemoryInfo()
        val ramMb = try {
            am?.getMemoryInfo(mem)
            mem.totalMem / (1024L * 1024L)
        } catch (_: Exception) {
            0L
        }
        return DeviceProfile(
            androidVersion = Build.VERSION.RELEASE ?: "?",
            sdkInt = Build.VERSION.SDK_INT,
            manufacturer = Build.MANUFACTURER.replaceFirstChar {
                if (it.isLowerCase()) it.titlecase(Locale.US) else it.toString()
            },
            model = Build.MODEL,
            abis = Build.SUPPORTED_ABIS?.toList() ?: emptyList(),
            ramMb = ramMb,
            cores = Runtime.getRuntime().availableProcessors(),
        )
    }
}
