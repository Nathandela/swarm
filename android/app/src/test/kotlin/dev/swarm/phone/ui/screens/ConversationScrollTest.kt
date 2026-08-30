package dev.swarm.phone.ui.screens

import android.content.Context
import android.view.View
import android.view.ViewGroup
import android.widget.FrameLayout
import android.widget.LinearLayout
import android.widget.ScrollView
import androidx.test.core.app.ApplicationProvider
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner

/**
 * FAILING-FIRST (TDD RED, GG-5) for the owner's ORIGINAL complaint, which six lanes of
 * chat-surface work rebuilt the screen around and never answered. Beads: agents-tracker-tu7z
 * (H.1, P0) and agents-tracker-jz0z (H.2, P1). Plan: docs/specifications/chat-surface-plan.md §14.
 *
 * **NO TEST IN THIS APP ASSERTED A SCROLL POSITION UNTIL THIS FILE, AND THAT WAS THE DEFECT'S
 * WHOLE COVER.** The wave closed on 1,570 green Kotlin tests over a conversation that opened on
 * its FIRST message and stayed there: [conversationScaffoldView] handed its content a
 * brand-new `ScrollView` at
 * `scrollY = 0`, the transcript is oldest-at-top, and the full-rebuild path sets no position. It
 * never recovered either -- `SessionDetailView`'s stick-to-bottom is gated on
 * `listIsScrolledToBottom`, which is FALSE the moment a reader is parked at the top, so every
 * append landed below the fold with no scroll. Every one of those 1,570 assertions was about a
 * view's presence, its text or its arrangement, and not one of them could see this. So the
 * assertions here are about `scrollY` and nothing else.
 *
 * **WHY THE ASSERTIONS ARE MADE OVER LAID-OUT VIEWS AND NOT OVER A FIELD.** A `ScrollView` cannot
 * scroll before it has been measured and laid out: `ScrollView.scrollTo` clamps against its own
 * viewport height and its child's height, and both are zero until a layout pass has run. A test
 * that built the scaffold and read `scrollY` immediately would pass against a fix that does
 * nothing on a handset, which is exactly the class of green this file exists to stop being. So
 * every test here measures and lays the scaffold out at a fixed window size first, and reads the
 * offset the layout actually produced.
 *
 * **WHAT THIS FILE CANNOT REACH.** `PhoneSurface.drawScaffold` -- the remember-and-restore either
 * side of the rebuild -- because `PhoneRuntime.phone()` answers `PhoneStartup.Unavailable` on
 * every JVM run, so no session row exists and no drill-down can be opened from an
 * `ActivityScenario`. `PhoneSurfaceConversationHostTest` records the same bound. What IS reached
 * is the seam: `a rebuild lands the reader where they left` performs, by hand, the exact sequence
 * `drawScaffold` performs -- read the offset off the scroll that is about to be discarded, detach
 * the content host, build the next scaffold with that offset -- so the arithmetic and the
 * layout-ordering are proved here and only the two guards around them are owed a gate line.
 */
@RunWith(RobolectricTestRunner::class)
class ConversationScrollTest {

    private val context = ApplicationProvider.getApplicationContext<Context>()

    // A handset-shaped window with a header above the scroll, so the viewport is strictly
    // SHORTER than the window. It is not decoration: "the bottom" is `content - viewport`, and a
    // fixture whose viewport was the whole window -- or zero -- would let an implementation that
    // confused the two pass anyway.
    private val windowWidthPx = 1080
    private val windowHeightPx = 1920
    private val headerHeightPx = 160
    private val rowHeightPx = 300

    // -----------------------------------------------------------------------
    // H.1 -- a conversation opens at its NEWEST message.
    // -----------------------------------------------------------------------

    @Test
    fun `a conversation opens at its newest message`() {
        val content = contentHost(column(rows = 10))
        val root = scaffold(content)
        layOut(root)

        val scroll = root.scroll()
        assertTrue(
            "the transcript is not taller than the viewport, so this test is not asking the " +
                "question it was written to ask",
            content.height > scroll.height,
        )
        assertEquals(
            "the conversation opened at scrollY=${scroll.scrollY} out of a possible " +
                "${bottomOf(scroll)}: the transcript is oldest-at-top, so a reader who opens a " +
                "session lands on its FIRST messages and the agent's latest work is below the " +
                "fold. This is agents-tracker-tu7z, and it is the complaint the owner made " +
                "before any of this surface was built",
            bottomOf(scroll),
            scroll.scrollY,
        )
    }

