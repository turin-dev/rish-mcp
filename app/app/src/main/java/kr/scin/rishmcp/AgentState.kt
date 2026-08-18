package kr.scin.rishmcp

/** Live status the service publishes and the UI polls. Plain volatiles — single process. */
object AgentState {
    enum class Conn { IDLE, CONNECTING, CONNECTED, DISCONNECTED }

    @Volatile var conn: Conn = Conn.IDLE
    @Volatile var shell: String = "?"
    @Volatile var activeBackend: String = "none"  // "shizuku", "adb", or "none"
    @Volatile var network: String = "?"           // "wifi", "cellular", "other", "none"
    @Volatile var lastEvent: String = ""          // short human note (last error / transition)
    @Volatile var connectedSince: Long = 0L
    @Volatile var commandsRun: Long = 0L
    @Volatile var lastCommandAt: Long = 0L
    @Volatile var serviceRunning: Boolean = false

    fun reset() {
        conn = Conn.IDLE
        connectedSince = 0L
        lastEvent = ""
    }
}
