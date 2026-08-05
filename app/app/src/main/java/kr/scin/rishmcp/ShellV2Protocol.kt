package kr.scin.rishmcp

import java.io.ByteArrayOutputStream
import java.io.InputStream

/**
 * Parses the ADB "shell,v2" packet framing directly on top of libadb-android's
 * raw [AdbStream][io.github.muntashirakon.adb.AdbStream], since that library's
 * own `shell:` helper only exposes combined, unframed output with no exit code.
 *
 * Each packet is `[1-byte id][4-byte little-endian length][payload]`. This
 * framing is stable AOSP protocol (see platform/packages/modules/adb's
 * SHELL_PROTOCOL.md), not something rish-mcp invented, so it's safe to
 * hand-roll — unlike the wireless-pairing handshake, which is left to
 * libadb-android.
 */
object ShellV2Protocol {
    private const val ID_STDIN = 0
    private const val ID_STDOUT = 1
    private const val ID_STDERR = 2
    private const val ID_EXIT = 3
    private const val ID_CLOSE_STDIN = 4
    private const val ID_WINDOW_SIZE_CHANGE = 5

    private const val HEADER_SIZE = 5

    /** The raw ADB service destination for a non-interactive v2 shell call. */
    fun destination(cmd: String): String = "shell,v2,raw:$cmd"

    /**
     * Reads v2 packets from [input] until an exit packet arrives or the
     * stream ends. stdout/stderr are each capped at [maxOutputBytes]; going
     * over sets `truncated` rather than throwing. If the stream closes
     * before an exit packet arrives (e.g. the caller force-closed it after a
     * timeout), `code` is -1 — the same "killed" convention the old Shizuku
     * agent used.
     */
    fun readResult(input: InputStream, maxOutputBytes: Int): ParsedResult {
        val stdout = CappedBuffer(maxOutputBytes)
        val stderr = CappedBuffer(maxOutputBytes)
        var exitCode = -1

        val header = ByteArray(HEADER_SIZE)
        while (true) {
            if (!readFully(input, header)) break
            val id = header[0].toInt() and 0xFF
            val length = littleEndianInt(header, 1)
            val payload = if (length > 0) ByteArray(length) else EMPTY
            if (length > 0 && !readFully(input, payload)) break

            when (id) {
                ID_STDOUT -> stdout.append(payload)
                ID_STDERR -> stderr.append(payload)
                ID_EXIT -> {
                    exitCode = if (payload.isNotEmpty()) payload[0].toInt() and 0xFF else -1
                    break
                }
                ID_STDIN, ID_CLOSE_STDIN, ID_WINDOW_SIZE_CHANGE -> Unit // not relevant to a one-shot exec
                else -> Unit // unknown/invalid id: ignore rather than fail the whole command
            }
        }

        return ParsedResult(
            code = exitCode,
            stdout = stdout.toText(),
            stderr = stderr.toText(),
            truncated = stdout.truncated || stderr.truncated,
        )
    }

    private fun littleEndianInt(buf: ByteArray, offset: Int): Int =
        (buf[offset].toInt() and 0xFF) or
            ((buf[offset + 1].toInt() and 0xFF) shl 8) or
            ((buf[offset + 2].toInt() and 0xFF) shl 16) or
            ((buf[offset + 3].toInt() and 0xFF) shl 24)

    /** Reads exactly buf.size bytes, or returns false on a clean/partial EOF. */
    private fun readFully(input: InputStream, buf: ByteArray): Boolean {
        var off = 0
        while (off < buf.size) {
            val n = input.read(buf, off, buf.size - off)
            if (n < 0) return false
            off += n
        }
        return true
    }

    private val EMPTY = ByteArray(0)
}

data class ParsedResult(
    val code: Int,
    val stdout: String,
    val stderr: String,
    val truncated: Boolean,
)

/** Accumulates bytes up to [limit], flagging overflow instead of growing forever. */
private class CappedBuffer(private val limit: Int) {
    private val buf = ByteArrayOutputStream()
    var truncated = false
        private set

    fun append(bytes: ByteArray) {
        if (truncated || bytes.isEmpty()) return
        val remaining = limit - buf.size()
        if (remaining <= 0) {
            truncated = true
            return
        }
        if (bytes.size <= remaining) {
            buf.write(bytes)
        } else {
            buf.write(bytes, 0, remaining)
            truncated = true
        }
    }

    fun toText(): String = buf.toString(Charsets.UTF_8.name())
}
