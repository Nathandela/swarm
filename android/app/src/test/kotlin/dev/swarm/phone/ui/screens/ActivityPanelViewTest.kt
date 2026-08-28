package dev.swarm.phone.ui.screens

import android.content.Context
import android.view.View
import android.view.ViewGroup
import android.widget.TextView
import androidx.test.core.app.ApplicationProvider
import dev.swarm.phone.theme.SwarmTheme
import dev.swarm.phone.ui.JournalPageView
import dev.swarm.phone.ui.JournalRow
import dev.swarm.phone.ui.kit.KitTag
import dev.swarm.phone.ui.kit.kitFind
import dev.swarm.phone.ui.kit.kitRequire
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertSame
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner

/**
 * FAILING-FIRST (TDD RED, GG-5) for PB-DS-6 and PB-DS-9 over the activity screen AS DRAWN.
 *
 * WHAT THIS ASKS THAT `ActivityPanelTest` CANNOT. That suite asks what the screen SAYS; this asks
 * whether it is on screen -- which component renders it, in what order, and whether the model's
 * rows all made it out. "The model is beautiful and nothing renders it" is the defect PB-DS-6 was
 * recorded NOT MET over, and a suite that only asserted the model harder would reproduce it.
 *
 * WHAT IS DELIBERATELY NOT ASSERTED: appearance. The row's card, its three type roles and its
 * dropped timestamp gutter are PB-DS-10's and are asserted in `ui/kit/ActivityRowTest`; repeating
 * them here would be a second opinion that can disagree with the first. What IS asserted here is
 * the one thing that suite cannot see: that this screen passes no timestamp, which is a fact about
 * the CALL rather than about the component.
 */
@RunWith(RobolectricTestRunner::class)
class ActivityPanelViewTest {

    private val context: Context
        get() = SwarmTheme.applyTo(ApplicationProvider.getApplicationContext())

    private fun panel(
        rows: List<JournalRow> = listOf(
            JournalRow(cursor = 1, sessionId = "quanthome", type = "launched", group = "", tsUnixMs = 0L),
            JournalRow(cursor = 2, sessionId = "blog", type = "group_transition", group = "needs_input", tsUnixMs = 0L),
        ),
        stale: Boolean = false,
    ) = ActivityPanelScreen.of(
        JournalPageView(rows = rows, nextCursor = rows.size.toLong(), stale = stale),
    )

    private fun view(
        panel: ActivityPanel,
        below: View? = null,
        onSelectSession: (String) -> Unit = {},
    ): View = activityPanelView(
        context = context,
        panel = panel,
        below = below,
        onSelectSession = onSelectSession,
    )

    // ---- W7.4: tappable rows, and the time cell for a stamped row ------------
    //
    // FAILING-FIRST (TDD RED, GG-5).

    @Test
    fun `a row opens its session`() {
        var chosen: String? = null
        val root = view(panel(), onSelectSession = { chosen = it })

        // Newest first: cursor 2 (blog) is drawn above cursor 1 (quanthome).
        root.allTagged(ActivityTag.ROW)[1].performClick()

        assertEquals(
            "tapping an activity row did not open the session it names, or opened the wrong one",
            "quanthome",
            chosen,
        )
    }

