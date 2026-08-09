package dev.swarm.phone.ui.screens

import android.content.Context
import android.view.View
import android.view.ViewGroup
import android.widget.FrameLayout
import android.widget.TextView
import androidx.test.core.app.ApplicationProvider
import dev.swarm.phone.theme.SwarmTheme
import dev.swarm.phone.ui.InteractionItem
import dev.swarm.phone.ui.SessionDetail
import dev.swarm.phone.ui.SessionLease
import dev.swarm.phone.ui.kit.KitTag
import dev.swarm.phone.ui.kit.kitRequire
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertSame
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner

/**
 * agents-tracker-ksvb.3: the screen a streaming agent redraws.
 *
 * **WHAT WAS WRONG, AND IT IS A RATE RATHER THAN A RENDERING.** `PhoneSurface.drawDetail` guards on
 * whole-panel equality -- "redraw only when the panel has changed" -- and the panel CONTAINS the
 * thing a working agent changes on every event. `render()` runs on every event, so the guard was
 * false on every frame and the entire screen was destroyed and rebuilt at the agent's output rate:
 * the header, the notices, the conversation, `[Take control]`, Stop and Kill, every one of them a
 * new view.
 *
 * A rebuilt `TextView` re-measures and re-runs its own antialiasing from scratch, so the screen
 * shimmered while it streamed, which is exactly when a person is looking at it. It also throws away
 * everything a live view holds: the selection, the accessibility focus, the scroll position.
 *
 * **THE FIX IS THE NARROWEST ONE THAT CAN WORK, AND ITS NARROWNESS IS THE ASSERTION.** When two
 * panels differ in the CONVERSATION ALONE, the transcript subtree is recomposed into the container
 * that is already on screen and nothing else is touched. When they differ in anything else -- a
 * lease that changed, a notice that appeared, a session that was renamed -- the screen is rebuilt
 * the way it always was. A redraw that tried to patch more would be a second, contradictable
 * statement of what is on screen, which is the defect PB-DS-9 fences the screen package against.
 *
 * ## THE SUBJECT MOVED WITH THE TRAFFIC; IT DID NOT DIE WITH THE GRID
 *
 * This suite stood over the daemon-rendered terminal grid: two screens (`peekPanelRedraw` and
 * `sessionDetailRedraw`), and the difference each accepted was the SNAPSHOT.
 * `docs/adr/ADR-009-structured-chat-interaction.md` (1)/(3) deletes the well, both peek screens and
 * `TerminalPeek` -- "no terminal emulation and no raw grid anywhere in the app" -- and the
 * transcript is what a working agent now changes on every item, contained by the panel in exactly
 * the same way. So every case below is the same case on the surface that replaced its subject:
 * `peekPanelRedraw`'s two tests fold into these, because the peek's own chrome (row 22's note and
 * its `[Take control]`) was re-homed onto this screen with the deletion, and this screen is where
 * they can still be asserted.
 *
 * **WHAT RETIRED, AND IT IS EXACTLY ONE ASSERTION.** The peek refused to patch a terminal that had
 * been RESIZED -- "the grid's row count is the well's own floor, so a resize changes the card's
 * height and not just its text". A conversation has no `cols x rows`; ADR-009 (3) deleted the last
 * screen that carried them, and `SessionDetail.snapshotRows` with it. There is no such difference
 * left to refuse, so the case is gone rather than restated over a field that does not exist. Its
 * negative control is not lost: the three refusals below cover a change of chrome (the lease), a
 * change of height (a notice appearing) and a change of copy (the title).
 *
 * **WHAT THE PATCH CAN NO LONGER PROMISE, recorded rather than quietly dropped.** The peek's
 * positive case asserted the well was the SAME `TextView` afterwards -- an 11.5 sp mono grid that
 * neither re-measured nor re-antialiased, because a grid is a string and the patch assigned `.text`
 * to it. A conversation is a LIST, so there is no `.text` to set and the rows are recomposed by
 * design (`sessionDetailRedraw`'s own KDoc says so). What survives that difference is what the
 * defect was actually about: the header, the notices and all three control slots are left exactly
 * where the finger last saw them, and those are asserted below by identity.
 *
 * WHY THE DECISION LIVES HERE AND NOT IN `PhoneSurface`. That class needs `swarmmobile.App`, a
 * gomobile AAR over .so files cross-compiled for Android ABIs, so on this JVM every one of its
 * render paths past `PhoneRuntime.phone()` is unreachable (the argument is
 * `PhoneSurfaceNavigationTest`'s, in full). The part that can be got wrong is WHICH CHANGES may be
 * patched and whether the patch keeps the views -- both pure -- so that part is a function of a host
 * and two panels, and the surface calls it.
 */
