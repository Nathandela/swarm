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

    /** A second distinct string, so a two-cell component can be searched cell by cell. */
    private val shortSubtitle = "working - nathans-mbp"

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

    /**
     * THE THIRD SHAPE, and it is a third SHAPE rather than a third leniency.
     *
     * [assertClamped] fixes two things at once -- one line, and the platform's mark at the END --
     * and the second half is a claim about WHERE a name is distinguished. `Kit.identityCell`'s own
     * argument is that the front of a name is what tells two apart, which is true of a project, a
     * machine, a session and a tab. It is false of exactly one cell in this kit: a PATH, whose
     * distinguishing half is its last segment. `ui/kit/Composer.kt` clipped at the end is
     * `ui/kit/Compo...`, which has thrown away the part the reader was looking for.
     *
     * So this asserts the SAME line count and the SAME requirement that a truncation be visible --
     * the property the suite exists for, that a shortened name never reads as a shorter name -- and
     * differs only in which end gives. Nothing that faces [assertClamped] is moved here; row 31's
     * path is the only cell that has ever needed it, and if a second one appears it should have to
     * argue for itself the way that row does.
     */
    private fun assertClampedMidway(what: String, view: View?) {
        val text = view as? TextView
        assertTrue("$what is not a TextView, so nothing can be asserted about its lines", text != null)
        assertEquals(
            "$what draws more than one line: a path that wraps reshapes every row under it, " +
                "exactly as a name does",
            1,
            text!!.maxLines,
        )
        assertEquals(
            "$what does not clip in the MIDDLE, and it is a path: clipped at the end it loses " +
                "its last segment, which is the only part that distinguishes two of them",
            TextUtils.TruncateAt.MIDDLE,
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
        // `.sheet2 .ctx` is an identity ELSEWHERE -- a machine, a project, a command -- and here
        // it carries a daemon's own error string, which is read to the end or not at all. A
        // clamped one would hide the half of a reason that names which refusal this was.
        assertWrapping("`.sheet2 .ctx` (the machine's own reason)", noticeDetail(context, long))
        assertWrapping("row 8's empty state", emptyState(context, long))
        assertWrapping("row 22's note under a well", readOnlyNote(context, long))
    }

    // ---- the conversation surface (Wave C) ---------------------------------

    /**
     * THE SEVEN COMPONENTS THIS SUITE DID NOT COVER, added with the wave rather than after it.
     *
     * A hand-enumerated audit that silently omits new components is a gate that has stopped
     * covering the thing it was written for -- the same shape as a registry claim over a file that
     * does not exist. `messageBubble` and `conversationHeader` had been absent since they landed.
     *
     * ONE COMPONENT IS DELIBERATELY NOT HERE AND IT IS NOT AN EXEMPTION: `overflowControl` takes
     * no string at all (`overflowControl(context)`), so there is no long value to hand it and
     * nothing this suite can ask. It is out of scope by its signature rather than by permission.
     */
    @Test
    fun `the conversation surface obeys the same two rules, and one path obeys a third`() {
        val header = conversationHeader(
            context, title = long, subtitle = shortSubtitle, group = "working", back = null, menu = null,
        )
        assertClamped("row 27's session name", header.firstTextSaying(long), 1)
        assertClamped("row 27's machine and state line", header.firstTextSaying(shortSubtitle), 1)

        val menu = conversationMenu(context, listOf(MenuChoice("id", long))) {}
        assertClamped("row 28's menu row", menu.firstTextSaying(long), 1)

        assertClamped("row 30's earlier chip", earlierChip(context, long), 1)
        assertClamped("row 32's decision pill", decisionPill(context, long), 1)

        val change = fileChangeRow(context, verb = "modify", path = long, counts = "+12 -24")
        assertClamped("row 31's change verb", change.firstTextSaying("modify"), 1)
        assertClamped("row 31's line counts", change.firstTextSaying("+12 -24"), 1)
        assertClampedMidway("row 31's path", change.firstTextSaying(long))
    }

    @Test
    fun `a message and a tear are prose, and neither may be clipped`() {
        assertWrapping(
            "row 26's bubble -- it is what a PERSON said, and half of somebody's own sentence " +
                "hidden behind an ellipsis is the defect this suite exists to avoid, not the fix",
            messageBubble(context, long),
        )
        assertWrapping(
            "row 29's tear -- its label carries the repair, and a clamped divider would drop the " +
                "one actionable word on the line rather than the tail of a name",
            gapDivider(context, long).firstTextSaying(long),
        )
    }

    /** The first TextView in this tree whose text is exactly [text]. */
    private fun View.firstTextSaying(text: String): View? {
        if (this is TextView && this.text?.toString() == text) return this
        if (this !is android.view.ViewGroup) return null
        for (i in 0 until childCount) {
            getChildAt(i).firstTextSaying(text)?.let { return it }
        }
        return null
    }
}
