package kr.scin.rishmcp

import android.app.Activity
import android.content.Intent
import android.os.Build
import android.os.Bundle
import kr.scin.rishmcp.Prefs.adbPort
import kr.scin.rishmcp.Prefs.deviceToken
import kr.scin.rishmcp.Prefs.enabled
import kr.scin.rishmcp.Prefs.relayUrl

/**
 * DUMP-protected entry point for `adb shell am start` provisioning. Keeping
 * this separate means the launcher-exported MainActivity never consumes
 * configuration extras from arbitrary apps.
 */
class ProvisioningActivity : Activity() {
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.UPSIDE_DOWN_CAKE &&
            getLaunchedFromUid() != SHELL_UID
        ) {
            finish()
            return
        }

        intent.getStringExtra("relay")
            ?.trim()
            ?.takeIf { RelayUrlPolicy.parse(it) != null }
            ?.let { relayUrl = it }
        intent.getStringExtra("token")
            ?.trim()
            ?.takeIf(String::isNotEmpty)
            ?.let { deviceToken = it }
        if (intent.hasExtra("adbPort")) {
            intent.getIntExtra("adbPort", 0).takeIf { it in VALID_PORTS }?.let { adbPort = it }
        }
        if (intent.getBooleanExtra("autostart", false)) {
            enabled = true
            AgentService.start(this, reconnect = true)
        }

        startActivity(
            Intent(this, MainActivity::class.java)
                .addFlags(Intent.FLAG_ACTIVITY_CLEAR_TOP or Intent.FLAG_ACTIVITY_SINGLE_TOP),
        )
        finish()
    }

    companion object {
        private const val SHELL_UID = 2000
        private val VALID_PORTS = 1..65_535
    }
}
