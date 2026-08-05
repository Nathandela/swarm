package dev.swarm.phone

import android.app.Activity
import android.provider.Settings
import androidx.test.core.app.ActivityScenario
import dev.swarm.phone.push.WakeNotifications
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.Shadows.shadowOf

/**
 * FAILING-FIRST (TDD RED, GG-5) for agents-tracker-0dij -- the settings surface's half of the
 * guided POST_NOTIFICATIONS flow.
 *
 * WHAT THIS FILE CAN SEE, AND WHAT IT CANNOT, stated first because the limit decides the
 * assertions. `PhoneRuntime.phone()` answers [PhoneStartup.Unavailable] on every JVM run -- the
 * phone core is a gomobile AAR of .so files cross-compiled for Android ABIs -- so
 * `SettingsSurface.render` takes the Unavailable branch, the panel is never drawn, and no switch on
 * screen can be tapped. "Tapping a switch while DENIED requests the permission" is therefore not
 * assertable here; it is fenced over the source in android/gate/notificationask_test.go, which is
 * the same line android/gate/qx9m_camerareach_test.go draws over the camera's ask for the same
 * reason. `SettingsSurfaceReplaceTest` records the identical limit for the replace chip.
 *
 * WHAT IS ASSERTABLE IS CONSTRUCTION AND THE ONE PRESS THAT REACHES NO FACADE. The redirect leaves
 * the app through `Activity.startActivity`, which Robolectric records -- so the intent behind the
 * control, the control's touch filter and its membership of the surface's declared actions are all
 * checkable on a JVM with no phone core.
 */
@RunWith(RobolectricTestRunner::class)
class SettingsSurfaceNotificationsTest {

    /**
     * The control offered on PERMANENTLY_DENIED goes to the app's NOTIFICATION settings.
     *
     * `ACTION_APPLICATION_DETAILS_SETTINGS` -- what the pairing screen sends the camera to -- is the
     * app info page, from which notifications are two taps and a scroll away. The platform ships a
     * screen for exactly this, and it is the difference between a control that fixes the problem and
     * one that puts the user somewhere the problem can be fixed.
     */
    @Test
    fun `the settings redirect opens this app's notification settings`() {
        withSettingsSurface { activity, surface ->
            surface.openNotificationSettings.performClick()

            val started = shadowOf(activity).nextStartedActivity
            assertNotNull(
                "agents-tracker-0dij: pressing the redirect started nothing, so the one state " +
                    "where the platform will not prompt again has no way out of it",
                started,
            )
            assertEquals(
                "the redirect does not open the notification settings screen. The app info page " +
                    "is where the camera's redirect goes and it is two taps and a scroll from the " +
                    "switch this control exists to un-block",
                Settings.ACTION_APP_NOTIFICATION_SETTINGS,
                started.action,
            )
            assertEquals(
                "the intent names no package, so it opens whichever app the system feels like -- " +
                    "in practice nothing",
                activity.packageName,
                started.getStringExtra(Settings.EXTRA_APP_PACKAGE),
            )
        }
    }

    /**
     * It is one of the surface's declared controls, and it carries the filter itself.
     *
     * The membership list is what `PhoneActivity.touchFilteredViews()` publishes and what
     * `PhoneActivityWindowTest` walks; the property is what an overlay actually meets. Either alone
     * is satisfiable without the other, which is why both are asserted -- `SettingsSurfaceReplaceTest`
     * makes the same pair of claims about the chip that ends the pairing.
     */
    @Test
    fun `the settings redirect is a control the surface declares and filters`() {
        withSettingsSurface { _, surface ->
            assertTrue(
                "agents-tracker-0dij: the notification redirect is not among the surface's " +
                    "declared action views, so no fence in this module has ever looked at it",
                surface.touchFilteredActions.contains(surface.openNotificationSettings),
            )
            assertTrue(
                "PB-SEC-12 clause 1: the notification redirect does not filter obscured touches",
                surface.openNotificationSettings.filterTouchesWhenObscured,
            )
        }
    }

