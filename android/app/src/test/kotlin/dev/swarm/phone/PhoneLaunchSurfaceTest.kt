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
 * PB-E2E-5 STAYS DEFERRED. Nothing here drives a camera, an FCM delivery or a real launch: the
 * phone core is a native library cross-compiled for Android ABIs, so `PhoneRuntime.phone()`
 * answers [PhoneStartup.Unavailable] under Robolectric and no assertion below depends on a
 * machine being reachable. What is asserted is that the controls exist and are the ones the
 * surface built -- not that pressing one launches anything.
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
     * THE PER-USE GATE ASSERTION IS GONE AND ITS ANTI-VACUITY JOB IS NOT (ADR-007 B133).
     *
     * What stood here was `the_control_that_starts_a_session_carries_the_per_use_gate`. It read
     * `PhoneSurface.gatedActions` and required the launch control to be in it, because
     * requirements 6.0 put LAUNCH in PB-SEC-2's PER-USE tier and
     * android/gate/s20_pbsec2_peruse_test.go carried a CALL-SITE FLOOR -- every production call
     * of a per-use verb declared through `PhoneSurface.perUseButton`, failing if fewer than two
     * such call sites were found -- precisely so the gate would become mandatory the day this
     * screen landed. PB-SEC-2 is VOID; `perUseButton`, `gatedButton` and `gatedActions` are all
     * gone; the floor's subject does not exist. It is deleted rather than neutered, because a
     * gate assertion rewritten to assert nothing is worse than no assertion.
     *
     * ITS OTHER JOB HAS TO SURVIVE, and this is why the file does not simply lose a test. Three
     * views carrying the right words and no wiring satisfy the two assertions above -- measured,
     * once, by adding exactly that to [PhoneSurface] and watching them go green. So the join is
     * re-anchored to the fence that DID survive: PB-SEC-12 clause 1's touch filter, which
     * matters more now that there is no second checkpoint behind any of these controls.
     *
     * WHAT IT CANNOT SEE, because a fence that overclaims is worse than one that states its
     * limit: this reads a LIST the surface publishes, so it proves REGISTRATION, not the
     * factory. A plain Button added to [PhoneSurface.touchFilteredActions] would satisfy it, and
     * `PhoneSurfaceControlsTest.every_button_and_switch_on_screen_filters_obscured_touches` is
     * the half that walks the hierarchy instead. It makes NO claim about authentication, because
     * there is none to claim.
     */
    @Test
    fun the_control_that_starts_a_session_is_one_the_surface_declares() {
        ActivityScenario.launch(PhoneActivity::class.java).use { scenario ->
            scenario.onActivity { activity ->
                val declared = activity.touchFilteredViews()
                    .filterIsInstance<TextView>()
                    .map { it.text.toString().lowercase() }
                assertTrue(
                    "PB-APP-6 / PB-SEC-12: the control that starts a session is not among the " +
                        "surface's declared action views, so either it does not exist or it was " +
                        "added as a bare Button beside the ones built by the factory. Launch " +
                        "starts work on someone's machine from a phone; the overlay filter is " +
                        "the only thing standing between a tap on it and a tap the user could " +
                        "not see.\n" +
                        describe("the declared controls were", declared),
                    declared.any { label -> submitControl.any { label.contains(it) } },
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
                        "want at least $CONTROL_FLOOR. Either this test has stopped reading the " +
                        "hierarchy -- in which case every assertion above would fail as a " +
                        "missing launch screen rather than as a broken scan -- or a control " +
                        "moved into a panel that is not open, which is what happened when the " +
                        "terminal peek was composed. Read the labels below before touching the " +
                        "floor: a scan that names the controls it did find is working.\n" +
                        describe("what it saw", labels),
                    labels.size >= CONTROL_FLOOR,
                )
                assertTrue(
                    "the walk found no text field at all on the phone screen. The launch form's " +
                        "three and the pairing manual-entry field are all EditTexts and all are " +
                        "attached, so an empty answer is the scan failing rather than the app. " +
                        "The composer's field is NOT among them any more (agents-tracker-nx44.6): " +
                        "it moved to the session detail with the Send control beside it.",
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

    /**
     * Every control a person can press, by the words on it.
     *
     * IT IS NOT `filterIsInstance<Button>()` ANY MORE, and the widening is the point rather than a
     * convenience. `ctaButton` -- derivation §3's `.acts2 button`, which is what the launch form's
     * submit is now -- returns a `TextView`: the kit builds its own surface out of a `LayerDrawable`
     * and a `Paint`, and `Button`'s own background would fight it. So the shape a launch control
     * has on this screen is a clickable TextView, and a scan that only knew about `Button` would
     * report the launch screen as missing on the day it started being drawn as designed.
     *
     * `hasOnClickListeners` IS THE PROPERTY THAT MATTERS, not the class. What every assertion here
     * asks is whether a person can press something that starts a session; a label with no listener
     * is a caption, whatever it is a subclass of.
     */
    private fun android.app.Activity.controlLabels(): List<String> = onScreen()
        .filterIsInstance<TextView>()
        .filter { it is Button || it.hasOnClickListeners() }
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
        /**
         * The number of pressable controls the surface carries WITH NO PANEL OPEN.
         *
         * It is 1 -- "launch a session" -- and the file's standing rule is that this number never
         * moves without NAMING the control that moved and saying where it went. Four times now:
         *
         *  - 5 -> 4: **Take control** left, into `peekPanelView`, which `peekHost` holds only while
         *    there is a peek to control. Offering it to someone who is not looking at a terminal
         *    was the old behaviour and it was wrong.
         *  - 4 -> 3: **Kill session** left, into `sessionDetailView`, which the Inbox destination
         *    holds only while a session is drilled into. It was a loose button ending whichever
         *    session the surface happened to be targeting, one tap away, with the model's
         *    `killRequiresConfirmation` reaching nothing; it is now on the screen that names the
         *    session it ends, behind that confirmation. PB-APP-3's **Stop** landed beside it and
         *    does NOT raise this number, because it is on the same screen and is never loose.
         *  - 3 -> 2: **Revoke this device** left, into the Settings destination, where it is the
         *    "Replace this computer" control on `SettingsSurface` (agents-tracker-64rf). It was
         *    loose in this column for the same reason the pairing panel was, and the owner could
         *    not find either on a real handset. Unpairing is now reachable from the screen that
         *    NAMES the machine it unpairs, and the revoke keeps PB-SEC-12 clause 1's touch filter
         *    because `SettingsSurface` builds it and contributes it through
         *    `settings.touchFilteredActions` -- the coverage moved, it never lapsed.
         *  - 2 -> 1: **Send line** left, into `sessionDetailView`, which the Inbox destination
         *    holds only while a session is drilled into (agents-tracker-nx44.6). It and the field
         *    beside it were derivation row 9's composer standing in a bare column under the triage
         *    inbox -- and `PhoneSurface.detachHostedViews` takes that column off the window on the
         *    way into the drill-down, which is the ONE screen whose lease sentence promises that
         *    what you type is sent live. So the surface promised typing where there was no field
         *    and offered a field where there was no promise. Both are `ui/kit/Composer.kt`'s bar on
         *    the session detail now -- the control is the bar's 40 dp square since phone refit W3,
         *    spoken "Send" or "Stop" rather than labelled -- and it keeps PB-SEC-12 clause 1's
         *    touch filter because `PhoneSurface` still builds it and still lists it in
         *    `touchFilteredActions`; the coverage moved, it never lapsed. `PhoneSurfaceControlsTest`
         *    reads the control by its description.
         *
         * ALL FOUR MOVES ARE THE PRODUCT BEING CORRECTED, NOT THE SCAN BREAKING. The distinction the
         * floor exists to draw is between "the surface shrank" and "the walk stopped reading the
         * window", and on each run that failed here the walk was reading perfectly -- it named
         * every control it found, and every label assertion in this file passed.
         *
         * The floor has no headroom, which is the honest cost of counting: the next control that
         * legitimately moves into a panel fails this test too. That is preferable to raising a
         * ceiling nobody can justify -- and the label assertions above, not this count, are what
         * actually pin the launch screen.
         */
        const val CONTROL_FLOOR = 1
    }
}
