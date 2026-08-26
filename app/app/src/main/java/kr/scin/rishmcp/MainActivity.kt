package kr.scin.rishmcp

import android.Manifest
import android.content.pm.PackageManager
import android.os.Build
import android.os.Bundle
import android.os.Handler
import android.os.Looper
import android.os.Process
import android.view.View
import android.widget.TextView
import android.widget.Toast
import androidx.appcompat.app.AppCompatActivity
import androidx.lifecycle.lifecycleScope
import com.google.android.material.button.MaterialButton
import com.google.android.material.textfield.TextInputEditText
import kotlinx.coroutines.launch
import kr.scin.rishmcp.Prefs.adbPort
import kr.scin.rishmcp.Prefs.deviceToken
import kr.scin.rishmcp.Prefs.enabled
import kr.scin.rishmcp.Prefs.relayUrl
import rikka.shizuku.Shizuku

/**
 * Provisioning UI for the preferred Shizuku backend and the on-device ADB
 * fallback. Headless `am start` provisioning is isolated in
 * [ProvisioningActivity].
 */
class MainActivity : AppCompatActivity() {

    private lateinit var statusDot: View
    private lateinit var statusText: TextView
    private lateinit var uptime: TextView
    private lateinit var rowShell: TextView
    private lateinit var rowNetwork: TextView
    private lateinit var rowDevice: TextView
    private lateinit var rowStats: TextView
    private lateinit var rowEvent: TextView
    private lateinit var relayField: TextInputEditText
    private lateinit var tokenField: TextInputEditText
    private lateinit var pairHint: TextView
    private lateinit var pairingSection: View
    private lateinit var pairingPortField: TextInputEditText
    private lateinit var pairingCodeField: TextInputEditText
    private lateinit var connectPortField: TextInputEditText
    private lateinit var shizukuButton: MaterialButton

    private val shizukuPermissionListener = Shizuku.OnRequestPermissionResultListener { requestCode, result ->
        if (requestCode != SHIZUKU_PERMISSION_REQUEST) return@OnRequestPermissionResultListener
        // Shizuku does not promise that binder callbacks arrive on the main
        // thread. Keep Toast/view updates and service interaction on it.
        runOnUiThread {
            val granted = result == PackageManager.PERMISSION_GRANTED
            toast(if (granted) "Shizuku permission granted" else "Shizuku permission denied")
            if (granted && AgentState.serviceRunning) AgentService.start(this, reconnect = true)
            render()
        }
    }

    private val ui = Handler(Looper.getMainLooper())
    private val ticker = object : Runnable {
        override fun run() { render(); ui.postDelayed(this, 1000) }
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        setContentView(R.layout.activity_main)

        statusDot = findViewById(R.id.statusDot)
        statusText = findViewById(R.id.statusText)
        uptime = findViewById(R.id.uptime)
        rowShell = findViewById(R.id.rowShell)
        rowNetwork = findViewById(R.id.rowNetwork)
        rowDevice = findViewById(R.id.rowDevice)
        rowStats = findViewById(R.id.rowStats)
        rowEvent = findViewById(R.id.rowEvent)
        relayField = findViewById(R.id.relayField)
        tokenField = findViewById(R.id.tokenField)
        pairHint = findViewById(R.id.pairHint)
        pairingSection = findViewById(R.id.pairingSection)
        pairingPortField = findViewById(R.id.pairingPortField)
        pairingCodeField = findViewById(R.id.pairingCodeField)
        connectPortField = findViewById(R.id.connectPortField)
        shizukuButton = findViewById(R.id.btnShizuku)

        findViewById<TextView>(R.id.subtitle).text =
            if (DeviceProfile.isWatch(this)) "Wear OS · Shizuku / ADB → MCP" else "Shizuku / ADB shell → MCP"

        relayField.setText(relayUrl)
        tokenField.setText(deviceToken)
        if (adbPort > 0) connectPortField.setText(adbPort.toString())

        // Wireless-debugging pairing (adb pair) only exists on Android 11+;
        // below that, the only path is the PC+adb tcpip bridge.
        val hasWirelessPairing = Build.VERSION.SDK_INT >= Build.VERSION_CODES.R
        pairingSection.visibility = if (hasWirelessPairing) View.VISIBLE else View.GONE
        pairHint.text = if (hasWirelessPairing) {
            "설정 → 개발자 옵션 → 무선 디버깅 → 페어링 코드로 기기 페어링에서 확인한 포트/코드를 입력하세요"
        } else {
            "Android 11 미만: PC에서 adb로 'adb tcpip <port>'를 1회 실행한 뒤, 그 포트만 아래에 입력하세요"
        }

        shizukuButton.setOnClickListener { requestShizuku() }
        findViewById<MaterialButton>(R.id.btnPair).setOnClickListener { pairAdb() }
        findViewById<MaterialButton>(R.id.btnSaveAdbPort).setOnClickListener { saveAdbPort() }
        findViewById<MaterialButton>(R.id.btnStart).setOnClickListener { saveAndStart() }
        findViewById<MaterialButton>(R.id.btnStop).setOnClickListener {
            enabled = false; AgentService.stop(this); toast("agent stopped"); render()
        }
        findViewById<MaterialButton>(R.id.btnTest).setOnClickListener { runTest() }

        Shizuku.addRequestPermissionResultListener(shizukuPermissionListener)
        maybeRequestNotifications()
    }