    /**
     * The SECOND half of tu7z, and the half that explains "never recovers".
     *
     * `SessionDetailView.patchConversation` only follows the agent when `listIsScrolledToBottom`
     * answered true BEFORE the burst -- and that predicate is `scrollY + viewport >= content - 4`
     * against the nearest scrolling ancestor. A conversation opened at the top answers false on
     * its first append and on every append after it, so the stick-to-bottom that the whole
     * incremental patch path was built around is disarmed for the life of the session. The
     * predicate is re-stated here rather than called because it is private to that file, which
     * another lane owns; the arithmetic is copied deliberately and the slack is its
     * `SCROLL_BOTTOM_SLACK_PX`.
     */
    @Test
    fun `an opened conversation is armed for stick-to-bottom`() {
        val content = contentHost(column(rows = 10))
        val root = scaffold(content)
        layOut(root)

        val scroll = root.scroll()
        assertTrue(
            "listIsScrolledToBottom answers false on a freshly opened conversation " +
                "(scrollY=${scroll.scrollY} + viewport=${scroll.height} < " +
                "content=${content.height} - 4), so TranscriptIncremental.stickToBottom will " +
                "refuse to follow the agent on this session's first append and on every one " +
                "after it. The screen does not merely open in the wrong place; it stays there",
            scroll.scrollY + scroll.height >= content.height - 4,
        )
    }

    /**
     * THE REAL ORDERING, and the reason the anchor cannot be a one-shot fired at construction.
     *
     * `PhoneSurface.renderReady` calls `drawContent` before `drawScaffold`, so on the draw that
     * OPENS a session the content host is already full -- but `drawScaffold` also runs on a draw
     * where it is not, and the anchor has to survive being asked too early and answer on the
     * layout that has something in it.
     *
     * **AN EMPTY CONTENT HOST DOES NOT MEASURE TO ZERO**, which is the trap this test is really
     * set for. `isFillViewport` re-measures a child shorter than the viewport to EXACTLY the
     * viewport, so an anchor armed on "a height at all" is spent on the empty pass, disarms, and
     * leaves the transcript that arrives afterwards sitting at its oldest message -- tu7z again,
     * with one frame of delay in front of it and a test suite that says it was fixed.
     */
    @Test
    fun `content that arrives after the scaffold is built still opens at the newest message`() {
        val content = contentHost()
        val root = scaffold(content)
        layOut(root)

        content.addView(column(rows = 10))
        layOut(root)

        val scroll = root.scroll()
        assertEquals(
            "the scaffold was laid out once over an empty content host and gave up: a " +
                "conversation whose transcript arrives on a later pass opens at its oldest " +
                "message, which is tu7z with one frame of delay in front of it",
            bottomOf(scroll),
            scroll.scrollY,
        )
    }

    // -----------------------------------------------------------------------
    // H.2 -- the scroll survives a scaffold rebuild.
    // -----------------------------------------------------------------------

    /**
     * agents-tracker-jz0z, over the exact sequence `PhoneSurface.drawScaffold` performs.
     *
     * `ScaffoldKey` includes `literal` and `composer`, so opening an R8 output screen or an R9
     * diff and coming back rebuilds the scaffold, as does a `composerIsBar` flip when a session
     * loses its message sink. Each rebuild used to hand the reader a brand-new `ScrollView` at
     * zero -- and `PhoneSurface` asserted the opposite in as many words, which is why this test
     * carries the sequence rather than a claim about it.
     */
    @Test
    fun `a rebuild lands the reader where they left`() {
        val content = contentHost(column(rows = 10))
        val first = scaffold(content)
        layOut(first)

        // The reader scrolls back up to re-read something.
        val where = bottomOf(first.scroll()) / 2
        first.scroll().scrollTo(0, where)
        assertEquals("the fixture failed to move the reader", where, first.scroll().scrollY)

        // `drawScaffold`, in order: read the offset off the scroll that is about to be
        // discarded, detach every long-lived host from it, build the next composition.
        val remembered = (content.parent as ScrollView).scrollY
        (content.parent as ViewGroup).removeView(content)
        val second = scaffold(content, scrollY = remembered)
        layOut(second)

        assertEquals(
            "the reader came back to scrollY=${second.scroll().scrollY} instead of $where. " +
                "conversationScaffoldView builds a fresh ScrollView per call and the offset " +
                "lived on the one just discarded, so opening a tool's output and coming back " +
                "dumps the reader at the top of the transcript -- and PhoneSurface's own comment " +
                "claimed contentHost kept it, which a FrameLayout cannot do",
            where,
            second.scroll().scrollY,
        )
    }

    /**
     * A remembered offset is an offset into a conversation that may have MOVED under it.
     *
     * A page of history prepended past the retention bound, or an item the machine replaced,
     * changes the content height between the two scaffolds. The restore must land inside the
     * conversation rather than out of range.
     */
    @Test
    fun `a remembered offset past the end of a shortened conversation lands at the end`() {
        val content = contentHost(column(rows = 8))
        val root = scaffold(content, scrollY = 50_000)
        layOut(root)

        val scroll = root.scroll()
        assertTrue(
            "the fixture's transcript fits the screen, so its bottom IS zero and this test " +
                "would pass without restoring anything at all -- the vacuous green H.5 is on " +
                "the ledger for",
            content.height > scroll.height,
        )
        assertEquals(
            "restoring an offset from a longer conversation put the reader at " +
                "${scroll.scrollY}, past the end of the one on screen",
            bottomOf(scroll),
            scroll.scrollY,
        )
    }

