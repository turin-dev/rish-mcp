package kr.scin.rishmcp

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.app.Service
import android.content.Context
import android.content.Intent
import android.os.IBinder
import android.util.Log

/**
 * Always-on foreground service. Holds the relay connection (via
 * [ConnectionManager]) and the Shizuku/ADB shell backends.
 */
class AgentService : Service() {

    private lateinit var connectionManager: ConnectionManager

    override fun onBind(intent: Intent?): IBinder? = null

    override fun onCreate() {
        super.onCreate()
        AgentState.serviceRunning = true
        startForeground(NOTIF_ID, buildNotification("starting…"))
        val adbClient = runCatching { AdbShellClient.getInstance(this) }
            .onFailure {
                Log.e(TAG, "ADB backend initialization failed; Shizuku remains available", it)
                AgentState.lastEvent = "ADB unavailable: ${it.message}"
            }
            .getOrNull()
        connectionManager = ConnectionManager(
            context = this,
            adbShellClient = adbClient,
            onStateChanged = ::updateNotif,
        )
        connectionManager.start()
        updateNotif()
    }

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        // Re-provisioning (new relay/token/adb host) restarts the connection
        // without a force-stop.
        if (intent?.getBooleanExtra("reconnect", false) == true) {
            connectionManager.forceReconnect("reconfigured")
        }
        return START_STICKY
    }

    override fun onDestroy() {
        AgentState.serviceRunning = false
        AgentState.conn = AgentState.Conn.IDLE
        connectionManager.stop()
        super.onDestroy()
    }

    private fun updateNotif() {
        val text = when (AgentState.conn) {
            AgentState.Conn.CONNECTED -> "connected · ${AgentState.network} · shell ${AgentState.shell}"
            AgentState.Conn.CONNECTING -> "connecting…"
            AgentState.Conn.DISCONNECTED -> "reconnecting…"
            AgentState.Conn.IDLE -> AgentState.lastEvent.ifBlank { "idle" }
        }
        getSystemService(NotificationManager::class.java).notify(NOTIF_ID, buildNotification(text))
    }

    private fun buildNotification(text: String): Notification {
        val nm = getSystemService(NotificationManager::class.java)
        val channel = NotificationChannel(CHANNEL, "rish-mcp agent", NotificationManager.IMPORTANCE_LOW)
        channel.setShowBadge(false)
        nm.createNotificationChannel(channel)
        val openApp = PendingIntent.getActivity(
            this,
            0,
            Intent(this, MainActivity::class.java).addFlags(Intent.FLAG_ACTIVITY_SINGLE_TOP),
            PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE,
        )
        return Notification.Builder(this, CHANNEL)
            .setContentTitle(if (DeviceProfile.isWatch(this)) "rish-mcp watch agent" else "rish-mcp agent")
            .setContentText(text)
            .setSmallIcon(android.R.drawable.stat_sys_data_bluetooth)
            .setContentIntent(openApp)
            .setOnlyAlertOnce(true)
            .setOngoing(true)
            .build()
    }

    companion object {
        private const val CHANNEL = "rishmcp-agent"
        private const val NOTIF_ID = 42
        private const val TAG = "rishmcp-service"

        fun start(ctx: Context, reconnect: Boolean = false) {
            val intent = Intent(ctx, AgentService::class.java).putExtra("reconnect", reconnect)
            ctx.startForegroundService(intent)
        }

        fun stop(ctx: Context) {
            ctx.stopService(Intent(ctx, AgentService::class.java))
        }
    }
}
