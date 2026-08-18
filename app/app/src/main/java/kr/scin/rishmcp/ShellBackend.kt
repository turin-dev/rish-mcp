package kr.scin.rishmcp

/** A command executor that provides adb-shell-equivalent privileges. */
interface ShellBackend {
    val kind: Kind
    val isReady: Boolean

    suspend fun exec(cmd: String, timeoutMs: Long): ShellResult

    enum class Kind(val wireName: String) {
        SHIZUKU("shizuku"),
        ADB("adb"),
    }
}

/**
 * Chooses a backend for one command. The choice is deliberately sticky for
 * that invocation: if the selected backend dies after dispatch, the command
 * is not replayed on the other backend because shell commands are not
 * necessarily idempotent.
 */
class ShellBackendRouter(
    private val shizuku: ShellBackend,
    private val adb: ShellBackend,
) {
    fun active(): ShellBackend? = when {
        shizuku.isReady -> shizuku
        adb.isReady -> adb
        else -> null
    }

    suspend fun exec(cmd: String, timeoutMs: Long): ShellResult {
        val selected = active()
            ?: return ShellResult.unavailable("no shell backend is ready; start/grant Shizuku or connect ADB")
        return selected.exec(cmd, timeoutMs)
    }
}
