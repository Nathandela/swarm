package dev.swarm.phone.ui.screens

import dev.swarm.phone.ui.InteractionItem
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * FAILING-FIRST (TDD RED, GG-5) for Wave R6 review finding B12 -- the incremental transcript
 * model CANNOT EXPRESS WHAT M3.1 NEEDS. Bead agents-tracker-hggx.7.
 *
 * Two holes, and both are the same shape: the mutation list is not a complete description of
 * the difference between two reads, so a redraw that applies it faithfully still ends up
 * showing something neither read contained.
 *
 *  1. **[TranscriptMutation.Append] carried no position while Rebind did.** "Append" is the
 *     wrong verb for what `load earlier` (M3.1/ADR-014) produces: a page of history arrives at
 *     the FRONT of the list. A redraw handed a positionless append can only add at the end, so
 *     the reader's "load earlier" tap put a week-old exchange BELOW the message they were
 *     reading -- a conversation reordered by the phone, which is the one thing a chat surface
 *     cannot survive (`TranscriptScreen`'s own words about the wire's order).
 *
 *  2. **No removal existed.** A read that no longer holds an item -- the head trim
 *     `phonecore.MaxItemsPerSession` performs on every insert past the bound -- left the row on
 *     screen forever, because nothing in the mutation list says it went. The transcript then
 *     shows an item the phone does not hold, above rows it does, and no later read can dislodge
 *     it.
 *
 * AND THE SCROLL RULE FOLLOWS FROM (1): sticking to the bottom is right for new conversation
 * arriving at the END and wrong for history arriving at the FRONT. Before positions existed the
 * predicate could not tell those apart, so "load earlier" scrolled the reader away from the
 * history they had just asked for.
 */
class TranscriptIncrementalPositionTest {

    private fun item(id: String, text: String = "t") = InteractionItem(
        sessionId = "m/s1", itemId = id, cursor = id.hashCode().toLong(),
        kind = "agent_message", text = text,
    )

    // ---- (1) an insertion carries where it goes -----------------------------

    @Test
    fun `a load-earlier page inserts at the front and says so`() {
        val onScreen = listOf(item("live-1"), item("live-2"))
        val withHistory = listOf(item("old-1"), item("old-2")) + onScreen

        val inserts = TranscriptIncremental.reconcile(onScreen, withHistory)
            .filterIsInstance<TranscriptMutation.Append>()

        assertEquals(2, inserts.size)
        assertEquals(
            "the page ADR-014 pages backwards has no position on it, so a redraw can only put " +
                "a week-old exchange below the message the reader was reading",
            listOf(0, 1),
            inserts.map { it.index },
        )
        assertEquals(listOf("old-1", "old-2"), inserts.map { it.item.itemId })
    }

    @Test
    fun `a live item still inserts at the end`() {
        val inserts = TranscriptIncremental.reconcile(
            listOf(item("a"), item("b")),
            listOf(item("a"), item("b"), item("c")),
        ).filterIsInstance<TranscriptMutation.Append>()

        assertEquals(listOf(2), inserts.map { it.index })
    }

    // ---- (2) an item the read no longer holds leaves the screen -------------

    @Test
    fun `a head trim emits a removal for the row that went`() {
        val before = listOf(item("evicted"), item("kept-1"), item("kept-2"))
        val after = listOf(item("kept-1"), item("kept-2"))

        val muts = TranscriptIncremental.reconcile(before, after)
        val removals = muts.filterIsInstance<TranscriptMutation.Remove>()

        assertEquals(
            "the retention bound trimmed the head and the mutation list said nothing, so the " +
                "row stays on screen over a transcript that no longer holds it",
            listOf("evicted"),
            removals.map { it.itemId },
        )
        assertTrue(
            "a pure trim must not also rebind the survivors: their views (and the finger on " +
                "them) are exactly what the incremental redraw exists to keep",
            muts.filterIsInstance<TranscriptMutation.Rebind>().isEmpty(),
        )
    }

    @Test
    fun `an unchanged read still yields nothing at all`() {
        // The negative control for the removal arm: a diff that started emitting removals for
        // every read would rebuild the column, which is the esed defect wearing a new name.
        val same = listOf(item("a"), item("b"))
        assertTrue(TranscriptIncremental.reconcile(same, same).isEmpty())
    }

    // ---- the scroll rule that follows ---------------------------------------

    @Test
    fun `history arriving at the front never scrolls the reader to the bottom`() {
        val muts = TranscriptIncremental.reconcile(
            listOf(item("live-1")),
            listOf(item("old-1"), item("live-1")),
        )
        assertFalse(
            "the reader asked for older messages and was thrown to the newest one. Only new " +
                "conversation at the END is something to follow",
            TranscriptIncremental.stickToBottom(atBottom = true, mutations = muts),
        )
    }

    @Test
    fun `new conversation at the end still follows a reader who was at the bottom`() {
        val muts = TranscriptIncremental.reconcile(
            listOf(item("a")),
            listOf(item("a"), item("b")),
        )
        assertTrue(TranscriptIncremental.stickToBottom(atBottom = true, mutations = muts))
        assertFalse(TranscriptIncremental.stickToBottom(atBottom = false, mutations = muts))
    }
}
