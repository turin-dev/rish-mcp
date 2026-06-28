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
import com.google.android.material.button.MaterialButton
import com.google.android.material.textfield.TextInputEditText
import kr.scin.rishmcp.Prefs.deviceToken
import kr.scin.rishmcp.Prefs.enabled
import kr.scin.rishmcp.Prefs.relayUrl
import rikka.shizuku.Shizuku

class MainActivity : AppCompatActivity() {

    private lateinit var statusDot: View
    private lateinit var statusText: TextView
    private lateinit var uptime: TextView
    private lateinit var rowShizuku: TextView
    private lateinit var rowNetwork: TextView
    private lateinit var rowDevice: TextView
    private lateinit var rowStats: TextView
    private lateinit var rowEvent: TextView
    private lateinit var relayField: TextInputEditText
    private lateinit var tokenField: TextInputEditText

    private val ui = Handler(Looper.getMainLooper())
    private val ticker = object : Runnable {
        override fun run() { render(); ui.postDelayed(this, 1000) }
    }

    private val permListener = Shizuku.OnRequestPermissionResultListener { _, result ->
        runOnUiThread {
            toast(if (result == PackageManager.PERMISSION_GRANTED) "Shizuku granted" else "Shizuku denied")
            render()
        }
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        setContentView(R.layout.activity_main)

        statusDot = findViewById(R.id.statusDot)
        statusText = findViewById(R.id.statusText)
        uptime = findViewById(R.id.uptime)
        rowShizuku = findViewById(R.id.rowShizuku)
        rowNetwork = findViewById(R.id.rowNetwork)
        rowDevice = findViewById(R.id.rowDevice)
        rowStats = findViewById(R.id.rowStats)
        rowEvent = findViewById(R.id.rowEvent)
        relayField = findViewById(R.id.relayField)
        tokenField = findViewById(R.id.tokenField)

        relayField.setText(relayUrl)
        tokenField.setText(deviceToken)

        findViewById<MaterialButton>(R.id.btnShizuku).setOnClickListener { requestShizuku() }
        findViewById<MaterialButton>(R.id.btnStart).setOnClickListener { saveAndStart() }
        findViewById<MaterialButton>(R.id.btnStop).setOnClickListener {
            enabled = false; AgentService.stop(this); toast("agent stopped"); render()
        }
        findViewById<MaterialButton>(R.id.btnTest).setOnClickListener { runTest() }

        Shizuku.addRequestPermissionResultListener(permListener)
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
    override fun onDestroy() {
        Shizuku.removeRequestPermissionResultListener(permListener); super.onDestroy()
    }

    /**
     * Headless provisioning from a shell:
     *   am start -n kr.scin.rishmcp/.MainActivity \
     *     --es relay wss://mcp.example.com/agent --es token <DEVICE_TOKEN> --ez autostart true
     */
    private fun handleProvisioning(intent: Intent?) {
        intent ?: return
        var changed = false
        intent.getStringExtra("relay")?.let { relayUrl = it; relayField.setText(it); changed = true }
        intent.getStringExtra("token")?.let { deviceToken = it; tokenField.setText(it); changed = true }
        if (intent.getBooleanExtra("autostart", false)) {
            enabled = true
            AgentService.start(this, reconnect = true)
            toast("provisioned & started")
        } else if (changed) {
            toast("config received")
        }
        render()
    }

    private fun requestShizuku() {
        if (!Shizuku.pingBinder()) { toast("Shizuku app is not running"); return }
        if (Shizuku.checkSelfPermission() == PackageManager.PERMISSION_GRANTED) {
            toast("already granted"); render(); return
        }
        Shizuku.requestPermission(1001)
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

        val shizukuLive = when {
            !Shizuku.pingBinder() -> "not running"
            Shizuku.checkSelfPermission() != PackageManager.PERMISSION_GRANTED -> "permission needed"
            else -> s.shizuku
        }
        rowShizuku.text = "Shizuku:  $shizukuLive"
        rowNetwork.text = "Network:  ${s.network}"
        rowDevice.text = "Device:   ${Prefs.deviceId(this)}"
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
