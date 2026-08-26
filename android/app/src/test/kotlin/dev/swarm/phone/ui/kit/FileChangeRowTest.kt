package dev.swarm.phone.ui.kit

import android.content.Context
import android.text.TextUtils
import android.widget.LinearLayout
import android.widget.TextView
import androidx.test.core.app.ApplicationProvider
import dev.swarm.phone.R
import dev.swarm.phone.theme.SwarmTheme
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotEquals
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner

/**
 * One file the agent changed (derivation row 31, owner ruling R9).
 *
 * THE THING IT MUST NOT BECOME IS WHAT SHIPS TODAY. `TranscriptPanel.kt:465` draws a unified diff
 * inline and unconditionally, so a twelve-file refactor costs a screen per file on the one surface
 * whose purpose is continuous reading. The diff is not deleted here -- it is moved to a screen that
 * can give it room to scroll sideways. What this suite asserts is that the line stays a line.
 */
@RunWith(RobolectricTestRunner::class)
class FileChangeRowTest {

    private val context: Context
        get() = SwarmTheme.applyTo(ApplicationProvider.getApplicationContext())

    private fun row(
        verb: String = "modify",
        path: String = "android/app/src/main/kotlin/dev/swarm/phone/ui/kit/Composer.kt",
        counts: String = "+12 -24",
    ): LinearLayout = fileChangeRow(context, verb, path, counts)

    private fun cells(row: LinearLayout): List<TextView> =
        (0 until row.childCount).map { row.getChildAt(it) }.filterIsInstance<TextView>()

    @Test
    fun `it is three cells on one line, in the order the copy sheet writes them`() {
        val cells = cells(row())
        assertEquals(
            "`<change> - <path> - +N -M`, and the whole of the change is one line rather than a " +
                "screen",
            3,
            cells.size,
        )
        assertEquals("modify", cells[0].text.toString())
        assertTrue("the path is the wire's own", cells[1].text.toString().endsWith("Composer.kt"))
        assertEquals("+12 -24", cells[2].text.toString())
    }

    @Test
    fun `the counts are one string in one ink`() {
        val cells = cells(row())
        assertEquals(
            "the drawing tints `+N` and `-M` separately, and doing that means splitting the " +
                "machine's own words to decide which half is which -- the parse IS-TOOL-1 refuses " +
                "one hop earlier. The sign carries the direction, and the sign is the wire's",
            cells[1].currentTextColor,
            cells[2].currentTextColor,
        )
        assertNotEquals(
            "the verb is the sentence of this row and takes the primary ink, which is row 14's cell",
            cells[0].currentTextColor,
            cells[2].currentTextColor,
        )
    }

    @Test
    fun `a path is clipped in the middle, and it is the only identity here that is`() {
        val path = cells(row())[1]
        assertEquals(
            "`Kit.identityCell`'s rule is that a name is distinguished by its FRONT -- true of a " +
                "project, a machine and a session, and false of a path, whose distinguishing half " +
                "is its last segment",
            TextUtils.TruncateAt.MIDDLE,
            path.ellipsize,
        )
        assertEquals("one line: a wrapping path reshapes every row under it", 1, path.maxLines)
    }

    @Test
    fun `the path is the cell that gives`() {
        val cells = cells(row())
        assertEquals(
            "the verb and the counts keep their own widths, so the long cell is the one that gives",
            1f,
            (cells[1].layoutParams as LinearLayout.LayoutParams).weight,
            0f,
        )
        assertEquals(0f, (cells[0].layoutParams as LinearLayout.LayoutParams).weight, 0f)
        assertEquals(0f, (cells[2].layoutParams as LinearLayout.LayoutParams).weight, 0f)
    }

    @Test
    fun `it is drawn as the row it sits beside`() {
        val row = row()
        assertEquals(
            "it sits AMONG tool rows in the same stream; a different fill would make it read as a " +
                "different KIND of object when it is the same kind carrying different cells",
            cardSurface(context, attention = false).spec,
            (row.background as SubstrateSurface).spec,
        )
        assertEquals(
            "row 14's own horizontal step",
            Kit.dimenPx(context, R.dimen.swarm_space_12),
            row.paddingStart,
        )
        assertEquals(
            "and its vertical one",
            Kit.dimenPx(context, R.dimen.swarm_space_10),
            row.paddingTop,
        )
    }

    @Test
    fun `it clears the touch floor, because it opens a diff`() {
        assertEquals(
            "row 14 states no floor because an activity row is not tappable, which is the one " +
                "thing separating this from it",
            Kit.dpPx(context, KitMetrics.MIN_TARGET_DP),
            row().minimumHeight,
        )
        assertTrue("row 23 applies to every focusable", row().isFocusable)
    }

    @Test
    fun `it opens nothing by itself`() {
        assertTrue(
            "what a row OPENS is the screen's, everywhere in this kit but the menu -- and the menu " +
                "is an exception because its rows are built from data rather than composed",
            !row().hasOnClickListeners(),
        )
    }

    @Test
    fun `a rename is one row and not two`() {
        val cells = cells(row(verb = "rename", path = "SessionDetailView.kt -> ConversationView.kt", counts = ""))
        assertEquals("rename", cells[0].text.toString())
        assertEquals(
            "the copy sheet writes a rename as `<old> -> <new>` inside the path cell, so the shape " +
                "of the row does not change with the verb",
            3,
            cells.size,
        )
    }
}
