package dev.swarm.phone.ui.screens

import dev.swarm.phone.ui.InteractionItem
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * FAILING-FIRST (TDD RED, GG-5) for Mirror M2.3 -- the incremental transcript as a MODEL:
 * `TranscriptIncremental.reconcile(old, new)` diffs two reads of the transcript BY item_id
 * (IS-ENV-2's fold rule carried to the screen: never by position) into the minimal mutation
 * list `sessionDetailRedraw` applies -- appends for new ids, in-place rebinds for changed ones,
 * NEVER a full rebuild for a superset read. Plus the two scroll rules the mirror row names:
 * stick-to-bottom only when the reader was at the bottom, and the anchor surviving a burst.
 * (Bead agents-tracker-hggx.7; compile-RED on the frozen symbols.)
 *
 * WHY A MODEL: the defect this row closes (esed) is `sessionDetailRedraw` rebuilding the whole
 * column per event, which re-lays out under the reader's finger at whatever rate the agent
 * works. The interesting half -- WHICH rows change -- is a pure function over two lists and
 * needs no Activity to check; the view-side application keeps its own Robolectric coverage.
 */
class TranscriptIncrementalTest {

    private fun item(id: String, status: String = "completed", text: String = "t", resolved: Boolean = false) =
        InteractionItem(
            sessionId = "m/s1", itemId = id, cursor = id.hashCode().toLong(), kind = "tool_run",
            status = status, text = text, resolved = resolved,
        )

    // ---- reconcile: keyed by item_id ---------------------------------------

    @Test
    fun aPureAppendBurstYieldsOnlyAppends() {
        val old = listOf(item("a"), item("b"))
        val burst = (1..50).map { item("new-$it") }
        val muts = TranscriptIncremental.reconcile(old, old + burst)
        assertEquals(50, muts.filterIsInstance<TranscriptMutation.Append>().size)
        assertTrue(
            "a superset read rebinds nothing: unchanged rows keep their views (and the finger " +
                "on them keeps its scroll)",
            muts.filterIsInstance<TranscriptMutation.Rebind>().isEmpty(),
        )
    }

    @Test
    fun aStatusFlipMutatesInPlaceAndRebuildsNothing() {
        val old = listOf(item("a"), item("run", status = "in_progress"), item("c"))
        val new = listOf(item("a"), item("run", status = "completed"), item("c"))
        val muts = TranscriptIncremental.reconcile(old, new)
        val rebinds = muts.filterIsInstance<TranscriptMutation.Rebind>()
        assertEquals("exactly the flipped row rebinds", 1, rebinds.size)
        assertEquals(1, rebinds[0].index)
        assertEquals("completed", rebinds[0].item.status)
        assertTrue(muts.filterIsInstance<TranscriptMutation.Append>().isEmpty())
    }

    @Test
    fun anApprovalResolutionIsAnInPlaceMutationToo() {
        // IS-LIFE-2's dismissal is a rebind of the card that was answered -- the surrounding
        // conversation must not flinch.
        val old = listOf(item("card", status = "in_progress"), item("after"))
        val new = listOf(item("card", status = "in_progress", resolved = true), item("after"))
        val muts = TranscriptIncremental.reconcile(old, new)
        val rebinds = muts.filterIsInstance<TranscriptMutation.Rebind>()
        assertEquals(1, rebinds.size)
        assertEquals(0, rebinds[0].index)
        assertTrue(rebinds[0].item.resolved)
    }

    @Test
    fun anUnchangedReadYieldsNoMutationsAtAll() {
        val same = listOf(item("a"), item("b"), item("c"))
        assertTrue(
            "redrawing an unchanged transcript is the esed defect itself",
            TranscriptIncremental.reconcile(same, same).isEmpty(),
        )
    }

    @Test
    fun aStreamingTextGrowthRebindsItsOwnRowOnly() {
        val old = listOf(item("a"), item("msg", status = "in_progress", text = "The fix"))
        val new = listOf(item("a"), item("msg", status = "in_progress", text = "The fix is a one-liner"))
        val muts = TranscriptIncremental.reconcile(old, new)
        val rebinds = muts.filterIsInstance<TranscriptMutation.Rebind>()
        assertEquals(1, rebinds.size)
        assertEquals("The fix is a one-liner", rebinds[0].item.text)
    }

    // ---- scroll rules -------------------------------------------------------

    @Test
    fun stickToBottomFollowsOnlyAReaderAlreadyThere() {
        val muts = TranscriptIncremental.reconcile(listOf(item("a")), listOf(item("a"), item("b")))
        assertTrue(
            "at the bottom, the transcript follows the conversation",
            TranscriptIncremental.stickToBottom(atBottom = true, mutations = muts),
        )
        assertFalse(
            "scrolled up, a burst must NOT yank the reader down (mirror M2.3: scroll preserved)",
            TranscriptIncremental.stickToBottom(atBottom = false, mutations = muts),
        )
    }

    @Test
    fun aRebindAloneNeverScrolls() {
        val muts = TranscriptIncremental.reconcile(
            listOf(item("run", status = "in_progress")),
            listOf(item("run", status = "completed")),
        )
        assertFalse(
            "a status flip is not new conversation; following it would jump on every pulse",
            TranscriptIncremental.stickToBottom(atBottom = true, mutations = muts),
        )
    }

    @Test
    fun theAnchorSurvivesABurstByItemIdNotByIndex() {
        val old = listOf(item("a"), item("anchor"), item("c"))
        val new = listOf(item("a"), item("anchor"), item("c")) + (1..30).map { item("burst-$it") }
        assertEquals(
            "the reader's row is found again by id; position is what the burst changed",
            1, TranscriptIncremental.anchorIndex(new, "anchor"),
        )
        assertEquals(-1, TranscriptIncremental.anchorIndex(old, "no-such-row"))
    }
}
