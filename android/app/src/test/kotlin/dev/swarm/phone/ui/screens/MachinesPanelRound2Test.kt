package dev.swarm.phone.ui.screens

import org.junit.Assert.assertEquals
import org.junit.Test

/**
 * FAILING-FIRST (TDD RED, GG-5) for wave R4's D3 round-2 fix pack (bead agents-tracker-0ox9):
 * the PANEL model's half of the review findings, frozen as words on a plain JVM.
 *
 * COMPILE-RED ON PURPOSE: `FORGET_CONFIRM`, `PAIR_FIRST` and the two-argument `statusLine`
 * overload do not exist yet. The structural half -- WHO spends each of these -- is
 * android/gate/r4_d3_round2_test.go's, on mrq5's recorded split: the gate fences the spend, this
 * file fences what the words say, and neither repeats the other.
 *
 * WHAT EACH CONSTANT ANSWERS:
 *
 *  - FORGET_CONFIRM: forgetting a pairing destroys its keys, namespace and caches with no undo,
 *    and it was one un-confirmed tap on a denyChip while `kill` -- ONE session -- has asked
 *    since S24. The question names what is destroyed, what is NOT (the computer itself), and
 *    carries MM8's reassurance so the dialog does not read as app-wide data loss.
 *  - PAIR_FIRST: the resolver's PAIR_ONLY answer used to compose nothing and say nothing -- the
 *    silent no-op shape. The sentence is the answer as a state.
 *  - statusLine(row, nowUnixMs): playbook 4.2:198 gives a row FOUR facts (name, reachability,
 *    last sync, needs-input) and only three reached the screen. The age spends the app's one
 *    elapsed-duration model ([dev.swarm.phone.ui.MachineFreshness.sinceLastHeard]) rather than a
 *    second formatter that can drift; the one-argument statusLine is untouched, so every
 *    existing assertion is exactly as true as it was.
 */
class MachinesPanelRound2Test {

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
    // The forget confirmation: recorded once, names the blast radius honestly.
    // -------------------------------------------------------------------

    @Test
    fun theForgetQuestionNamesWhatItDestroysAndWhatItDoesNot() {
        assertEquals(
            "the forget confirmation must say what this phone deletes, that the computer " +
                "itself is untouched, and that other computers are unaffected (MM8's " +
                "sentence) -- anything less reads as app-wide data loss or as nothing at all",
            "Forget this computer? This phone deletes its pairing keys and cached sessions " +
                "for it. The computer itself is untouched, and other computers are unaffected.",
            MachinesPanelScreen.FORGET_CONFIRM,
        )
    }

    // -------------------------------------------------------------------
    // The resolver's PAIR_ONLY answer is a sentence, not a silence.
    // -------------------------------------------------------------------

    @Test
    fun thePairOnlyAnswerIsASentenceTheUserCanRead() {
        assertEquals(
            "with zero machines the Computers entry must SAY why there is nothing to show; " +
                "bouncing back to Settings wordlessly is the silent no-op shape on the path " +
                "hard rule 5 does not name",
            "No computers are paired yet. Pair this phone with a computer first; Computers " +
                "fills in from the first pairing.",
            MachinesPanelScreen.PAIR_FIRST,
        )
    }

    // -------------------------------------------------------------------
    // The fourth row fact: last successful sync, as an elapsed age.
    // -------------------------------------------------------------------

    @Test
    fun theStatusLineCarriesTheLastSyncAge() {
        val now = 10_000_000L
        val fourMinutes = 4 * 60_000L
        assertEquals(
            "connected, synced 4m ago",
            MachinesPanelScreen.statusLine(row("m-a", lastSyncUnixMs = now - fourMinutes), now),
        )
    }

    @Test
    fun aParkedRowShowsItsAgeBesideItsStaleness() {
        // ADR-018 MM3 / playbook 4.2:200-202: a parked row must VISIBLY show its last-sync age;
        // "stale" alone says what it is, the age says how much it costs.
        val now = 10_000_000_000L
        val threeDays = 3 * 24 * 60 * 60_000L
        assertEquals(
            "stale, synced 3d ago",
            MachinesPanelScreen.statusLine(
                row("m-a", connected = false, lastSyncUnixMs = now - threeDays),
                now,
            ),
        )
    }

    @Test
    fun aRowThatNeverSyncedSaysSoInsteadOfClaimingTheEpoch() {
        // SyncStatus.NEVER's own reasoning one screen over: a zero stamp is "never", because
        // "synced 19700d ago" reports the epoch as a fact about this pairing.
        val line = MachinesPanelScreen.statusLine(row("m-a", lastSyncUnixMs = 0L), 10_000L)
        assertEquals("connected, never synced", line)
    }

    @Test
    fun theNeedsInputCountSurvivesTheAgeJoiningTheLine() {
        val now = 10_000_000L
        assertEquals(
            "all four of playbook 4.2:198's row facts share one sublabel; adding the third " +
                "must not evict the fourth",
            "connected, synced 1m ago, 2 sessions need input",
            MachinesPanelScreen.statusLine(
                row("m-a", lastSyncUnixMs = now - 60_000L, needsInput = 2),
                now,
            ),
        )
    }

    @Test
    fun theOneArgumentStatusLineIsUnchanged() {
        // The round-1 contract stands: reachability plus needs-input, no clock. Existing
        // callers and existing assertions stay exactly as true as they were.
        assertEquals(
            "connected, 1 session needs input",
            MachinesPanelScreen.statusLine(row("m-a", needsInput = 1)),
        )
    }
}
