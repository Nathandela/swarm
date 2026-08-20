package dev.swarm.phone.ui.screens

import dev.swarm.phone.ui.InteractionItem
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * FAILING-FIRST (TDD RED, GG-5) for Wave R6 review ROUND 2's two transcript-model blockers.
 * Bead agents-tracker-hggx.7, Mirror M2.4 / M3.1.
 *
 * ## 1. THE PHONE SENT THE CLOSED TURN'S ID, so every ordinary send was refused
 *
 * `latestTurnId` was `items.lastOrNull()?.turnId`, and IS-ENV-1 makes that the WRONG fact once
 * a turn ends: the daemon's `turnIDLocked` reads the open turn, STAMPS it onto the terminal
 * `agent_message`, and THEN deletes it. So after a turn closes the last item still carries
 * turn A while the daemon's current turn is the empty string -- and `composerSend` refuses
 * `stale_turn` unless `expected_turn` equals the current one. protocol.md states the other
 * half in as many words: "an idle session is matched by the EMPTY expected_turn".
 *
 * The consequence was the ORDINARY path, not an edge: the agent finishes, you read the answer,
 * you reply from the phone -- refused, every time. Re-reading the transcript (the refusal's own
 * stated remedy) could not change the answer, because nothing in the retained items ever
 * produces the empty value once a turn has closed, and no turn-END item exists to reset it
 * (`session_status` is emitted only at SESSION end).
 *
 * So this model mirrors IS-ENV-1's rule rather than sampling the last row: a turn OPENS on a
 * `user_message` and CLOSES on any terminal `agent_message`, and what a send is drawn against
 * is the turn that is OPEN -- empty when none is.
 *
 * ## 2. `before_item` COULD BE A SYNTHETIC GAP ID, which the daemon can never match
 *
 * `applyStructuredGapLocked` mints `structured_gap:<ts>` and folds the tear as a FIRST-CLASS
 * element (finding B4's own fix), and `oldestItemId` took the first element whatever it was.
 * On the daemon `historyItemID` answers "" for every non-`interaction` record, so the boundary
 * scan cannot match a gap id and `interactionHistory` refuses `invalid_field`. Reachable
 * whenever the oldest element this phone holds is a tear -- a reseed floor (IS-CAP-4) cutting
 * just before a proven gap -- and PERMANENT once reached, because only a successful page could
 * change which element is oldest.
 *
 * ## 3. And the control stops being offered when the PHONE can hold no more
 *
 * Distinct from the machine's floor and never collapsed into it: the floor says "nothing older
 * is retained", capacity says "there is more and this handset cannot hold it". A screen that
 * showed the floor's silence for both would tell the reader they had reached the beginning of
 * a conversation that goes further back.
 */
class TranscriptTurnAndAnchorTest {

    private val session = "mbp/quanthome"

    private fun item(
        id: String,
        kind: String,
        turn: String = "",
        status: String = "",
    ) = InteractionItem(
        sessionId = session, itemId = id, cursor = 1, kind = kind,
        status = status, turnId = turn, text = "words",
    )

    // ---- 1. the turn a send is drawn against ---------------------------------

    @Test
    fun `a closed turn is not offered as the turn a send is drawn against`() {
        val panel = TranscriptScreen.of(
            listOf(
                item("u1", "user_message", turn = "turn-a"),
                item("a1", "agent_message", turn = "turn-a", status = "completed"),
            ),
        )
        assertEquals(
            "the phone named the turn the daemon had just CLOSED, so composer_send is refused " +
                "stale_turn on every idle session -- which is the ordinary path, not an edge",
            "",
            panel.latestTurnId,
        )
    }

    @Test
    fun `an open turn is named while the agent is still working`() {
        val panel = TranscriptScreen.of(
            listOf(
                item("u1", "user_message", turn = "turn-a"),
                item("a1", "agent_message", turn = "turn-a", status = "in_progress"),
            ),
        )
        assertEquals("turn-a", panel.latestTurnId)
    }

    @Test
    fun `a newer user message reopens the turn a send is drawn against`() {
        val panel = TranscriptScreen.of(
            listOf(
                item("u1", "user_message", turn = "turn-a"),
                item("a1", "agent_message", turn = "turn-a", status = "completed"),
                item("u2", "user_message", turn = "turn-b"),
                item("t1", "tool_run", turn = "turn-b", status = "in_progress"),
            ),
        )
        assertEquals(
            "IS-ENV-1: a user_message OPENS a turn and every item inside it carries that turn",
            "turn-b",
            panel.latestTurnId,
        )
    }

    @Test
    fun `an item outside any turn names no turn`() {
        val panel = TranscriptScreen.of(listOf(item("s1", "session_status")))
        assertEquals("", panel.latestTurnId)
    }

    // ---- 2. what "load earlier" pages before ---------------------------------

    @Test
    fun `the load-earlier anchor is never the synthetic id of a tear`() {
        val panel = TranscriptScreen.of(
            listOf(
                item("structured_gap:2026-08-19T12:00:00Z", "structured_gap"),
                item("a1", "agent_message", turn = "turn-a", status = "completed"),
            ),
        )
        assertEquals(
            "the phone offered the daemon an item id no producer ever minted, so every page " +
                "is refused invalid_field and the control refuses forever",
            "a1",
            panel.oldestItemId,
        )
        assertTrue(panel.offersLoadEarlier)
    }

    @Test
    fun `a phone holding only a tear has nothing it can page before`() {
        val panel = TranscriptScreen.of(
            listOf(item("structured_gap:2026-08-19T12:00:00Z", "structured_gap")),
        )
        assertEquals("", panel.oldestItemId)
        assertFalse(
            "a control with no id to name asks the machine for a page it will refuse",
            panel.offersLoadEarlier,
        )
    }

    // ---- 3. the phone's own end of the conversation --------------------------

    @Test
    fun `the control goes once this phone can hold no more of the conversation`() {
        val items = listOf(item("a1", "agent_message", turn = "turn-a", status = "completed"))
        assertTrue(TranscriptScreen.of(items).offersLoadEarlier)
        assertFalse(
            "the phone refused the machine's page whole because it can hold no more, and the " +
                "screen went on offering a control that does nothing",
            TranscriptScreen.of(items, atCapacity = true).offersLoadEarlier,
        )
    }
}