    // -----------------------------------------------------------------------
    // Controls. Both of these PASS BEFORE THE FIX AS WELL AS AFTER IT, deliberately and stated
    // out loud: they are here to catch the two ways a fix for the tests above goes wrong, not to
    // demonstrate the defect. A control that cannot fail red is only worth having if it is
    // labelled as one -- H.5 is on this repo's ledger precisely because unlabelled ones were not.
    // -----------------------------------------------------------------------

    /**
     * A transcript that fits the screen has no bottom that differs from its top, and the anchor
     * stays armed rather than being spent on a no-op -- which is also what makes the previous
     * test's late arrival reachable.
     */
    @Test
    fun `a conversation shorter than the screen does not scroll`() {
        val content = contentHost(column(rows = 2))
        val root = scaffold(content)
        layOut(root)

        val scroll = root.scroll()
        assertTrue(
            "the fixture's transcript is taller than the viewport, so this control is not " +
                "asking its question",
            content.height <= scroll.height,
        )
        assertEquals(
            "a two-message session was scrolled somewhere, which on a transcript that fits the " +
                "screen can only be off the top of itself",
            0,
            scroll.scrollY,
        )
    }

    /**
     * THE NEGATIVE HALF OF H.1, and the reason the anchor is spent once rather than on every
     * layout. "Scroll to the bottom whenever the content changes height" passes every test above
     * and yanks a reader who has scrolled up to re-read something down to the newest message the
     * instant their agent writes a line -- which is the defect M2.3's stick-to-bottom predicate
     * was written to avoid, re-introduced one level below it.
     */
    @Test
    fun `growing the transcript does not yank a reader who has scrolled up`() {
        val transcript = column(rows = 10)
        val content = contentHost(transcript)
        val root = scaffold(content)
        layOut(root)

        val scroll = root.scroll()
        val where = bottomOf(scroll) / 3
        scroll.scrollTo(0, where)

        repeat(5) { transcript.addView(row()) }
        layOut(root)

        assertEquals(
            "the reader was pulled to ${scroll.scrollY} by an append they did not ask for. The " +
                "scaffold's anchor is the OPENING position; following the agent afterwards is " +
                "TranscriptIncremental.stickToBottom's decision, and it declines for a reader " +
                "who has scrolled away",
            where,
            scroll.scrollY,
        )
    }

    // -----------------------------------------------------------------------
    // The fixture. A transcript is a vertical column of fixed-height rows inside the surface's
    // own content host, which is what `PhoneSurface` hands the scaffold: a `FrameLayout` whose
    // child is replaced per draw. The heights are explicit because `ScrollView` measures its
    // child with an UNSPECIFIED height spec -- a view that answered WRAP_CONTENT would measure
    // to nothing and `isFillViewport` would then stretch it to exactly one screen, and a
    // transcript that fits the screen cannot be opened at the wrong end.
    // -----------------------------------------------------------------------

    private fun scaffold(content: View, scrollY: Int? = null): View = conversationScaffoldView(
        context = context,
        header = View(context).apply {
            layoutParams = LinearLayout.LayoutParams(MATCH, headerHeightPx)
        },
        content = content,
        composer = null,
        status = null,
        scrollY = scrollY,
    )

    private fun contentHost(child: View? = null): FrameLayout =
        FrameLayout(context).apply { child?.let { addView(it) } }

    private fun column(rows: Int): LinearLayout = LinearLayout(context).apply {
        orientation = LinearLayout.VERTICAL
        repeat(rows) { addView(row()) }
    }

    private fun row(): View = View(context).apply {
        layoutParams = LinearLayout.LayoutParams(MATCH, rowHeightPx)
    }

    private fun layOut(root: View) {
        root.measure(
            View.MeasureSpec.makeMeasureSpec(windowWidthPx, View.MeasureSpec.EXACTLY),
            View.MeasureSpec.makeMeasureSpec(windowHeightPx, View.MeasureSpec.EXACTLY),
        )
        root.layout(0, 0, windowWidthPx, windowHeightPx)
    }

    /** The scaffold's scroll, by [ScaffoldTag.CONTENT] and never by child index. */
    private fun View.scroll(): ScrollView {
        val found = find(ScaffoldTag.CONTENT)
        assertTrue("the scaffold has no content region at all", found is ScrollView)
        return found as ScrollView
    }

    /**
     * The offset at which the last message is on screen: what "the newest message" means in px.
     *
     * Use the child's laid-out bottom rather than its height. The conversation viewport owns a
     * top reading inset, so its child starts below y=0 and that inset is part of the scrollable
     * extent. Subtracting only `child.height - viewport.height` is therefore one inset short of
     * the real ScrollView clamp and mistakes the correctly anchored position for overscroll.
     */
    private fun bottomOf(scroll: ScrollView): Int =
        maxOf(0, (scroll.getChildAt(0)?.bottom ?: 0) + scroll.paddingBottom - scroll.height)

    private fun View.find(tag: String): View? {
        if (this.tag == tag) return this
        if (this is ViewGroup) {
            for (i in 0 until childCount) getChildAt(i).find(tag)?.let { return it }
        }
        return null
    }

    private companion object {
        const val MATCH = ViewGroup.LayoutParams.MATCH_PARENT
    }
}
