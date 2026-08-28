package dev.swarm.phone.ui.screens

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * FAILING-FIRST (TDD RED, GG-5) for wave R4's D3 follow-on slice (bead agents-tracker-0ox9):
 * the machine switcher COMPOSED, not merely modelled.
 *
 * COMPILE-RED ON PURPOSE: `MachinesPanelScreen`, `MachinesPanel` and the broken-row fields on
 * `MachineRowModel` do not exist yet. The R4 round-1 slice delivered `MachinesScreen` as a pure
 * model and `docs/verification/r4-multimachine.md` D3 discloses the gap this slice closes: nothing
 * in android/app/src/main references it, so no user can reach ADD_COMPUTER, SWITCH_COMPUTER,
 * FORGET_COMPUTER or GLOBAL_INBOX. This file freezes the PANEL-LEVEL contract the composition
 * will spend -- the recorded copy, the broken-pairing fault as a user-visible state, and the
 * honest connection-cap sentence -- in the module's established shape (PB-DS-9: copy lives on the
 * screen model, views take data and copy from it, logic lives where the JVM suite can drive it).
 *
 * THE THREE RECORDED UX DEFECT SHAPES this contract exists to refuse:
 *
 *  - FINDABLE: a control that exists must be NAMED by the composition (the pairing panel was
 *    correctly built and an owner could not find it under an anonymous `below:` slot,
 *    agents-tracker-64rf; the composer repeated it, agents-tracker-nx44.6). So every affordance
 *    here has RECORDED copy the view must spend -- a label typed at a call site is the drift
 *    this model forbids.
 *  - REACHABLE: android/gate/r4_d3_reachability_test.go carries the composition-graph half.
 *  - NEVER HAND-FED: state is built through the REAL resolver from first-run --
 *    `MachinesScreen.destinationFor` from zero machines -- not by conjuring a MACHINES world the
 *    resolver never answered.
 */
class MachinesPanelScreenTest {

    private fun row(
        id: String,
        name: String = "laptop",
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

    // -------------------------------------------------------------------
    // First-run, through the real resolver. Never hand-fed.
    // -------------------------------------------------------------------

    @Test
    fun theSwitcherWorldIsEnteredThroughTheResolverFromFirstRun() {
        // First run: zero machines. The resolver -- not the test -- decides there is no
        // switcher world at all here, only the pair-only screen.
        var machineCount = 0
        assertEquals(
            "a first-run phone (zero machines) resolved to a machines destination; the switcher " +
                "must not exist before the resolver says the world it lives in does",
            MachinesDestination.PAIR_ONLY,
            MachinesScreen.destinationFor(machineCount),
        )

        // The first Add computer is what changes the resolver's answer. Only after it does a
        // panel exist to compose.
        machineCount += 1
        assertEquals(
            MachinesDestination.MACHINES,
            MachinesScreen.destinationFor(machineCount),
        )
        val panel = MachinesPanelScreen.of(listOf(row("m-a")), cap = 3)
        assertEquals(1, panel.rows.size)
    }

    @Test
    fun thePanelRowSetIsTheModelsOwnAndNotARestatement() {
        // The panel folds duplicate machine ids exactly as MachinesScreen.rows does: one row per
        // pairing. A panel that re-derived its own row set is the second copy that drifts.
        val panel = MachinesPanelScreen.of(listOf(row("m-a"), row("m-a")), cap = 3)
        assertEquals(
            "a duplicate machine id is two rows on the panel; identity is the machine id (MM4) " +
                "and two rows for one pairing is two identities for one send sequencer",
            1,
            panel.rows.size,
        )
    }

    // -------------------------------------------------------------------
    // The recorded copy: every affordance nameable, named ONCE, spent by the view.
    // -------------------------------------------------------------------

    @Test
    fun everyAffordanceCarriesRecordedCopy() {
        assertEquals(
            "the navigation entry that leads to the switcher is not named 'Computers'; a control " +
                "that exists must be findable by its recorded name (defect shape 1, " +
                "agents-tracker-64rf)",
            "Computers",
            MachinesPanelScreen.ENTRY_LABEL,
        )
        assertEquals(
            "playbook 4.1 step 4's own words: the developer 'chooses Add computer'",
            "Add computer",
            MachinesPanelScreen.ADD_LABEL,
        )
        assertEquals(
            "playbook 4.9's own words, phone-side and distinct from machine-side revoke",
            "Forget this computer",
            MachinesPanelScreen.FORGET_LABEL,
        )
        assertEquals(
            "the aggregate inbox destination across every pairing (inbox.global)",
            "All sessions",
            MachinesPanelScreen.GLOBAL_INBOX_LABEL,
        )
    }

    // -------------------------------------------------------------------
    // The broken pairing is a USER-VISIBLE STATE: its own fault, on its own row.
    // -------------------------------------------------------------------

    @Test
    fun aBrokenRowCarriesItsOwnFaultAndNeverReadsConnected() {
        val broken = MachineRowModel(
            machineId = "m-b",
            displayName = "desk",
            connected = false,
            lastSyncUnixMs = 5L,
            needsInput = 0,
            broken = true,
            brokenReason = "the sealed blob refused to open",
        )
        assertTrue("a broken pairing's row does not say it is broken", broken.broken)
        assertNotEquals(
            "a broken row reads as connected, which is the dishonest rendering ADR-018 MM3 " +
                "forbids in either direction",
            "connected",
            broken.reachability,
        )
    }

    @Test
    fun theBrokenNoticeNamesTheFaultAndReassuresAboutTheOthers() {
        val broken = MachineRowModel(
            machineId = "m-b",
            displayName = "desk",
            connected = false,
            lastSyncUnixMs = 5L,
            needsInput = 0,
            broken = true,
            brokenReason = "the sealed blob refused to open",
        )
        val notice = MachinesPanelScreen.brokenNotice(broken)
        assertNotNull(
            "a broken pairing renders no notice at all: App.SelectMachine's refusal would land " +
                "as a crash or a silent no-op instead of a state the user can read (MM8, " +
                "machines.recovery)",
            notice,
        )
        assertEquals(
            "the notice does not name the row that cannot open and the two per-row remedies, " +
                "which is what keeps a user off the wholesale remedy that destroys every " +
                "pairing (MM8, phone refit W5.4)",
            "Can't open desk. Forget it or pair again.",
            notice,
        )
    }

    @Test
    fun aHealthyRowHasNoBrokenNotice() {
        assertNull(
            "a healthy row renders a broken notice; a fault sentence over a working pairing is " +
                "the same dishonest rendering in the other direction",
            MachinesPanelScreen.brokenNotice(row("m-a")),
        )
    }

    // -------------------------------------------------------------------
    // The documented connection cap, rendered honestly (ADR-018, MachineList.Cap).
    // -------------------------------------------------------------------

    @Test
    fun theCapIsStatedWhenRowsExceedItAndSilentWhenTheyDoNot() {
        val over = MachinesPanelScreen.of(
            listOf(row("m-a"), row("m-b"), row("m-c"), row("m-d", connected = false)),
            cap = 3,
        )
        assertNotNull(
            "four pairings under a cap of 3 render no cap sentence; the cap is a documented " +
                "product limitation rendered honestly, never hidden (ADR-018)",
            over.capNotice,
        )
        assertTrue(
            "the cap sentence does not state the number the rows were arbitrated under",
            over.capNotice!!.contains("3"),
        )
        assertNull(
            "a roster inside the cap renders a cap sentence anyway, which is a warning about " +
                "nothing",
            MachinesPanelScreen.of(listOf(row("m-a")), cap = 3).capNotice,
        )
    }
}
