package kr.scin.rishmcp

import kotlinx.coroutines.runBlocking
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertSame
import org.junit.Test

class ShellBackendRouterTest {
    @Test
    fun `Shizuku is preferred when both backends are ready`() {
        val shizuku = FakeBackend(ShellBackend.Kind.SHIZUKU, ready = true)
        val adb = FakeBackend(ShellBackend.Kind.ADB, ready = true)

        val active = ShellBackendRouter(shizuku, adb).active()

        assertSame(shizuku, active)
    }

    @Test
    fun `ADB is used when Shizuku is unavailable`() = runBlocking {
        val shizuku = FakeBackend(ShellBackend.Kind.SHIZUKU, ready = false)
        val adb = FakeBackend(ShellBackend.Kind.ADB, ready = true)

        val result = ShellBackendRouter(shizuku, adb).exec("id", 1_000)

        assertEquals("adb", result.stdout)
        assertEquals(0, shizuku.executions)
        assertEquals(1, adb.executions)
    }

    @Test
    fun `failed Shizuku command is never replayed on ADB`() = runBlocking {
        val shizuku = FakeBackend(
            ShellBackend.Kind.SHIZUKU,
            ready = true,
            result = ShellResult.unavailable("binder died"),
        )
        val adb = FakeBackend(ShellBackend.Kind.ADB, ready = true)

        val result = ShellBackendRouter(shizuku, adb).exec("touch /data/local/tmp/once", 1_000)

        assertEquals("binder died", result.stderr)
        assertEquals(1, shizuku.executions)
        assertEquals(0, adb.executions)
    }

    @Test
    fun `no ready backend returns a bounded error`() = runBlocking {
        val router = ShellBackendRouter(
            FakeBackend(ShellBackend.Kind.SHIZUKU, ready = false),
            FakeBackend(ShellBackend.Kind.ADB, ready = false),
        )

        assertNull(router.active())
        val result = router.exec("id", 1_000)
        assertEquals(-1, result.code)
        assertEquals("", result.stdout)
        assertEquals(false, result.truncated)
    }

    private class FakeBackend(
        override val kind: ShellBackend.Kind,
        ready: Boolean,
        private val result: ShellResult = ShellResult(0, kind.wireName, "", false, 1),
    ) : ShellBackend {
        override val isReady = ready
        var executions = 0

        override suspend fun exec(cmd: String, timeoutMs: Long): ShellResult {
            executions++
            return result
        }
    }
}
