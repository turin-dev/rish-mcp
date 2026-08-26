package kr.scin.rishmcp

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Test

class CommandPolicyTest {
    @Test
    fun `valid command passes unchanged`() {
        assertNull(CommandPolicy.validationError("request-1", "id"))
        assertEquals(60_000L, CommandPolicy.clampTimeout(60_000))
    }

    @Test
    fun `blank and oversized identifiers are rejected`() {
        assertEquals("reqId is blank", CommandPolicy.validationError("", "id"))
        val oversized = "x".repeat(CommandPolicy.MAX_REQUEST_ID_CHARS + 1)
        assertEquals(
            "reqId too long (257 > 256)",
            CommandPolicy.validationError(oversized, "id"),
        )
    }

    @Test
    fun `blank and oversized commands are rejected`() {
        assertEquals("cmd is blank", CommandPolicy.validationError("1", "  "))
        val oversized = "x".repeat(CommandPolicy.MAX_COMMAND_CHARS + 1)
        assertEquals(
            "cmd too long (65537 > 65536)",
            CommandPolicy.validationError("1", oversized),
        )
    }

    @Test
    fun `timeouts are clamped to the supported execution window`() {
        assertEquals(CommandPolicy.MIN_TIMEOUT_MS, CommandPolicy.clampTimeout(-1))
        assertEquals(CommandPolicy.MAX_TIMEOUT_MS, CommandPolicy.clampTimeout(Long.MAX_VALUE))
    }
}