    override fun onResume() { super.onResume(); ui.post(ticker) }
    override fun onPause() { super.onPause(); ui.removeCallbacks(ticker) }

    override fun onDestroy() {
        runCatching { Shizuku.removeRequestPermissionResultListener(shizukuPermissionListener) }
        super.onDestroy()
    }

    private fun requestShizuku() {
        val running = runCatching { Shizuku.pingBinder() }.getOrDefault(false)
        if (!running) {
            val launch = packageManager.getLaunchIntentForPackage(SHIZUKU_PACKAGE)
            if (launch != null) {
                startActivity(launch)
                toast("Start Shizuku, then return to rish-mcp")
            } else {
                toast("Install and start Shizuku, or use ADB fallback")
            }
            return
        }
        if (runCatching { Shizuku.isPreV11() }.getOrDefault(true)) {
            toast("Shizuku server API v11+ is required")
            return
        }
        if (runCatching { Shizuku.getUid() }.getOrDefault(-1) != Process.SHELL_UID) {
            toast("Root-mode Shizuku is rejected; start Shizuku in ADB mode")
            return
        }
        val granted = runCatching {
            Shizuku.checkSelfPermission() == PackageManager.PERMISSION_GRANTED
        }.getOrDefault(false)
        if (granted) {
            if (AgentState.serviceRunning) AgentService.start(this, reconnect = true)
            toast("Shizuku is ready")
            return
        }
        if (runCatching { Shizuku.shouldShowRequestPermissionRationale() }.getOrDefault(false)) {
            toast("Grant rish-mcp from Shizuku's authorized applications screen")
            startShizukuManager()
            return
        }
        runCatching { Shizuku.requestPermission(SHIZUKU_PERMISSION_REQUEST) }
            .onFailure { toast("Unable to request Shizuku permission: ${it.message}") }
    }

    private fun startShizukuManager() {
        packageManager.getLaunchIntentForPackage(SHIZUKU_PACKAGE)?.let(::startActivity)
    }

    private fun pairAdb() {
        val port = pairingPortField.text.toString().trim().toIntOrNull()
        val code = pairingCodeField.text.toString().trim()
        if (port == null || port !in VALID_PORTS || !PAIRING_CODE.matches(code)) {
            toast("1–65535 포트와 6자리 pairing code를 입력하세요")
            return
        }
        lifecycleScope.launch {
            AgentState.shell = "pairing…"
            render()
            val ok = try {
                AdbShellClient.getInstance(this@MainActivity).pairWireless("127.0.0.1", port, code)
            } catch (e: Throwable) {
                false
            }
            AgentState.shell = if (ok) "paired" else "pairing failed"
            toast(if (ok) "페어링 완료 — 아래 Connect port도 입력하고 저장하세요" else "페어링 실패")
            render()
        }
    }

