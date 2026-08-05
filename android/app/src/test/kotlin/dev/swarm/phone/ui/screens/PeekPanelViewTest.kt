package dev.swarm.phone.ui.screens

import android.content.Context
import android.view.View
import android.view.ViewGroup
import android.widget.TextView
import androidx.test.core.app.ApplicationProvider
import dev.swarm.phone.theme.SwarmTheme
import dev.swarm.phone.ui.TerminalPeek
import dev.swarm.phone.ui.kit.KitTag
import dev.swarm.phone.ui.kit.kitFind
import dev.swarm.phone.ui.kit.kitRequire
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertSame
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner

/**
 * FAILING-FIRST (TDD RED, GG-5) for PB-DS-6 and PB-DS-9 over the terminal peek AS DRAWN.
 *
 * WHAT THIS ASKS THAT `PeekPanelScreenTest` CANNOT. That suite asks what the screen SAYS; this
 * asks whether it is on screen -- which component renders it, in what order, and whether the
 * control the surface supplied is composed only while the model offers it. "The model is beautiful
 * and nothing renders it" is the defect PB-DS-6 was recorded NOT MET over, and PB-INPUT-2 was
 * recorded against a surface whose Take control button looked identical in both lease states.
 *
 * WHAT IS DELIBERATELY NOT ASSERTED: appearance. The drill-down header's metrics, the mono well's
 * phosphor ink and the read-only note's type are PB-DS-10's and are asserted in `ui/kit`;
 * repeating them here would be a second opinion that can disagree with the first.
 */
@RunWith(RobolectricTestRunner::class)
class PeekPanelViewTest {

    private val context: Context
        get() = SwarmTheme.applyTo(ApplicationProvider.getApplicationContext())

    private fun peek(
        session: String = "mbp/quanthome",
        text: String = "$ go test ./...",
        cols: Int = 80,
        rows: Int = 24,
        stale: Boolean = false,
        leaseHeld: Boolean = false,
        online: Boolean = true,
    ) = TerminalPeek(
        sessionId = session,
        text = text,
        cols = cols,
        rows = rows,
        stale = stale,
        leaseHeld = leaseHeld,
        online = online,
    )

    /** A stand-in for the control `PhoneSurface` owns: the verb, the touch filter, the lease op. */
    private fun stubControl(): View = View(context)

    private fun view(
        panel: PeekPanel,
        takeControl: View = stubControl(),
        below: View? = null,
    ): View = peekPanelView(context, panel, takeControl, below)

    private fun View.allTagged(tag: String): List<View> {
        val found = mutableListOf<View>()
        fun walk(v: View) {
            if (v.tag == tag) found += v
            if (v is ViewGroup) for (i in 0 until v.childCount) walk(v.getChildAt(i))
        }
        walk(this)
        return found
    }

    private fun textOf(view: View?): String = (view as? TextView)?.text?.toString().orEmpty()

    // ---- the composition ---------------------------------------------------

    @Test
    fun `the peek is composed of the kit components C3 names`() {
        val root = view(PeekPanelScreen.of(peek()))

        assertNotNull("C3.1 -- the screen has no header", root.kitFind(PeekTag.NAV))
        assertNotNull("C3.2 -- the snapshot has no well", root.kitFind(PeekTag.WELL))
        assertNotNull("C3.3 -- row 22's note is not drawn", root.kitFind(PeekTag.NOTE))
        assertNotNull("PB-INPUT-2 -- the lease sentence is not drawn", root.kitFind(PeekTag.LEASE))
    }

