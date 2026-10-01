package com.sentineltech.karaoketv

import android.app.Activity
import android.content.Intent
import android.os.Bundle
import android.widget.Button
import android.widget.EditText
import android.widget.TextView

/** Settings screen: server URL, room code and TV key. */
class SetupActivity : Activity() {

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        setContentView(R.layout.activity_setup)

        val server = findViewById<EditText>(R.id.server)
        val room = findViewById<EditText>(R.id.room)
        val key = findViewById<EditText>(R.id.key)
        val error = findViewById<TextView>(R.id.error)

        val current = Settings.load(this)
        server.setText(current.server)
        room.setText(current.room)
        key.setText(current.key)

        findViewById<Button>(R.id.save).setOnClickListener {
            val s = Settings(server.text.toString().trim().trimEnd('/'), room.text.toString().trim(), key.text.toString().trim())
            if (!s.complete) {
                error.setText(R.string.setup_invalid)
                return@setOnClickListener
            }
            s.save(this)
            startActivity(Intent(this, MainActivity::class.java).addFlags(Intent.FLAG_ACTIVITY_CLEAR_TASK or Intent.FLAG_ACTIVITY_NEW_TASK))
            finish()
        }
    }
}
