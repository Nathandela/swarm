package dev.swarm.phone.ui.kit

import android.content.Context
import android.view.View.MeasureSpec
import android.view.ViewGroup
import android.widget.LinearLayout
import android.widget.TextView
import androidx.test.core.app.ApplicationProvider
import dev.swarm.phone.R
import dev.swarm.phone.theme.SwarmTheme
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotEquals
import org.junit.Assert.assertSame
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import kotlin.math.roundToInt

/**
 * FAILING-FIRST (TDD RED, GG-5) for derivation row 9's BAR -- the half of the composer no factory
 * has ever built (agents-tracker-hxv, agents-tracker-nx44.6).
 *
 * `textField` has cited row 9 since S23 and it is the row's FIELD only; the bar around it --
 * `--p-tabbg`, a 1 dp `--p-hair` top rule, `space_8` x `space_14` of padding and a `space_8` gap
 * -- was never spent anywhere, so the app's one composer was a bare `EditText` and a button added
 * to a `LinearLayout` with none of it.
 *
 * THE FIELD AND THE SEND CONTROL ARE SLOTS, which is `approvalSheet`'s ruling one component over:
 * `textField` is already every field in the app and `ctaButton` already every action, so a bar
 * that built its own would be a second copy of both -- and the send control reaches a facade verb
 * and carries PB-SEC-12 clause 1's touch filter, which is `PhoneSurface`'s and never a factory's.
 */
@RunWith(RobolectricTestRunner::class)
class ComposerTest {

    private val context: Context
        get() = SwarmTheme.applyTo(ApplicationProvider.getApplicationContext())

    private fun dimenPx(name: String): Int {
        val id = context.resources.getIdentifier(name, "dimen", context.packageName)
        assertNotEquals("R.dimen.$name is not in the merged resource table", 0, id)
        return context.resources.getDimension(id).roundToInt()
    }

    /** Row 9: "bar padding `space_8` x `space_14`, gap `space_8`". */
    @Test
    fun `the composer bar spends row 9's own padding and its own gap`() {
        val field = TextView(context)
        val send = TextView(context)
        val bar = composerBar(context, field, send)

        val claims = listOf(
            Claim("row 9 bar padding-x (start)", dimenPx("swarm_space_14"), bar.paddingStart),
            Claim("row 9 bar padding-x (end)", dimenPx("swarm_space_14"), bar.paddingEnd),
            Claim("row 9 bar padding-y (top)", dimenPx("swarm_space_8"), bar.paddingTop),
            Claim("row 9 bar padding-y (bottom)", dimenPx("swarm_space_8"), bar.paddingBottom),
            Claim(
                "row 9 gap between the field and the send control",
                dimenPx("swarm_space_8"),
                (send.layoutParams as ViewGroup.MarginLayoutParams).marginStart,
            ),
        )

        assertEquals(mismatches(claims).joinToString("\n"), emptyList<String>(), mismatches(claims))
    }

    /**
     * Row 9: "bar `--p-tabbg` ... bar: 1 dp `--p-hair` top rule" -- which is `tabBar`'s recipe
     * exactly, and deliberately the same object rather than a second one that drifts.
     */
    @Test
    fun `the bar is --p-tabbg under a one-dp hairline, the tab bar's own recipe`() {
        val surface = composerBar(context, TextView(context), TextView(context)).background as? TopRule

        assertTrue(
            "the composer bar draws no top rule at all, so nothing separates it from the " +
                "transcript scrolling under it -- Substrate bans drop shadows, which makes that " +
                "hairline the only separation there is",
            surface != null,
        )
        assertEquals(
            Kit.colour(context, R.color.swarm_tabbar_background),
            requireNotNull(surface).fill,
        )
        assertEquals(Kit.colour(context, R.color.swarm_hairline), surface.rule)
        assertEquals(Kit.dpPx(context, KitMetrics.HAIRLINE_DP).toFloat(), surface.rulePx, 0f)
    }

    @Test
    fun `the field takes the room the bar has and the send control keeps its own`() {
        val field = TextView(context)
        val send = TextView(context)
        val bar = composerBar(context, field, send)

        assertEquals(2, bar.childCount)
        assertSame(
            "the field is not first, so a composer reads right to left against every other bar " +
                "in the app",
            field,
            bar.getChildAt(0),
        )
        assertSame(send, bar.getChildAt(1))
        assertEquals(
            "the field does not take the bar's spare room, so a composer whose send control is " +
                "one word long draws a field the same width as one whose label is four",
            1f,
            (field.layoutParams as LinearLayout.LayoutParams).weight,
            0f,
        )
        assertEquals(0f, (send.layoutParams as LinearLayout.LayoutParams).weight, 0f)
    }

