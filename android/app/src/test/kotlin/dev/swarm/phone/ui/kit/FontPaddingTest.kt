package dev.swarm.phone.ui.kit

import android.content.Context
import android.view.View
import android.view.ViewGroup
import android.widget.TextView
import androidx.test.core.app.ApplicationProvider
import dev.swarm.phone.theme.SwarmTheme
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner

/**
 * FAILING-FIRST (TDD RED, GG-5) for agents-tracker-ksvb.3: the kit's own line box.
 *
 * WHAT `includeFontPadding` IS, AND WHY IT IS NOT IN THE TYPE SCALE. It is a `TextView` PROPERTY
 * and not a `TextAppearance` attribute -- there is no `<item>` that can carry it -- so it cannot
 * be stated in `type.xml` beside the size, the weight and the family, and it is set where the kit
 * constructs the view instead. On by default, it pads a line by the difference between the font's
 * ASCENT/DESCENT and its TOP/BOTTOM, which varies per family and per weight.
 *
 * WHY THAT MATTERS HERE RATHER THAN IN GENERAL. This app sets nineteen styles across three
 * families at sizes from 9.5 to 34 sp and stacks them one above another in every row: a session
 * row is `Title.Row` over `Body.Secondary`, a machine row the same, a settings row three deep. The
 * extra pad is a different number for each of them, so the gaps the design states as `space_4`
 * arrive on screen as `space_4` plus an unstated per-style delta -- which is why two rows built
 * from the same steps did not line up, and why the second line of a session row sat further from
 * the first than the derivation table says. Turning it off uniformly makes a stated step the whole
 * distance between two baselines.
 *
 * UNIFORMLY IS THE LOAD-BEARING WORD. Half a kit with font padding and half without is worse than
 * either, because the delta then differs between two components that the design gives identical
 * spacing -- which is the state this closes.
 */
@RunWith(RobolectricTestRunner::class)
class FontPaddingTest {

    private val context: Context
        get() = SwarmTheme.applyTo(ApplicationProvider.getApplicationContext())

    /** Every `TextView` a component draws, itself included. */
    private fun textViews(root: View): List<TextView> {
        val found = mutableListOf<TextView>()
        fun walk(v: View) {
            if (v is TextView) found += v
            if (v is ViewGroup) for (i in 0 until v.childCount) walk(v.getChildAt(i))
        }
        walk(root)
        return found
    }

    /**
     * The representatives, chosen to cover the three ways a kit view gets its text.
     *
     *  - [sessionRow] is a COMPOSITION: four styles in one card, and the one the field report is
     *    about -- its second line sat too far below its first.
     *  - [ctaButton] and [filterChip] are the two single-view factories whose styles carried the
     *    `line-height: 1` defect this issue also repairs, so the two repairs meet on them.
     *  - [monoWell] is the mono family, where the font-padding delta is a different number again.
     *  - [notice] is the kit's youngest component, added while this issue was open.
     */
    private fun representatives(): Map<String, View> = mapOf(
        "sessionRow" to sessionRow(
            context = context,
            project = "mbp/swarm",
            agent = "claude",
            need = "waiting on you",
            group = "needs_input",
            lit = true,
            promoted = false,
        ),
        "ctaButton" to ctaButton(context, "Approve", CtaKind.APPROVE),
        "filterChip" to filterChip(context, "All machines", selected = false, present = null),
        "monoWell" to monoWell(context, "$ swarm remote off", terminal = true),
        "notice" to notice(context, "The link dropped."),
    )

    @Test
    fun `every text the kit builds carries this app's line box and not the font's`() {
        for ((name, root) in representatives()) {
            val views = textViews(root)
            assertTrue(
                "$name draws no text at all, so this assertion passed over an empty list",
                views.isNotEmpty(),
            )
            for (view in views) {
                assertFalse(
                    "$name draws text with includeFontPadding on, so its line box carries the " +
                        "font's own ascent/descent-to-top/bottom slack. That slack is a different " +
                        "number per family, weight and size, so every stated gap in this kit " +
                        "arrives with an unstated delta added to it and two components the design " +
                        "spaces identically do not line up",
                    view.includeFontPadding,
                )
            }
        }
    }
}