@RunWith(RobolectricTestRunner::class)
class StreamingRedrawTest {

    private val context: Context
        get() = SwarmTheme.applyTo(ApplicationProvider.getApplicationContext())

    private val session = "mbp/quanthome"

    private val firstItem = "Running the test suite."

    private val secondItem = "internal/attach passed in 1.24s."

    /** The conversation as the wire delivers it: one folded item per thing the agent said. */
    private fun conversationOf(vararg lines: String): TranscriptPanel = TranscriptScreen.of(
        lines.mapIndexed { index, line ->
            InteractionItem(
                sessionId = session,
                itemId = "i-$index",
                cursor = (index + 1).toLong(),
                kind = "agent_message",
                text = line,
            )
        },
    )

    /** A conversation the machine is blocked on, for the tap the patch has to carry through. */
    private fun conversationAwaitingApproval(): TranscriptPanel = TranscriptScreen.of(
        listOf(
            InteractionItem(
                sessionId = session,
                itemId = "i-approve-1",
                cursor = 2,
                kind = "approval_request",
                body = """
                    {"summary":"Push the release commit to main?",
                     "action":{"command":"git push origin main"},
                     "decisions":[{"id":"accept","label":"Allow"}]}
                """.trimIndent(),
            ),
        ),
    )

    /**
     * The panel, with the lease UNHELD by default so that `[Take control]` is actually drawn.
     *
     * IT IS THE MOST EXPENSIVE SLOT TO REBUILD, which is why it is the default state here: the
     * surface owns that button and `sessionDetailView` re-parents it on every composition, so a
     * rebuild at output rate re-attaches it under the finger about to press it.
     */
    private fun detailPanel(
        transcript: TranscriptPanel = conversationOf(firstItem),
        leaseHeld: Boolean = false,
        journalStale: Boolean = false,
        title: String = "api refactor",
    ): SessionDetailPanel {
        val detail = SessionDetail(
            sessionId = session,
            leaseHeld = leaseHeld,
            online = true,
            journalStale = journalStale,
            title = title,
        )
        return SessionDetailScreen.of(
            detail,
            transcript,
            SessionLease(sessionId = session, leaseHeld = leaseHeld, online = true),
        )
    }

    /** The surface's host: one view, holding whatever the last full draw composed. */
    private fun host(
        panel: SessionDetailPanel,
        onApproval: ((String) -> Unit)? = null,
    ): FrameLayout = FrameLayout(context).apply {
        addView(
            sessionDetailView(
                context = context,
                panel = panel,
                takeControl = TextView(context),
                release = TextView(context),
                stop = TextView(context),
                kill = TextView(context),
                resync = TextView(context),
                acknowledge = TextView(context),
                composer = TextView(context),
                outcome = "",
                onBack = {},
                onApproval = onApproval,
            ),
        )
    }

    private fun View.allTagged(tag: String): List<View> {
        val found = mutableListOf<View>()
        fun walk(v: View) {
            if (v.tag == tag) found += v
            if (v is ViewGroup) for (i in 0 until v.childCount) walk(v.getChildAt(i))
        }
        walk(this)
        return found
    }

    /** What the conversation says on screen, in the order it is drawn. */
    private fun spokenIn(root: View): List<String> = root.allTagged(TranscriptTag.BLOCK)
        .map { (it.kitRequire(KitTag.ACTIVITY_BODY) as TextView).text.toString() }

