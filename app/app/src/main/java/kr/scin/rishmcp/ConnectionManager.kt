package kr.scin.rishmcp

import android.content.Context
import android.net.ConnectivityManager
import android.net.Network
import android.net.NetworkCapabilities
import android.os.Build
import android.os.Handler
import android.os.Looper
import android.util.Log
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.cancel
import kotlinx.coroutines.launch
import kr.scin.rishmcp.Prefs.deviceToken
import kr.scin.rishmcp.Prefs.relayUrl
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.Response
import okhttp3.WebSocket
import okhttp3.WebSocketListener
import org.json.JSONObject
import java.util.concurrent.Semaphore
import java.util.concurrent.TimeUnit
import java.util.concurrent.atomic.AtomicBoolean
import java.util.concurrent.atomic.AtomicLong

/**
 * Owns the relay WebSocket and routes "exec" frames to the preferred shell
 * backend (Shizuku first, on-device ADB as fallback).
 * Split out of AgentService so the service stays a thin foreground-service
 * shell (docs/DESIGN.md §2.1: "AdbShellClient/ConnectionManager 사용하도록
 * 내부 배선만 교체").
 *
 * Every device kind currently uses the same always-on WebSocket. Watches use
 * longer ping/heartbeat intervals through [DeviceProfile].
 */
