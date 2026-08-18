package kr.scin.rishmcp

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Test

class RelayUrlPolicyTest {
    @Test
    fun `websocket and bare relay addresses normalize for OkHttp`() {
        assertEquals("https://relay.example/agent", RelayUrlPolicy.parse("wss://relay.example/agent").toString())
        assertEquals("http://relay.example/agent", RelayUrlPolicy.parse("ws://relay.example/agent").toString())
        assertEquals("https://relay.example/agent", RelayUrlPolicy.parse("relay.example/agent").toString())
    }

    @Test
    fun `unsupported and malformed schemes are rejected`() {
        assertNull(RelayUrlPolicy.parse(""))
        assertNull(RelayUrlPolicy.parse("ftp://relay.example/agent"))
        assertNull(RelayUrlPolicy.parse("wss://"))
    }

    @Test
    fun `agent query replaces hostile duplicates and encodes values`() {
        val url = RelayUrlPolicy.withAgentQuery(
            "wss://relay.example/agent?token=attacker&keep=yes",
            mapOf("token" to "owner&secret", "name" to "Pixel 9/Pro"),
        )!!

        assertEquals("owner&secret", url.queryParameter("token"))
        assertEquals(1, url.queryParameterValues("token").size)
        assertEquals("Pixel 9/Pro", url.queryParameter("name"))
        assertEquals("yes", url.queryParameter("keep"))
    }
}
