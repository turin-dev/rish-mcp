package kr.scin.rishmcp

import org.junit.Assert.assertEquals
import org.junit.Test

class VersionTest {

    @Test
    fun `build config exposes the 1_0 release identity`() {
        assertEquals("1.0.0", BuildConfig.VERSION_NAME)
        assertEquals(10000, BuildConfig.VERSION_CODE)
    }
}
