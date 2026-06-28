package kr.scin.rishmcp

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.Service
import android.content.ComponentName
import android.content.Intent
import android.content.ServiceConnection
import android.content.pm.PackageManager
import android.net.ConnectivityManager
import android.net.Network
import android.net.NetworkCapabilities
import android.net.NetworkRequest
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
 * Always-on foreground service. Holds one outbound WebSocket to the relay and
 * executes forwarded commands via the Shizuku-backed [ShellUserService].
 *
 * Watchdog: (1) a ConnectivityManager callback forces an immediate reconnect on
 * any network change (data<->wifi) so there is no ~20s ping-timeout gap; (2)
 * Shizuku binder listeners rebind the shell backend when Shizuku comes/goes; and
 * (3) a periodic heartbeat re-establishes anything that silently dropped.
 */
class AgentService : Service() {

    private val main = Handler(Looper.getMainLooper())
    private val execPool = Executors.newSingleThreadExecutor()
    private val http = OkHttpClient.Builder()
        .pingInterval(20, TimeUnit.SECONDS)
        .retryOnConnectionFailure(true)
        .build()
    private val connectivity by lazy { getSystemService(ConnectivityManager::class.java) }

    @Volatile private var ws: WebSocket? = null
    @Volatile private var userService: IUserService? = null
    @Volatile private var stopped = false
    private var backoffMs = 1000L

    // --- lifecycle ------------------------------------------------------------

    override fun onBind(intent: Intent?): IBinder? = null