    /**
     * THE BACK CONTROL IS GONE, and agents-tracker-joe7 is why. This asserted it was drawn:
     *
     *     assertEquals("Inbox", textOf(nav.kitRequire(KitTag.DRILL_BACK)))
     *
     * -- a full 48 dp target with a focus ring, a chevron and a destination label, and no listener
     * behind any of it, on the Inbox tab of every paired phone with a session. The omission was
     * documented rather than hidden (`peekPanelView`'s own KDoc: "no listener is attached, because
     * a back control that scrolled somewhere would be a navigation this product has not designed"),
     * and a documented dead control is still a dead control: the visually identical chevron on the
     * session detail IS wired, two screens apart. There is nowhere for this one to go -- the peek
     * is composed UNDER the inbox list rather than pushed over it -- so the affordance is not drawn.
     *
     * THE HEADER ITSELF STAYS §4's. The title is still `Title.Sheet` on the drill header rather
     * than the root header's 27 sp `.pnav .big`, which is what the last assertion has always said
     * and is unchanged: this screen sits below a root screen whether or not it can go back.
     */
    @Test
    fun `the header is the drill-down header, and it offers no back control`() {
        val panel = PeekPanelScreen.of(peek(session = "mbp/quanthome", cols = 120, rows = 40))
        val nav = view(panel).kitRequire(PeekTag.NAV)

        assertNull(
            "the peek draws a back control -- 48 dp, focusable, chevron and destination -- with " +
                "no navigation behind it. A control that looks like a control and does not act is " +
                "worse than no control: the user learns the screen is broken",
            nav.kitFind(KitTag.DRILL_BACK),
        )
        assertEquals("mbp/quanthome · 120x40", textOf(nav.kitRequire(KitTag.DRILL_TITLE)))
        assertNull(
            "the ROOT header's display title was drawn on a drill-down screen. `.pnav .big` is " +
                "27 sp; §4 gives this screen a 15.5 sp `Title.Sheet`",
            nav.kitFind(KitTag.TITLE),
        )
    }

    @Test
    fun `the composition is in C3's order`() {
        val root = view(PeekPanelScreen.of(peek()))
        val order = mutableListOf<String>()
        fun walk(v: View) {
            (v.tag as? String)?.let { if (it in PeekTag.COMPOSITION) order += it }
            if (v is ViewGroup) for (i in 0 until v.childCount) walk(v.getChildAt(i))
        }
        walk(root)

        // Row 22 puts the button DIRECTLY under the note it was promoted out of, and the lease
        // sentence follows because it is about the keyboard rather than about the snapshot.
        assertEquals(
            listOf(PeekTag.NAV, PeekTag.WELL, PeekTag.NOTE, PeekTag.TAKE_CONTROL, PeekTag.LEASE),
            order,
        )
    }

    // ---- what the well carries ---------------------------------------------

    /**
     * THE BANNER HAS LEFT THE WELL, and agents-tracker-0qe7 is why. This asserted the opposite:
     *
     *     assertTrue(
     *         "the well holds the grid and not the banner, so a snapshot the phone knows is out of " +
     *             "date reads as live:\n$well",
     *         well.lines().size > 1,
     *     )
     *
     * -- that a one-line stale grid renders as two lines in the well, the extra one being the
     * warning. The well is the one surface in this app that prints the machine's output byte for
     * byte (ADR-007 D2 keeps the VT emulator on the machine for exactly that reason), so a sentence
     * of English inside it is indistinguishable from something the agent typed. The subject is
     * unchanged and is asserted harder: the snapshot must still be marked, and the well must still
     * hold only the grid.
     */
    @Test
    fun `the stale banner is a notice above the well, not a line inside the grid`() {
        val grid = "$ go test ./..."
        val stale = PeekPanelScreen.of(peek(text = grid, stale = true))
        val root = view(stale)
        val well = textOf(root.kitRequire(PeekTag.WELL))

        assertEquals(
            "the well holds a sentence about the view as well as the view, printed in the " +
                "machine's own register where a reader cannot tell it from output:\n$well",
            grid,
            well,
        )
        assertEquals(
            "a snapshot the phone knows is out of date is drawn with no mark at all, so it reads " +
                "as live",
            stale.staleNotice,
            textOf(root.kitFind(PeekTag.STALE)),
        )

        val order = mutableListOf<String>()
        fun walk(v: View) {
            (v.tag as? String)?.let { if (it == PeekTag.STALE || it == PeekTag.WELL) order += it }
            if (v is ViewGroup) for (i in 0 until v.childCount) walk(v.getChildAt(i))
        }
        walk(root)
        assertEquals(
            "the notice is drawn after the grid it qualifies, which is a warning the eye reaches " +
                "having already read the thing it warns about",
            listOf(PeekTag.STALE, PeekTag.WELL),
            order,
        )
    }

