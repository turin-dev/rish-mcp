package kr.scin.rishmcp

import android.Manifest
import android.content.Intent
import android.content.pm.PackageManager
import android.os.Build
import android.os.Bundle
import android.text.InputType
import android.view.Gravity
import android.widget.Button
import android.widget.EditText
import android.widget.LinearLayout
import android.widget.TextView
import android.widget.Toast
import androidx.appcompat.app.AppCompatActivity
import kr.scin.rishmcp.Prefs.deviceToken
import kr.scin.rishmcp.Prefs.enabled
import kr.scin.rishmcp.Prefs.relayUrl
import rikka.shizuku.Shizuku

class MainActivity : AppCompatActivity() {

    private lateinit var status: TextView
    private lateinit var urlField: EditText
    private lateinit var tokenField: EditText

    private val permListener = Shizuku.OnRequestPermissionResultListener { _, result ->
        runOnUiThread {
            toast(if (result == PackageManager.PERMISSION_GRANTED) "Shizuku granted" else "Shizuku denied")
            refreshStatus()
        }
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        val pad = (16 * resources.displayMetrics.density).toInt()
        val root = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            setPadding(pad, pad, pad, pad)
        }

        root.addView(TextView(this).apply {
            text = "rish-mcp agent"
            textSize = 22f
        })

        status = TextView(this).apply { setPadding(0, pad, 0, pad) }
        root.addView(status)

        root.addView(label("Relay URL (wss://…/agent)"))
        urlField = EditText(this).apply {
            setText(this@MainActivity.relayUrl)
            inputType = InputType.TYPE_TEXT_VARIATION_URI
        }
        root.addView(urlField)

        root.addView(label("Device token"))
        tokenField = EditText(this).apply {
            setText(this@MainActivity.deviceToken)
            inputType = InputType.TYPE_TEXT_VARIATION_VISIBLE_PASSWORD
        }
        root.addView(tokenField)

        root.addView(Button(this).apply {
            text = "Grant Shizuku permission"
            setOnClickListener { requestShizuku() }
        })
        root.addView(Button(this).apply {
            text = "Save & Start agent"
            setOnClickListener { saveAndStart() }
        })
        root.addView(Button(this).apply {
            text = "Stop agent"
            setOnClickListener {
                this@MainActivity.enabled = false
                AgentService.stop(this@MainActivity)
                refreshStatus()
            }
        })

        setContentView(root)
        Shizuku.addRequestPermissionResultListener(permListener)
        maybeRequestNotifications()
        handleProvisioning(intent)
    }

    override fun onNewIntent(intent: Intent?) {
        super.onNewIntent(intent)
        setIntent(intent)
        handleProvisioning(intent)
    }

    /**
     * Headless provisioning from a shell, e.g.:
     *   am start -n kr.scin.rishmcp/.MainActivity \
     *     --es relay wss://mcp.example.com/agent --es token <DEVICE_TOKEN> --ez autostart true
     * Lets an operator configure the agent without typing on the device.
     */
    private fun handleProvisioning(intent: Intent?) {
        intent ?: return
        var changed = false
        intent.getStringExtra("relay")?.let { relayUrl = it; urlField.setText(it); changed = true }
        intent.getStringExtra("token")?.let { deviceToken = it; tokenField.setText(it); changed = true }
        if (intent.getBooleanExtra("autostart", false)) {
            enabled = true
            AgentService.start(this)
            toast("agent provisioned & started")
        } else if (changed) {
            toast("config received")
        }
        refreshStatus()
    }

    override fun onResume() {
        super.onResume()
        refreshStatus()
    }

    override fun onDestroy() {
        Shizuku.removeRequestPermissionResultListener(permListener)
        super.onDestroy()
    }

    private fun label(s: String) = TextView(this).apply {
        text = s
        setPadding(0, 24, 0, 4)
    }

    private fun requestShizuku() {
        if (!Shizuku.pingBinder()) {
            toast("Shizuku app is not running")
            return
        }
        if (Shizuku.checkSelfPermission() == PackageManager.PERMISSION_GRANTED) {
            toast("Already granted")
            refreshStatus()
            return
        }
        Shizuku.requestPermission(1001)
    }

    private fun saveAndStart() {
        relayUrl = urlField.text.toString().trim()
        deviceToken = tokenField.text.toString().trim()
        enabled = true
        AgentService.start(this)
        toast("agent started")
        refreshStatus()
    }

    private fun refreshStatus() {
        val shizuku = when {
            !Shizuku.pingBinder() -> "Shizuku: not running"
            Shizuku.checkSelfPermission() == PackageManager.PERMISSION_GRANTED -> "Shizuku: granted ✓"
            else -> "Shizuku: permission needed"
        }
        status.text = "$shizuku\nAgent enabled: $enabled\nDevice: ${Prefs.deviceId(this)}"
        status.gravity = Gravity.START
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
