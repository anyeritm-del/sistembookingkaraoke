package com.sentineltech.karaoketv

import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent

/**
 * Opens the timer after the TV boots or after the app is updated.
 * On Android 10+ this only works when the app may draw over other apps
 * (SYSTEM_ALERT_WINDOW, granted once with adb; see README).
 */
class BootReceiver : BroadcastReceiver() {
    override fun onReceive(context: Context, intent: Intent) {
        when (intent.action) {
            Intent.ACTION_BOOT_COMPLETED,
            Intent.ACTION_MY_PACKAGE_REPLACED -> context.startActivity(
                Intent(context, MainActivity::class.java).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)
            )
        }
    }
}
