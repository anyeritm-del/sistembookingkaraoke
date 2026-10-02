package com.sentineltech.karaoketv

import android.content.Context
import android.net.Uri

/**
 * TV settings saved on the device. Only the server URL is required: the TV
 * page then shows its pairing screen and keeps the TV's own token itself.
 * Room code and TV key are the older way and are optional.
 */
data class Settings(val server: String, val room: String, val key: String) {

    val complete: Boolean get() = server.startsWith("https://") && (room.isBlank() == key.isBlank())

    /** URL of the TV page, with ?room=&key= only when the older way is used. */
    fun tvUrl(): String {
        val b = Uri.parse(server.trimEnd('/')).buildUpon().appendPath("tv")
        if (room.isNotBlank() && key.isNotBlank()) {
            b.appendQueryParameter("room", room).appendQueryParameter("key", key)
        }
        return b.build().toString()
    }

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
