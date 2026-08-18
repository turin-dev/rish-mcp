package kr.scin.rishmcp

import android.content.ComponentName
import android.content.Context
import android.content.ServiceConnection
import android.content.pm.PackageManager
import android.os.IBinder
import android.os.Process
import android.util.Log
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext
import org.json.JSONObject
import rikka.shizuku.Shizuku

/** Optional shell backend backed by a Shizuku UserService. */
class ShizukuShellClient(
    context: Context,
    private val onStateChanged: () -> Unit,
) : ShellBackend {
    enum class State(val label: String) {
        STOPPED("stopped"),
        NOT_RUNNING("not running"),
        PERMISSION_REQUIRED("permission needed"),
        UNSUPPORTED("server API too old"),
        ROOT_REJECTED("root mode rejected"),
        BINDING("binding…"),
        CONNECTED("connected"),
        ERROR("error"),
    }

    private val appContext = context.applicationContext
    private val userServiceArgs = Shizuku.UserServiceArgs(
        ComponentName(appContext.packageName, ShellUserService::class.java.name),
    )
        .daemon(false)
        .tag("rish-mcp-shell-v1")
        .processNameSuffix("shell")
        .debuggable(BuildConfig.DEBUG)
        .version(BuildConfig.VERSION_CODE)

    @Volatile private var service: IUserService? = null
    @Volatile private var started = false
    @Volatile var state: State = State.STOPPED
        private set

    override val kind = ShellBackend.Kind.SHIZUKU
    override val isReady: Boolean
        get() = service != null && state == State.CONNECTED

    private val serviceConnection = object : ServiceConnection {
        override fun onServiceConnected(name: ComponentName, binder: IBinder) {
            service = IUserService.Stub.asInterface(binder)
            updateState(State.CONNECTED)
            Log.i(TAG, "Shizuku UserService connected")
        }

        override fun onServiceDisconnected(name: ComponentName) {
            service = null
            updateState(State.ERROR)
            Log.w(TAG, "Shizuku UserService disconnected")
        }
    }

    private val binderReceived = Shizuku.OnBinderReceivedListener { ensureConnected() }
    private val binderDead = Shizuku.OnBinderDeadListener {
        service = null
        updateState(State.NOT_RUNNING)
    }

    fun start() {
        if (started) return
        started = true
        Shizuku.addBinderReceivedListenerSticky(binderReceived)
        Shizuku.addBinderDeadListener(binderDead)
        ensureConnected()
    }

    fun stop() {
        if (!started) return
        started = false
        runCatching { Shizuku.removeBinderReceivedListener(binderReceived) }
        runCatching { Shizuku.removeBinderDeadListener(binderDead) }
        runCatching { Shizuku.unbindUserService(userServiceArgs, serviceConnection, true) }
        service = null
        updateState(State.STOPPED)
    }

    fun ensureConnected() {
        if (!started || state == State.BINDING || isReady) return
        val binderAlive = runCatching { Shizuku.pingBinder() }.getOrDefault(false)
        if (!binderAlive) {
            updateState(State.NOT_RUNNING)
            return
        }
        if (runCatching { Shizuku.isPreV11() }.getOrDefault(true)) {
            updateState(State.UNSUPPORTED)
            return
        }
        val serverUid = runCatching { Shizuku.getUid() }.getOrDefault(-1)
        if (serverUid != Process.SHELL_UID) {
            updateState(State.ROOT_REJECTED)
            return
        }
        val permissionGranted = runCatching {
            Shizuku.checkSelfPermission() == PackageManager.PERMISSION_GRANTED
        }.getOrDefault(false)
        if (!permissionGranted) {
            updateState(State.PERMISSION_REQUIRED)
            return
        }
        updateState(State.BINDING)
        runCatching { Shizuku.bindUserService(userServiceArgs, serviceConnection) }
            .onFailure {
                Log.w(TAG, "Shizuku UserService bind failed", it)
                updateState(State.ERROR)
            }
    }

    override suspend fun exec(cmd: String, timeoutMs: Long): ShellResult = withContext(Dispatchers.IO) {
        val remote = service ?: return@withContext ShellResult.unavailable("Shizuku UserService is not connected")
        try {
            val value = JSONObject(remote.exec(cmd, timeoutMs))
            ShellResult(
                code = value.optInt("code", -1),
                stdout = value.optString("stdout"),
                stderr = value.optString("stderr"),
                truncated = value.optBoolean("truncated"),
                durationMs = value.optLong("durationMs"),
            )
        } catch (error: Throwable) {
            Log.w(TAG, "Shizuku command failed", error)
            service = null
            updateState(State.ERROR)
            ShellResult.unavailable("Shizuku command failed: ${error.message}")
        }
    }

    private fun updateState(next: State) {
        if (state == next) return
        state = next
        onStateChanged()
    }

    companion object {
        private const val TAG = "rishmcp-shizuku"
    }
}
