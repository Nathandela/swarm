package dev.swarm.phone.ui.screens

import android.content.Context
import android.view.View
import android.widget.FrameLayout
import android.widget.TextView
import androidx.test.core.app.ApplicationProvider
import dev.swarm.phone.theme.SwarmTheme
import dev.swarm.phone.ui.SessionDetail
import dev.swarm.phone.ui.TerminalPeek
import dev.swarm.phone.ui.kit.kitRequire
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertSame
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner

/**
 * FAILING-FIRST (TDD RED, GG-5) for agents-tracker-ksvb.3: the screen a streaming agent redraws.
 *
 * **WHAT WAS WRONG, AND IT IS A RATE RATHER THAN A RENDERING.** `PhoneSurface.drawPeek` and
 * `drawDetail` guard on whole-panel equality -- "redraw only when the panel has changed" -- and
 * both panels CONTAIN THE SNAPSHOT. `render()` runs on every journal event, and an agent writing
 * to its terminal produces those steadily, so the guard was false on every frame and the entire
 * screen was destroyed and rebuilt at output rate: the header, the notices, the well, the note,
 * `[Take control]` and the lease sentence, every one of them a new view.
 *
 * That is the third dancing-type cause and the most literal. A rebuilt `TextView` re-measures, and
 * a re-measured line of 11.5 sp mono re-runs its own antialiasing from scratch -- so the grid
 * shimmered while it streamed, which is exactly when a person is looking at it. It also throws
 * away everything a live view holds: the selection, the accessibility focus, the scroll position
 * of the well.
 *
 * **THE FIX IS THE NARROWEST ONE THAT CAN WORK, AND ITS NARROWNESS IS THE ASSERTION.** When two
 * panels differ in the SNAPSHOT ALONE, the text is assigned to the well that is already on screen
 * and nothing else is touched. When they differ in anything else -- a lease that changed, a notice
 * that appeared, a grid that was resized, so the well's own floor moved -- the screen is rebuilt
 * the way it always was. A redraw that tried to patch more would be a second, contradictable
 * statement of what is on screen, which is the defect PB-DS-9 fences the screen package against.
 *
 * WHY THE DECISION LIVES HERE AND NOT IN `PhoneSurface`. That class needs `swarmmobile.App`, a
 * gomobile AAR over .so files cross-compiled for Android ABIs, so on this JVM every one of its
 * render paths past `PhoneRuntime.phone()` is unreachable and `drawPeek` cannot be called at all
 * (the argument is `PhoneSurfaceNavigationTest`'s, in full). The part that can be got wrong is
 * WHICH CHANGES may be patched and whether the patch keeps the view -- both pure -- so that part
 * is a function of a host and two panels, and the surface calls it.
 */
@RunWith(RobolectricTestRunner::class)
class StreamingRedrawTest {

    private val context: Context
        get() = SwarmTheme.applyTo(ApplicationProvider.getApplicationContext())

    private val firstFrame = "$ go test ./..."

    private val secondFrame = "$ go test ./...\nok  internal/attach  1.24s"

    private fun peekPanel(text: String = firstFrame, rows: Int = 24): PeekPanel =
        PeekPanelScreen.of(
            TerminalPeek(
                sessionId = "mbp/quanthome",
                text = text,
                cols = 80,
                rows = rows,
                stale = false,
                leaseHeld = true,
                online = true,
            ),
        )

    private fun detailPanel(text: String = firstFrame, rows: Int = 24): SessionDetailPanel =
        SessionDetailScreen.of(
            SessionDetail(
                sessionId = "mbp/quanthome",
                journal = emptyList(),
                snapshotText = text,
                leaseHeld = true,
                online = true,
                journalStale = false,
                snapshotRows = rows,
            ),
        )

    /** The surface's host: one view, holding whatever the last full draw composed. */
    private fun host(child: View): FrameLayout =
        FrameLayout(context).apply { addView(child) }

    private fun peekHost(panel: PeekPanel): FrameLayout =
        host(peekPanelView(context, panel, View(context)))

    private fun detailHost(panel: SessionDetailPanel): FrameLayout =
        host(sessionDetailView(context, panel, View(context), View(context), "") {})

