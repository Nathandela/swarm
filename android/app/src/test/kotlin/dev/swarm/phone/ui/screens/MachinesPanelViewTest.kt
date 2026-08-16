package dev.swarm.phone.ui.screens

import android.content.Context
import android.view.View
import android.view.ViewGroup
import android.widget.TextView
import androidx.test.core.app.ApplicationProvider
import dev.swarm.phone.theme.SwarmTheme
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner

/**
 * FAILING-FIRST (TDD RED, GG-5) for wave R4's machine switcher AS DRAWN (bead
 * agents-tracker-0ox9).
 *
 * COMPILE-RED ON PURPOSE: `machinesPanelView` and `MachinesPanelScreen` do not exist yet.
 * `docs/verification/r4-multimachine.md` D3 is explicit about the state this file ends: the
 * screen model exists and "no user can reach ADD_COMPUTER, SWITCH_COMPUTER, FORGET_COMPUTER or
 * GLOBAL_INBOX". `MachinesScreenTest` asks what the model SAYS; this asks whether it is on
 * screen and whether pressing it does anything -- the split `ActivityPanelTest` /
 * `ActivityPanelViewTest` already draws, because "the model is beautiful and nothing renders it"
 * is the defect PB-DS-6 was recorded NOT MET over.
 *
 * EVERY CONTROL IS FOUND BY ITS RECORDED COPY, never by index and never by a label typed here:
 * the label a test types independently of the model is the drift that made the pairing panel
 * exist and be unfindable (agents-tracker-64rf). Appearance is deliberately not asserted --
 * android/gate/s24_screens_test.go's Obsidian sweeps (ADR-009 visual direction) cover every
 * production screen the moment the file exists.
 *
 * WHAT THIS FILE CANNOT SEE: the facade. `swarmmobile.App` is native-backed and cannot be
 * constructed on this JVM, so the callbacks asserted here are the seam the surface wires to
 * `FacadeBridge`; android/gate/r4_d3_reachability_test.go fences that the wire exists and that
 * the six R4 verbs leave android/unbound-verbs.tsv by being CALLED.
 */
@RunWith(RobolectricTestRunner::class)
class MachinesPanelViewTest {

    private val context: Context
        get() = SwarmTheme.applyTo(ApplicationProvider.getApplicationContext())

    private fun row(
        id: String,
        name: String,
        connected: Boolean = true,
        lastSyncUnixMs: Long = 1_000L,
        needsInput: Int = 0,
    ) = MachineRowModel(
        machineId = id,
        displayName = name,
        connected = connected,
        lastSyncUnixMs = lastSyncUnixMs,
        needsInput = needsInput,
    )

    private fun brokenRow(id: String, name: String, reason: String) = MachineRowModel(
        machineId = id,
        displayName = name,
        connected = false,
        lastSyncUnixMs = 5L,
        needsInput = 0,
        broken = true,
        brokenReason = reason,
    )

    /**
     * @param nowUnixMs FROZEN, and it is load-bearing rather than tidy (round 3, test-integrity
     *  finding). This helper used to pass no clock, so the row's sublabel was computed against
     *  the REAL one over `lastSyncUnixMs = 1000L` and rendered `synced 20681d ago` -- a string
     *  that satisfies `contains("2")` for a machine with NOTHING waiting on the user. The
     *  assertion below was frozen text over a vacuous check. The clock is pinned here and the
     *  assertion asks for the PHRASE, which is a strengthening: it was satisfiable by an
     *  accident and now is not.
     */
    private fun view(
        machines: List<MachineRowModel>,
        cap: Int = 3,
        onAddComputer: () -> Unit = {},
        onSwitchComputer: (String) -> Unit = {},
        onForgetComputer: (String) -> Unit = {},
        onOpenGlobalInbox: () -> Unit = {},
        nowUnixMs: Long = 10_000L,
    ): View = machinesPanelView(
        context = context,
        panel = MachinesPanelScreen.of(machines, cap = cap),
        onAddComputer = onAddComputer,
        onSwitchComputer = onSwitchComputer,
        onForgetComputer = onForgetComputer,
        onOpenGlobalInbox = onOpenGlobalInbox,
        nowUnixMs = nowUnixMs,
    )

    // -------------------------------------------------------------------
    // ADD_COMPUTER: findable by its recorded copy, and pressing it asks.
    // -------------------------------------------------------------------

