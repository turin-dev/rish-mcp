package kr.scin.rishmcp

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test
import java.io.ByteArrayInputStream
import java.io.ByteArrayOutputStream
import java.io.InputStream

class ShellV2ProtocolTest {

    @Test
    fun `parses separated stdout, stderr, and exit code`() {
        val stream = packets(
            packet(ID_STDOUT, "hello\n".toByteArray()),
            packet(ID_STDERR, "oops\n".toByteArray()),
            packet(ID_EXIT, byteArrayOf(3)),
        )

        val result = ShellV2Protocol.readResult(stream, maxOutputBytes = 1024)

        assertEquals(3, result.code)
        assertEquals("hello\n", result.stdout)
        assertEquals("oops\n", result.stderr)
        assertFalse(result.truncated)
    }

    @Test
    fun `truncates stdout past the cap instead of growing forever`() {
        val chunk = ByteArray(100) { 'a'.code.toByte() }
        val stream = packets(
            packet(ID_STDOUT, chunk),
            packet(ID_STDOUT, chunk),
            packet(ID_EXIT, byteArrayOf(0)),
        )

        val result = ShellV2Protocol.readResult(stream, maxOutputBytes = 150)

        assertEquals(150, result.stdout.toByteArray().size)
        assertTrue(result.truncated)
    }

    @Test
    fun `stream closing without an exit packet reports code -1 instead of throwing`() {
        val stream = packets(packet(ID_STDOUT, "partial".toByteArray()))

        val result = ShellV2Protocol.readResult(stream, maxOutputBytes = 1024)

        assertEquals(-1, result.code)
        assertEquals("partial", result.stdout)
    }

    @Test
    fun `unknown packet ids are skipped rather than failing the command`() {
        val stream = packets(
            packet(255, byteArrayOf(9, 9)),
            packet(ID_STDOUT, "ok\n".toByteArray()),
            packet(ID_EXIT, byteArrayOf(0)),
        )

        val result = ShellV2Protocol.readResult(stream, maxOutputBytes = 1024)

        assertEquals(0, result.code)
        assertEquals("ok\n", result.stdout)
    }

    @Test
    fun `oversized packet payload is capped and drained to prevent OOM`() {
        // A single packet with a payload larger than MAX_PACKET_BYTES must not
        // cause an OOM; the excess is drained and the truncated flag is set.
        val bigPayload = ByteArray(300_000) { 'x'.code.toByte() }
        val stream = packets(
            packet(ID_STDOUT, bigPayload),
            packet(ID_EXIT, byteArrayOf(0)),
        )

        val result = ShellV2Protocol.readResult(stream, maxOutputBytes = 1024 * 1024)

        assertEquals(0, result.code)
        // The payload was capped at 256 KiB, so stdout should be at most that.
        assertTrue("stdout len = ${result.stdout.length}", result.stdout.length <= 256 * 1024)
        assertTrue(result.truncated)
    }

    @Test
    fun `oversized packet does not corrupt subsequent exit packet`() {
        // Even when the payload is astronomically large on the wire, the
        // drain mechanism must keep the stream framing valid so that the
        // following EXIT packet is still readable.
        val bigPayload = ByteArray(400_000) { 'y'.code.toByte() }
        val stream = packets(
            packet(ID_STDOUT, bigPayload),
            packet(ID_EXIT, byteArrayOf(42)),
        )

        val result = ShellV2Protocol.readResult(stream, maxOutputBytes = 1024 * 1024)

        assertEquals(42, result.code)
        assertTrue(result.truncated)
    }

    @Test
    fun `moderately large payload is accepted without truncation`() {
        // A payload that fits within MAX_PACKET_BYTES must be accepted whole.
        val payload = ByteArray(100_000) { 'z'.code.toByte() }
        val stream = packets(
            packet(ID_STDOUT, payload),
            packet(ID_EXIT, byteArrayOf(0)),
        )

        val result = ShellV2Protocol.readResult(stream, maxOutputBytes = 1024 * 1024)

        assertEquals(0, result.code)
        assertEquals(100_000, result.stdout.length)
        assertFalse(result.truncated)
    }

    @Test
    fun `destination wraps the command in a raw v2 shell service string`() {
        assertEquals("shell,v2,raw:id", ShellV2Protocol.destination("id"))
    }

    private fun packets(vararg raw: ByteArray): InputStream {
        val out = ByteArrayOutputStream()
        raw.forEach { out.write(it) }
        return ByteArrayInputStream(out.toByteArray())
    }

    private fun packet(id: Int, payload: ByteArray): ByteArray {
        val out = ByteArrayOutputStream()
        out.write(id)
        val len = payload.size
        out.write(len and 0xFF)
        out.write((len shr 8) and 0xFF)
        out.write((len shr 16) and 0xFF)
        out.write((len shr 24) and 0xFF)
        out.write(payload)
        return out.toByteArray()
    }

    companion object {
        private const val ID_STDOUT = 1
        private const val ID_STDERR = 2
        private const val ID_EXIT = 3
    }
}
