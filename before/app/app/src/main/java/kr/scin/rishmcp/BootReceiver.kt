package kr.scin.rishmcp

import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import kr.scin.rishmcp.Prefs.enabled

/** Restart the agent after reboot if the user had it enabled. */
class BootReceiver : BroadcastReceiver() {
    override fun onReceive(context: Context, intent: Intent?) {
        val action = intent?.action ?: return
        if (action == Intent.ACTION_BOOT_COMPLETED || action == Intent.ACTION_LOCKED_BOOT_COMPLETED) {
            if (context.enabled) AgentService.start(context)
        }
    }
}
