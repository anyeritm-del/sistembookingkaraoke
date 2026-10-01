package com.sentineltech.karaoketv

import android.annotation.SuppressLint
import android.app.Activity
import android.content.Intent
import android.graphics.Color
import android.net.http.SslError
import android.os.Build
import android.os.Bundle
import android.os.Handler
import android.os.Looper
import android.view.KeyEvent
import android.view.View
import android.view.WindowManager
import android.webkit.SslErrorHandler
import android.webkit.WebResourceError
import android.webkit.WebResourceRequest
import android.webkit.WebView
import android.webkit.WebViewClient
import android.widget.Toast

/**
 * Full-screen kiosk that shows the room timer page from the server.
 * The page itself does the countdown and alarms; this app only:
 *  - keeps the screen on and hides system bars,
 *  - allows sound without a key press (no autoplay block),
 *  - reloads when the network fails,
 *  - starts after the TV boots (see BootReceiver).
 *
 * Open settings: long-press BACK, or press MENU.
 */
class MainActivity : Activity() {

    private lateinit var web: WebView
    private val handler = Handler(Looper.getMainLooper())
    private val retry = Runnable { web.reload() }

    @SuppressLint("SetJavaScriptEnabled")
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        window.addFlags(WindowManager.LayoutParams.FLAG_KEEP_SCREEN_ON)

        val settings = Settings.load(this)
        if (!settings.complete) {
            openSetup()
            finish()
            return
        }

        web = WebView(this).apply {
            setBackgroundColor(Color.BLACK)
            isFocusable = true
            isFocusableInTouchMode = true
            this.settings.javaScriptEnabled = true
            this.settings.domStorageEnabled = true
            this.settings.mediaPlaybackRequiresUserGesture = false
            webViewClient = KioskClient()
        }
        setContentView(web)
        web.loadUrl(settings.tvUrl())
        web.requestFocus()
    }

    override fun onResume() {
        super.onResume()
        hideSystemBars()
        if (::web.isInitialized) web.onResume()
    }

    override fun onPause() {
        if (::web.isInitialized) web.onPause()
        super.onPause()
    }

    override fun onDestroy() {
        handler.removeCallbacks(retry)
        if (::web.isInitialized) web.destroy()
        super.onDestroy()
    }

    override fun onKeyDown(keyCode: Int, event: KeyEvent): Boolean {
        if (keyCode == KeyEvent.KEYCODE_BACK) {
            event.startTracking() // so a long press reaches onKeyLongPress
            return true // a short BACK does nothing: guests cannot leave the timer
        }
        if (keyCode == KeyEvent.KEYCODE_MENU) {
            openSetup()
            return true
        }
        return super.onKeyDown(keyCode, event)
    }

    override fun onKeyLongPress(keyCode: Int, event: KeyEvent): Boolean {
        if (keyCode == KeyEvent.KEYCODE_BACK) {
            openSetup()
            return true
        }
        return super.onKeyLongPress(keyCode, event)
    }

    private fun openSetup() {
        startActivity(Intent(this, SetupActivity::class.java))
    }

    @Suppress("DEPRECATION")
    private fun hideSystemBars() {
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.R) {
            window.insetsController?.let {
                it.hide(android.view.WindowInsets.Type.systemBars())
                it.systemBarsBehavior = android.view.WindowInsetsController.BEHAVIOR_SHOW_TRANSIENT_BARS_BY_SWIPE
            }
        } else {
            window.decorView.systemUiVisibility = (View.SYSTEM_UI_FLAG_FULLSCREEN
                or View.SYSTEM_UI_FLAG_HIDE_NAVIGATION
                or View.SYSTEM_UI_FLAG_IMMERSIVE_STICKY)
        }
    }

    private inner class KioskClient : WebViewClient() {
        override fun onReceivedError(view: WebView, request: WebResourceRequest, error: WebResourceError) {
            if (request.isForMainFrame) scheduleRetry()
        }

        override fun onReceivedSslError(view: WebView, handler: SslErrorHandler, error: SslError) {
            handler.cancel() // never accept bad certificates
            Toast.makeText(this@MainActivity, R.string.ssl_error, Toast.LENGTH_LONG).show()
            scheduleRetry()
        }

        // Stay inside the app for our own server; ignore links elsewhere.
        override fun shouldOverrideUrlLoading(view: WebView, request: WebResourceRequest): Boolean {
            val target = request.url.host
            val ours = android.net.Uri.parse(Settings.load(this@MainActivity).server).host
            return target != ours
        }
    }

    private fun scheduleRetry() {
        handler.removeCallbacks(retry)
        handler.postDelayed(retry, RETRY_MS)
    }

    companion object {
        private const val RETRY_MS = 10_000L
    }
}
