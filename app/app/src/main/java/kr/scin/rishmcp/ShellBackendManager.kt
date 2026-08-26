package kr.scin.rishmcp

import android.content.Context
import android.util.Log
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.launch
import kr.scin.rishmcp.Prefs.adbHost
import kr.scin.rishmcp.Prefs.adbPort
import java.util.concurrent.atomic.AtomicBoolean

/**
 * Keeps both shell transports healthy. Shizuku is preferred because it avoids
 * a loopback adbd port and survives wireless-debugging port changes; ADB is a
 * transparent fallback when Shizuku is absent, stopped, or not authorized.
 */
class ShellBackendManager(
    private val context: Context,
    private val scope: CoroutineScope,
    private val adbClient: AdbShellClient?,
    private val onStateChanged: () -> Unit,
) {
    private val shizukuClient = ShizukuShellClient(context, ::refreshStatus)
    private val adbConnecting = AtomicBoolean(false)
    private val adbBackend = object : ShellBackend {
        override val kind = ShellBackend.Kind.ADB
        override val isReady: Boolean get() = adbClient?.isConnected == true
        override suspend fun exec(cmd: String, timeoutMs: Long) =
            adbClient?.exec(cmd, timeoutMs) ?: ShellResult.unavailable("ADB backend failed to initialize")
    }
    private val router = ShellBackendRouter(shizukuClient, adbBackend)

    val activeKind: ShellBackend.Kind?
        get() = router.active()?.kind

    fun start() {
        shizukuClient.start()
        ensureConnected()
    }

    fun stop() {
        shizukuClient.stop()
        refreshStatus()
    }

    fun ensureConnected() {
        shizukuClient.ensureConnected()
        if (!shizukuClient.isReady) ensureAdbConnected()
        refreshStatus()
    }

    suspend fun exec(cmd: String, timeoutMs: Long): ShellResult {
        val selected = router.active()
            ?: return ShellResult.unavailable(
                "no shell backend is ready; start/grant Shizuku or connect ADB",
            )
        AgentState.activeBackend = selected.kind.wireName
        return selected.exec(cmd, timeoutMs)
    }

    private fun ensureAdbConnected() {
        val adb = adbClient ?: return
        if (adb.isConnected || !adbConnecting.compareAndSet(false, true)) return
        val host = context.adbHost
        val port = context.adbPort
        if (port <= 0) {
            adbConnecting.set(false)
            refreshStatus()
            return
        }
        scope.launch {
            refreshStatus("ADB connecting…")
            try {
                adb.connectDevice(host, port)
            } catch (error: Throwable) {
                Log.w(TAG, "ADB connect failed", error)
                AgentState.lastEvent = "ADB connect error: ${error.message}"
            } finally {
                adbConnecting.set(false)
                refreshStatus()
            }
        }
    }

    private fun refreshStatus(override: String? = null) {
        val active = router.active()?.kind
        AgentState.activeBackend = active?.wireName ?: "none"
        AgentState.shell = override ?: when (active) {
            ShellBackend.Kind.SHIZUKU -> "Shizuku connected"
            ShellBackend.Kind.ADB -> "ADB connected · Shizuku ${shizukuClient.state.label}"
            null -> when {
                adbConnecting.get() -> "ADB connecting… · Shizuku ${shizukuClient.state.label}"
                adbClient == null -> "Shizuku ${shizukuClient.state.label} · ADB unavailable"
                context.adbPort <= 0 -> "Shizuku ${shizukuClient.state.label} · ADB not paired"
                else -> "Shizuku ${shizukuClient.state.label} · ADB disconnected"
            }
        }
        onStateChanged()
    }

    companion object {
        private const val TAG = "rishmcp-shell"
    }
}
