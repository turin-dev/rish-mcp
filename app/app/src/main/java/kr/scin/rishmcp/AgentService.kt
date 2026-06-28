package kr.scin.rishmcp

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.Service
import android.content.ComponentName
import android.content.Intent
import android.content.ServiceConnection
import android.content.pm.ServiceInfo
import android.os.Build
import android.os.Handler
import android.os.IBinder
import android.os.Looper
import android.util.Log
import kr.scin.rishmcp.Prefs.deviceToken
import kr.scin.rishmcp.Prefs.relayUrl
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.Response
import okhttp3.WebSocket
import okhttp3.WebSocketListener
import org.json.JSONObject
import rikka.shizuku.Shizuku
import java.util.concurrent.Executors
import java.util.concurrent.TimeUnit

/**
 * Always-on foreground service. Holds a single outbound WebSocket to the relay
 * (so the phone needs no inbound connectivity), and executes commands the relay
 * forwards by calling into the Shizuku-backed [ShellUserService].
 */
class AgentService : Service() {

    private val main = Handler(Looper.getMainLooper())
    private val execPool = Executors.newSingleThreadExecutor()
    private val http = OkHttpClient.Builder()
        .pingInterval(20, TimeUnit.SECONDS)
        .retryOnConnectionFailure(true)
        .build()

    @Volatile private var ws: WebSocket? = null
    @Volatile private var userService: IUserService? = null
    @Volatile private var stopped = false
    private var backoffMs = 1000L
    private var status = "starting"

    private val userServiceArgs by lazy {
        Shizuku.UserServiceArgs(ComponentName(packageName, ShellUserService::class.java.name))
            .daemon(false)
            .processNameSuffix("shell")
            .debuggable(BuildConfig.DEBUG)
            .version(BuildConfig.VERSION_CODE)
    }

    private val userServiceConn = object : ServiceConnection {
        override fun onServiceConnected(name: ComponentName?, binder: IBinder?) {
            userService = IUserService.Stub.asInterface(binder)
            Log.i(TAG, "Shizuku user service bound")
        }
        override fun onServiceDisconnected(name: ComponentName?) {
            userService = null
            Log.w(TAG, "Shizuku user service disconnected")
        }
    }

    override fun onBind(intent: Intent?): IBinder? = null

    override fun onCreate() {
        super.onCreate()
        startForeground(NOTIF_ID, buildNotification("starting…"))
        bindShizuku()
        connect()
    }

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        return START_STICKY
    }

    override fun onDestroy() {
        stopped = true
        try { ws?.close(1000, "service stopping") } catch (_: Throwable) {}
        try { Shizuku.unbindUserService(userServiceArgs, userServiceConn, true) } catch (_: Throwable) {}
        execPool.shutdownNow()
        super.onDestroy()
    }

    // --- Shizuku --------------------------------------------------------------

    private fun bindShizuku() {
        if (!Shizuku.pingBinder()) {
            setStatus("Shizuku not running")
            return
        }
        if (Shizuku.checkSelfPermission() != android.content.pm.PackageManager.PERMISSION_GRANTED) {
            setStatus("Shizuku permission not granted")
            return
        }
        try {
            Shizuku.bindUserService(userServiceArgs, userServiceConn)
        } catch (e: Throwable) {
            Log.e(TAG, "bindUserService failed", e)
            setStatus("bind failed: ${e.message}")
        }
    }

    // --- WebSocket relay ------------------------------------------------------

    private fun connect() {
        if (stopped) return
        val url = relayUrl
        val token = deviceToken
        if (url.isBlank() || token.isBlank()) {
            setStatus("not configured")
            return
        }
        // OkHttp wants an ws:// or wss:// scheme; accept http(s):// in config too.
        val wsBase = when {
            url.startsWith("ws") -> url
            url.startsWith("http") -> "ws" + url.substring(4) // http->ws, https->wss
            else -> "wss://$url"
        }
        val full = buildString {
            append(wsBase)
            append(if (wsBase.contains("?")) "&" else "?")
            append("token=").append(token)
            append("&deviceId=").append(Prefs.deviceId(this@AgentService))
            append("&name=").append(Build.MODEL.replace(" ", "_"))
            append("&sdk=").append(Build.VERSION.SDK_INT)
        }
        setStatus("connecting…")
        ws = http.newWebSocket(Request.Builder().url(full).build(), listener)
    }

    private fun scheduleReconnect() {
        if (stopped) return
        main.postDelayed({ connect() }, backoffMs)
        backoffMs = (backoffMs * 2).coerceAtMost(30_000)
    }

    private val listener = object : WebSocketListener() {
        override fun onOpen(webSocket: WebSocket, response: Response) {
            backoffMs = 1000
            setStatus("connected")
        }

        override fun onMessage(webSocket: WebSocket, text: String) {
            val msg = try { JSONObject(text) } catch (_: Throwable) { return }
            if (msg.optString("type") != "exec") return
            val reqId = msg.optString("reqId")
            val cmd = msg.optString("cmd")
            val timeoutMs = msg.optLong("timeoutMs", 60_000)
            execPool.execute {
                val svc = userService
                val resultJson = if (svc == null) {
                    JSONObject()
                        .put("code", -1).put("stdout", "")
                        .put("stderr", "shell backend unavailable (Shizuku not bound)")
                        .put("truncated", false).put("durationMs", 0)
                        .toString()
                } else {
                    try { svc.exec(cmd, timeoutMs) } catch (e: Throwable) {
                        JSONObject().put("code", -1).put("stdout", "")
                            .put("stderr", "exec error: ${e.message}")
                            .put("truncated", false).put("durationMs", 0).toString()
                    }
                }
                val out = JSONObject(resultJson).put("type", "result").put("reqId", reqId)
                webSocket.send(out.toString())
            }
        }

        override fun onFailure(webSocket: WebSocket, t: Throwable, response: Response?) {
            setStatus("disconnected: ${t.message}")
            scheduleReconnect()
        }

        override fun onClosed(webSocket: WebSocket, code: Int, reason: String) {
            setStatus("closed")
            scheduleReconnect()
        }
    }

    // --- Foreground notification ---------------------------------------------

    private fun setStatus(s: String) {
        status = s
        Log.i(TAG, "status: $s")
        val nm = getSystemService(NotificationManager::class.java)
        nm.notify(NOTIF_ID, buildNotification(s))
    }

    private fun buildNotification(text: String): Notification {
        val nm = getSystemService(NotificationManager::class.java)
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
            val ch = NotificationChannel(CHANNEL, "rish-mcp agent", NotificationManager.IMPORTANCE_LOW)
            ch.setShowBadge(false)
            nm.createNotificationChannel(ch)
        }
        val b = Notification.Builder(this, CHANNEL)
            .setContentTitle("rish-mcp agent")
            .setContentText(text)
            .setSmallIcon(android.R.drawable.stat_sys_data_bluetooth)
            .setOngoing(true)
        return b.build()
    }

    companion object {
        private const val TAG = "rishmcp"
        private const val CHANNEL = "rishmcp-agent"
        private const val NOTIF_ID = 42

        fun start(ctx: android.content.Context) {
            val i = Intent(ctx, AgentService::class.java)
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
                ctx.startForegroundService(i)
            } else {
                ctx.startService(i)
            }
        }

        fun stop(ctx: android.content.Context) {
            ctx.stopService(Intent(ctx, AgentService::class.java))
        }
    }
}
