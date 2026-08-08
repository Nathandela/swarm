package dev.swarm.phone.ui.kit

import android.content.Context
import android.text.TextUtils
import android.view.View
import android.widget.TextView
import androidx.test.core.app.ApplicationProvider
import dev.swarm.phone.theme.SwarmTheme
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner

/**
 * FAILING-FIRST (TDD RED, GG-5) for agents-tracker-ksvb.3: what a long string is allowed to do.
 *
 * **THE KIT SET NO `maxLines` AND NO `ellipsize` ANYWHERE**, so every cell in it wrapped. That is
 * right for prose and wrong for an identity, and the difference is what this suite draws.
 *
 * AN IDENTITY CELL IS A NAME, AND A NAME THAT WRAPS RESHAPES THE ROW AROUND IT. A session's
 * project is a path, an agent is a handle, a machine's endpoint is an id -- none of them is a
 * sentence, and none is read by reading to the end. A monorepo path in `.prow .pj` turned a
 * two-line card into a four-line one, which moves every row under it; a long tab label wrapped
 * inside a bar whose height is a fixed `tabbar_height`. The design draws all of them on one line,
 * so one line is what they get, with the platform's own truncation mark saying that there is more.
 *
 * BODY COPY IS THE OTHER HALF AND IT IS ASSERTED HERE TOO. The need line, the machine's meta line,
 * a notice and the empty state are prose a person is meant to read to the end; clamping those
 * would hide the second half of a sentence behind an ellipsis, which is a worse defect than the
 * one being fixed. A suite that only asserted the clamps would be satisfied by clamping
 * everything.
 *
 * THE CAPPED NOTE IS THE ONE CASE IN BETWEEN. `readOnlyNote` is prose, and it is also what the
 * scaffold's connection banner is built from -- and that banner sits ABOVE the scroll, so a line
 * that wraps to four pushes the whole app down. Two lines and an ellipsis is the compromise, and
 * it is a parameter rather than the component's own rule because the same component under a
 * terminal well must wrap freely.
 */
@RunWith(RobolectricTestRunner::class)
class EllipsizeTest {

    private val context: Context
        get() = SwarmTheme.applyTo(ApplicationProvider.getApplicationContext())

    /** A string no cell in this design has room for. */
    private val long = "a-very-long-identifier-that-no-row-in-this-design-has-room-to-draw"

    private fun sessionRow(): View = sessionRow(
        context = context,
        project = long,
        agent = long,
        need = long,
        group = "needs_input",
        lit = true,
        promoted = false,
    )

    private fun machineRow(): View = machineRow(
        context = context,
        machine = long,
        presence = long,
        mark = PresenceMark.ONLINE,
        endpoint = long,
    )

    private fun tabBar(): View = tabBar(context, listOf(TabItem(label = "Inbox")))

    private fun assertClamped(what: String, view: View?, lines: Int) {
        val text = view as? TextView
        assertTrue("$what is not a TextView, so nothing can be asserted about its lines", text != null)
        assertEquals(
            "$what draws more than $lines line(s): an identity is a name, and a name that wraps " +
                "reshapes every row under it",
            lines,
            text!!.maxLines,
        )
        assertEquals(
            "$what wraps or clips instead of ending in an ellipsis, so a truncated name reads as " +
                "a shorter name rather than as a name with more to it",
            TextUtils.TruncateAt.END,
            text.ellipsize,
        )
    }

    private fun assertWrapping(what: String, view: View?) {
        val text = view as? TextView
        assertTrue("$what is not a TextView", text != null)
        assertEquals(
            "$what is clamped, and it is prose: the second half of a sentence a person is meant " +
                "to read would be hidden behind an ellipsis",
            Int.MAX_VALUE,
            text!!.maxLines,
        )
        assertNull("$what carries a truncation mark and it is prose", text.ellipsize)
    }

    // ---- the identities ----------------------------------------------------

    @Test
    fun `every identity cell is one line, ended with an ellipsis`() {
        val session = sessionRow()
        assertClamped("`.prow .pj` (the session's project)", session.kitFind(KitTag.PROJECT), 1)
        assertClamped("`.prow .ag` (the session's agent)", session.kitFind(KitTag.AGENT), 1)

        val machine = machineRow()
        assertClamped("row 11's machine name", machine.kitFind(KitTag.MACHINE_NAME), 1)
        assertClamped("row 11's endpoint id", machine.kitFind(KitTag.MACHINE_ENDPOINT), 1)

        assertClamped(
            "`.chip` (a scope chip)",
            filterChip(context, long, selected = false, present = null),
            1,
        )
        assertClamped("`.ptabs div` (a tab label)", tabBar().kitFind(KitTag.TAB_LABEL), 1)
        assertClamped(
            "`.pnav .big` (the root nav title)",
            navHeader(context, long, live = null).kitFind(KitTag.TITLE),
            1,
        )
        assertClamped(
            "§4's drill-down title",
            navHeaderDrill(context, back = "Inbox", title = long).kitFind(KitTag.DRILL_TITLE),
            1,
        )
    }

    // ---- the prose ---------------------------------------------------------

    @Test
    fun `body copy still wraps, because it is meant to be read to the end`() {
        assertWrapping("`.prow .ln` (the session's need)", sessionRow().kitFind(KitTag.NEED))
        assertWrapping("row 11's meta line", machineRow().kitFind(KitTag.MACHINE_META))
        assertWrapping("§4's notice line", notice(context, long))
        assertWrapping("row 8's empty state", emptyState(context, long))
        assertWrapping("row 22's note under a well", readOnlyNote(context, long))
    }

    // ---- the one in between ------------------------------------------------

    @Test
    fun `the capped note stops at two lines so it cannot push the app down`() {
        assertClamped("row 22's note, capped", readOnlyNote(context, long, capped = true), 2)
    }
}
