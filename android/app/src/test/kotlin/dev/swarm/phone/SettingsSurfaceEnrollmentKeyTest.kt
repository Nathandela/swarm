package dev.swarm.phone

import android.app.Activity
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.Robolectric
import org.robolectric.RobolectricTestRunner

@RunWith(RobolectricTestRunner::class)
class SettingsSurfaceEnrollmentKeyTest {
    @Test
    fun `explicit defended press shows only the supplied public enrollment key`() {
        val activity = Robolectric.buildActivity(Activity::class.java).setup().get()
        var reads = 0
        val expected = "BAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
        val surface = SettingsSurface(
            activity = activity,
            runtime = PhoneRuntime(activity),
            enrollmentKey = { reads += 1; expected },
        )

        assertEquals(0, reads)
        assertEquals(android.view.View.GONE, surface.enrollmentKeyDisplay.visibility)
        assertTrue(surface.showEnrollmentKey.filterTouchesWhenObscured)
        assertTrue(surface.touchFilteredActions.contains(surface.showEnrollmentKey))

        surface.showEnrollmentKey.performClick()

        assertEquals(1, reads)
        assertEquals(expected, surface.enrollmentKeyDisplay.text.toString())
        assertEquals(android.view.View.VISIBLE, surface.enrollmentKeyDisplay.visibility)
        assertTrue(surface.enrollmentKeyDisplay.isTextSelectable)
    }

    @Test
    fun `failed enrollment lookup reveals no exception detail or stale key`() {
        val activity = Robolectric.buildActivity(Activity::class.java).setup().get()
        var reads = 0
        val surface = SettingsSurface(
            activity = activity,
            runtime = PhoneRuntime(activity),
            enrollmentKey = {
                reads += 1
                if (reads == 1) "public-key" else error("private-provider-detail")
            },
        )

        surface.showEnrollmentKey.performClick()
        surface.showEnrollmentKey.performClick()

        assertEquals("", surface.enrollmentKeyDisplay.text.toString())
        assertEquals(android.view.View.GONE, surface.enrollmentKeyDisplay.visibility)
        assertEquals("Enrollment key is unavailable on this phone.", surface.outcome.text.toString())
        assertFalse(surface.outcome.text.contains("private-provider-detail"))
    }
}
