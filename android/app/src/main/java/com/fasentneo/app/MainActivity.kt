package com.fasentneo.app

import android.annotation.SuppressLint
import android.content.Context
import android.net.wifi.WifiManager
import android.os.Build
import android.os.Bundle
import android.os.Handler
import android.os.Looper
import android.util.Log
import android.webkit.JavascriptInterface
import android.webkit.WebChromeClient
import android.webkit.WebView
import android.webkit.WebViewClient
import androidx.appcompat.app.AppCompatActivity
import java.io.File

class MainActivity : AppCompatActivity() {

    private var serverProcess: Process? = null
    private lateinit var webView: WebView
    private val handler = Handler(Looper.getMainLooper())
    private var connectRetries = 0

    companion object {
        private const val TAG = "FasentNeo"
        private const val HTTP_PORT = 8080
        private const val MAX_RETRIES = 20
        private const val RETRY_DELAY_MS = 500L
    }

    inner class DeviceInfoInterface {
        @JavascriptInterface
        fun getDeviceInfo(): String {
            val model = Build.MODEL ?: "Android"
            val ip = getWifiIP()
            return """{"name":"$model","ip":"$ip"}"""
        }

        private fun getWifiIP(): String {
            try {
                val wifiManager = applicationContext.getSystemService(Context.WIFI_SERVICE) as WifiManager
                val ipInt = wifiManager.connectionInfo.ipAddress
                if (ipInt != 0) {
                    return String.format("%d.%d.%d.%d",
                        ipInt and 0xff,
                        ipInt shr 8 and 0xff,
                        ipInt shr 16 and 0xff,
                        ipInt shr 24 and 0xff
                    )
                }
            } catch (e: Exception) {
                Log.w(TAG, "Failed to get WiFi IP", e)
            }
            return ""
        }
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)

        webView = WebView(this).apply {
            settings.javaScriptEnabled = true
            settings.domStorageEnabled = true
            settings.allowFileAccess = false
            settings.databaseEnabled = true
            settings.setGeolocationEnabled(false)
            settings.mediaPlaybackRequiresUserGesture = false
            webViewClient = WebViewClient()
            webChromeClient = WebChromeClient()
            addJavascriptInterface(DeviceInfoInterface(), "AndroidDevice")
        }
        setContentView(webView)

        startServer()
        connectToServer()
    }

    private fun startServer() {
        Thread {
            try {
                val nativeLibDir = applicationInfo.nativeLibraryDir
                val goBinary = File(nativeLibDir, "libfasentneo.so")

                if (!goBinary.exists()) {
                    Log.e(TAG, "Binary not found at ${goBinary.absolutePath}")
                    showError("Binary not found")
                    return@Thread
                }

                goBinary.setExecutable(true, false)

                val env = mapOf(
                    "HOME" to filesDir.absolutePath,
                    "TMPDIR" to cacheDir.absolutePath
                )

                val pb = ProcessBuilder(goBinary.absolutePath)
                    .directory(filesDir)
                    .redirectErrorStream(true)

                pb.environment().putAll(env)

                serverProcess = pb.start()

                serverProcess?.inputStream?.bufferedReader()?.use { reader ->
                    reader.lines().forEach { line ->
                        Log.i(TAG, line)
                    }
                }
            } catch (e: Exception) {
                Log.e(TAG, "Failed to start server", e)
                showError("Start failed: ${e.message}")
            }
        }.start()
    }

    private fun connectToServer() {
        val url = "http://127.0.0.1:$HTTP_PORT"
        handler.postDelayed({
            Thread {
                try {
                    val conn = java.net.URL(url).openConnection()
                    conn.connectTimeout = 1000
                    conn.readTimeout = 1000
                    conn.connect()
                    conn.getInputStream().close()

                    handler.post {
                        webView.loadUrl(url)
                    }
                } catch (e: Exception) {
                    connectRetries++
                    if (connectRetries < MAX_RETRIES) {
                        connectToServer()
                    } else {
                        showError("服务启动超时，请确认设备是 ARM64 架构")
                    }
                }
            }.start()
        }, RETRY_DELAY_MS)
    }

    @SuppressLint("SetJavaScriptEnabled")
    private fun showError(msg: String) {
        handler.post {
            val html = """
                <html><body style="background:#0f0f14;color:#e0e0e8;font-family:sans-serif;
                display:flex;align-items:center;justify-content:center;height:100vh;text-align:center;">
                <div><h2>⚠️ 启动失败</h2><p>$msg</p></div></body></html>
            """.trimIndent()
            webView.loadDataWithBaseURL(null, html, "text/html", "UTF-8", null)
        }
    }

    override fun onDestroy() {
        super.onDestroy()
        serverProcess?.destroy()
        serverProcess = null
    }

    override fun onBackPressed() {
        if (webView.canGoBack()) {
            webView.goBack()
        } else {
            super.onBackPressed()
        }
    }
}
