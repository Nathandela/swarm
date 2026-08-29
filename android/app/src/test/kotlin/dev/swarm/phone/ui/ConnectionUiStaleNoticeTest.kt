package dev.swarm.phone.ui

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * FAILING-FIRST (TDD RED, GG-5) for the conversation drawing's `sync` row -- **a stale stream
 * was borrowing the tear's word, and the tear's word is a claim about a POSITION.**
 *
 * THE TWO FACTS ARE NOT THE SAME FACT, and the drawing separates them on purpose. `gap` is a
 * PROVEN, POSITIONED tear in the record: the phone knows a hole exists and knows where it sits,
 * which is why its drawing is a rule across the flow with a word on it and a repair beside it.
 * `sync` is a STALE STREAM: the events this phone holds may be behind the machine's, and that
 * is all. The sheet's own note is the ruling -- *"it must not borrow the tear's drawing: a stale
 * stream has no position, so it cannot claim one."*
 *
 * THE DEFECT THIS PINS. [StreamView.notice] shipped *"The journal view has a gap and may be
 * missing events."* for [StreamBadge.STALE] -- a sentence that ASSERTS the tear as fact and then
 * hedges only its consequence. That is strictly stronger than staleness supports, and it is
 * backwards: the certain half is the part the phone cannot prove, and the hedged half is the
 * part it can. A reader is told a hole exists, given no position for it, and offered no repair
 * -- which is the shape of a fault report the app cannot substantiate.
 *
 * WHY THE STREAM NAME LEAVES THE SENTENCE. It is not a loss: [StreamView.stream] still carries
 * it, `SettingsPanel`'s health line still names the unhealthy channels from that field, and
 * PB-APP-8's per-stream discipline is a property of the MODEL rather than of this string. What
 * the drawing tables is one sentence for the state, and a string not on that sheet is not on the
 * screen.
 */
class ConnectionUiStaleNoticeTest {

    /** The drawing's `sync` row, verbatim. */
    private val tabled =
        "Some updates may be missing."

    @Test
    fun `a stale stream says what the drawing tables and nothing stronger`() {
        val stale = StreamView(stream = "journal", stale = true, resyncPending = false)
        assertEquals(StreamBadge.STALE, stale.badge)
        assertEquals(
            "the stale notice is not the drawing's `sync` sentence. A string not on the copy " +
                "sheet is not on the screen, and this is the one state whose sentence the " +
                "committee found overclaiming",
            tabled,
            stale.notice,
        )
    }

    @Test
    fun `a stale stream never claims a tear it cannot position`() {
        val stale = StreamView(stream = "journal", stale = true, resyncPending = false)
        assertFalse(
            "the stale notice spends the word `gap`, which is the TEAR's word. A gap is proven " +
                "and positioned; staleness is neither, so a sentence that asserts one is a claim " +
                "the phone cannot back -- and it offers the reader nothing to do about the hole " +
                "it just reported",
            stale.notice.contains("gap", ignoreCase = true),
        )
    }


    /**
     * The rest of PB-APP-8's per-stream model is unchanged by the copy, and this says so rather
     * than leaving it to be assumed: LIVE stays silent, a repair in flight is still its own
     * badge, and PB-SYNC-3's rule that the stale mark clears when a repair LANDS still holds.
     */
    @Test
    fun `the badge model is untouched by the sentence`() {
        val live = StreamView(stream = "reply", stale = false, resyncPending = false)
        assertEquals(StreamBadge.LIVE, live.badge)
        assertTrue("a live stream has nothing to say", live.notice.isBlank())

        val repairing = StreamView(stream = "journal", stale = true, resyncPending = true)
        assertEquals(StreamBadge.RESYNCING, repairing.badge)
        assertTrue(
            "PB-SYNC-3: the stale mark clears when the repair LANDS, never when it is asked for",
            repairing.stale,
        )
        assertTrue("a repair in flight is still worth a line", repairing.notice.isNotBlank())
    }
}
