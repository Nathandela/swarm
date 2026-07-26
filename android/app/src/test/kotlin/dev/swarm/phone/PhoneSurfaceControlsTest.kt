package dev.swarm.phone

import android.view.View
import android.view.ViewGroup
import android.widget.Button
import android.widget.CompoundButton
import android.widget.EditText
import android.widget.TextView
import androidx.test.core.app.ActivityScenario
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner

/**
 * Phase B slice S19 -- PB-E2E-2's in-app actions have SUBJECTS, and every one of them is
 * overlay-protected.
 *
 * WHY THIS TEST EXISTS AT ALL. android/gate/s19_pbe2e2_test.go asserts the SOURCE fact -- that
 * production Kotlin calls each facade verb the smoke needs -- and a source scan cannot say
 * whether the call sits behind a control a person can press. S18's Activity passed every
 * source-level fence in this module while shipping three buttons and no scanner, no destination
 * confirmation, no SAS display, no confirm control and no keyboard; four of the requirement's
 * five actions had no subject and nothing anywhere was red. This is the runtime half.
 *
 * IT ASSERTS THE CONTROLS BY THE LABEL A USER READS, which is also what the instrumented smoke
 * presses (app/src/androidTest/.../PhoneScreenDriver.kt). A label changed on one side and not the
 * other is a smoke that cannot find the button, and this is where that surfaces -- on a JVM, in
 * two seconds, rather than on an emulator ten minutes into a run.
 *
 * PB-E2E-5 STAYS DEFERRED. Nothing here drives a camera, a biometric or an FCM delivery: the
 * phone core cannot even be built on the unit-test JVM, so what is asserted is that the controls
 * exist and carry the overlay filter -- not that pressing one succeeds.
 */
@RunWith(RobolectricTestRunner::class)
class PhoneSurfaceControlsTest {

    /** PB-E2E-2's five in-app actions, as the labels that perform them. */
    private val requiredControls = mapOf(
        "Scan the code on your machine" to
            "\"pairs against a local relay + daemon\": there is no way to start a scan",
        "Use this code" to
            "PB-PAIR-2's manual-entry fallback, which is also how the smoke hands the QR over",
        "Join this destination" to
            "PB-PAIR-6's destination confirmation, which BeginPairing leaves the app owing",
        "They match" to
            "\"SAS matches\": there is nothing to answer the comparison with",
        "They do not match" to
            "PB-SAS-3's mismatch answer, which is NOT cancel -- it is the only signal this " +
                "protocol has for a man-in-the-middle",
        "Take control" to "\"takes control\"",
        "Send line" to "\"types\": there is no control that sends a keystroke",
    )

    @Test
    fun every_action_pb_e2e_2_names_has_a_control_that_performs_it() {
        ActivityScenario.launch(PhoneActivity::class.java).use { scenario ->
            scenario.onActivity { activity ->
                val labels = activity.gatedActionViews()
                    .filterIsInstance<TextView>()
                    .map { it.text.toString() }
                for ((label, clause) in requiredControls) {
                    assertTrue(
                        "PB-E2E-2: no gated control labelled \"$label\", so the smoke cannot " +
                            "perform $clause.\nthe gated controls were:\n" +
                            labels.joinToString("\n"),
                        labels.contains(label),
                    )
                }
            }
        }
    }

    /**
     * PB-SEC-12 clause 1 for the controls S19 added, asserted as a PROPERTY OF THE HIERARCHY
     * rather than of a list the surface hands out.
     *
     * `PhoneActivityWindowTest` already walks `gatedActionViews()`, and that list is exactly what
     * a new panel can forget to contribute to. This looks at every Button and Switch actually on
     * screen instead, so a control added without being gated fails here even if nobody remembered
     * to add it to the list.
     */
    @Test
    fun every_button_and_switch_on_screen_filters_obscured_touches() {
        ActivityScenario.launch(PhoneActivity::class.java).use { scenario ->
            scenario.onActivity { activity ->
                val pressable = activity
                    .findViewById<ViewGroup>(android.R.id.content)
                    .flatten()
                    .filter { it is Button || it is CompoundButton }
                assertTrue(
                    "PB-SEC-12: no pressable control on screen, so this assertion has no subject",
                    pressable.isNotEmpty(),
                )
                for (control in pressable) {
                    assertTrue(
                        "PB-SEC-12: \"${(control as TextView).text}\" does not filter obscured " +
                            "touches. Tapjacking is the attack where an overlay covers a control " +
                            "so the user's tap lands on something they cannot see, and the ones " +
                            "here revoke a device, take control of a shell, join a relay and " +
                            "answer a man-in-the-middle check",
                        control.filterTouchesWhenObscured,
                    )
                }
            }
        }
    }

    /**
     * PB-SAS-3, at runtime. android/gate/s16_ui_test.go fences the SHAPE of a SAS field in the
     * sources; this fences that no field on the pairing screen could collect one, whatever it is
     * named. The six symbols are compared by the person holding both screens -- a field would
     * move the comparison to the phone, which sees one string and whatever an attacker relayed.
     */
    @Test
    fun no_field_on_screen_collects_a_short_authentication_string() {
        ActivityScenario.launch(PhoneActivity::class.java).use { scenario ->
            scenario.onActivity { activity ->
                val hints = activity
                    .findViewById<ViewGroup>(android.R.id.content)
                    .flatten()
                    .filterIsInstance<EditText>()
                    .map { it.hint?.toString().orEmpty().lowercase() }
                for (hint in hints) {
                    assertTrue(
                        "PB-SAS-3: a text field on the pairing screen invites a code to be " +
                            "typed (\"$hint\"). The SAS is compared on two displays and never " +
                            "entered",
                        !hint.contains("sas") && !hint.contains("symbol") &&
                            !hint.contains("six") && !hint.contains("emoji"),
                    )
                }
            }
        }
    }

    private fun View.flatten(): List<View> = when (this) {
        is ViewGroup -> listOf(this) + (0 until childCount).flatMap { getChildAt(it).flatten() }
        else -> listOf(this)
    }
}
