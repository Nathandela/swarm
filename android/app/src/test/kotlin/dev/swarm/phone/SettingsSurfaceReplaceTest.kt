package dev.swarm.phone

import androidx.test.core.app.ActivityScenario
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner

/**
 * FAILING-FIRST (TDD RED, GG-5) for agents-tracker-mrq5 -- the runtime half of the confirmation
 * behind `Replace this computer`.
 *
 * WHAT THIS FILE CAN SEE, AND WHAT IT CANNOT, stated first because the limit is what decides the
 * assertions. `PhoneRuntime.phone()` answers [PhoneStartup.Unavailable] on every JVM run: the phone
 * core is a native library cross-compiled for Android ABIs, so `SettingsSurface.render` takes the
 * Unavailable branch, the settings panel is never drawn, and no press can be driven as far as a
 * dialog. "The first press did not revoke" is therefore not assertable here -- a surface with no
 * confirmation at all would satisfy it, because the press it drove did nothing either way. Those
 * facts are fenced over the source in android/gate/mrq5_replaceconfirm_test.go, which is the same
 * line android/gate/d0b8_unpair_test.go draws over the same function for the same reason.
 *
 * WHAT IS ASSERTABLE IS CONSTRUCTION, and it happens to be the fact that matters most. PB-SEC-12
 * clause 1's `filterTouchesWhenObscured` is a property of a View INSTANCE, applied once by
 * [SecureWindow.gate] at construction; and since ADR-007 B133 removed phone-side authentication it
 * is the only thing standing between an authorising control and a tap the user could not see.
 * `PhoneSurface.confirmThenPress` records the gap in its own KDoc: the tap that OPENS a platform
 * dialog is filtered, and the dialog's own buttons are in a window that surface does not own, so a
 * confirmation built out of them moves the destructive tap to an undefended view. The confirming
 * control here is one this surface builds, so the filter reaches it -- and that is checkable on a
 * JVM without a phone core, because building views is all it takes.
 */
@RunWith(RobolectricTestRunner::class)
class SettingsSurfaceReplaceTest {

    /**
     * The view that takes the CONFIRMING tap is one the surface declares.
     *
     * The declaration is what `PhoneActivity.touchFilteredViews()` publishes and what
     * `PhoneActivityWindowTest` walks, so a confirming control outside it is a control no fence in
     * this module has ever looked at.
     */
    @Test
    fun `the control that confirms a replace is one the surface declares`() {
        withSettingsSurface { surface ->
            assertTrue(
                "agents-tracker-mrq5: the control that confirms a replace is not among the " +
                    "surface's declared action views. A confirmation whose own button lives in a " +
                    "window this surface does not own moves the destructive tap to a view nothing " +
                    "filters, which is the overlay defence being spent on the harmless press and " +
                    "not on the one that ends the pairing",
                surface.touchFilteredActions.contains(surface.confirmReplace),
            )
            assertTrue(
                "PB-SEC-12 clause 1: the control that confirms a replace does not filter " +
                    "obscured touches. The list naming it is satisfied by remembering to add a " +
                    "name; the property is what an overlay actually meets",
                surface.confirmReplace.filterTouchesWhenObscured,
            )
        }
    }

    /**
     * And it carries the filter itself, which is the half a membership list cannot state: the list
     * is satisfied by remembering to add a name to it.
     */
    @Test
    fun `every control the settings surface declares filters obscured touches`() {
        withSettingsSurface { surface ->
            val declared = surface.touchFilteredActions
            assertTrue(
                "agents-tracker-mrq5: the settings surface declares no action views at all, so " +
                    "this assertion has no subject",
                declared.isNotEmpty(),
            )
            for (control in declared) {
                assertTrue(
                    "PB-SEC-12 clause 1: a declared settings control does not filter obscured " +
                        "touches ($control). Tapjacking is the attack where an overlay covers a " +
                        "control so the user's tap lands on something they cannot see, and the " +
                        "ones here stop notification delivery and end the pairing",
                    control.filterTouchesWhenObscured,
                )
            }
        }
    }

    /**
     * A surface of its own rather than the one `PhoneActivity` holds, because that one is private
     * to `PhoneSurface` and nothing exposes it. Construction is the whole subject here, and it
     * reaches neither the phone core nor the window: [SettingsSurface] builds its controls in its
     * constructor precisely so their identity -- and their touch filter -- survives every redraw.
     */
    private fun withSettingsSurface(assertions: (SettingsSurface) -> Unit) {
        ActivityScenario.launch(PhoneActivity::class.java).use { scenario ->
            scenario.onActivity { activity ->
                assertions(SettingsSurface(activity, PhoneRuntime(activity)))
            }
        }
    }
}
