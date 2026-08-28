package dev.swarm.phone.ui.screens

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotEquals
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * FAILING-FIRST (TDD RED, GG-5) for wave R4's D3 round-3 fix pack (bead agents-tracker-0ox9):
 * the PANEL model's half of round 3's findings, frozen as words on a plain JVM.
 *
 * COMPILE-RED ON PURPOSE: `SELECTED_MARK`, `switchedTo`, `ADD_LIMITS`, `ADD_CONFIRM`,
 * `ADD_IN_FLIGHT`, the three-argument `statusLine` and `MachinesPanel.selectedMachineId` do not
 * exist yet. The structural half -- WHO spends each of these -- is
 * android/gate/r4_d3_round3_test.go's, on mrq5's recorded split: the gate fences the spend, this
 * file fences what the words say, and neither repeats the other.
 *
 * WHAT EACH CONSTANT ANSWERS (round 3's review, in its severity order):
 *
 *  - SELECTED_MARK / statusLine(row, now, selected) / MachinesPanel.selectedMachineId:
 *    BLOCKING 1. A successful SWITCH_COMPUTER was indistinguishable from a dead button --
 *    `switchComputer` settled with the default no-op, and the redraw guard early-returned
 *    because the panel was byte-identical. It CANNOT change from the facade: the roster's
 *    Connected flag only moves when the roster exceeds the cap, and MachineInfo carries no
 *    current-machine fact at all. So the mark is the SURFACE's own record of the selection,
 *    carried on the panel -- which makes the panel differ, which is what ends the early return.
 *  - switchedTo(name): the same finding's spoken half. It names the machine AND states the
 *    limit in mobile/machines.go:19-21's own words: the selection is recorded, the live relay
 *    session has not moved.
 *  - ADD_LIMITS: BLOCKING 2. Add computer cannot be COMPLETED by a user in this slice -- the
 *    added row awaits that machine's own pairing ceremony (bead agents-tracker-ak2s), and
 *    switching does not re-target the live session. The screen says both, on screen, rather
 *    than leaving a user to discover a permanently stale row.
 *  - ADD_CONFIRM / ADD_IN_FLIGHT: BLOCKING 3. Add stops the drain (App.Stop -> suspendInput ->
 *    coalesce.Abandon + Leases().SeverAll + a real disconnect), which is strictly more
 *    destructive than Forget's one pairing -- and Forget asks. The question names that blast
 *    radius; ADD_IN_FLIGHT is what a refused second tap is told, because a dropped tap that
 *    says nothing is the silent no-op shape again.
 */
class MachinesPanelRound3Test {

    private fun row(
        id: String,
        connected: Boolean = true,
        lastSyncUnixMs: Long = 1_000L,
        needsInput: Int = 0,
    ) = MachineRowModel(
        machineId = id,
        displayName = "laptop",
        connected = connected,
        lastSyncUnixMs = lastSyncUnixMs,
        needsInput = needsInput,
    )

    // -------------------------------------------------------------------
    // BLOCKING 1: the switch leaves a mark, and the mark is on the row.
    // -------------------------------------------------------------------

    @Test
    fun theSelectedRowSaysSoOnItsStatusLine() {
        val now = 10_000_000L
        assertEquals(
            "the row the user switched to must SAY it is the selected one; without a mark a " +
                "successful switch changes nothing on screen and is indistinguishable from a " +
                "dead button (the panel cannot change by itself -- the roster's Connected flag " +
                "only moves when the roster exceeds the cap)",
            "selected, connected, synced 1m ago",
            MachinesPanelScreen.statusLine(
                row("m-a", lastSyncUnixMs = now - 60_000L),
                now,
                selected = true,
            ),
        )
    }

    @Test
    fun theMarkIsTheModelsRecordedWordAndNotATypedOne() {
        assertTrue(
            "the selected mark must be a recorded constant the composition and its tests both " +
                "spend; a word typed at a call site is the drift that made the pairing panel " +
                "unfindable (PB-DS-9, agents-tracker-64rf)",
            MachinesPanelScreen.statusLine(row("m-a"), 10_000L, selected = true)
                .startsWith(MachinesPanelScreen.SELECTED_MARK),
        )
    }

    @Test
    fun anUnselectedRowsLineIsExactlyWhatItWasBefore() {
        // The round-2 contract stands unweakened: adding the mark must not move, reword or
        // evict any of playbook 4.2:198's four facts on the rows that carry no mark.
        val now = 10_000_000L
        val subject = row("m-a", lastSyncUnixMs = now - 60_000L, needsInput = 2)
        assertEquals(
            "connected, synced 1m ago, 2 sessions need input",
            MachinesPanelScreen.statusLine(subject, now, selected = false),
        )
        assertEquals(
            "the defaulted two-argument call and the explicit unselected call must be the same " +
                "sentence, or the mark has silently changed every existing caller",
            MachinesPanelScreen.statusLine(subject, now),
            MachinesPanelScreen.statusLine(subject, now, selected = false),
        )
    }

    @Test
    fun thePanelCarriesTheSelectionSoTheRedrawGuardCanSeeIt() {
        val rows = listOf(row("m-a"), row("m-b"))
        val onA = MachinesPanelScreen.of(rows, cap = 3, selected = "m-a")
        val onB = MachinesPanelScreen.of(rows, cap = 3, selected = "m-b")
        assertEquals("the panel does not carry which machine is selected", "m-a", onA.selectedMachineId)
        assertNotEquals(
            "switching machines produces a byte-identical panel, so PhoneSurface.drawMachines' " +
                "equality guard early-returns and NOTHING is redrawn -- which is exactly how a " +
                "successful switch became a silent no-op (BLOCKING 1's mechanism)",
            onA,
            onB,
        )
    }

    @Test
    fun aSwitchThatSucceededSaysSoAndSaysWhatItDidNotDo() {
        assertEquals(
            "a successful switch must speak: it names the machine, and it states the limit in " +
                "mobile/machines.go:19-21's own words -- SelectMachine records the viewed " +
                "pairing and does NOT yet re-target the App's live relay session. Claiming the " +
                "session moved would be the dishonest rendering in the other direction",
            "Now viewing laptop.",
            MachinesPanelScreen.switchedTo("laptop"),
        )
    }

    // -------------------------------------------------------------------
    // BLOCKING 2: what Add can and cannot finish, said on screen.
    // -------------------------------------------------------------------

    @Test
    fun theAddFormStatesBothLimitsItCannotClearItself() {
        assertEquals(
            "the add form presents two raw text boxes and calls AddMachine: the row it creates " +
                "awaits that machine's own pairing ceremony (bead agents-tracker-ak2s) and " +
                "switching to it does not re-target the live relay session. Both limits are " +
                "mobile/machines.go:19-21's own disclosure and must be on screen, not only in a " +
                "verification file",
            "You'll pair with it next.",
            MachinesPanelScreen.ADD_LIMITS,
        )
    }

    // -------------------------------------------------------------------
    // BLOCKING 3: Add asks first, and a second tap is refused out loud.
    // -------------------------------------------------------------------

    @Test
    fun theAddQuestionNamesTheBlastRadiusItActuallyHas() {
        assertEquals(
            "Add runs App.Stop around the migration: suspendInput abandons every buffered " +
                "keystroke as undelivered, severs every input lease and drops the connection. " +
                "That is strictly more destructive than Forget's one pairing, and Forget asks. " +
                "The question must name what is briefly lost and what is not",
            "Add laptop? The app reconnects for a moment.",
            MachinesPanelScreen.ADD_CONFIRM("laptop"),
        )
    }

    // -------------------------------------------------------------------
    // Test integrity (round 3, MAJOR): why the view suite asserts the PHRASE.
    // -------------------------------------------------------------------

    @Test
    fun theLastSyncAgeAloneSuppliesDigitsAndNoNeedsInputPhrase() {
        // MachinesPanelViewTest.aRowRendersItsNeedsInputCount asserted `contains("2")` over a
        // helper that passed NO clock, so the sublabel was computed against the real one over
        // lastSyncUnixMs = 1000L: `synced 20681d ago` satisfied it for a machine with nothing
        // waiting. The assertion's text never changed and it stopped asking anything. This is
        // the demonstration, kept because it is the reason that suite now freezes its clock and
        // asks for the phrase -- a strengthening, never a weakening.
        val line = MachinesPanelScreen.statusLine(
            row("m-a", lastSyncUnixMs = 1_000L, needsInput = 0),
            1_787_000_000_000L,
        )
        assertTrue("the age no longer contains the digit the old check counted: $line", line.contains("2"))
        assertTrue(
            "a machine with nothing waiting must render no needs-input phrase: $line",
            !line.contains("need input") && !line.contains("needs input"),
        )
    }

    @Test
    fun aSecondAddWhileOneIsRunningIsToldWhyNothingHappened() {
        assertEquals(
            "a refused double tap must SAY it was refused; dropping it silently is the " +
                "silent-no-op shape hard rule 5 forbids, on the app's most destructive control",
            "Still adding…",
            MachinesPanelScreen.ADD_IN_FLIGHT,
        )
    }
}