    @Test
    fun addComputerIsFindableByItsRecordedCopyAndFires() {
        var added = 0
        val screen = view(listOf(row("m-a", "laptop")), onAddComputer = { added++ })
        screen.tapControl(MachinesPanelScreen.ADD_LABEL)
        assertEquals(
            "pressing '${MachinesPanelScreen.ADD_LABEL}' did nothing; playbook 4.1 step 4 is " +
                "the developer choosing Add computer, and App.AddMachine adds BESIDE the " +
                "existing pairings (never replacing) with MM6's migration on first use",
            1,
            added,
        )
    }

    // -------------------------------------------------------------------
    // SWITCH_COMPUTER: a row is the control, and it names its machine ID.
    // -------------------------------------------------------------------

    @Test
    fun tappingARowSwitchesToThatMachineById() {
        val switched = mutableListOf<String>()
        val screen = view(
            listOf(row("m-a", "laptop"), row("m-b", "laptop")),
            onSwitchComputer = { switched += it },
        )
        screen.tapControl("laptop", occurrence = 1)
        assertEquals(
            "tapping the SECOND row named 'laptop' did not switch to m-b; identity is the " +
                "machine id and never the display name (MM4) -- two rows may share a name " +
                "without colliding",
            listOf("m-b"),
            switched,
        )
    }

    // -------------------------------------------------------------------
    // FORGET_COMPUTER: offered per row, phone-side, by its recorded copy.
    // -------------------------------------------------------------------

    @Test
    fun everyRowOffersForgetAndItNamesItsMachine() {
        val forgotten = mutableListOf<String>()
        val screen = view(
            listOf(row("m-a", "laptop")),
            onForgetComputer = { forgotten += it },
        )
        screen.tapControl(MachinesPanelScreen.FORGET_LABEL)
        assertEquals(
            "'${MachinesPanelScreen.FORGET_LABEL}' did not reach the row's machine id; " +
                "App.ForgetMachine removes exactly one pairing's registry row, namespace, keys " +
                "and caches (playbook 4.9), and a forget without an id is a forget by proximity",
            listOf("m-a"),
            forgotten,
        )
    }

    // -------------------------------------------------------------------
    // GLOBAL_INBOX: the aggregate destination has a findable way in.
    // -------------------------------------------------------------------

    @Test
    fun theGlobalInboxEntryIsFindableAndFires() {
        var opened = 0
        val screen = view(listOf(row("m-a", "laptop")), onOpenGlobalInbox = { opened++ })
        screen.tapControl(MachinesPanelScreen.GLOBAL_INBOX_LABEL)
        assertEquals(
            "pressing '${MachinesPanelScreen.GLOBAL_INBOX_LABEL}' did nothing; the aggregate " +
                "inbox (inbox.global) is a destination, and a destination nothing navigates to " +
                "is the PB-DS-6 defect again",
            1,
            opened,
        )
    }

    // -------------------------------------------------------------------
    // The four row facts of playbook 4.2:198, on screen and not only in the model.
    // -------------------------------------------------------------------

    @Test
    fun aParkedRowRendersStaleAndAConnectedRowRendersConnected() {
        val screen = view(
            listOf(row("m-a", "laptop"), row("m-b", "desk", connected = false)),
        )
        val words = screen.texts()
        assertTrue(
            "the connected row's reachability word is not on screen; the model computes it " +
                "precisely so no view invents its own vocabulary",
            words.any { it.contains("connected") },
        )
        assertTrue(
            "the parked row does not render as stale; a row beyond the connection cap must " +
                "visibly show it is not live (ADR-018 MM3 -- rendering a deliberately " +
                "unconnected row as live is the dishonest rendering the cap ruling forbids)",
            words.any { it.contains("stale") },
        )
    }

    @Test
    fun aRowRendersItsNeedsInputCount() {
        val screen = view(listOf(row("m-a", "laptop", needsInput = 2)), nowUnixMs = 10_000L)
        assertTrue(
            "the needs-input count is not on screen; it is the fourth fact of playbook " +
                "4.2:198 and the one that tells a user which computer is waiting on them",
            screen.texts().any { it.contains("2 sessions need input") },
        )
    }

    /**
     * The negative control the assertion above lacked, and the reason the clock is frozen: a
     * machine with nothing waiting must render no needs-input phrase at all. A digit-counting
     * check passed this case (the last-sync age supplies digits); a phrase check cannot.
     */
    @Test
    fun aRowWithNothingWaitingRendersNoNeedsInputPhrase() {
        val screen = view(listOf(row("m-a", "laptop", needsInput = 0)), nowUnixMs = 10_000L)
        assertTrue(
            "a machine with no session waiting on the user reported one anyway; the fourth row " +
                "fact must be silent when it is zero, or every row always looks urgent",
            screen.texts().none { it.contains("need input") || it.contains("needs input") },
        )
    }

