package com.goplay.mobile.data

import android.content.Context
import android.net.ConnectivityManager
import android.net.NetworkCapabilities

data class NetworkStatus(
    val connected: Boolean,
    val wifi: Boolean,
    val cellular: Boolean,
    val vpn: Boolean,
    val validated: Boolean,
) {
    val label: String
        get() = when {
            !connected -> "无网络"
            vpn -> "VPN"
            wifi -> if (validated) "Wi-Fi（已验证）" else "Wi-Fi"
            cellular -> if (validated) "移动数据（已验证）" else "移动数据"
            else -> "其他网络"
        }
}

class NetworkMonitor(context: Context) {
    private val cm = context.applicationContext
        .getSystemService(Context.CONNECTIVITY_SERVICE) as ConnectivityManager

    fun status(): NetworkStatus {
        val network = cm.activeNetwork ?: return NetworkStatus(false, false, false, false, false)
        val caps = cm.getNetworkCapabilities(network)
            ?: return NetworkStatus(false, false, false, false, false)
        return NetworkStatus(
            connected = true,
            wifi = caps.hasTransport(NetworkCapabilities.TRANSPORT_WIFI),
            cellular = caps.hasTransport(NetworkCapabilities.TRANSPORT_CELLULAR),
            vpn = caps.hasTransport(NetworkCapabilities.TRANSPORT_VPN),
            validated = caps.hasCapability(NetworkCapabilities.NET_CAPABILITY_VALIDATED),
        )
    }

    /** Non-Wi-Fi downloads are blocked when the user enabled "Wi-Fi only". */
    fun downloadAllowed(wifiOnly: Boolean): Boolean {
        if (!wifiOnly) return true
        val status = status()
        return status.connected && (status.wifi || status.vpn)
    }
}