class ConnectionManager(
    private val context: Context,
    adbShellClient: AdbShellClient?,
    private val onStateChanged: () -> Unit,
) {
    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.IO)
    private val shellBackends = ShellBackendManager(context, scope, adbShellClient, ::onShellStateChanged)
    private val main = Handler(Looper.getMainLooper())
    private val http by lazy {
        OkHttpClient.Builder()
            .pingInterval(DeviceProfile.webSocketPingSeconds(context), TimeUnit.SECONDS)
            .retryOnConnectionFailure(true)
            .build()
    }
    private val connectivity by lazy { context.getSystemService(ConnectivityManager::class.java) }

    @Volatile private var ws: WebSocket? = null
    @Volatile private var stopped = false
    @Volatile private var connectedNetHandle = 0L
    @Volatile private var publishedBackend: String? = null
    private val backoffMs = AtomicLong(1000)
    private val reconnectScheduled = AtomicBoolean(false)
    private val commandSlots = Semaphore(MAX_CONCURRENT_COMMANDS)

    /**
     * Monotonic generation counter for relay sockets. connectRelay() bumps it
     * and binds the new socket's listener to that value; listener callbacks
     * ignore themselves unless their epoch is still current. This replaces a
     * bare `webSocket !== ws` staleness check, which had a read-timing race:
     * a dying socket's onFailure could read `ws` before forceReconnect()
     * nulled it, pass the check, and schedule a second reconnection right as
     * connectRelay() was creating the replacement socket — two live sockets
     * on the wire. Binding the epoch at creation time closes that window.
     *
     * Extracted to [EpochGate] so the race semantics are testable without
     * Android dependencies.
     */
    private val epochGate = EpochGate()

    fun start() {
        stopped = false
        registerNetworkCallback()
        shellBackends.start()
        connectRelay()
        main.postDelayed(heartbeat, DeviceProfile.heartbeatMs(context))
    }

    fun stop() {
        stopped = true
        main.removeCallbacksAndMessages(null)
        reconnectScheduled.set(false)
        epochGate.next()
        val closing = ws
        ws = null
        runCatching { closing?.close(1000, "service stopping") }
        runCatching { connectivity.unregisterNetworkCallback(netCallback) }
        shellBackends.stop()
        scope.cancel()
    }

    /** Called by AgentService when relay/token/adb host config changes. */
    fun forceReconnect(reason: String) {
        if (stopped) return
        AgentState.lastEvent = "reconnect: $reason"
        publishedBackend = null
        backoffMs.set(1000)
        main.removeCallbacks(reconnect)
        reconnectScheduled.set(false)
        // Bump the epoch BEFORE tearing down the socket: any callback the old
        // socket still fires (onFailure from the cancel, onClosed from the
        // close) then self-ignores, so it can neither schedule a duplicate
        // reconnect nor clobber the new socket's state.
        epochGate.next()
        runCatching { ws?.cancel() }
        ws = null
        shellBackends.ensureConnected()
        connectRelay()
    }

    // --- relay WebSocket --------------------------------------------------------

    private fun connectRelay() {
        if (stopped) return
        if (ws != null) return
        val url = context.relayUrl
        val token = context.deviceToken
        if (url.isBlank() || token.isBlank()) {
            AgentState.conn = AgentState.Conn.IDLE
            AgentState.lastEvent = "not configured"
            onStateChanged()
            return
        }
        val full = buildRelayUrl(url, token)
        if (full == null) {
            AgentState.conn = AgentState.Conn.IDLE
            AgentState.lastEvent = "invalid relay URL"
            onStateChanged()
            return
        }
        AgentState.conn = AgentState.Conn.CONNECTING
        onStateChanged()
        val epoch = epochGate.next()
        publishedBackend = null
        ws = http.newWebSocket(Request.Builder().url(full).build(), listener(epoch))
    }

    private val reconnect = Runnable {
        reconnectScheduled.set(false)
        if (!stopped && ws == null && AgentState.conn != AgentState.Conn.CONNECTED) connectRelay()
    }

    private fun scheduleReconnect() {
        if (stopped || !reconnectScheduled.compareAndSet(false, true)) return
        main.postDelayed(reconnect, backoffMs.get())
        backoffMs.set((backoffMs.get() * 2).coerceAtMost(30_000))
    }

    private fun listener(epoch: Long) = object : WebSocketListener() {
        private fun stale(webSocket: WebSocket): Boolean =
            webSocket !== ws || !epochGate.isCurrent(epoch)

        override fun onOpen(webSocket: WebSocket, response: Response) {
            if (stale(webSocket)) return
            backoffMs.set(1000)
            connectedNetHandle = connectivity.activeNetwork?.networkHandle ?: 0L
            AgentState.conn = AgentState.Conn.CONNECTED
            AgentState.connectedSince = System.currentTimeMillis()
            AgentState.lastEvent = "connected"
            onStateChanged()
            publishBackend(webSocket)
        }

        override fun onMessage(webSocket: WebSocket, text: String) {
            if (stale(webSocket)) return
            if (text.length > CommandPolicy.MAX_FRAME_CHARS) {
                Log.w(TAG, "oversized agent frame ignored (${text.length} chars)")
                return
            }
            val msg = try { JSONObject(text) } catch (_: Throwable) { return }
            if (msg.optString("type") != "exec") return
            val reqId = msg.optString("reqId")
            val cmd = msg.optString("cmd")
            val timeoutMs = CommandPolicy.clampTimeout(msg.optLong("timeoutMs", 60_000))
            CommandPolicy.validationError(reqId, cmd)?.let { detail ->
                sendError(webSocket, reqId, detail)
                return
            }
            if (!commandSlots.tryAcquire()) {
                sendError(webSocket, reqId, "agent is busy ($MAX_CONCURRENT_COMMANDS commands already running)")
                return
            }
            scope.launch {
                val result = try {
                    try {
                        shellBackends.exec(cmd, timeoutMs)
                    } catch (e: Throwable) {
                        Log.w(TAG, "exec failed", e)
                        ShellResult.unavailable("exec error: ${e.message}")
                    }
                } finally {
                    commandSlots.release()
                }
                AgentState.commandsRun++
                AgentState.lastCommandAt = System.currentTimeMillis()
                val out = JSONObject()
                    .put("type", "result")
                    .put("reqId", reqId)
                    .put("code", result.code)
                    .put("stdout", result.stdout)
                    .put("stderr", result.stderr)
                    .put("truncated", result.truncated)
                    .put("durationMs", result.durationMs)
                if (!webSocket.send(out.toString())) {
                    Log.w(TAG, "result send failed (socket closed?) reqId=$reqId")
                }
            }
        }

        /** Sends a synthetic failure result so the relay client never hangs. */
        private fun sendError(webSocket: WebSocket, reqId: String, detail: String) {
            val out = JSONObject()
                .put("type", "result")
                .put("reqId", reqId)
                .put("code", -1)
                .put("stdout", "")
                .put("stderr", detail)
                .put("truncated", false)
                .put("durationMs", 0)
            if (!webSocket.send(out.toString())) {
                Log.w(TAG, "error result send failed reqId=$reqId")
            }
        }

        override fun onFailure(webSocket: WebSocket, t: Throwable, response: Response?) {
            if (stale(webSocket)) return
            ws = null
            AgentState.conn = AgentState.Conn.DISCONNECTED
            AgentState.lastEvent = "disconnected: ${t.message}"
            onStateChanged()
            scheduleReconnect()
        }

        override fun onClosed(webSocket: WebSocket, code: Int, reason: String) {
            if (stale(webSocket)) return
            ws = null
            AgentState.conn = AgentState.Conn.DISCONNECTED
            AgentState.lastEvent = "closed: $reason"
            onStateChanged()
            scheduleReconnect()
        }
    }

    // --- network watchdog ---------------------------------------------------
    // Use the DEFAULT-network callback: it fires when the network the app's
    // traffic actually uses changes, not on every signal blip. On Wear OS this
    // may be Wi-Fi, cellular, or a Bluetooth/phone-proxied path.

    private val netCallback = object : ConnectivityManager.NetworkCallback() {
        override fun onAvailable(network: Network) {
            AgentState.network = DeviceProfile.networkLabel(connectivity.getNetworkCapabilities(network))
            if (AgentState.conn == AgentState.Conn.CONNECTED &&
                connectedNetHandle == network.networkHandle
            ) {
                return // same network, keep socket
            }
            AgentState.lastEvent = "default network → ${AgentState.network}"
            scheduleSwitchReconnect()
        }

        override fun onCapabilitiesChanged(network: Network, caps: NetworkCapabilities) {
            AgentState.network = DeviceProfile.networkLabel(caps) // label only — no reconnect on signal changes
        }

        override fun onLost(network: Network) {
            AgentState.network = "none"
            AgentState.lastEvent = "network lost"
        }
    }

    private fun registerNetworkCallback() {
        try {
            connectivity.registerDefaultNetworkCallback(netCallback)
        } catch (e: Throwable) {
            Log.e(TAG, "registerDefaultNetworkCallback failed", e)
        }
    }

    private val switchReconnect = Runnable { forceReconnect("network switch") }
    private fun scheduleSwitchReconnect() {
        if (stopped) return
        main.removeCallbacks(switchReconnect)
        main.postDelayed(switchReconnect, 800)
    }

    // --- heartbeat -----------------------------------------------------------

    private val heartbeat = object : Runnable {
        override fun run() {
            if (stopped) return
            shellBackends.ensureConnected()
            if (ws == null && AgentState.conn != AgentState.Conn.CONNECTED &&
                AgentState.conn != AgentState.Conn.CONNECTING
            ) {
                main.removeCallbacks(reconnect)
                reconnectScheduled.set(false)
                connectRelay()
            }
            main.postDelayed(this, DeviceProfile.heartbeatMs(context))
        }
    }

    private fun buildRelayUrl(rawUrl: String, token: String) = RelayUrlPolicy.withAgentQuery(
        rawUrl,
        mapOf(
            "token" to token,
            "deviceId" to Prefs.deviceId(context),
            "name" to Build.MODEL,
            "sdk" to Build.VERSION.SDK_INT.toString(),
            "kind" to DeviceProfile.kind(context),
            "ver" to BuildConfig.VERSION_NAME,
            "vc" to BuildConfig.VERSION_CODE.toString(),
            "backend" to (shellBackends.activeKind?.wireName ?: "none"),
        ),
    )

    private fun onShellStateChanged() {
        onStateChanged()
        ws?.let(::publishBackend)
    }

    private fun publishBackend(webSocket: WebSocket) {
        val backend = shellBackends.activeKind?.wireName ?: "none"
        if (publishedBackend == backend) return
        val sent = webSocket.send(
            JSONObject()
                .put("type", "status")
                .put("backend", backend)
                .toString(),
        )
        if (sent) publishedBackend = backend
    }

    companion object {
        private const val TAG = "rishmcp"
        private const val MAX_CONCURRENT_COMMANDS = 4
    }
}