    override fun onCreate() {
        super.onCreate()
        AgentState.serviceRunning = true
        startForeground(NOTIF_ID, buildNotification("starting…"))
        registerShizukuListeners()
        registerNetworkCallback()
        bindShizuku()
        connect()
        main.postDelayed(heartbeat, HEARTBEAT_MS)
    }

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        // Re-provisioning (new relay/token) restarts the connection without force-stop.
        if (intent?.getBooleanExtra("reconnect", false) == true) forceReconnect("reconfigured")
        return START_STICKY
    }

    override fun onDestroy() {
        stopped = true
        AgentState.serviceRunning = false
        AgentState.conn = AgentState.Conn.IDLE
        main.removeCallbacksAndMessages(null)
        try { ws?.close(1000, "service stopping") } catch (_: Throwable) {}
        try { connectivity.unregisterNetworkCallback(netCallback) } catch (_: Throwable) {}
        unregisterShizukuListeners()
        try { Shizuku.unbindUserService(userServiceArgs, userServiceConn, true) } catch (_: Throwable) {}
        execPool.shutdownNow()
        super.onDestroy()
    }

    // --- Shizuku --------------------------------------------------------------

    private val userServiceArgs by lazy {
        Shizuku.UserServiceArgs(ComponentName(packageName, ShellUserService::class.java.name))
            .daemon(false)
            .processNameSuffix("shell")
            .debuggable(BuildConfig.DEBUG)
            .version(BuildConfig.VERSION_CODE)
    }

    private val userServiceConn = object : ServiceConnection {
        override fun onServiceConnected(name: ComponentName?, binder: IBinder?) {
            userService = if (binder != null && binder.pingBinder()) IUserService.Stub.asInterface(binder) else null
            AgentState.shizuku = if (userService != null) "bound ✓" else "bind failed"
            Log.i(TAG, "Shizuku user service bound")
        }
        override fun onServiceDisconnected(name: ComponentName?) {
            userService = null
            AgentState.shizuku = "disconnected"
            Log.w(TAG, "Shizuku user service disconnected")
        }
    }

    private val binderReceived = Shizuku.OnBinderReceivedListener {
        Log.i(TAG, "Shizuku binder received"); bindShizuku()
    }
    private val binderDead = Shizuku.OnBinderDeadListener {
        userService = null; AgentState.shizuku = "dead"; Log.w(TAG, "Shizuku binder dead")
    }

    private fun registerShizukuListeners() {
        Shizuku.addBinderReceivedListenerSticky(binderReceived)
        Shizuku.addBinderDeadListener(binderDead)
    }
    private fun unregisterShizukuListeners() {
        try { Shizuku.removeBinderReceivedListener(binderReceived) } catch (_: Throwable) {}
        try { Shizuku.removeBinderDeadListener(binderDead) } catch (_: Throwable) {}
    }

    private fun shizukuReady(): Boolean =
        Shizuku.pingBinder() &&
            Shizuku.checkSelfPermission() == PackageManager.PERMISSION_GRANTED

    private fun bindShizuku() {
        if (userService != null) return
        if (!Shizuku.pingBinder()) { AgentState.shizuku = "not running"; return }
        if (Shizuku.checkSelfPermission() != PackageManager.PERMISSION_GRANTED) {
            AgentState.shizuku = "permission needed"; return
        }
        try {
            AgentState.shizuku = "binding…"
            Shizuku.bindUserService(userServiceArgs, userServiceConn)
        } catch (e: Throwable) {
            Log.e(TAG, "bindUserService failed", e)
            AgentState.shizuku = "bind error"
        }
    }

    // --- network watchdog -----------------------------------------------------

    private val netCallback = object : ConnectivityManager.NetworkCallback() {
        override fun onAvailable(network: Network) { evaluateNetwork() }
        override fun onLost(network: Network) {
            AgentState.network = "none"; AgentState.lastEvent = "network lost"
        }
        override fun onCapabilitiesChanged(network: Network, caps: NetworkCapabilities) {
            val type = when {
                caps.hasTransport(NetworkCapabilities.TRANSPORT_WIFI) -> "wifi"
                caps.hasTransport(NetworkCapabilities.TRANSPORT_CELLULAR) -> "cellular"
                caps.hasTransport(NetworkCapabilities.TRANSPORT_ETHERNET) -> "ethernet"
                else -> "other"
            }
            val validated = caps.hasCapability(NetworkCapabilities.NET_CAPABILITY_VALIDATED)
            if (AgentState.network != type) {
                AgentState.network = type
                AgentState.lastEvent = "network → $type"
            }
            if (validated) scheduleImmediateReconnect()
        }
    }

    private fun registerNetworkCallback() {
        val req = NetworkRequest.Builder()
            .addCapability(NetworkCapabilities.NET_CAPABILITY_INTERNET)
            .build()
        try { connectivity.registerNetworkCallback(req, netCallback) } catch (e: Throwable) {
            Log.e(TAG, "registerNetworkCallback failed", e)
        }
    }

    private fun evaluateNetwork() {
        val caps = connectivity.getNetworkCapabilities(connectivity.activeNetwork)
        if (caps?.hasCapability(NetworkCapabilities.NET_CAPABILITY_VALIDATED) == true) {
            scheduleImmediateReconnect()
        }
    }

    // Debounce: network callbacks fire in bursts; collapse to one reconnect.
    private val reconnectRunnable = Runnable { forceReconnect("network changed") }
    private fun scheduleImmediateReconnect() {
        if (stopped) return
        main.removeCallbacks(reconnectRunnable)
        main.postDelayed(reconnectRunnable, 400)
    }

    private fun forceReconnect(reason: String) {
        if (stopped) return
        AgentState.lastEvent = "reconnect: $reason"
        backoffMs = 1000
        try { ws?.cancel() } catch (_: Throwable) {}
        ws = null
        connect()
    }

    // --- heartbeat ------------------------------------------------------------

    private val heartbeat = object : Runnable {
        override fun run() {
            if (stopped) return
            if (!shizukuReady()) {
                userService = null
                AgentState.shizuku = if (!Shizuku.pingBinder()) "not running" else "permission needed"
            } else if (userService == null) {
                bindShizuku()
            }
            if (AgentState.conn != AgentState.Conn.CONNECTED &&
                AgentState.conn != AgentState.Conn.CONNECTING) {
                connect()
            }
            main.postDelayed(this, HEARTBEAT_MS)
        }
    }

    // --- WebSocket relay ------------------------------------------------------

    private fun connect() {
        if (stopped) return
        val url = relayUrl
        val token = deviceToken
        if (url.isBlank() || token.isBlank()) {
            AgentState.conn = AgentState.Conn.IDLE
            AgentState.lastEvent = "not configured"
            updateNotif(); return
        }
        val wsBase = when {
            url.startsWith("ws") -> url
            url.startsWith("http") -> "ws" + url.substring(4)
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
        AgentState.conn = AgentState.Conn.CONNECTING
        updateNotif()
        ws = http.newWebSocket(Request.Builder().url(full).build(), listener)
    }

    private fun scheduleReconnect() {
        if (stopped) return
        main.postDelayed({ if (AgentState.conn != AgentState.Conn.CONNECTED) connect() }, backoffMs)
        backoffMs = (backoffMs * 2).coerceAtMost(30_000)
    }

    private val listener = object : WebSocketListener() {
        override fun onOpen(webSocket: WebSocket, response: Response) {
            backoffMs = 1000
            AgentState.conn = AgentState.Conn.CONNECTED
            AgentState.connectedSince = System.currentTimeMillis()
            AgentState.lastEvent = "connected"
            updateNotif()
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
                        .put("truncated", false).put("durationMs", 0).toString()
                } else {
                    try { svc.exec(cmd, timeoutMs) } catch (e: Throwable) {
                        JSONObject().put("code", -1).put("stdout", "")
                            .put("stderr", "exec error: ${e.message}")
                            .put("truncated", false).put("durationMs", 0).toString()
                    }
                }
                AgentState.commandsRun++
                AgentState.lastCommandAt = System.currentTimeMillis()
                val out = JSONObject(resultJson).put("type", "result").put("reqId", reqId)
                webSocket.send(out.toString())
            }
        }

        override fun onFailure(webSocket: WebSocket, t: Throwable, response: Response?) {
            if (webSocket !== ws) return // stale socket from a forced reconnect
            AgentState.conn = AgentState.Conn.DISCONNECTED
            AgentState.lastEvent = "disconnected: ${t.message}"
            updateNotif()
            scheduleReconnect()
        }

        override fun onClosed(webSocket: WebSocket, code: Int, reason: String) {
            if (webSocket !== ws) return
            AgentState.conn = AgentState.Conn.DISCONNECTED
            AgentState.lastEvent = "closed: $reason"
            updateNotif()
            scheduleReconnect()
        }
    }

    // --- notification ---------------------------------------------------------

    private fun updateNotif() {
        val text = when (AgentState.conn) {
            AgentState.Conn.CONNECTED -> "connected · ${AgentState.network}"
            AgentState.Conn.CONNECTING -> "connecting…"
            AgentState.Conn.DISCONNECTED -> "reconnecting…"
            AgentState.Conn.IDLE -> AgentState.lastEvent.ifBlank { "idle" }
        }
        getSystemService(NotificationManager::class.java).notify(NOTIF_ID, buildNotification(text))
    }

    private fun buildNotification(text: String): Notification {
        val nm = getSystemService(NotificationManager::class.java)
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
            val ch = NotificationChannel(CHANNEL, "rish-mcp agent", NotificationManager.IMPORTANCE_LOW)
            ch.setShowBadge(false)
            nm.createNotificationChannel(ch)
        }
        return Notification.Builder(this, CHANNEL)
            .setContentTitle("rish-mcp agent")
            .setContentText(text)
            .setSmallIcon(android.R.drawable.stat_sys_data_bluetooth)
            .setOngoing(true)
            .build()
    }

    companion object {
        private const val TAG = "rishmcp"
        private const val CHANNEL = "rishmcp-agent"
        private const val NOTIF_ID = 42
        private const val HEARTBEAT_MS = 30_000L

        fun start(ctx: android.content.Context, reconnect: Boolean = false) {
            val i = Intent(ctx, AgentService::class.java).putExtra("reconnect", reconnect)
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) ctx.startForegroundService(i)
            else ctx.startService(i)
        }

        fun stop(ctx: android.content.Context) {
            ctx.stopService(Intent(ctx, AgentService::class.java))
        }
    }
}
