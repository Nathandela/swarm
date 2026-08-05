package dev.swarm.phone

import android.app.Application
import android.content.pm.PackageManager
import androidx.test.core.app.ApplicationProvider
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.Shadows.shadowOf

/**
 * FAILING-FIRST (TDD RED, GG-5) for agents-tracker-nz9h.
 *
 * WHY THIS SEAM AND NOT A RENDER TEST. [PairingSurface.scannerState] is the function the issue
 * names, and it is reachable only from `renderReady`, which runs only when
 * `PhoneRuntime.phone()` answers [PhoneStartup.Ready] -- and the phone core is a native library
 * cross-compiled for Android ABIs that this unit-test JVM cannot load (every neighbouring
 * `PairingSurface` comment says so; there is no `PairingSurfaceTest` in this module for the same
 * reason). [hasCameraHardware] is extracted so the one new fact this fix depends on --
 * `PackageManager.FEATURE_CAMERA_ANY` -- has a seam a test can drive without a live phone core.
 */
@RunWith(RobolectricTestRunner::class)
class PairingCameraHardwareTest {

    private fun packageManager(): PackageManager =
        ApplicationProvider.getApplicationContext<Application>().packageManager

    @Test
    fun `a handset with no camera feature answers false`() {
        val pm = packageManager()
        shadowOf(pm).setSystemFeature(PackageManager.FEATURE_CAMERA_ANY, false)

        assertFalse(hasCameraHardware(pm))
    }

    @Test
    fun `a handset carrying any camera feature answers true`() {
        val pm = packageManager()
        shadowOf(pm).setSystemFeature(PackageManager.FEATURE_CAMERA_ANY, true)

        assertTrue(hasCameraHardware(pm))
    }
}
