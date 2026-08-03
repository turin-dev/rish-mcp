package kr.scin.rishmcp

import android.content.Context
import android.content.pm.PackageManager
import android.net.NetworkCapabilities

/** Small compatibility layer shared by phone/tablet and Wear OS builds. */
object DeviceProfile {
    fun isWatch(ctx: Context): Boolean =
        ctx.packageManager.hasSystemFeature(PackageManager.FEATURE_WATCH)

    fun kind(ctx: Context): String = if (isWatch(ctx)) "watch" else "android"

    fun idPrefix(ctx: Context): String = if (isWatch(ctx)) "watch" else "android"

    /**
     * Watches rely on the relay's lower-frequency server ping instead of also
     * sending their own OkHttp pings. OkHttp treats 0 as "disabled".
     */
    fun webSocketPingSeconds(ctx: Context): Long = if (isWatch(ctx)) 0L else 20L

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