    @Test
    fun `a fresh snapshot draws no stale notice at all`() {
        assertNull(
            "a blank stale notice is drawn over a current snapshot, which is a warning nobody " +
                "wrote -- the same call `sessionDetailView` makes for its own notices",
            view(PeekPanelScreen.of(peek(stale = false))).kitFind(PeekTag.STALE),
        )
    }

    // ---- the control is composed, not hidden -------------------------------

    @Test
    fun `take control is on screen exactly while the model offers it`() {
        // PB-INPUT-2's recorded failure mode is that this control looked identical in both
        // states. The inverse defect is the one that nearly shipped elsewhere in this slice:
        // drawing it unconditionally, which tells a user who already holds the lease to take it.
        val offered = PeekPanelScreen.of(peek(leaseHeld = false))
        assertTrue("the model does not offer the control, so this asserts nothing", offered.offersTakeControl)
        assertEquals(1, view(offered).allTagged(PeekTag.TAKE_CONTROL).size)

        val held = PeekPanelScreen.of(peek(leaseHeld = true))
        assertTrue("the model still offers the control, so this asserts nothing", !held.offersTakeControl)
        assertEquals(
            "the Take control button is on screen for a session whose lease the machine has " +
                "already confirmed",
            0,
            view(held).allTagged(PeekTag.TAKE_CONTROL).size,
        )
    }

    @Test
    fun `the control on screen is the one the surface supplied`() {
        val supplied = stubControl()
        val root = view(PeekPanelScreen.of(peek(leaseHeld = false)), takeControl = supplied)

        assertSame(supplied, root.allTagged(PeekTag.TAKE_CONTROL).single())
    }

    @Test
    fun `a control re-composed after a redraw is not refused for having a parent`() {
        // The panel is rebuilt whenever the snapshot changes, which is every terminal frame. A
        // slot arriving at its next addView still claiming a discarded parent is refused by
        // Android outright, and the failure is a crash on a screen somebody is holding.
        val supplied = stubControl()
        val panel = PeekPanelScreen.of(peek(leaseHeld = false))
        view(panel, takeControl = supplied)
        val second = view(panel, takeControl = supplied)

        assertSame(supplied, second.allTagged(PeekTag.TAKE_CONTROL).single())
    }

    // ---- the lease sentence -------------------------------------------------

    @Test
    fun `the lease sentence on screen is the one the model chose for that state`() {
        listOf(true, false).forEach { held ->
            val panel = PeekPanelScreen.of(peek(leaseHeld = held))
            assertEquals(
                panel.leaseNotice,
                textOf(view(panel).kitRequire(PeekTag.LEASE)),
            )
        }
        assertTrue(
            "the two lease states put the same sentence on screen, which is the state PB-INPUT-2 " +
                "was recorded NOT MET in -- a user could not tell until a keystroke vanished",
            textOf(view(PeekPanelScreen.of(peek(leaseHeld = true))).kitRequire(PeekTag.LEASE)) !=
                textOf(view(PeekPanelScreen.of(peek(leaseHeld = false))).kitRequire(PeekTag.LEASE)),
        )
    }

    @Test
    fun `what this slice has not recomposed is hosted under the panel, not instead of it`() {
        val trailing = View(context)
        val root = view(PeekPanelScreen.of(peek()), below = trailing) as ViewGroup

        assertSame(trailing, root.getChildAt(root.childCount - 1))
        assertNotNull("hosting the remainder dropped the screen", root.kitFind(PeekTag.NAV))
    }
}