    /**
     * A composer is re-parented every time the screen holding it is rebuilt -- the field holds what
     * the user typed, so it is built once and lives for the process -- and Android refuses a child
     * that still claims a discarded parent.
     */
    @Test
    fun `the bar takes a field that a previous bar was holding`() {
        val field = TextView(context)
        val send = TextView(context)
        composerBar(context, field, send)

        val second = composerBar(context, field, send)

        assertSame(second, field.parent)
        assertSame(second, send.parent)
    }

    /**
     * Phone refit W3.2 (owner ruling; row 9's `action-box 40`): the bar's one control is a 40 dp
     * square. The bar hands every slot WRAP x WRAP, so the square is the control's own MINIMUM
     * width and the box between its paddings -- `textField`'s arrangement for its 36 dp well
     * inside a 48 dp target, one slot over.
     */
    @Test
    fun `the composer action is a 40dp square`() {
        val action = composerAction(context)
        val box = Kit.dpPx(context, KitMetrics.COMPOSER_ACTION_DP)
        action.measure(MeasureSpec.UNSPECIFIED, MeasureSpec.UNSPECIFIED)

        assertEquals("the control's box is not 40 dp wide", box, action.measuredWidth)
        assertEquals(
            "the box between the paddings is not square, so the glyph is not centred in the " +
                "40 dp the row states",
            box,
            action.measuredHeight - action.paddingTop - action.paddingBottom,
        )
        assertEquals(
            "the glyph is not the kit's ink; the drawables ship the platform's white precisely " +
                "so the tint has something opaque to replace",
            Kit.colour(context, R.color.swarm_text_primary),
            action.imageTintList?.defaultColor,
        )
    }

    /** PB-DS-12's floor, kept: 40 dp is drawn, and 48 dp is what the finger gets. */
    @Test
    fun `the square keeps the 48dp touch floor`() {
        val action = composerAction(context)
        action.measure(MeasureSpec.UNSPECIFIED, MeasureSpec.UNSPECIFIED)

        assertEquals(
            "the control measures under 48 dp tall, so the square shrank the target with it -- " +
                "a target is not a size (row 4's own words)",
            Kit.dpPx(context, KitMetrics.MIN_TARGET_DP),
            action.measuredHeight,
        )
    }

    /**
     * W3 review round (2026-08-28): THE SQUARE PAINTS ITS DISABLED STATE. `View.enable` (offline)
     * and `VerbDispatch.press` (while a send crosses) set `isEnabled = false` and rely on the
     * drawable state to show it, as `CtaButton` does (row 24's pair); a single-colour tint drew a
     * dead control at full strength. Disabled is `--p-ink3`, the ink every dead control shares.
     */
    @Test
    fun `a disabled square paints the tertiary ink`() {
        val action = composerAction(context)
        val tint = action.imageTintList!!

        assertEquals(
            "a disabled square keeps the live ink, so a control that cannot be pressed -- " +
                "offline, or while a send crosses -- looks exactly like one that can",
            Kit.colour(context, R.color.swarm_text_tertiary),
            tint.getColorForState(intArrayOf(-android.R.attr.state_enabled), 0),
        )
        assertEquals(
            Kit.colour(context, R.color.swarm_text_primary),
            tint.getColorForState(intArrayOf(android.R.attr.state_enabled), 0),
        )
    }
    /** Phone refit W5.2: the refusal names the computer where the screen knows its name. */
    @Test
    fun `noticeFor names the machine when one is known`() {
        assertEquals(
            "Not sent. Finish typing on MacBookPro first.",
            ComposerModel.noticeFor("INPUT_BUSY", machine = "MacBookPro").copy,
        )
    }

    @Test
    fun `noticeFor falls back to your computer`() {
        assertEquals(
            "Not sent. Finish typing on your computer first.",
            ComposerModel.noticeFor("INPUT_BUSY").copy,
        )
        assertEquals(
            "an empty name is no name, not a blank in the sentence",
            ComposerModel.noticeFor("INPUT_BUSY"),
            ComposerModel.noticeFor("INPUT_BUSY", machine = ""),
        )
    }
}
