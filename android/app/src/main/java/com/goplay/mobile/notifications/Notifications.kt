package com.goplay.mobile.notifications

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.content.Context
import android.content.Intent
import android.os.Build
import androidx.core.app.NotificationCompat
import com.goplay.mobile.MainActivity
import com.goplay.mobile.R
import com.goplay.mobile.capture.ScreenCaptureService

object Notifications {
    const val CHANNEL_ID = "goplay_capture"
    const val NOTIFICATION_ID = 0x6201

    fun ensureChannel(context: Context) {
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.O) return
        val manager = context.getSystemService(NotificationManager::class.java) ?: return
        if (manager.getNotificationChannel(CHANNEL_ID) != null) return
        val channel = NotificationChannel(
            CHANNEL_ID,
            context.getString(R.string.app_name),
            NotificationManager.IMPORTANCE_LOW,
        ).apply {
            description = "GoPlay 截屏与 AI 会话状态"
            setShowBadge(false)
            enableVibration(false)
            setSound(null, null)
        }
        manager.createNotificationChannel(channel)
    }

    fun buildCaptureNotification(
        context: Context,
        text: String,
        paused: Boolean,
    ): Notification {
        val flags = PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE
        val openIntent = Intent(context, MainActivity::class.java).apply {
            addFlags(Intent.FLAG_ACTIVITY_SINGLE_TOP or Intent.FLAG_ACTIVITY_CLEAR_TOP)
        }
        val openPending = PendingIntent.getActivity(context, 1, openIntent, flags)
        val stopIntent = Intent(context, ScreenCaptureService::class.java).apply {
            action = ScreenCaptureService.ACTION_STOP
        }
        val stopPending = PendingIntent.getService(context, 2, stopIntent, flags)
        val title = if (paused) "GoPlay · AI 已暂停" else "GoPlay · AI 运行中"
        return NotificationCompat.Builder(context, CHANNEL_ID)
            .setSmallIcon(R.drawable.ic_stat_goplay)
            .setContentTitle(title)
            .setContentText(text)
            .setStyle(NotificationCompat.BigTextStyle().bigText(text))
            .setContentIntent(openPending)
            .setOngoing(true)
            .setOnlyAlertOnce(true)
            .setCategory(NotificationCompat.CATEGORY_SERVICE)
            .setPriority(NotificationCompat.PRIORITY_LOW)
            .setForegroundServiceBehavior(NotificationCompat.FOREGROUND_SERVICE_IMMEDIATE)
            .addAction(0, context.getString(R.string.stop_capture), stopPending)
            .build()
    }

    fun update(context: Context, text: String, paused: Boolean) {
        val manager = context.getSystemService(NotificationManager::class.java) ?: return
        try {
            manager.notify(NOTIFICATION_ID, buildCaptureNotification(context, text, paused))
        } catch (_: SecurityException) {
            // Notification permission can be denied on Android 13+; the FGS still runs.
        }
    }
}
