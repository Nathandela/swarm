package dev.swarm.phone

import android.view.View
import android.view.ViewGroup
import android.widget.Button
import android.widget.EditText
import android.widget.TextView
import androidx.test.core.app.ActivityScenario
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner

/**
 * FAILING-FIRST (TDD RED, GG-5) for PB-APP-6 -- **a person can start a session from the phone**.
 *
 * WHY IT IS HOSTED AND NOT A MODEL TEST, which is the whole point of the file. PB-APP-6's
 * acceptance is "UI + facade test", and `ui/MachineAndLaunch.kt` already ships [LaunchScreen]
 * with five green unit tests over it -- a model that decides what a launch screen shows, with
 * no launch screen. android/unbound-verbs.tsv says so in its own words: *"MachineAndLaunch is
 * the model, and the surface has no machine pane, no launch form and no session picker."* A
 * requirement whose acceptance names a UI cannot be met by testing the model harder, and
 * ADR-007 B80 is the record of that going the other way: the ledger said the screen did not
 * exist and the traceability table said PB-APP-6 was shipped. So every assertion below reads
 * the hierarchy the shipped [PhoneActivity] actually puts on screen.
 *
 * IT BEARS ON THE BINDING EXIT CRITERION. Section 1 requires a demonstration that a phone
 * "pairs, observes, LAUNCHES, and types into a real session". There is no path from a phone
 * screen to a launch today -- not a broken one, none -- and this is where that becomes red.
 *
 * WHAT IT DELIBERATELY DOES NOT PIN. Not an arrangement, not an order, not a parent-child
 * relation, not a count, not a visibility. A launch form built as a panel of this surface, as a
 * sheet, or behind a control that reveals it all satisfy this file, provided the views are
 * ATTACHED to the window (`View.GONE` is fine) -- which is the pattern [PairingSurface] and
 * [SettingsSurface] already use: every view is added once and visibility is what changes. The
 * wordings below are a SET rather than a string for the same reason; see [SUBMIT_CONTROL].
 *
 * PB-E2E-5 STAYS DEFERRED. Nothing here drives a camera, a biometric, an FCM delivery or a real
 * launch: the phone core is a native library cross-compiled for Android ABIs, so
 * `PhoneRuntime.phone()` answers [PhoneStartup.Unavailable] under Robolectric and no assertion
 * below depends on a machine being reachable. What is asserted is that the controls exist and
 * carry PB-SEC-2's gate -- not that pressing one launches anything.
 */
@RunWith(RobolectricTestRunner::class)
class PhoneLaunchSurfaceTest {

    /**
     * A control that starts a session, by the words a user would read on it.
     *
     * IT IS A SET AND THE SET IS THE CONTRACT. The requirement is that SOMETHING on the screen
     * starts a session, not that it is spelled a particular way, and a fence pinned to one
     * string is a fence that blocks a reasonable implementation for a synonym. An implementer
     * who chooses other words widens this list and the smoke's own label together
     * (app/src/androidTest/.../PbE2E2PairAndTypeTest.kt drives the same labels), which is the
     * S19 discipline: the label a user reads is the label the demonstration presses.
     */
    private val submitControl = listOf("launch", "start", "new session")

    /** The two fields [dev.swarm.phone.ui.LaunchScreen.submit] refuses a draft without. */
    private val agentField = listOf("agent")
    private val cwdField = listOf("director", "folder", "cwd", "working")

    /**
     * PB-APP-6's two required fields, on screen, because the alternative is worse than a missing
     * screen.
     *
     * `LaunchScreen.submit` REQUIRES a non-blank agent and a non-blank working directory -- the
     * daemon has no default for either and refuses a launch without them. A surface with no
     * fields could only satisfy that by inventing values, which is a hardcoded launch spec
     * shipped in production code and is exactly what PB-INPUT-2's `leaseHeld = false` already
     * cost this project once: a literal standing in for a fact nobody collected.
     */
    @Test
    fun a_person_can_say_which_agent_to_start_and_where_it_starts() {
        ActivityScenario.launch(PhoneActivity::class.java).use { scenario ->
            scenario.onActivity { activity ->
                val hints = activity.fieldHints()
                assertTrue(
                    "PB-APP-6: no field on the phone screen asks WHICH AGENT to start, so a " +
                        "launch spec cannot be built from anything the user said. " +
                        "LaunchScreen.submit refuses a draft with a blank agent, so a surface " +
                        "without this field can only pass a value nobody chose.\n" +
                        describe("the fields on screen were", hints),
                    hints.any { hint -> agentField.any { hint.contains(it) } },
                )
                assertTrue(
                    "PB-APP-6: no field on the phone screen asks WHERE the agent starts. The " +
                        "working directory is the second field LaunchScreen.submit requires, " +
                        "and it is the one a policy refusal is usually about (\"cwd is outside " +
                        "the allowed roots\") -- so a hardcoded one is a launch the user cannot " +
                        "correct.\n" + describe("the fields on screen were", hints),
                    hints.any { hint -> cwdField.any { hint.contains(it) } },
                )
            }
        }
    }

    /**
     * The action itself. Section 1's "launches" has no subject without it.
     */
    @Test
    fun a_control_on_the_phone_screen_starts_a_session() {
        ActivityScenario.launch(PhoneActivity::class.java).use { scenario ->
            scenario.onActivity { activity ->
                val labels = activity.controlLabels()
                assertTrue(
                    "PB-APP-6: no control on the phone screen starts a session. `App.Launch` is " +
                        "bound, tested on the Go side and reached by no Kotlin at all, which is " +
                        "this phase's standing defect class -- and section 1's binding exit " +
                        "criterion is that a phone \"pairs, observes, LAUNCHES, and types into " +
                        "a real session\".\n" + describe("the controls on screen were", labels),
                    labels.any { label -> submitControl.any { label.contains(it) } },
                )
            }
        }
    }

