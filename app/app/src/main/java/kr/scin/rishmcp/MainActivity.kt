package kr.scin.rishmcp

import android.Manifest
import android.content.Intent
import android.content.pm.PackageManager
import android.os.Build
import android.os.Bundle
import android.os.Handler
import android.os.Looper
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

/**
 * Provisioning UI. Replaces the old "Grant Shizuku" flow with ADB pairing:
 * on Android 11+, wireless-debugging pairing (a code the user reads off
 * Settings); below that, just the port from the PC+adb tcpip bridge
 * (docs/DESIGN.md §3.1). Also handles headless `am start` provisioning.
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

        findViewById<TextView>(R.id.subtitle).text =
            if (DeviceProfile.isWatch(this)) "Wear OS · ADB shell → MCP" else "ADB shell → MCP agent"

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

        findViewById<MaterialButton>(R.id.btnPair).setOnClickListener { pairAdb() }
        findViewById<MaterialButton>(R.id.btnSaveAdbPort).setOnClickListener { saveAdbPort() }
        findViewById<MaterialButton>(R.id.btnStart).setOnClickListener { saveAndStart() }
        findViewById<MaterialButton>(R.id.btnStop).setOnClickListener {
            enabled = false; AgentService.stop(this); toast("agent stopped"); render()
        }
        findViewById<MaterialButton>(R.id.btnTest).setOnClickListener { runTest() }

        maybeRequestNotifications()
        handleProvisioning(intent)
    }

    override fun onNewIntent(intent: Intent?) {
        super.onNewIntent(intent)
        setIntent(intent)
        handleProvisioning(intent)
    }

    override fun onResume() { super.onResume(); ui.post(ticker) }
    override fun onPause() { super.onPause(); ui.removeCallbacks(ticker) }

    /**
     * Headless provisioning from a shell:
     *   am start -n kr.scin.rishmcp/.MainActivity \
     *     --es relay wss://mcp.example.com/agent --es token <DEVICE_TOKEN> \
     *     --ei adbPort <PORT> --ez autostart true
     */
    private fun handleProvisioning(intent: Intent?) {
        intent ?: return
        var changed = false
        intent.getStringExtra("relay")?.let { relayUrl = it; relayField.setText(it); changed = true }
        intent.getStringExtra("token")?.let { deviceToken = it; tokenField.setText(it); changed = true }
        if (intent.hasExtra("adbPort")) {
            val port = intent.getIntExtra("adbPort", 0)
            if (port > 0) {
                adbPort = port
                connectPortField.setText(port.toString())
                changed = true
            }
        }
        if (intent.getBooleanExtra("autostart", false)) {
            enabled = true
            AgentService.start(this, reconnect = true)
            toast("provisioned & started")
        } else if (changed) {
            toast("config received")
        }
        render()
    }

    private fun pairAdb() {
        val port = pairingPortField.text.toString().trim().toIntOrNull()
        val code = pairingCodeField.text.toString().trim()
        if (port == null || port <= 0 || code.isBlank()) {
            toast("pairing port와 code를 입력하세요")
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
        if (port == null || port <= 0) {
            toast("포트를 입력하세요")
            return
        }
        adbPort = port
        toast("adb port 저장됨")
        if (AgentState.serviceRunning) AgentService.start(this, reconnect = true)
        render()
    }

    private fun saveAndStart() {
        relayUrl = relayField.text.toString().trim()
        deviceToken = tokenField.text.toString().trim()
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

        rowShell.text = "ADB shell: ${s.shell}"
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
}