    private fun saveAdbPort() {
        val port = connectPortField.text.toString().trim().toIntOrNull()
        if (port == null || port !in VALID_PORTS) {
            toast("1–65535 범위의 포트를 입력하세요")
            return
        }
        adbPort = port
        toast("adb port 저장됨")
        if (AgentState.serviceRunning) AgentService.start(this, reconnect = true)
        render()
    }

    private fun saveAndStart() {
        val relay = relayField.text.toString().trim()
        val token = tokenField.text.toString().trim()
        if (RelayUrlPolicy.parse(relay) == null) {
            toast("올바른 relay URL을 입력하세요")
            return
        }
        if (token.isEmpty()) {
            toast("device token을 입력하세요")
            return
        }
        relayUrl = relay
        deviceToken = token
        enabled = true
        AgentService.start(this, reconnect = true)
        toast("agent started")
        render()
    }

    private fun runTest() {
        toast(
            if (AgentState.conn == AgentState.Conn.CONNECTED)
                "connected — issue run_shell from your AI to test"
            else "not connected yet (state: ${AgentState.conn})"
        )
    }

    // --- live rendering -------------------------------------------------------

    private fun render() {
        val s = AgentState
        val (label, color) = when {
            !s.serviceRunning -> "stopped" to R.color.status_grey
            s.conn == AgentState.Conn.CONNECTED -> "connected" to R.color.status_green
            s.conn == AgentState.Conn.CONNECTING -> "connecting…" to R.color.status_amber
            s.conn == AgentState.Conn.DISCONNECTED -> "reconnecting…" to R.color.status_amber
            else -> "idle" to R.color.status_grey
        }
        statusText.text = label
        statusDot.background?.setTint(getColor(color))
        uptime.text = if (s.conn == AgentState.Conn.CONNECTED && s.connectedSince > 0)
            "up ${fmtDuration(System.currentTimeMillis() - s.connectedSince)}" else ""

        rowShell.text = "Shell:    ${s.shell}"
        val shizuku = shizukuStatus()
        shizukuButton.text = when (shizuku) {
            "ready" -> "Shizuku ready"
            "permission needed" -> "Grant Shizuku"
            "root mode rejected" -> "Use Shizuku ADB mode"
            else -> "Open Shizuku"
        }
        shizukuButton.isEnabled = shizuku != "ready"
        rowNetwork.text = "Network:  ${s.network}"
        rowDevice.text = "Device:   ${Build.MODEL} · ${Prefs.deviceId(this)}"
        rowStats.text = "Commands: ${s.commandsRun}" +
            if (s.lastCommandAt > 0) "  (last ${fmtDuration(System.currentTimeMillis() - s.lastCommandAt)} ago)" else ""
        rowEvent.text = s.lastEvent
    }

    private fun fmtDuration(ms: Long): String {
        val sec = ms / 1000
        return when {
            sec < 60 -> "${sec}s"
            sec < 3600 -> "${sec / 60}m ${sec % 60}s"
            else -> "${sec / 3600}h ${(sec % 3600) / 60}m"
        }
    }

    private fun maybeRequestNotifications() {
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU &&
            checkSelfPermission(Manifest.permission.POST_NOTIFICATIONS) != PackageManager.PERMISSION_GRANTED
        ) {
            requestPermissions(arrayOf(Manifest.permission.POST_NOTIFICATIONS), 1002)
        }
    }

    private fun toast(s: String) = Toast.makeText(this, s, Toast.LENGTH_SHORT).show()

    private fun shizukuStatus(): String {
        if (!runCatching { Shizuku.pingBinder() }.getOrDefault(false)) return "not running"
        if (runCatching { Shizuku.isPreV11() }.getOrDefault(true)) return "server API too old"
        if (runCatching { Shizuku.getUid() }.getOrDefault(-1) != Process.SHELL_UID) {
            return "root mode rejected"
        }
        return if (runCatching {
                Shizuku.checkSelfPermission() == PackageManager.PERMISSION_GRANTED
            }.getOrDefault(false)
        ) "ready" else "permission needed"
    }

    companion object {
        private const val SHIZUKU_PERMISSION_REQUEST = 1001
        private const val SHIZUKU_PACKAGE = "moe.shizuku.privileged.api"
        private val VALID_PORTS = 1..65_535
        private val PAIRING_CODE = Regex("^[0-9]{6}$")
    }
}
