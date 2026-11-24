package com.goplay.mobile.voice

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.app.Service
import android.content.Context
import android.content.Intent
import android.content.pm.ServiceInfo
import android.os.Build
import android.os.IBinder
import androidx.core.app.NotificationCompat
import androidx.core.content.ContextCompat
import com.goplay.mobile.MainActivity
import com.goplay.mobile.R

/**
 * Keeps the process in the foreground with the microphone service type while a
 * continuous voice conversation is active.
 *
 * SpeechRecognizer runs in this app's process, but Android 11+ only lets a
 * background app keep the microphone open when it owns a foreground service of
 * type "microphone". Without this service the recognizer is torn down as soon
 * as the user switches to the game, which made the AI look deaf.
 *
 * The recognizer itself stays in [com.goplay.mobile.voice.VoiceBridge] so this
 * service is deliberately tiny: start foreground, keep the process alive, stop
 * when the call ends.
 */
class VoiceKeepAliveService : Service() {
    override fun onBind(intent: Intent?): IBinder? = null

    override fun onCreate() {
        super.onCreate()
        ensureChannel()
    }

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        startForegroundCompat()
        return START_STICKY
    }

    override fun onDestroy() {
        try {
            stopForeground(STOP_FOREGROUND_REMOVE)
        } catch (_: Exception) {
            // Already stopped.
        }
        super.onDestroy()
    }

    private fun startForegroundCompat() {
        val notification = buildNotification()
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.R) {
            startForeground(
                NOTIFICATION_ID,
                notification,
                ServiceInfo.FOREGROUND_SERVICE_TYPE_MICROPHONE,
            )
        } else {
            startForeground(NOTIFICATION_ID, notification)
        }
    }

    private fun buildNotification(): Notification {
        val flags = PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE
        val openIntent = Intent(this, MainActivity::class.java).apply {
            addFlags(Intent.FLAG_ACTIVITY_SINGLE_TOP or Intent.FLAG_ACTIVITY_CLEAR_TOP)
        }
        val openPending = PendingIntent.getActivity(this, 11, openIntent, flags)
        return NotificationCompat.Builder(this, CHANNEL_ID)
            .setSmallIcon(R.drawable.ic_stat_goplay)
            .setContentTitle("GoPlay · 实时语音")
            .setContentText("正在听你说话；切到游戏后仍可继续对话")
            .setContentIntent(openPending)
            .setOngoing(true)
            .setOnlyAlertOnce(true)
            .setCategory(NotificationCompat.CATEGORY_SERVICE)
            .setPriority(NotificationCompat.PRIORITY_LOW)
            .setForegroundServiceBehavior(NotificationCompat.FOREGROUND_SERVICE_IMMEDIATE)
            .build()
    }

    private fun ensureChannel() {
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.O) return
        val manager = getSystemService(NotificationManager::class.java) ?: return
        if (manager.getNotificationChannel(CHANNEL_ID) != null) return
        val channel = NotificationChannel(
            CHANNEL_ID,
            "GoPlay 实时语音",
            NotificationManager.IMPORTANCE_LOW,
        ).apply {
            description = "保持麦克风在游戏前台时可用"
            setShowBadge(false)
            enableVibration(false)
            setSound(null, null)
        }
        manager.createNotificationChannel(channel)
    }

    companion object {
        private const val CHANNEL_ID = "goplay_voice"
        private const val NOTIFICATION_ID = 0x6202

        fun start(context: Context) {
            try {
                ContextCompat.startForegroundService(
                    context,
                    Intent(context, VoiceKeepAliveService::class.java),
                )
            } catch (_: Exception) {
                // Some OEMs reject a microphone FGS from certain states. The
                // conversation still works while the console is foreground.
            }
        }

        fun stop(context: Context) {
            try {
                context.stopService(Intent(context, VoiceKeepAliveService::class.java))
            } catch (_: Exception) {
                // Already stopped.
            }
        }
    }
}
