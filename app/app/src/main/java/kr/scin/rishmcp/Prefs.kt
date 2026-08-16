package kr.scin.rishmcp

import android.content.Context
import java.util.UUID

/** Tiny SharedPreferences wrapper for agent config. */
object Prefs {
    private const val FILE = "rishmcp"

    fun get(ctx: Context) = ctx.getSharedPreferences(FILE, Context.MODE_PRIVATE)

    // No official relay is hosted for this project (plan.md: "relay는 공식
    // 호스팅하지 않음") — unlike the old Shizuku agent, there's no default to
    // fall back to; the owner must configure their own.
    var Context.relayUrl: String
        get() = get(this).getString("relayUrl", "") ?: ""
        set(v) { get(this).edit().putString("relayUrl", v).apply() }

    var Context.deviceToken: String
        get() = get(this).getString("deviceToken", "") ?: ""
        set(v) { get(this).edit().putString("deviceToken", v).apply() }

    var Context.enabled: Boolean
        get() = get(this).getBoolean("enabled", false)
        set(v) { get(this).edit().putBoolean("enabled", v).apply() }

    /** Loopback host to reach this device's own adbd — 127.0.0.1 covers both
     *  the post-pairing (11+) and tcpip-bridge (pre-11) cases. */
    var Context.adbHost: String
        get() = get(this).getString("adbHost", "127.0.0.1") ?: "127.0.0.1"
        set(v) { get(this).edit().putString("adbHost", v).apply() }

    /** Port for a live shell session, set once pairing (or the pre-11
     *  USB/tcpip bridge) has completed. 0 means "not paired yet". */
    var Context.adbPort: Int
        get() = get(this).getInt("adbPort", 0)
        set(v) { get(this).edit().putInt("adbPort", v).apply() }

    fun deviceId(ctx: Context): String {
        val p = get(ctx)
        var id = p.getString("deviceId", null)
        if (id == null) {
            id = DeviceProfile.kind(ctx) + "-" + UUID.randomUUID().toString().take(8)
            p.edit().putString("deviceId", id).apply()
        }
        return id
    }
}