    /**
     * PB-SEC-2's tier, at the control rather than in the table.
     *
     * Requirements 6.0 and `ui/MachineAndLaunch.kt`'s own `GateFreshness` both put LAUNCH in the
     * PER-USE tier, and android/gate/s20_pbsec2_peruse_test.go already requires every production
     * call of a per-use facade verb to be declared through `PhoneSurface.perUseButton` -- with a
     * comment saying `app.launch(` is in that list precisely so the gate becomes mandatory the
     * day this screen lands. A per-use control reaches [PhoneSurface.gatedActions] by
     * construction, so this asserts the join: the control a user presses to start a session is
     * the gated one, and not a plain Button added beside it.
     *
     * IT IS ALSO THE ANTI-VACUITY HALF of the test above. Three views carrying the right words
     * and no wiring satisfy the two assertions before this one -- measured, by adding exactly
     * that to [PhoneSurface] and watching them go green while this one stayed red.
     *
     * WHAT IT CANNOT SEE, because a fence that overclaims is worse than one that says its limit:
     * this reads a LIST the surface publishes, so it proves REGISTRATION, not the factory. A
     * plain Button added to [PhoneSurface.gatedActions] would satisfy it. The per-use half is
     * android/gate/s20_pbsec2_peruse_test.go, which requires every production call of a per-use
     * facade verb to be declared through `PhoneSurface.perUseButton` and already carries
     * `app.launch(` in its list for that day; what this adds is the other direction -- that the
     * control a user presses is one of the surface's declared gated ones, which is
     * [PhoneSurface.gatedActions]' own stated rule ("a new panel contributes its own gated set
     * here rather than being remembered about") and PB-SEC-12 clause 1's subject.
     */
    @Test
    fun the_control_that_starts_a_session_carries_the_per_use_gate() {
        ActivityScenario.launch(PhoneActivity::class.java).use { scenario ->
            scenario.onActivity { activity ->
                val gated = activity.gatedActionViews()
                    .filterIsInstance<TextView>()
                    .map { it.text.toString().lowercase() }
                assertTrue(
                    "PB-SEC-2 / PB-APP-6: the control that starts a session is not in the " +
                        "surface's gated set, so either it does not exist or it was added as a " +
                        "plain control beside the gated ones. Requirements 6.0 puts LAUNCH in " +
                        "the PER-USE tier: ending or starting work on someone's machine is one " +
                        "prompt away from a phone in a stranger's hand, and a per-use verb " +
                        "reached without PhoneSurface.perUseButton is the silent downgrade " +
                        "ADR-007 B51 found shipped.\n" +
                        describe("the gated controls were", gated),
                    gated.any { label -> submitControl.any { label.contains(it) } },
                )
            }
        }
    }

    /**
     * Defect class (i) turned on this file: every assertion above is of the form "nothing was
     * found", and a hierarchy walk that returned nothing produces exactly that answer -- while
     * the repair a reader reaches for is to add controls that are already there.
     *
     * The floors are on the SCAN, not on the design. The surface carries nine or so pressable
     * controls and two fields today; a run that finds materially fewer has stopped reading the
     * window.
     */
    @Test
    fun the_scan_can_see_the_screen_it_is_asking_about() {
        ActivityScenario.launch(PhoneActivity::class.java).use { scenario ->
            scenario.onActivity { activity ->
                val labels = activity.controlLabels()
                val hints = activity.fieldHints()
                assertTrue(
                    "the walk found ${labels.size} pressable control(s) on the phone screen, " +
                        "want at least $CONTROL_FLOOR. The surface has not shrunk; this test " +
                        "has stopped reading the hierarchy, and every assertion above would " +
                        "then fail as a missing launch screen rather than as a broken scan.\n" +
                        describe("what it saw", labels),
                    labels.size >= CONTROL_FLOOR,
                )
                assertTrue(
                    "the walk found no text field at all on the phone screen. The keyboard and " +
                        "the pairing manual-entry field are both EditTexts and both are " +
                        "attached, so an empty answer is the scan failing rather than the app.",
                    hints.isNotEmpty(),
                )
            }
        }
    }

    // -----------------------------------------------------------------------
    // Reading the window.
    //
    // Everything is lower-cased once, here, so no assertion above has to remember to; and
    // nothing filters on visibility, because a launch form that is attached and hidden until a
    // control reveals it is a reasonable implementation and this file must not forbid it.
    // -----------------------------------------------------------------------

    private fun android.app.Activity.onScreen(): List<View> =
        findViewById<ViewGroup>(android.R.id.content).flatten()

    private fun android.app.Activity.controlLabels(): List<String> = onScreen()
        .filterIsInstance<Button>()
        .map { it.text.toString().lowercase() }

    private fun android.app.Activity.fieldHints(): List<String> = onScreen()
        .filterIsInstance<EditText>()
        .map { it.hint?.toString().orEmpty().lowercase() }

    private fun describe(what: String, seen: List<String>): String =
        "$what:\n" + seen.joinToString("\n") { "  \"$it\"" }

    private fun View.flatten(): List<View> = when (this) {
        is ViewGroup -> listOf(this) + (0 until childCount).flatMap { getChildAt(it).flatten() }
        else -> listOf(this)
    }

    private companion object {
        const val CONTROL_FLOOR = 5
    }
}