    // ---- the peek ----------------------------------------------------------

    @Test
    fun `a new grid reaches the peek's well without rebuilding the screen`() {
        val drawn = peekPanel()
        val host = peekHost(drawn)
        val well = host.kitRequire(PeekTag.WELL)
        val nav = host.kitRequire(PeekTag.NAV)
        val note = host.kitRequire(PeekTag.NOTE)

        assertTrue(
            "a peek differing only in its grid was refused, so the whole screen is rebuilt at " +
                "the rate the agent writes",
            peekPanelRedraw(host, drawn, peekPanel(text = secondFrame)),
        )

        assertSame(
            "the well is a NEW TextView, so an 11.5 sp mono grid re-measures and re-antialiases " +
                "itself on every frame the agent produces",
            well,
            host.kitRequire(PeekTag.WELL),
        )
        assertSame("the header was rebuilt for a grid that changed under it", nav, host.kitRequire(PeekTag.NAV))
        assertSame("row 22's note was rebuilt for a grid that changed above it", note, host.kitRequire(PeekTag.NOTE))
        assertEquals(secondFrame, (well as TextView).text.toString())
    }

    @Test
    fun `the peek refuses to patch anything but the grid`() {
        val drawn = peekPanel()
        val host = peekHost(drawn)

        assertFalse(
            "a peek whose lease sentence changed was patched in place, so the screen now shows " +
                "one panel's grid inside another panel's chrome",
            peekPanelRedraw(host, drawn, PeekPanelScreen.of(
                TerminalPeek(
                    sessionId = "mbp/quanthome", text = secondFrame, cols = 80, rows = 24,
                    stale = false, leaseHeld = false, online = true,
                ),
            )),
        )
        assertFalse(
            "a peek whose terminal was RESIZED was patched in place. The grid's row count is the " +
                "well's own floor, so a resize changes the card's height and not just its text",
            peekPanelRedraw(host, drawn, peekPanel(text = secondFrame, rows = 40)),
        )
        assertFalse(
            "a peek was patched against nothing, so the first draw of a session would assign " +
                "text into a screen that was never composed",
            peekPanelRedraw(host, null, peekPanel(text = secondFrame)),
        )
    }

    // ---- the drill-down ----------------------------------------------------

    @Test
    fun `a new grid reaches the detail's card without rebuilding the screen`() {
        val drawn = detailPanel()
        val host = detailHost(drawn)
        val card = host.kitRequire(DetailTag.SNAPSHOT)
        val nav = host.kitRequire(DetailTag.NAV)
        val stop = host.kitRequire(DetailTag.STOP)

        assertTrue(
            "a session detail differing only in its grid was refused, so the transcript, the " +
                "controls and the header are rebuilt at the rate the agent writes",
            sessionDetailRedraw(host, drawn, detailPanel(text = secondFrame)),
        )

        assertSame("the snapshot card is a new TextView", card, host.kitRequire(DetailTag.SNAPSHOT))
        assertSame("the header was rebuilt", nav, host.kitRequire(DetailTag.NAV))
        assertSame("the Stop control was re-parented", stop, host.kitRequire(DetailTag.STOP))
        assertEquals(secondFrame, (card as TextView).text.toString())
    }

    @Test
    fun `the detail refuses to patch anything but the grid`() {
        val drawn = detailPanel()
        val host = detailHost(drawn)

        assertFalse(
            "a detail whose lease changed -- which is what the Stop control READS -- was patched " +
                "in place, so the button would go on saying what it said before the machine answered",
            sessionDetailRedraw(host, drawn, SessionDetailScreen.of(
                SessionDetail(
                    sessionId = "mbp/quanthome", journal = emptyList(), snapshotText = secondFrame,
                    leaseHeld = false, online = true, journalStale = false, snapshotRows = 24,
                ),
            )),
        )
        assertFalse(
            "a detail whose terminal was resized was patched in place",
            sessionDetailRedraw(host, drawn, detailPanel(text = secondFrame, rows = 40)),
        )
        assertFalse(
            "a detail was patched against nothing",
            sessionDetailRedraw(host, null, detailPanel(text = secondFrame)),
        )
    }
}
