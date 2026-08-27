package dev.swarm.phone.ui

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotEquals
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * FAILING-FIRST (TDD RED, GG-5) for chat-surface-plan row 0.6 -- **the refusal the whole of
 * Slice 0 exists to produce could not be said on screen.**
 *
 * WHAT SLICE 0 BUILT. `schema.CodeInputBusy` (`internal/protocol/schema/chat.go`) is the shim
 * refusing a `composer_send` rather than joining the reader's words to someone's half-typed
 * line: somebody has written to this session's PTY since the last submit, so the text would
 * merge with whatever is already on the input line and the carriage return would submit the
 * concatenation. It claims nothing about the CLI's input region -- ADR-017:175's
 * `expected_input_revision` would require characterising one, and `skeleton/chat.go` rightly
 * refuses to guess -- only the strictly weaker fact the shim owns absolutely, because the shim
 * holds the PTY's only serialised writer. A refusal writes NOTHING.
 *
 * WHERE IT DIED. `input_busy` existed in exactly ONE place in the tree: that Go constant. It was
 * absent from [MachineRefusalCodes.toToken], so [ErrorRouter.routeMachineCode] answered
 * [ErrorState.UNKNOWN] for it -- and `SessionDetailPanel.composerVerdictFor` keys the composer's
 * notice on `routed.state.name`, so the user read the generic *"Your message was refused and
 * not delivered."* The daemon had said precisely why, in a sentence the drawing had already
 * tabled, and the screen replaced it with a shrug. That is the exact defect
 * [MachineRefusalCodes.toToken]'s own KDoc records for `unavailable` and `invalid_field` one
 * round earlier, arriving from the other direction: there, a verb-specific fact was routed
 * through a generic remedy; here, a fact the copy sheet HAS a sentence for was not routed at all.
 *
 * WHY IT EARNS A ROW AND `unavailable` DOES NOT. That table's rule is that a fact about ONE verb
 * belongs to the screen's own sentence plus the machine's words. `input_busy` IS about one verb
 * -- and so is `stale_turn`, which has had its own row since Wave R6 for the reason that decides
 * this one: the drawing tables a sentence for it as a STATE OF THE SENT BUBBLE (`bubble.refused`,
 * beside `bubble.stale`), which makes it a rendered state of the composer and not a notice under
 * a control. Two states of one bubble, two rows.
 *
 * THE THIRD ASSERTION IS THE ONE THE COMMITTEE ASKED FOR. Three different stale-turn sentences
 * were shipping across the app and the drawing's `bubble.stale` row -- captioned *"shipped copy,
 * kept"* -- matched none of them. This file pins the router's to the tabled one; `ui/kit/
 * Composer.kt` holds the other two and converges separately.
 */
class ErrorRoutingRefusalCopyTest {

    /** The drawing's `bubble.refused` row, verbatim. */
    private val refusedCopy = "Not sent — the terminal's input line was not empty."

    /** The drawing's `bubble.stale` row, verbatim. */
    private val staleCopy =
        "Not sent — the conversation moved on. Read the latest turn and send again."

    @Test
    fun `the code this app translates is the daemons own spelling`() {
        assertEquals(
            "the literal must be `schema.CodeInputBusy` verbatim. Nothing translates between the " +
                "two vocabularies, so a drifted spelling is not a compile error -- it is a " +
                "refusal that silently falls to the reserved row forever",
            "input_busy",
            MachineRefusalCodes.INPUT_BUSY,
        )
    }

    @Test
    fun `an input-busy refusal is its own state and not the apps shrug`() {
        val routed = ErrorRouter.routeMachineCode(MachineRefusalCodes.INPUT_BUSY)
        assertNotEquals(
            "`input_busy` still falls to the reserved row, so the composer keys its notice on " +
                "UNKNOWN and the user reads \"Your message was refused and not delivered\" -- " +
                "the app's shrug in place of the one refusal Slice 0 was built to produce",
            ErrorState.UNKNOWN,
            routed.state,
        )
        assertEquals(ErrorState.INPUT_BUSY, routed.state)
    }

    @Test
    fun `it says the drawings sentence and no other`() {
        assertEquals(
            "the routed sentence is not the drawing's `bubble.refused` row. A string not on the " +
                "copy sheet is not on the screen",
            refusedCopy,
            ErrorRouter.routeMachineCode(MachineRefusalCodes.INPUT_BUSY).message,
        )
    }

    @Test
    fun `the sentence names the input line and never the reader`() {
        val copy = ErrorRouter.routeMachineCode(MachineRefusalCodes.INPUT_BUSY).message
        assertTrue(
            "the sentence must open by saying the message did not go. A refusal that leads with " +
                "the cause reads as an explanation of a delivery that happened",
            copy.startsWith("Not sent"),
        )
        assertFalse(
            "the sentence blames the reader for a line somebody else was typing on. The shim " +
                "refused rather than merge, which is the app working, not the user erring",
            copy.contains("you", ignoreCase = true),
        )
    }

    @Test
    fun `waiting is the remedy, because the line clears without the reader doing anything`() {
        val routed = ErrorRouter.routeMachineCode(MachineRefusalCodes.INPUT_BUSY)
        assertEquals(
            "the remedy must be the one that is true: the draft is fine and there is nothing to " +
                "re-read, so REFRESH is stale_turn's answer to a different question. The input " +
                "line empties when whoever is typing submits or clears it, and sending again " +
                "then works -- which is WAIT_AND_RETRY exactly",
            Remedy.WAIT_AND_RETRY,
            routed.remedy,
        )
        assertTrue(
            "a refusal the reader can act on must not read as one they cannot",
            routed.remedy.actionableHere,
        )
        assertFalse(
            "nothing about a busy input line is a pairing problem",
            routed.offersPairing,
        )
    }

    @Test
    fun `the two composer refusals stay tellable apart`() {
        val busy = ErrorRouter.routeMachineCode(MachineRefusalCodes.INPUT_BUSY)
        val stale = ErrorRouter.routeMachineCode(MachineRefusalCodes.STALE_TURN)
        assertNotEquals(
            "two states that read identically are one state. The reader's next move differs: " +
                "one waits for a line to clear, the other re-reads a turn that moved on",
            busy.state,
            stale.state,
        )
        assertNotEquals(busy.message, stale.message)
    }

    @Test
    fun `the routers stale-turn sentence is the drawings tabled one`() {
        assertEquals(
            "the router ships a third stale-turn wording. The drawing's `bubble.stale` row is " +
                "captioned \"shipped copy, kept\" and is the one string this state may say",
            staleCopy,
            ErrorRouter.routeMachineCode(MachineRefusalCodes.STALE_TURN).message,
        )
        assertEquals(ErrorState.STALE_TURN, ErrorRouter.routeMachineCode("stale_turn").state)
    }

    /**
     * The reserved row survives the addition, which is the property the table's own KDoc argues
     * for at length: a code this build has never seen is not a network problem, and a
     * plausible-looking neighbour would tell the user to wait out a fault waiting cannot fix.
     */
    @Test
    fun `a code this build has never seen is still UNKNOWN`() {
        assertEquals(ErrorState.UNKNOWN, ErrorRouter.routeMachineCode("some_future_code").state)
        assertEquals(ErrorState.UNKNOWN, ErrorRouter.routeMachineCode("").state)
    }

    /**
     * `unavailable` and `invalid_field` are NAMED in [MachineRefusalCodes] and deliberately absent
     * from its map (round 3, finding F4). Adding a third row is not licence to add theirs: their
     * absence is an instruction to the screen, and `SessionDetailScreen` carries out that
     * instruction today.
     */
    @Test
    fun `the deliberate absences stay absent`() {
        for (code in listOf(MachineRefusalCodes.UNAVAILABLE, MachineRefusalCodes.INVALID_FIELD)) {
            assertEquals(
                "`$code` is routed through the generic table again, which replaces the " +
                    "verb-specific sentence the screen already authors with a generic remedy -- " +
                    "the defect finding F4 recorded",
                ErrorState.UNKNOWN,
                ErrorRouter.routeMachineCode(code).state,
            )
        }
    }
}
