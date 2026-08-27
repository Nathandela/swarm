package dev.swarm.phone.ui.kit

import android.content.Context
import android.graphics.drawable.ColorDrawable
import android.widget.LinearLayout
import android.widget.TextView
import androidx.test.core.app.ApplicationProvider
import dev.swarm.phone.theme.SwarmTheme
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotEquals
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner

/**
 * The record has a hole, said at the place the record tore (derivation row 29).
 *
 * WHAT IT REPLACES IS A PARAGRAPH IN THE WRONG PLACE: today a proven gap draws a sentence and two
 * full-width buttons ABOVE the conversation, a notice with no position standing where the reader is
 * reading. So the assertions below are as much about SIZE as about paint -- a divider that grew
 * back into a block would have undone the whole of the change.
 */
@RunWith(RobolectricTestRunner::class)
class GapDividerTest {

    private val context: Context
        get() = SwarmTheme.applyTo(ApplicationProvider.getApplicationContext())

    private fun word(divider: LinearLayout): TextView =
        (0 until divider.childCount).map { divider.getChildAt(it) }.filterIsInstance<TextView>().single()

    private fun rules(divider: LinearLayout) =
        (0 until divider.childCount).map { divider.getChildAt(it) }.filter { it !is TextView }

    @Test
    fun `it is a rule with a word on it and nothing else`() {
        val divider = gapDivider(context, "records missing")
        assertEquals(
            "one line, at the place the record tore. Today this is a paragraph and two full-width " +
                "buttons standing above the conversation",
            3,
            divider.childCount,
        )
        assertEquals("records missing", word(divider).text.toString())
        assertEquals("a rule on each side, so the word sits in the record", 2, rules(divider).size)
    }

    @Test
    fun `it says the machine's words in the notice line's error voice`() {
        val divider = gapDivider(context, "records missing")
        val notice = notice(context, "records missing", NoticeKind.ERROR)
        assertEquals(
            "what changed is how much of the screen the statement may take, not who is making it",
            notice.currentTextColor,
            word(divider).currentTextColor,
        )
        assertEquals(
            "the same type role too: a second size would make the tear shout over every other " +
                "notice in the app",
            notice.textSize,
            word(divider).textSize,
            0f,
        )
    }

    @Test
    fun `the rules are warmed toward the error ink and are not the plain hairline`() {
        val divider = gapDivider(context, "records missing")
        for (rule in rules(divider)) {
            assertEquals(
                "row 12's own `color-mix(--p-err 36%, --p-hair)`, spent a third time rather than " +
                    "mixed a fourth: the drawing's `rgba(--p-err, 0.32)` is an alpha that is a " +
                    "function of a token, which PB-TOK-7 forbids typing",
                Kit.errorBorder(context),
                (rule.background as ColorDrawable).color,
            )
        }
        assertNotEquals(
            "a plain hairline here would draw the same break the transcript already draws between " +
                "two turns, which is not a claim about the record",
            Kit.colour(context, dev.swarm.phone.R.color.swarm_hairline),
            Kit.errorBorder(context),
        )
    }

    @Test
    fun `both rules are one hairline tall and share whatever width is left`() {
        val divider = gapDivider(context, "records missing")
        for (rule in rules(divider)) {
            val params = rule.layoutParams as LinearLayout.LayoutParams
            assertEquals(
                "dpPx and not dp: this is a laid-out height rather than a stroke, and at density " +
                    "2.625 the unrounded value lays out 2 px against the 3 px the header's own " +
                    "rule paints on the same screen",
                Kit.dpPx(context, KitMetrics.HAIRLINE_DP),
                params.height,
            )
            assertEquals("weighted equally, so the words sit at the centre of the column", 1f, params.weight, 0f)
        }
        assertEquals(
            "the words keep their own width -- a weighted label would let a long repair phrase eat " +
                "the rules until the divider stopped reading as one",
            LinearLayout.LayoutParams.WRAP_CONTENT,
            word(divider).layoutParams.width,
        )
    }

    @Test
    fun `the whole line clears the touch floor, because the repair is a word inside it`() {
        assertEquals(
            "row 22's general case: an inline span cannot carry a 48 dp target, so the line is the " +
                "control and the line is where the floor is spent",
            Kit.dpPx(context, KitMetrics.MIN_TARGET_DP),
            gapDivider(context, "records missing").minimumHeight,
        )
    }

    @Test
    fun `it carries no air of its own beyond that floor`() {
        val params = gapDivider(context, "records missing").layoutParams as LinearLayout.LayoutParams
        assertEquals("the air belongs to the column that composes it", 0, params.topMargin)
        assertEquals("and at the other edge", 0, params.bottomMargin)
        assertTrue(
            "a divider that hugged its words would leave the tear floating in the middle of the " +
                "reading column",
            params.width == LinearLayout.LayoutParams.MATCH_PARENT,
        )
    }
}