    // -------------------------------------------------------------------
    // The broken pairing renders ITS OWN fault -- a state, not a crash or a no-op.
    // -------------------------------------------------------------------

    @Test
    fun aBrokenRowRendersItsOwnFaultAndDoesNotSwitch() {
        val switched = mutableListOf<String>()
        val screen = view(
            listOf(
                row("m-a", "laptop"),
                brokenRow("m-b", "desk", reason = "the sealed blob refused to open"),
            ),
            onSwitchComputer = { switched += it },
        )
        val words = screen.texts()
        assertTrue(
            "the broken pairing's own fault is nowhere on screen; App.SelectMachine's refusal " +
                "must be a user-visible state on the row that owns it, never a crash and never " +
                "a silent no-op (MM8, machines.recovery)",
            words.any { it.contains("the sealed blob refused to open") },
        )
        assertTrue(
            "the screen does not say the other computers are unaffected, which is the half of " +
                "the sentence that stops a user reaching for the wholesale remedy that " +
                "destroys every pairing",
            words.any { it.lowercase().contains("other computers are unaffected") },
        )

        screen.tapRowNamed("desk")
        assertEquals(
            "tapping the broken row issued a switch anyway; the panel already knows the row is " +
                "broken, so pressing it must surface the fault rather than spend a signed " +
                "operation on a refusal the screen can state itself",
            emptyList<String>(),
            switched,
        )
    }

    @Test
    fun aBrokenRowStillOffersForget() {
        val forgotten = mutableListOf<String>()
        val screen = view(
            listOf(brokenRow("m-b", "desk", reason = "the sealed blob refused to open")),
            onForgetComputer = { forgotten += it },
        )
        screen.tapControl(MachinesPanelScreen.FORGET_LABEL)
        assertEquals(
            "the broken row does not offer Forget; forget-or-re-pair are the broken pairing's " +
                "whole affordance set (MM8), and a broken row with no way out is a permanent " +
                "error the user can only escape by clearing app data -- every pairing with it",
            listOf("m-b"),
            forgotten,
        )
    }

    // -------------------------------------------------------------------
    // Reading and pressing the screen. Controls are found by their words, then the
    // press climbs to the nearest clickable ancestor -- the kit composes a label
    // inside its control, and a listener on the words alone is the defect
    // PhoneSurfaceNavigationTest.tapTab already refuses.
    // -------------------------------------------------------------------

    private fun View.flatten(): List<View> = when (this) {
        is ViewGroup -> listOf(this) + (0 until childCount).flatMap { getChildAt(it).flatten() }
        else -> listOf(this)
    }

    private fun View.texts(): List<String> =
        flatten().filterIsInstance<TextView>().map { it.text.toString() }

    private fun View.clickableFor(label: String, occurrence: Int = 0): View {
        val matches = flatten().filterIsInstance<TextView>()
            .filter { it.text.toString() == label }
        assertTrue(
            "no view on the machines screen reads \"$label\" (found ${matches.size}, wanted " +
                "at least ${occurrence + 1}); a control that exists must be findable by its " +
                "recorded copy",
            matches.size > occurrence,
        )
        var view: View? = matches[occurrence]
        while (view != null && !view.hasOnClickListeners()) {
            view = view.parent as? View
        }
        assertNotNull(
            "\"$label\" is on screen but neither it nor any ancestor takes a tap; a label " +
                "with nothing behind it is decoration, not a control",
            view,
        )
        return view!!
    }

    private fun View.tapControl(label: String, occurrence: Int = 0) {
        clickableFor(label, occurrence).performClick()
    }

    /**
     * Tap the row named [name] WITHOUT requiring it to be clickable: a broken row may
     * legitimately take no tap at all, and this press asserts only that whatever happens is
     * not a switch.
     */
    private fun View.tapRowNamed(name: String) {
        val text = flatten().filterIsInstance<TextView>()
            .firstOrNull { it.text.toString() == name }
        assertNotNull("no row named \"$name\" on the machines screen", text)
        var view: View? = text
        while (view != null && !view.hasOnClickListeners()) {
            view = view.parent as? View
        }
        view?.performClick()
    }
}