    @Test
    fun `a new item reaches the conversation without rebuilding the screen`() {
        val drawn = detailPanel()
        val host = host(drawn)
        val transcript = host.kitRequire(DetailTag.TRANSCRIPT)
        val nav = host.kitRequire(DetailTag.NAV)
        val lease = host.kitRequire(DetailTag.LEASE)
        val takeControl = host.kitRequire(DetailTag.TAKE_CONTROL)
        val stop = host.kitRequire(DetailTag.STOP)
        val kill = host.kitRequire(DetailTag.KILL)

        assertTrue(
            "a detail differing only in its conversation was refused, so the header, the notices " +
                "and all three controls are rebuilt at the rate the agent writes",
            sessionDetailRedraw(host, drawn, detailPanel(conversationOf(firstItem, secondItem))),
        )

        assertEquals(
            "the new item did not reach the screen, so the patch reported a redraw it did not do",
            listOf(firstItem, secondItem),
            spokenIn(host),
        )
        assertSame(
            "the conversation is composed into a NEW container, so the transcript's own place on " +
                "the screen moves under whatever is reading it",
            transcript,
            host.kitRequire(DetailTag.TRANSCRIPT),
        )
        assertSame(
            "the header was rebuilt for a conversation that changed under it",
            nav,
            host.kitRequire(DetailTag.NAV),
        )
        assertSame(
            "row 22's lease sentence was rebuilt for a conversation that changed above it",
            lease,
            host.kitRequire(DetailTag.LEASE),
        )
        assertSame(
            "the Take control button was re-parented at the agent's output rate, which moves it " +
                "under the finger about to press it",
            takeControl,
            host.kitRequire(DetailTag.TAKE_CONTROL),
        )
        assertSame(
            "the Stop control was re-parented",
            stop,
            host.kitRequire(DetailTag.STOP),
        )
        assertSame(
            "the Kill control was re-parented",
            kill,
            host.kitRequire(DetailTag.KILL),
        )
    }

    @Test
    fun `the detail refuses to patch anything but the conversation`() {
        val drawn = detailPanel()
        val host = host(drawn)
        val next = conversationOf(firstItem, secondItem)

        assertFalse(
            "a detail whose LEASE changed -- which is what Stop reads and what decides whether " +
                "Take control is offered at all -- was patched in place, so the controls would go " +
                "on saying what they said before the machine answered",
            sessionDetailRedraw(host, drawn, detailPanel(next, leaseHeld = true)),
        )
        assertFalse(
            "a detail that GAINED A NOTICE was patched in place, so PB-APP-8's sentence about a " +
                "hole in the log never reaches the screen and the transcript goes on reading as " +
                "complete",
            sessionDetailRedraw(host, drawn, detailPanel(next, journalStale = true)),
        )
        assertFalse(
            "a detail whose session was RENAMED was patched in place. The name is drawn by the " +
                "header (agents-tracker-ksvb.1), which is precisely the part this patch does not " +
                "touch, so the screen would go on calling the session what it used to be called",
            sessionDetailRedraw(host, drawn, detailPanel(next, title = "release cut")),
        )
        assertFalse(
            "a detail was patched against nothing, so the first draw of a session would recompose " +
                "a conversation into a screen that was never composed",
            sessionDetailRedraw(host, null, detailPanel(next)),
        )

        assertEquals(
            "a refusal touched the screen anyway. `false` means the caller must rebuild, and a " +
                "host that was half-patched on the way to saying so shows one panel's " +
                "conversation inside another panel's chrome",
            listOf(firstItem),
            spokenIn(host),
        )
    }

    /**
     * The tap the patch has to carry, and the one thing a recomposition can silently drop.
     *
     * `sessionDetailRedraw` takes `onApproval` and passes it through unchanged BECAUSE the rows are
     * rebuilt: the blocks that arrive in a patch are new views, and the listener is what makes one
     * of them the way to the sheet. A patch that dropped it would draw the thing the machine is
     * blocked on as a row that does nothing -- the dead-chevron defect (agents-tracker-2yb), on the
     * one path where an approval is most likely to arrive, which is while the agent is streaming.
     */
    @Test
    fun `an approval arriving in a patch is still answerable`() {
        var opened: String? = null
        val drawn = detailPanel()
        val host = host(drawn)

        assertTrue(
            sessionDetailRedraw(
                host,
                drawn,
                detailPanel(conversationAwaitingApproval()),
                onApproval = { opened = it },
            ),
        )
        host.kitRequire(TranscriptTag.APPROVAL).performClick()

        assertEquals(
            "the patch recomposed the approval block without its listener, so the card the " +
                "machine is waiting on draws and does nothing",
            "i-approve-1",
            opened,
        )
    }
}
