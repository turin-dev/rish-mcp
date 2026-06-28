package kr.scin.rishmcp

import android.content.Context
import java.util.UUID

/** Tiny SharedPreferences wrapper for agent config. */
object Prefs {
    private const val FILE = "rishmcp"

    fun get(ctx: Context) = ctx.getSharedPreferences(FILE, Context.MODE_PRIVATE)

    var Context.relayUrl: String
        get() = get(this).getString("relayUrl", "wss://mcp.example.com/agent") ?: ""
        set(v) { get(this).edit().putString("relayUrl", v).apply() }

    var Context.deviceToken: String
        get() = get(this).getString("deviceToken", "") ?: ""
        set(v) { get(this).edit().putString("deviceToken", v).apply() }

    var Context.enabled: Boolean
        get() = get(this).getBoolean("enabled", false)
        set(v) { get(this).edit().putBoolean("enabled", v).apply() }

    fun deviceId(ctx: Context): String {
        val p = get(ctx)
        var id = p.getString("deviceId", null)
        if (id == null) {
            id = "s23-" + UUID.randomUUID().toString().take(8)
            p.edit().putString("deviceId", id).apply()
        }
        return id
    }
}
