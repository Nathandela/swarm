package dev.swarm.phone.ui.kit

import android.content.Context
import android.widget.LinearLayout
import androidx.test.core.app.ApplicationProvider
import dev.swarm.phone.R
import dev.swarm.phone.theme.SwarmTheme
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner

/**
 * The offer to fetch older messages, at the head of the list (derivation row 30).
 *
 * THE THING IT MUST NOT BECOME IS THE OBVIOUS IMPLEMENTATION. This app already has a full-width
 * tertiary button and it is what a `Load earlier messages` control looks like in most clients --
 * and the defect this whole surface exists to fix is three full-width buttons standing above a
 * conversation. A fourth at the top of the list is the same mistake at the other end, so the first
 * assertion here is that this is a chip.
 */
@RunWith(RobolectricTestRunner::class)
class EarlierChipTest {

    private val context: Context
        get() = SwarmTheme.applyTo(ApplicationProvider.getApplicationContext())

    @Test
    fun `it hugs its words and is not a full-width button`() {
        val chip = earlierChip(context, "Show earlier")
        assertEquals("Show earlier", chip.text.toString())
        assertEquals(
            "a pill sits above the first message rather than in front of it, and scrolls away " +
                "with the history it belongs to",
            LinearLayout.LayoutParams.WRAP_CONTENT,
            chip.layoutParams.width,
        )
    }

    @Test
    fun `it is row 10's floating chip and not a second recipe`() {
        val chip = earlierChip(context, "Show earlier")
        assertEquals(
            "the same surface `syncPill` already spends. A second recipe would differ from row " +
                "10's in no cell",
            chipSurface(context, selected = false).spec,
            (chip.background as SubstrateSurface).spec,
        )
    }

    @Test
    fun `it clears the touch floor with the chip's drawing unchanged`() {
        val chip = earlierChip(context, "Show earlier")
        assertEquals(
            "a pill sized to one short line of copy is the control that measures short by " +
                "construction",
            Kit.dpPx(context, KitMetrics.MIN_TARGET_DP),
            chip.minimumHeight,
        )
        assertEquals(
            "a minimum and not a size: row 10's padding is spent exactly as row 10 states it",
            Kit.dimenPx(context, R.dimen.swarm_space_12),
            chip.paddingStart,
        )
        assertEquals(
            "and the vertical step too -- growing a control until it measures 48 satisfies the " +
                "number and loses the drawing, which is what row 4 warns against",
            Kit.dimenPx(context, R.dimen.swarm_space_8),
            chip.paddingTop,
        )
    }

    @Test
    fun `it places itself nowhere`() {
        val params = earlierChip(context, "Show earlier").layoutParams as LinearLayout.LayoutParams
        assertEquals(
            "the drawing centres it over the first message, and centring is layout -- a chip that " +
                "centred itself would be right on this screen and wrong on the next one",
            -1,
            params.gravity,
        )
        assertEquals("the air is the composing column's", 0, params.topMargin)
    }

    @Test
    fun `it is focusable, and its ring follows its own corners`() {
        val chip = earlierChip(context, "Show earlier")
        assertTrue("row 23 applies to every focusable", chip.isFocusable)
        assertTrue(
            "unlike the header's controls this one paints a fill, so a ring at radius 0 would cut " +
                "across the corners it surrounds",
            chip.foreground != null,
        )
    }
}
