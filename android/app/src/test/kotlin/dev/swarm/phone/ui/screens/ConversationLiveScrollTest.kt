package dev.swarm.phone.ui.screens

import android.content.Context
import android.view.View
import android.view.ViewGroup
import android.widget.FrameLayout
import android.widget.LinearLayout
import android.widget.ScrollView
import android.widget.TextView
import androidx.test.core.app.ApplicationProvider
import dev.swarm.phone.theme.SwarmTheme
import dev.swarm.phone.ui.InteractionItem
import dev.swarm.phone.ui.SessionDetail
import dev.swarm.phone.ui.SessionLease
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner

/**
 * FAILING-FIRST for the live-message scroll regression reported on a handset.
 *
 * [TranscriptIncremental.stickToBottom] already states the right policy, but its model tests do
 * not prove that the Android view spends that decision at a usable time. The tail view is inserted
 * before it has been measured or laid out. Asking that zero-sized view to reveal its rectangle can
 * leave the old offset in place (or move toward the new view's temporary origin), so after the
 * layout pass the reader is above the latest message even though they were following the chat.
 *
 * These tests use the real conversation scaffold, real transcript rows, and a completed layout
 * pass on both sides of [sessionDetailRedraw]. Their subject is the viewport, not the pure policy:
 * following readers remain bottom-anchored, while readers who deliberately moved up keep their
 * exact offset.
 */
@RunWith(RobolectricTestRunner::class)
class ConversationLiveScrollTest {

    private val context: Context
        get() = SwarmTheme.applyTo(ApplicationProvider.getApplicationContext())

    private val widthPx = 1080
    private val heightPx = 900
    private val session = "mbp/live-scroll"

    @Test
    fun `an incoming message keeps a following reader anchored to the bottom`() {
        val drawn = panel((1..18).map(::item))
        val fixture = fixture(drawn)
        fixture.layOut()

        assertTrue("the fixture must have a scrollable transcript", fixture.bottom() > 0)
        assertEquals("the fixture did not begin at the newest message", fixture.bottom(), fixture.scroll.scrollY)

        val next = panel((1..19).map(::item))
        assertTrue(sessionDetailRedraw(fixture.detailHost, drawn, next))
        fixture.layOut()

        assertEquals(
            "the reader was following the conversation, but the incoming message left the " +
                "viewport at scrollY=${fixture.scroll.scrollY} while the new bottom is " +
                "${fixture.bottom()}",
            fixture.bottom(),
            fixture.scroll.scrollY,
        )
    }

    @Test
    fun `an incoming message does not hijack a reader who intentionally scrolled up`() {
        val drawn = panel((1..18).map(::item))
        val fixture = fixture(drawn)
        fixture.layOut()

        val readingOffset = fixture.bottom() / 3
        fixture.scroll.scrollTo(0, readingOffset)
        assertEquals("the fixture failed to move the reader away from the bottom", readingOffset, fixture.scroll.scrollY)

        val next = panel((1..19).map(::item))
        assertTrue(sessionDetailRedraw(fixture.detailHost, drawn, next))
        fixture.layOut()

        assertEquals(
            "an incoming message yanked a reader away from the older passage they were reading",
            readingOffset,
            fixture.scroll.scrollY,
        )
    }

    @Test
    fun `a streaming final message keeps a following reader anchored to the bottom`() {
        val beforeItems = (1..17).map(::item) + item(18, "Starting the explanation.")
        val drawn = panel(beforeItems)
        val fixture = fixture(drawn)
        fixture.layOut()

        assertEquals("the fixture did not begin at the newest message", fixture.bottom(), fixture.scroll.scrollY)
        val oldBottom = fixture.bottom()

        val streamed = item(
            18,
            "Starting the explanation.\n" +
                "This is the same agent message receiving streamed text, not a second item.\n".repeat(24),
        )
        val next = panel(beforeItems.dropLast(1) + streamed)
        assertTrue(sessionDetailRedraw(fixture.detailHost, drawn, next))
        fixture.layOut()

        assertTrue(
            "the streamed fixture did not grow, so it cannot prove bottom anchoring",
            fixture.bottom() > oldBottom,
        )
        assertEquals(
            "the final agent message grew while the reader was following it, but the viewport " +
                "stayed at scrollY=${fixture.scroll.scrollY} while the streamed bottom moved to " +
                "${fixture.bottom()}",
            fixture.bottom(),
            fixture.scroll.scrollY,
        )
    }

    @Test
    fun `streaming final message growth does not hijack a reader who scrolled up`() {
        val beforeItems = (1..17).map(::item) + item(18, "Starting the explanation.")
        val drawn = panel(beforeItems)
        val fixture = fixture(drawn)
        fixture.layOut()

        val readingOffset = fixture.bottom() / 3
        fixture.scroll.scrollTo(0, readingOffset)
        assertEquals("the fixture failed to move the reader away from the bottom", readingOffset, fixture.scroll.scrollY)

        val streamed = item(
            18,
            "Starting the explanation.\n" +
                "This is the same agent message receiving streamed text, not a second item.\n".repeat(24),
        )
        val next = panel(beforeItems.dropLast(1) + streamed)
        assertTrue(sessionDetailRedraw(fixture.detailHost, drawn, next))
        fixture.layOut()

        assertEquals(
            "streaming text pulled the reader away from the older passage they were reading",
            readingOffset,
            fixture.scroll.scrollY,
        )
    }

