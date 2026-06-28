package kr.scin.rishmcp

import org.json.JSONObject
import java.io.InputStream
import java.util.concurrent.Executors
import java.util.concurrent.TimeUnit

/**
 * Runs inside the process Shizuku spawns with shell (uid 2000) privileges.
 * Anything launched here via `sh -c` therefore runs as the shell user — the
 * same privilege level as `adb shell` / `rish`.
 */
class ShellUserService : IUserService.Stub {

    // Shizuku may instantiate with a no-arg or (Context) constructor; provide both.
    constructor()
    @Suppress("UNUSED_PARAMETER")
    constructor(context: android.content.Context)

    override fun destroy() {
        System.exit(0)
    }

    override fun exec(cmd: String, timeoutMs: Long): String {
        val t0 = System.currentTimeMillis()
        return try {
            val proc = ProcessBuilder("sh", "-c", cmd)
                .redirectErrorStream(false)
                .start()
            proc.outputStream.close()

            val pool = Executors.newFixedThreadPool(2)
            val outF = pool.submit<Pair<String, Boolean>> { drain(proc.inputStream) }
            val errF = pool.submit<Pair<String, Boolean>> { drain(proc.errorStream) }

            val finished = proc.waitFor(timeoutMs, TimeUnit.MILLISECONDS)
            if (!finished) proc.destroyForcibly()
            val code = if (finished) proc.exitValue() else -1
            val (out, outTrunc) = outF.get(2, TimeUnit.SECONDS)
            val (err, errTrunc) = errF.get(2, TimeUnit.SECONDS)
            pool.shutdownNow()

            JSONObject()
                .put("code", code)
                .put("stdout", out)
                .put("stderr", err)
                .put("truncated", outTrunc || errTrunc || !finished)
                .put("durationMs", System.currentTimeMillis() - t0)
                .toString()
        } catch (e: Throwable) {
            JSONObject()
                .put("code", -1)
                .put("stdout", "")
                .put("stderr", e.toString())
                .put("truncated", false)
                .put("durationMs", System.currentTimeMillis() - t0)
                .toString()
        }
    }

    /** Read a stream to a String, capping at MAX_BYTES. Returns (text, wasTruncated). */
    private fun drain(stream: InputStream): Pair<String, Boolean> {
        val buf = ByteArray(8192)
        val sb = StringBuilder()
        var total = 0
        var truncated = false
        stream.use { s ->
            while (true) {
                val n = s.read(buf)
                if (n < 0) break
                if (total < MAX_BYTES) {
                    val take = minOf(n, MAX_BYTES - total)
                    sb.append(String(buf, 0, take))
                    total += take
                    if (take < n) truncated = true
                } else {
                    truncated = true
                }
            }
        }
        return sb.toString() to truncated
    }

    companion object {
        private const val MAX_BYTES = 256 * 1024 // 256 KB per stream
    }
}
