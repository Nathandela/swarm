package dev.swarm.phone.ui.kit

import android.content.Context
import android.view.View
import android.widget.LinearLayout
import android.widget.TextView
import androidx.test.core.app.ApplicationProvider
import dev.swarm.phone.theme.SwarmTheme
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner

/**
 * Everything that is not reading or typing (derivation row 28, owner ruling R2).
 *
 * WHAT THIS COMPONENT IS FOR, stated as the thing it replaces: three stacked full-width CTAs above
 * a transcript, on a viewport with roughly 150 dp left for the conversation. Two of the three leave
 * the product and the third moves behind a 48 dp glyph. So the assertions here are about COST as
 * much as about paint -- the menu is allowed to be as elaborate as it likes while it is open,
 * because while it is closed it is one square in a header.
 */
@RunWith(RobolectricTestRunner::class)
class ConversationMenuTest {

    private val context: Context
        get() = SwarmTheme.applyTo(ApplicationProvider.getApplicationContext())

    private val choices = listOf(
        MenuChoice("details", "Session details"),
        MenuChoice("earlier", "Load earlier messages"),
        MenuChoice("kill", "Kill session", destructive = true),
    )

    private fun rowsOf(menu: LinearLayout): List<TextView> =
        (0 until menu.childCount).map { menu.getChildAt(it) }.filterIsInstance<TextView>()

    @Test
    fun `it draws one row per choice, in the order the caller gave them`() {
        val menu = conversationMenu(context, choices) {}
        assertEquals(
            "the choices are the caller's, because which of them exist is a fact about a session " +
                "-- an ended session has no kill",
            listOf("Session details", "Load earlier messages", "Kill session"),
            rowsOf(menu).map { it.text.toString() },
        )
    }

    @Test
    fun `an empty menu draws nothing rather than an empty block`() {
        assertEquals(
            "a block with no rows is a surface floating over the conversation saying nothing",
            0,
            conversationMenu(context, emptyList()) {}.childCount,
        )
    }

    @Test
    fun `choosing a row reports the choice's id and never its words`() {
        var chosen: String? = null
        val menu = conversationMenu(context, choices) { chosen = it }
        rowsOf(menu)[2].performClick()
        assertEquals(
            "a menu keyed on its own visible words would re-route itself the day `Kill session` " +
                "becomes `End session`",
            "kill",
            chosen,
        )
    }

    @Test
    fun `a destructive row differs from a route by ink and by nothing else`() {
        val menu = conversationMenu(context, choices) {}
        val route = rowsOf(menu)[0]
        val destructive = rowsOf(menu)[2]
        assertNotEquals(
            "`--p-ink` is a route and `--p-err` is a thing that cannot be undone",
            route.currentTextColor,
            destructive.currentTextColor,
        )
        assertEquals(
            "what changes is who is speaking, not how loudly -- a second size here would make the " +
                "destructive row shout over the two above it",
            route.textSize,
            destructive.textSize,
            0f,
        )
        assertEquals("and not a second weight either", route.typeface, destructive.typeface)
    }

    @Test
    fun `every row clears the touch floor`() {
        val floor = Kit.dpPx(context, KitMetrics.MIN_TARGET_DP)
        for (row in rowsOf(conversationMenu(context, choices) {})) {
            assertEquals(
                "the drawn line is under 34 dp, and the row under a mis-aimed tap on `Load " +
                    "earlier messages` is `Kill session`",
                floor,
                row.minimumHeight,
            )
        }
    }

    @Test
    fun `the rows are separated by a rule and not by a gap`() {
        val menu = conversationMenu(context, choices) {}
        val rules = (0 until menu.childCount)
            .map { menu.getChildAt(it) }
            .filter { it !is TextView }
        assertEquals("one rule between three rows, and none above the first", 2, rules.size)
        for (rule in rules) {
            val params = rule.layoutParams as LinearLayout.LayoutParams
            assertEquals(
                "a gap says these are three items in a list; a rule says these are three separate " +
                    "acts and the last one ends a session",
                Kit.dpPx(context, KitMetrics.HAIRLINE_DP),
                params.height,
            )
            assertEquals(
                "dpPx and not dp: at density 2.625 the unrounded value lays out 2 px where the " +
                    "header's own rule paints 3",
                0,
                params.topMargin,
            )
        }
    }

    @Test
    fun `the block is row 1's floating surface and not a fourth recipe`() {
        val menu = conversationMenu(context, choices) {}
        val spec = (menu.background as SubstrateSurface).spec
        assertEquals(
            "a menu is the same kind of object a toast is: opaque, one ladder step above the " +
                "ground, with nothing reading through it",
            toastSurface(context).spec,
            spec,
        )
    }

    @Test
    fun `the block hugs its longest row`() {
        val menu = conversationMenu(context, choices) {}
        assertEquals(
            "the menu is anchored under a control at the trailing edge of the header; a " +
                "full-bleed block there would be a sheet",
            LinearLayout.LayoutParams.WRAP_CONTENT,
            menu.layoutParams.width,
        )
        assertTrue(
            "every row takes the block's width, so the ink is the only difference between them",
            rowsOf(menu).all { it.layoutParams.width == LinearLayout.LayoutParams.MATCH_PARENT },
        )
    }

    @Test
    fun `the overflow control is a floor around a glyph and paints nothing`() {
        val control = overflowControl(context)
        val floor = Kit.dpPx(context, KitMetrics.MIN_TARGET_DP)
        assertEquals("PB-DS-12, on the leading edge", floor, control.minimumWidth)
        assertEquals("and on the other one", floor, control.minimumHeight)
        assertNull(
            "it has no fill of its own, which is why the floor costs nothing visually: what grows " +
                "is the empty space a finger may land in",
            control.background,
        )
        assertTrue("row 23 applies to every focusable", control.isFocusable)
    }

    @Test
    fun `the overflow control opens nothing by itself`() {
        assertTrue(
            "what an overflow OPENS is the screen's -- a kit that owned the popup would own where " +
                "it is positioned, what dismisses it, and what happens to the session while it is up",
            !overflowControl(context).hasOnClickListeners(),
        )
    }

    @Test
    fun `a rule is not a row`() {
        val menu = conversationMenu(context, choices) {}
        val rule: View = (0 until menu.childCount)
            .map { menu.getChildAt(it) }
            .first { it !is TextView }
        assertTrue(
            "a separator that took a click would answer a tap aimed at the row above or below it",
            !rule.isClickable,
        )
    }
}
