package kr.scin.rishmcp

/** Agent-side limits that mirror (and defend independently of) the relay. */
object CommandPolicy {
    const val MAX_COMMAND_CHARS = 64 * 1024
    const val MAX_REQUEST_ID_CHARS = 256
    const val MAX_FRAME_CHARS = MAX_COMMAND_CHARS + 4 * 1024
    const val MIN_TIMEOUT_MS = 1_000L
    const val MAX_TIMEOUT_MS = 600_000L

    fun validationError(requestId: String, command: String): String? = when {
        requestId.isBlank() -> "reqId is blank"
        requestId.length > MAX_REQUEST_ID_CHARS ->
            "reqId too long (${requestId.length} > $MAX_REQUEST_ID_CHARS)"
        command.isBlank() -> "cmd is blank"
        command.length > MAX_COMMAND_CHARS ->
            "cmd too long (${command.length} > $MAX_COMMAND_CHARS)"
        else -> null
    }

    fun clampTimeout(timeoutMs: Long): Long = timeoutMs.coerceIn(MIN_TIMEOUT_MS, MAX_TIMEOUT_MS)
}
