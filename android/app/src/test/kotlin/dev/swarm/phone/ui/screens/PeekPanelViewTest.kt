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

    @Test
    fun `the header is the drill-down header, with the model's destination and title`() {
        val panel = PeekPanelScreen.of(peek(session = "mbp/quanthome", cols = 120, rows = 40))
        val nav = view(panel).kitRequire(PeekTag.NAV)

        assertEquals("Inbox", textOf(nav.kitRequire(KitTag.DRILL_BACK)))
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

    @Test
    fun `the stale banner is inside the well, above the grid`() {
        // The model's own decision, and its KDoc argues it: a stale snapshot is still the
        // snapshot, so the warning belongs where the thing it warns about is rather than on a
        // line the eye skips on its way to the grid.
        val stale = PeekPanelScreen.of(peek(text = "$ go test ./...", stale = true))
        val well = textOf(view(stale).kitRequire(PeekTag.WELL))

        assertEquals(stale.snapshot, well)
        assertTrue(
            "the well holds the grid and not the banner, so a snapshot the phone knows is out of " +
                "date reads as live:\n$well",
            well.lines().size > 1,
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
