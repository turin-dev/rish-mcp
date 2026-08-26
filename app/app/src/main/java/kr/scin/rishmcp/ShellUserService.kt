package kr.scin.rishmcp

import org.json.JSONObject
import java.io.ByteArrayOutputStream
import java.io.InputStream
import java.nio.charset.StandardCharsets
import java.util.concurrent.Executors
import java.util.concurrent.TimeUnit

/** Runs in the uid-2000 process created by Shizuku. */
class ShellUserService : IUserService.Stub {
    constructor()

    @Suppress("UNUSED_PARAMETER")
    constructor(context: android.content.Context)

    override fun destroy() {
        System.exit(0)
    }

    override fun exec(cmd: String, timeoutMs: Long): String {
        val startedAt = System.currentTimeMillis()
        val safeTimeoutMs = timeoutMs.coerceIn(MIN_TIMEOUT_MS, MAX_TIMEOUT_MS)
        var process: Process? = null
        val readers = Executors.newFixedThreadPool(2)
        return try {
            process = ProcessBuilder("sh", "-c", cmd)
                .redirectErrorStream(false)
                .start()
            process.outputStream.close()

            val stdout = readers.submit<DrainResult> { drain(process.inputStream) }
            val stderr = readers.submit<DrainResult> { drain(process.errorStream) }
            val finished = process.waitFor(safeTimeoutMs, TimeUnit.MILLISECONDS)
            if (!finished) {
                process.destroyForcibly()
                process.waitFor(REAP_TIMEOUT_MS, TimeUnit.MILLISECONDS)
            }

            val out = stdout.get(READER_TIMEOUT_MS, TimeUnit.MILLISECONDS)
            val err = stderr.get(READER_TIMEOUT_MS, TimeUnit.MILLISECONDS)
            JSONObject()
                .put("code", if (finished) process.exitValue() else -1)
                .put("stdout", out.text)
                .put("stderr", err.text)
                .put("truncated", out.truncated || err.truncated || !finished)
                .put("durationMs", System.currentTimeMillis() - startedAt)
                .toString()
        } catch (error: Throwable) {
            process?.destroyForcibly()
            if (error is InterruptedException) Thread.currentThread().interrupt()
            JSONObject()
                .put("code", -1)
                .put("stdout", "")
                .put("stderr", error.toString())
                .put("truncated", false)
                .put("durationMs", System.currentTimeMillis() - startedAt)
                .toString()
        } finally {
            readers.shutdownNow()
        }
    }

    private fun drain(stream: InputStream): DrainResult {
        val buffer = ByteArray(8192)
        // Most commands produce tiny output; grow on demand instead of
        // reserving 256 KiB for each of stdout/stderr on every invocation.
        val retained = ByteArrayOutputStream(8192)
        var truncated = false
        stream.use { input ->
            while (true) {
                val count = input.read(buffer)
                if (count < 0) break
                val remaining = MAX_BYTES - retained.size()
                if (remaining > 0) retained.write(buffer, 0, minOf(count, remaining))
                if (count > remaining) truncated = true
            }
        }
        return DrainResult(retained.toString(StandardCharsets.UTF_8.name()), truncated)
    }

    private data class DrainResult(val text: String, val truncated: Boolean)

    companion object {
        private const val MAX_BYTES = 256 * 1024
        private const val MIN_TIMEOUT_MS = 1_000L
        private const val MAX_TIMEOUT_MS = 600_000L
        private const val REAP_TIMEOUT_MS = 2_000L
        private const val READER_TIMEOUT_MS = 2_000L
    }
}