    @Test
    fun `a deferred tail follow is cancelled when the reader scrolls up before layout`() {
        val drawn = panel((1..18).map(::item))
        val fixture = fixture(drawn)
        fixture.layOut()

        assertEquals("the fixture did not begin at the newest message", fixture.bottom(), fixture.scroll.scrollY)
        val next = panel((1..19).map(::item))
        assertTrue(sessionDetailRedraw(fixture.detailHost, drawn, next))

        // The new row has not been laid out yet. A touch/scroll event can run in this window,
        // before the next traversal spends the deferred follow decision.
        val readingOffset = fixture.scroll.scrollY / 3
        fixture.scroll.scrollTo(0, readingOffset)
        assertEquals("the fixture failed to model the reader scrolling during the deferred window", readingOffset, fixture.scroll.scrollY)
        fixture.layOut()

        assertEquals(
            "the deferred layout listener overrode a newer reader scroll",
            readingOffset,
            fixture.scroll.scrollY,
        )
    }

    @Test
    fun `front history arriving with streamed tail growth preserves the history anchor`() {
        // Begin with a genuinely scrollable transcript. A short first draw intentionally keeps
        // the scaffold's open-at-newest anchor armed until content grows, which is a separate
        // policy and would make this mixed-update regression ambiguous.
        val beforeItems = (1..17).map(::item) + item(18, "Starting the explanation.")
        val drawn = panel(beforeItems)
        val fixture = fixture(drawn)
        fixture.layOut()
        assertTrue("the fixture must have a scrollable transcript", fixture.bottom() > 0)
        assertEquals("the fixture did not begin at the newest message", fixture.bottom(), fixture.scroll.scrollY)
        val historyAnchor = fixture.scroll.scrollY

        val streamed = item(
            18,
            "Starting the explanation.\n" +
                "This is the same agent message receiving streamed text, not a second item.\n".repeat(24),
        )
        // Retire one held row in the same bounded-window read so the tail keeps its old index.
        // Without the explicit front-insert exclusion, that stable index makes the simultaneous
        // tail rebind look exactly like an ordinary streaming-only redraw and forces the bottom.
        val next = panel(
            listOf(item(0, "An older history message.")) +
                beforeItems.drop(1).dropLast(1) +
                streamed,
        )
        val mutations = TranscriptIncremental.reconcileBlocks(drawn.transcript.blocks, next.transcript.blocks)
        assertTrue(
            "the fixture must combine a front insertion with final-message growth",
            mutations.any { it is BlockMutation.Insert && !it.tail } &&
                mutations.any { it is BlockMutation.Rebind && it.index == next.transcript.blocks.lastIndex },
        )
        assertTrue(sessionDetailRedraw(fixture.detailHost, drawn, next))
        fixture.layOut()

        assertTrue("the combined update did not grow beyond the old bottom", fixture.bottom() > historyAnchor)
        assertEquals(
            "front-loaded history was discarded by a simultaneous tail-growth follow; " +
                "the old anchor was $historyAnchor and the new bottom is ${fixture.bottom()}",
            historyAnchor,
            fixture.scroll.scrollY,
        )
    }

    private fun item(
        number: Int,
        text: String = "Agent update $number: the implementation is still progressing normally.",
    ) = InteractionItem(
        sessionId = session,
        itemId = "message-$number",
        cursor = number.toLong(),
        kind = "agent_message",
        status = "completed",
        text = text,
        turnId = "turn-a",
    )

    private fun panel(items: List<InteractionItem>): SessionDetailPanel = SessionDetailScreen.of(
        SessionDetail(
            sessionId = session,
            online = true,
            journalStale = false,
            title = "live scroll",
        ),
        TranscriptScreen.of(items),
        SessionLease(sessionId = session, online = true),
        capabilities = SessionCapabilityFacts(structuredChat = true),
    )

    private fun fixture(panel: SessionDetailPanel): Fixture {
        val detailHost = FrameLayout(context).apply {
            addView(
                sessionDetailView(
                    context = context,
                    panel = panel,
                    resync = TextView(context),
                    acknowledge = TextView(context),
                    approval = TextView(context),
                    outcome = "",
                ),
            )
        }
        val root = conversationScaffoldView(
            context = context,
            header = View(context).apply {
                layoutParams = LinearLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, 80)
            },
            content = detailHost,
            composer = null,
            status = null,
        )
        val scroll = requireNotNull(root.find(ScaffoldTag.CONTENT) as? ScrollView)
        return Fixture(root, detailHost, scroll)
    }

    private inner class Fixture(
        val root: View,
        val detailHost: FrameLayout,
        val scroll: ScrollView,
    ) {
        fun layOut() {
            root.measure(
                View.MeasureSpec.makeMeasureSpec(widthPx, View.MeasureSpec.EXACTLY),
                View.MeasureSpec.makeMeasureSpec(heightPx, View.MeasureSpec.EXACTLY),
            )
            root.layout(0, 0, widthPx, heightPx)
        }

        fun bottom(): Int = maxOf(
            0,
            (scroll.getChildAt(0)?.bottom ?: 0) + scroll.paddingBottom - scroll.height,
        )
    }

    private fun View.find(tag: String): View? {
        if (this.tag == tag) return this
        if (this is ViewGroup) {
            for (index in 0 until childCount) getChildAt(index).find(tag)?.let { return it }
        }
        return null
    }
}
