package kr.scin.rishmcp

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * Locks in the epoch semantics that ConnectionManager relies on to keep a
 * dying WebSocket's callbacks from racing a replacement socket (the
 * read-timing race: onFailure could read `ws` before forceReconnect() nulled
 * it and schedule a duplicate reconnection right as the new socket was being
 * created — two live sockets on the wire).
 */
class EpochGateTest {

    @Test
    fun `next returns monotonically increasing generations`() {
        val gate = EpochGate()
        val g1 = gate.next()
        val g2 = gate.next()
        val g3 = gate.next()
        assertTrue(g1 < g2)
        assertTrue(g2 < g3)
    }

    @Test
    fun `isCurrent accepts the latest generation`() {
        val gate = EpochGate()
        val g = gate.next()
        assertTrue(gate.isCurrent(g))
    }

    @Test
    fun `isCurrent rejects generations superseded by force-reconnect bumps`() {
        val gate = EpochGate()
        val listenerEpoch = gate.next()   // listener bound to the dying socket
        gate.next()                       // forceReconnect() bumps before teardown
        assertFalse(gate.isCurrent(listenerEpoch))
    }

    @Test
    fun `force-reconnect cannot schedule duplicate reconnect - stale callbacks ignored`() {
        val gate = EpochGate()
        val oldListener = gate.next()         // old socket's listener epoch
        val newListener = gate.next()         // replacement socket claims a fresh one
        // The old socket fires onFailure/onClosed AFTER the new socket exists:
        // its epoch is already stale, even though it might still be the last
        // assigned ws pointer in a naive `!== ws` check.
        assertFalse(gate.isCurrent(oldListener))
        assertTrue(gate.isCurrent(newListener))
    }

    @Test
    fun `isCurrent on a fresh gate accepts nothing - no listener bound yet`() {
        val gate = EpochGate()
        assertFalse(gate.isCurrent(0L))
        assertFalse(gate.isCurrent(1L))
    }
}