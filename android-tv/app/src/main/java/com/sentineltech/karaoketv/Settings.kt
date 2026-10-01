package com.sentineltech.karaoketv

import android.content.Context
import android.net.Uri

/** TV settings saved on the device: server URL, room code and TV key. */
data class Settings(val server: String, val room: String, val key: String) {

    val complete: Boolean get() = server.startsWith("https://") && room.isNotBlank() && key.isNotBlank()

    /** URL of the TV page, for example https://x.vercel.app/tv?room=R01&key=... */
    fun tvUrl(): String = Uri.parse(server.trimEnd('/')).buildUpon()
        .appendPath("tv")
        .appendQueryParameter("room", room)
        .appendQueryParameter("key", key)
        .build()
        .toString()

    fun save(context: Context) {
        prefs(context).edit()
            .putString(SERVER, server.trim().trimEnd('/'))
            .putString(ROOM, room.trim().uppercase())
            .putString(KEY, key.trim())
            .apply()
    }

    companion object {
        private const val FILE = "karaoke_tv"
        private const val SERVER = "server"
        private const val ROOM = "room"
        private const val KEY = "key"

        fun load(context: Context): Settings {
            val p = prefs(context)
            return Settings(
                server = p.getString(SERVER, null) ?: BuildConfig.DEFAULT_SERVER,
                room = p.getString(ROOM, "") ?: "",
                key = p.getString(KEY, "") ?: "",
            )
        }

        private fun prefs(context: Context) = context.getSharedPreferences(FILE, Context.MODE_PRIVATE)
    }
}
