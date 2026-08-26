package kr.scin.rishmcp

import okhttp3.HttpUrl
import okhttp3.HttpUrl.Companion.toHttpUrlOrNull

/** Normalizes owner-supplied relay URLs and safely replaces agent query data. */
object RelayUrlPolicy {
    fun parse(rawUrl: String): HttpUrl? {
        val raw = rawUrl.trim()
        if (raw.isEmpty()) return null
        val normalized = when {
            raw.startsWith("wss://", ignoreCase = true) -> "https://${raw.substring(6)}"
            raw.startsWith("ws://", ignoreCase = true) -> "http://${raw.substring(5)}"
            raw.startsWith("https://", ignoreCase = true) ||
                raw.startsWith("http://", ignoreCase = true) -> raw
            "://" !in raw -> "https://$raw"
            else -> return null
        }
        return normalized.toHttpUrlOrNull()
    }

    fun withAgentQuery(rawUrl: String, values: Map<String, String>): HttpUrl? {
        val builder = parse(rawUrl)?.newBuilder() ?: return null
        values.forEach { (name, value) -> builder.setQueryParameter(name, value) }
        return builder.build()
    }
}
