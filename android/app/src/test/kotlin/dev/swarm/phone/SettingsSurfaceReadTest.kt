package dev.swarm.phone

import android.app.Activity
import androidx.test.core.app.ActivityScenario
import dev.swarm.phone.runtime.AppPermission
import dev.swarm.phone.runtime.PermissionAsks
import dev.swarm.phone.runtime.PermissionState
import dev.swarm.phone.ui.SettingsScreen
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.Shadows.shadowOf

/**
 * FAILING-FIRST (TDD RED, GG-5) for agents-tracker-doza and agents-tracker-qyb3: the two things
 * `SettingsSurface.read` does on EVERY resume and every journal-event render.
 *
 * WHY THE SEAMS ARE SPLIT OUT RATHER THAN DRIVEN THROUGH `read` ITSELF, which is the same limit
 * `deliveryNow` records one member below it: `PhoneRuntime.phone()` answers
 * [PhoneStartup.Unavailable] on every JVM run -- the phone core is a gomobile AAR of .so files
 * cross-compiled for Android ABIs -- so `render` takes the Unavailable branch and `read` is never
 * entered here at all. What CAN be executed is the part of it that touches the platform and the
 * model and not the facade, and agents-tracker-0dij is the standing reason to insist on
 * executing it: that defect was a correct resolver handed a LITERAL, green across every unit test
 * while the app resolved every fresh install to PERMANENTLY_DENIED.
 */
@RunWith(RobolectricTestRunner::class)
class SettingsSurfaceReadTest {

    // ---- agents-tracker-doza: a facade refusal on the resume path ----------
    //
    // `read` called `bridge.pushSettings()` unguarded. It runs from `render()`, which runs from
    // `PhoneActivity.onResume` and from every journal event, so a facade that refuses -- a core
    // that has been closed, a state blob that will not decode -- is an uncaught exception on the
    // looper: the app dies on a screen the user merely opened, with no tap involved. `machineOf`
    // in the same file already guards the identical class of call and says why.

    /**
     * An unreadable facade leaves the screen this panel last drew.
     *
     * IT IS THE LAST SCREEN AND NOT A DEFAULT, which is [SettingsScreen]'s own rule ("it renders
     * what was persisted, never a default"): the values the user last saw are the closest thing
     * to the truth still in reach, and switches that flip themselves to OFF because a read failed
     * would be the process-death defect that rule exists to prevent, arriving through the error
     * path instead.
     */
    @Test
    fun `a facade that cannot be read leaves the screen the panel last drew`() {
        withSettingsSurface { _, surface ->
            val held = SettingsScreen(alerts = true, mentions = false)

            assertEquals(
                "agents-tracker-doza: a refusal on the resume path threw out of the render, or " +
                    "replaced the user's settings with a default",
                held,
                surface.settingsOr(held) { throw RuntimeException("swarm/backend: state unreadable") },
            )
        }
    }

    /**
     * With nothing held, the fallback is the empty screen and not an invented one.
     *
     * This is the first draw after a process death, and the honest answer to "what has this user
     * chosen" when nothing can be read is "nothing is known" -- which renders as two switches off
     * rather than as two switches claiming a preference nobody set.
     */
    @Test
    fun `a first draw that cannot be read shows no preference rather than a made-up one`() {
        withSettingsSurface { _, surface ->
            assertEquals(
                SettingsScreen(alerts = false, mentions = false),
                surface.settingsOr(null) { throw RuntimeException("swarm/backend: state unreadable") },
            )
        }
    }

    /** And the guard is not a swallow: a read that works is the read that reaches the screen. */
    @Test
    fun `a facade that answers is what the panel draws`() {
        withSettingsSurface { _, surface ->
            val held = SettingsScreen(alerts = true, mentions = false)
            val fresh = SettingsScreen(alerts = false, mentions = true)

            assertEquals(
                "the guard returned the held screen over a read that succeeded, so the panel " +
                    "would never see a preference change again",
                fresh,
                surface.settingsOr(held) { fresh },
            )
        }
    }

    // ---- agents-tracker-qyb3: the ask bit is not monotonic ------------------
    //
    // `PermissionAsks.remember` writes true and nothing ever cleared it, so a phone that GRANTED
    // the permission and later revoked it in system settings resolved to PERMANENTLY_DENIED: both
    // switches disabled and a redirect offered, while the platform would still have prompted. The
    // resolve is the moment the truth is in hand, so it is the moment the bit is corrected.

    /** A grant clears the bit, so a later revoke resolves to the state the platform is really in. */
    @Test
    fun `a granted permission clears the record of having asked`() {
        withSettingsSurface { activity, surface ->
            PermissionAsks.remember(activity, AppPermission.POST_NOTIFICATIONS)
            shadowOf(activity).grantPermissions(AppPermission.POST_NOTIFICATIONS.manifestName)

            assertEquals(PermissionState.GRANTED, surface.notificationPermissionNow())
            assertFalse(
                "agents-tracker-qyb3: the ask bit survived a grant, so the next system-settings " +
                    "revoke resolves to PERMANENTLY_DENIED -- two dead switches and a redirect, " +
                    "on a phone the platform would still prompt for",
                PermissionAsks.hasAsked(activity, AppPermission.POST_NOTIFICATIONS),
            )
        }
    }

    /**
     * AND AN UNGRANTED PERMISSION KEEPS IT. The bit is what tells a first run from a permanent
     * refusal (`shouldShowRequestPermissionRationale` is false for both), so clearing it on any
     * other state would send a user who has permanently refused back to a prompt the platform
     * silently drops -- which is the failure `PermissionAsks` exists to prevent, inverted.
     */
    @Test
    fun `an ungranted permission keeps the record of having asked`() {
        withSettingsSurface { activity, surface ->
            PermissionAsks.remember(activity, AppPermission.POST_NOTIFICATIONS)

            surface.notificationPermissionNow()

            assertTrue(
                "agents-tracker-qyb3: the ask bit was cleared while the permission is still " +
                    "withheld, so a permanent refusal reads as a fresh install for ever",
                PermissionAsks.hasAsked(activity, AppPermission.POST_NOTIFICATIONS),
            )
        }
    }

    /** See `SettingsSurfaceNotificationsTest`: construction reaches neither the core nor the window. */
    private fun withSettingsSurface(assertions: (Activity, SettingsSurface) -> Unit) {
        ActivityScenario.launch(PhoneActivity::class.java).use { scenario ->
            scenario.onActivity { activity ->
                assertions(activity, SettingsSurface(activity, PhoneRuntime(activity)))
            }
        }
    }
}
