package kr.scin.rishmcp

/**
 * Monotonic generation counter that expires callbacks from superseded relay
 * WebSockets (ConnectionManager). A fresh generation is claimed via [next]
 * BEFORE the old socket is torn down, so every callback a dying socket still
 * fires — onFailure from the cancel, onClosed from the close — self-ignores
 * instead of scheduling a duplicate reconnect or clobbering the replacement
 * socket's state. A listener bound to an old generation rejects itself via
 * [isCurrent], closing the read-timing race where a bare `webSocket !== ws`
 * pointer check could pass right as the new socket was being created, leaving
 * two live sockets on the wire.
 *
 * Pure JVM class — no Android dependencies — so those race semantics are
 * regression-testable without Robolectric.
 */
internal class EpochGate {
    @Volatile
    private var current = -1L // start below 0 so a fresh gate accepts no generation

    /** Claims and returns the next generation, to bind to a new listener. */
    fun next(): Long = ++current

    /** True while [generation] is still the live one (listener not stale). */
    fun isCurrent(generation: Long): Boolean = generation == current

    val value: Long get() = current
}