    @Test
    fun `a stamped row draws its time under its day heading`() {
        val now = java.util.Calendar.getInstance().apply {
            set(2026, java.util.Calendar.AUGUST, 28, 12, 0, 0)
            set(java.util.Calendar.MILLISECOND, 0)
        }.timeInMillis
        val ts = now - 60 * 60_000L
        val stamped = ActivityPanelScreen.of(
            JournalPageView(
                rows = listOf(JournalRow(cursor = 1, sessionId = "quanthome", type = "launched", group = "", tsUnixMs = ts)),
                nextCursor = 1,
                stale = false,
            ),
            nowUnixMs = now,
        )
        val root = view(stamped)

        assertEquals(listOf("Today"), root.allTagged(ActivityTag.SECTION_LABEL).map { textOf(it) })
        assertEquals(
            listOf(dev.swarm.phone.ui.kit.ToolCard.timestampLabel(ts)),
            root.allTagged(KitTag.ACTIVITY_TIME).map { textOf(it) },
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

    private fun textOf(view: View?): String = (view as? TextView)?.text?.toString().orEmpty()

    // ---- the composition --------------------------------------------------

    @Test
    fun `the activity screen is composed of kit components`() {
        val root = view(panel())

        assertNotNull("the screen has no title", root.kitFind(ActivityTag.NAV))
        assertNotNull("the section has no label", root.kitFind(ActivityTag.SECTION_LABEL))
        assertNotNull(
            "the rows are not the kit's `activityRow`, so the screen hand-built them",
            root.kitFind(ActivityTag.ROW)?.kitFind(KitTag.ACTIVITY_BODY),
        )
    }

    @Test
    fun `the title is drawn by the nav header and carries no live counter`() {
        val nav = view(panel()).kitRequire(ActivityTag.NAV)

        assertEquals("Activity", textOf(nav.kitRequire(KitTag.TITLE)))
        assertNull(
            "a live counter was drawn on the activity screen. It is the inbox's in-context " +
                "liveness readout (derivation §1.4), and a log of what has already happened has " +
                "nothing in flight to count",
            nav.kitFind(KitTag.LIVE),
        )
    }

    @Test
    fun `the heading comes before the rows it heads`() {
        val root = view(panel())
        val order = mutableListOf<String>()
        fun walk(v: View) {
            (v.tag as? String)?.let { if (it in ActivityTag.COMPOSITION) order += it }
            if (v is ViewGroup) for (i in 0 until v.childCount) walk(v.getChildAt(i))
        }
        walk(root)

        assertEquals(
            listOf(
                ActivityTag.NAV,
                ActivityTag.SECTION_LABEL,
                ActivityTag.ROW,
                ActivityTag.ROW,
            ),
            order,
        )
    }

    @Test
    fun `every row in the model is on screen, in the model's order`() {
        val page = panel()
        val rows = view(page).allTagged(ActivityTag.ROW)

        assertEquals(page.sections.single().rows.size, rows.size)
        assertEquals(
            page.sections.single().rows.map { it.body },
            rows.map { textOf(it.kitRequire(KitTag.ACTIVITY_BODY)) },
        )
    }

    // ---- the timestamp this screen does not have ---------------------------

    /**
     * The assertion `ActivityRowTest` cannot make: that the CALLER passes no timestamp.
     *
     * That suite proves the component omits the cell when it is given none. This one proves this
     * screen gives it none -- which is the half that would break if someone reached for
     * `entry.cursor` to fill the gutter the mock draws. There is no time on the wire
     * (`protocol.JournalRecord` drops `internal/journal.Record.TS`), so a timestamp appearing here
     * could only have been manufactured.
     */
    @Test
    fun `no row on this screen carries a timestamp cell`() {
        val root = view(panel())

        assertEquals(
            "a timestamp cell was rendered. The journal carries no time, so whatever is in it " +
                "was invented on the handset",
            emptyList<View>(),
            root.allTagged(KitTag.ACTIVITY_TIME),
        )
    }

    // ---- PB-APP-8 and the empty page ---------------------------------------

    @Test
    fun `the stale line is drawn only when the log has a hole, and above the list`() {
        assertEquals(
            "a stale line was drawn over a whole journal",
            0,
            view(panel()).allTagged(ActivityTag.STALE).size,
        )

        val holed = panel(stale = true)
        val root = view(holed)
        assertEquals(
            listOf(holed.staleNotice),
            root.allTagged(ActivityTag.STALE).map { textOf(it) },
        )

        // Above the heading, not inside the list: the hole is in records that are not on screen
        // at all, so there is no row it could attach to and it has to be read before the list.
        val order = mutableListOf<String>()
        fun walk(v: View) {
            (v.tag as? String)?.let { if (it in ActivityTag.COMPOSITION) order += it }
            if (v is ViewGroup) for (i in 0 until v.childCount) walk(v.getChildAt(i))
        }
        walk(root)
        assertEquals(ActivityTag.STALE, order[1])
    }

    @Test
    fun `an empty page keeps its heading and draws the empty state under it`() {
        val empty = panel(rows = emptyList())
        val root = view(empty)

        assertNotNull(
            "the heading went with the rows. An empty section is still a section (PB-DS-9), and " +
                "dropping it makes \"no activity\" look like a feed that failed to load",
            root.kitFind(ActivityTag.SECTION_LABEL),
        )
        assertEquals(
            listOf(empty.sections.single().emptyCopy),
            root.allTagged(ActivityTag.EMPTY).map { textOf(it) },
        )
        assertEquals(
            "an empty page drew a row",
            0,
            root.allTagged(ActivityTag.ROW).size,
        )
    }

    @Test
    fun `a page with rows draws no empty state`() {
        assertEquals(
            "the empty state was drawn over a feed that has rows in it -- the inverse defect, " +
                "and the one that tells a user holding a live journal that there is nothing in it",
            0,
            view(panel()).allTagged(ActivityTag.EMPTY).size,
        )
    }

    @Test
    fun `what this slice has not recomposed is hosted under the panel, not instead of it`() {
        val trailing = View(context)
        val root = view(panel(), below = trailing) as ViewGroup

        assertSame(trailing, root.getChildAt(root.childCount - 1))
        assertNotNull("hosting the remainder dropped the screen", root.kitFind(ActivityTag.NAV))
    }
}
