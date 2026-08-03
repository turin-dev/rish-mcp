package kr.scin.rishmcp

import android.content.Context
import android.content.pm.PackageManager
import android.net.NetworkCapabilities

/** Small compatibility layer shared by phone/tablet and Wear OS builds. */
object DeviceProfile {

    // hasSystemFeature() is a binder call and the answer never changes at
    // runtime, so resolve it once instead of on every heartbeat tick.
    @Volatile private var watch: Boolean? = null

    fun isWatch(ctx: Context): Boolean = watch ?: run {
        val v = ctx.packageManager.hasSystemFeature(PackageManager.FEATURE_WATCH)
        watch = v
        v
    }

    /** Form factor reported to the relay; also the device-id prefix. */
    fun kind(ctx: Context): String = if (isWatch(ctx)) "watch" else "android"

    /**
     * Keepalive ping. A watch pings less often than a handheld to keep the radio
     * asleep, but NEVER disables pings: OkHttp's ping timeout is the only thing
     * that surfaces a half-open socket to [AgentService], and the heartbeat below
     * only reconnects when the state is already known-bad.
     */
    fun webSocketPingSeconds(ctx: Context): Long = if (isWatch(ctx)) 60L else 20L

    fun heartbeatMs(ctx: Context): Long = if (isWatch(ctx)) 90_000L else 30_000L

    fun networkLabel(caps: NetworkCapabilities?): String = when {
        caps == null -> "none"
        caps.hasTransport(NetworkCapabilities.TRANSPORT_WIFI) -> "wifi"
        caps.hasTransport(NetworkCapabilities.TRANSPORT_CELLULAR) -> "cellular"
        caps.hasTransport(NetworkCapabilities.TRANSPORT_ETHERNET) -> "ethernet"
        caps.hasTransport(NetworkCapabilities.TRANSPORT_BLUETOOTH) -> "phone/bluetooth"
        caps.hasTransport(NetworkCapabilities.TRANSPORT_VPN) -> "vpn"
        else -> "other"
    }
}