    /**
     * THE POST-DIALOG REDRAW RIDES `onResume`, and this is where that dependency is written down.
     *
     * There is no `onRequestPermissionsResult` anywhere in this app -- not on the Activity, not on
     * any surface -- so nothing is told when the user answers the permission dialog. What redraws
     * the settings panel afterwards is `PhoneActivity.onResume`, which fires when the dialog's
     * window goes away and calls `PhoneSurface.render()`; `SettingsSurface.read` re-resolves the
     * permission on every draw, so the grant lands on screen there or nowhere.
     *
     * THE ASSERTION IS THE ABSENCE ITSELF, because the absence is what makes the resume
     * load-bearing. An override added later that redrew nothing would be a second, silent path off
     * the dialog; an override that redrew properly would make this test's reasoning stale. Either
     * way the next reader must come back to this comment, which is the point.
     */
    @Test
    fun `the redraw after the permission dialog rides onResume`() {
        val declared = PhoneActivity::class.java.declaredMethods.map { it.name }.toSet()

        assertTrue(
            "PhoneActivity no longer overrides onResume, so nothing redraws the settings panel " +
                "after the permission dialog closes and a granted permission stays invisible " +
                "until the app is relaunched",
            "onResume" in declared,
        )
        assertTrue(
            "PhoneActivity now overrides onRequestPermissionsResult. That is a second path off " +
                "the permission dialog, and agents-tracker-0dij's flow was written on there being " +
                "exactly one: if this is now where the answer is handled, move this test's " +
                "reasoning there rather than deleting it",
            "onRequestPermissionsResult" !in declared,
        )
    }

    // ---- agents-tracker-2yfn: the way out of a blocked channel --------------
    //
    // FAILING-FIRST (TDD RED, GG-5). The channel block is the state POST_NOTIFICATIONS cannot see:
    // the user long-presses a wake, blocks `Agent updates`, and the permission stays GRANTED while
    // the framework drops every wake. The press is assertable here for the same reason the
    // permission redirect's is -- it leaves the app through `Activity.startActivity`, which
    // Robolectric records, and it reaches no phone core on the way.

    /**
     * The control offered over a blocked channel goes to THAT CHANNEL's page.
     *
     * `ACTION_APP_NOTIFICATION_SETTINGS` -- where the permission redirect goes -- is the app's
     * notification list, on which `Agent updates` is one row among however many an app has. The
     * platform ships a screen for one channel and it needs two extras: `EXTRA_APP_PACKAGE` to name
     * the app, and `EXTRA_CHANNEL_ID` to name the channel. Without the second the intent resolves to
     * nothing at all.
     */
    @Test
    fun `the channel redirect opens the wake channel's own settings`() {
        withSettingsSurface { activity, surface ->
            surface.openChannelSettings.performClick()

            val started = shadowOf(activity).nextStartedActivity
            assertNotNull(
                "agents-tracker-2yfn: pressing the channel redirect started nothing, so a user " +
                    "who has blocked the wake channel has no way back -- and nothing else in the " +
                    "app can tell, because the permission stays GRANTED",
                started,
            )
            assertEquals(
                "the channel redirect does not open the channel settings screen",
                Settings.ACTION_CHANNEL_NOTIFICATION_SETTINGS,
                started.action,
            )
            assertEquals(
                "the intent names no package, so it opens whichever app the system feels like",
                activity.packageName,
                started.getStringExtra(Settings.EXTRA_APP_PACKAGE),
            )
            assertEquals(
                "the intent names no channel, so `ACTION_CHANNEL_NOTIFICATION_SETTINGS` has " +
                    "nothing to show and resolves to nothing",
                WakeNotifications.CHANNEL_ID,
                started.getStringExtra(Settings.EXTRA_CHANNEL_ID),
            )
        }
    }

    /** It is one of the surface's declared controls and it carries the filter, like the other one. */
    @Test
    fun `the channel redirect is a control the surface declares and filters`() {
        withSettingsSurface { _, surface ->
            assertTrue(
                "agents-tracker-2yfn: the channel redirect is not among the surface's declared " +
                    "action views, so no fence in this module has ever looked at it",
                surface.touchFilteredActions.contains(surface.openChannelSettings),
            )
            assertTrue(
                "PB-SEC-12 clause 1: the channel redirect does not filter obscured touches",
                surface.openChannelSettings.filterTouchesWhenObscured,
            )
        }
    }

    /**
     * A surface of its own rather than the one `PhoneActivity` holds, because that one is private to
     * `PhoneSurface`. Construction is most of the subject here and it reaches neither the phone core
     * nor the window: [SettingsSurface] builds its controls in its constructor precisely so their
     * identity -- and their touch filter -- survives every redraw.
     */
    private fun withSettingsSurface(assertions: (Activity, SettingsSurface) -> Unit) {
        ActivityScenario.launch(PhoneActivity::class.java).use { scenario ->
            scenario.onActivity { activity ->
                assertions(activity, SettingsSurface(activity, PhoneRuntime(activity)))
            }
        }
    }
}
