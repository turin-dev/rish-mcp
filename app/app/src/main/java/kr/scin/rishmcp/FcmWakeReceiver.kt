package kr.scin.rishmcp

import android.util.Log
import com.google.firebase.messaging.FirebaseMessagingService
import com.google.firebase.messaging.RemoteMessage
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.launch

/**
 * Low-spec device wake path (docs/DESIGN.md §3.2, roadmap step 4): when the
 * relay has a queued command for a device that isn't holding an always-on
 * WebSocket, it's meant to wake it via FCM instead of paying the battery
 * cost of staying connected. This is the receiving end — starting
 * AgentService for a short on-demand session on wake.
 *
 * Inert without a real Firebase project (docs/DESIGN.md §7):
 * FirebaseMessagingService is only instantiated once google-services.json
 * exists and the conditional plugin in app/build.gradle.kts actually
 * initializes Firebase.
 *
 * Not yet wired even once that exists:
 * - The relay has no endpoint to receive the token onNewToken produces, and
 *   no internal/fcm package to send wake pushes with — see the TODOs below.
 * - ConnectionManager doesn't yet branch low-spec devices onto an on-demand
 *   connection instead of the always-on WS; it still just uses AgentService
 *   the same way for every device kind.
 * This class is the shape the receiving side should have once those land.
 */
class FcmWakeReceiver : FirebaseMessagingService() {

    private val scope = CoroutineScope(Dispatchers.Default)

    override fun onNewToken(token: String) {
        super.onNewToken(token)
        // TODO(docs/DESIGN.md §3.2): report this token to the relay so it
        // can address this device for a wake push. No relay-side endpoint
        // exists yet to receive it (needs an internal/fcm package + a
        // registration route alongside /agent).
        Log.i(TAG, "new FCM token (not yet reported to relay)")
    }

    override fun onMessageReceived(message: RemoteMessage) {
        super.onMessageReceived(message)
        if (message.data["type"] != "wake") return
        Log.i(TAG, "wake push received; starting a short on-demand session")
        scope.launch {
            AgentService.start(applicationContext, reconnect = true)
        }
    }

    companion object {
        private const val TAG = "rishmcp-fcm"
    }
}
