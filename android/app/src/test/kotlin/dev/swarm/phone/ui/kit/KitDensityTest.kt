package dev.swarm.phone.ui.kit

import android.content.Context
import androidx.test.core.app.ApplicationProvider
import dev.swarm.phone.theme.SwarmTheme
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotEquals
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import kotlin.math.roundToInt

/**
 * PB-DS-10 AT A DENSITY WHERE THE PLATFORM'S ARITHMETIC IS VISIBLE.
 *
 * Every other suite in this package runs at Robolectric's default, which is mdpi -- density 1.0,
 * where one dp is exactly one pixel and every rounding question in the kit has the same answer as
 * every truncation. That is the single density at which `Kit.dimen(...).toInt()` and
 * `resources.getDimensionPixelSize(...)` cannot disagree, and the kit spent its dimensions with
 * the first of those: a cast, which truncates, where the platform rounds half away from zero.
 *
 * `420dpi` is density 2.625, which is a Pixel's. There:
 *
 *   - a 1 dp hairline is 2.625 px. Truncated it renders 2 px and the platform's own answer is 3 --
 *     a THIRD of the element Substrate leans on for all of its depth, lost to a cast. Every card,
 *     chip, row and bar in this design is separated from the ground by that one line.
 *   - `space_4` is 10.5 px (10 vs 11), `space_6` is 15.75 (15 vs 16), `space_12` is 31.5 (31 vs
 *     32), `space_14` is 36.75 (36 vs 37).
 *
 * None of those is visible in a screenshot on its own, and all of them are visible together: the
 * kit renders a design that is uniformly a fraction of a pixel tighter than the one specified, in
 * a direction no reviewer would think to look.
 *
 * THE FIRST ASSERTION IS THAT THE QUALIFIER TOOK. A `@Config(qualifiers = ...)` that Robolectric
 * ignored would leave this suite running at mdpi, where every claim below passes over a
 * distinction that does not exist -- which is the same shape of vacuous green the truncation
 * itself hid behind.
 */
@RunWith(RobolectricTestRunner::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(qualifiers = "420dpi")
class KitDensityTest {

    private val context: Context
        get() = SwarmTheme.applyTo(ApplicationProvider.getApplicationContext())

    private val density: Float get() = context.resources.displayMetrics.density

    private fun px(dp: Float) = dp * density

    private fun dimen(name: String): Float {
        val id = context.resources.getIdentifier(name, "dimen", context.packageName)
        assertNotEquals("R.dimen.$name is not in the merged resource table", 0, id)
        return context.resources.getDimension(id)
    }

    /** What the platform spends for a step: `getDimensionPixelSize`, which rounds half up. */
    private fun dimenPx(name: String): Int = dimen(name).roundToInt()

    /** The cast the kit used to spend it with. */
    private fun truncated(name: String): Int = dimen(name).toInt()

    @Test
    fun `the qualifier really put this suite at a fractional density`() {
        assertTrue(
            "this suite is running at density $density, where truncating a dimension and " +
                "rounding it give the same answer -- so every claim in this file is about a " +
                "distinction that does not exist here. @Config(qualifiers = \"420dpi\") did not " +
                "take.",
            density != density.toInt().toFloat(),
        )
        val differ = listOf("swarm_space_4", "swarm_space_6", "swarm_space_12", "swarm_space_14")
            .filter { dimenPx(it) != truncated(it) }
        assertTrue(
            "not one of the scale steps this file asserts against resolves to a fractional " +
                "pixel count at density $density, so a component that truncated its dimensions " +
                "would pass every claim below",
            differ.isNotEmpty(),
        )
    }

    /**
     * The hairline, which is the one that matters most and the one a cast damages worst.
     *
     * Substrate's own rule is that elevation is a ladder step lighter and never a shadow, so a
     * card's separation from the ground is its 1 dp `--p-hair` border and nothing else. 2 px where
     * the platform draws 3 is a 33% error on the only depth cue the skin has.
     */
    @Test
    fun `the hairline is the whole pixels the platform would spend, not the truncated ones`() {
        val hairline = px(KitOrigin.cssFirstPx(".prow", "border"))
        val card = cardSurface(context, attention = false)
        val chip = chipSurface(context, selected = false)
        // THE THIRD SITE, WHICH THIS SUITE DID NOT COVER AND WHICH WAS THE ONE THAT WAS WRONG.
        // `--p-hair` at 1 dp is drawn in three places: the card's border, the chip's border and
        // the bar's top rule. The first two were asserted here; the third was not, and it was the
        // only one still spending `Kit.dp` -- a Float, 2.625 px at this density, against the 3 px
        // the other two spend. The gap in the coverage was exactly the shape of the defect: the
        // one path nothing looked at is the one path that drifted.
        val rule = tabBar(context, listOf(TabItem("Inbox"))).background as TopRule

        assertEquals(
            emptyList<String>(),
            mismatches(
                listOf(
                    Claim("`.prow` hairline", hairline.roundToInt(), card.spec.strokeWidthPx),
                    Claim("`.chip` hairline", hairline.roundToInt(), chip.spec.strokeWidthPx),
                    Claim("`.ptabs` top rule", hairline.roundToInt().toFloat(), rule.rulePx),
                ),
            ),
        )
        assertNotEquals(
            "at density $density the design's 1dp hairline is ${hairline}px, and rounding it is " +
                "the same as truncating it -- so this test cannot tell the two apart and the " +
                "assertion above says nothing",
            hairline.toInt(),
            hairline.roundToInt(),
        )
        // The control, on the comparison the assertion above uses: the truncated stroke must fail.
        assertTrue(
            "a hairline truncated to ${hairline.toInt()}px passes the comparison against the " +
                "${hairline.roundToInt()}px the platform spends",
            mismatches(
                listOf(Claim("hairline", hairline.roundToInt(), hairline.toInt())),
            ).isNotEmpty(),
        )
        // And the control for the rule claim, which compares Floats and therefore goes through
        // [mismatches]'s 0.01 tolerance rather than through equality. The unrounded value is the
        // one the bar actually shipped, so this is the shipped defect fed to the same function the
        // assertion above calls -- a tolerance wide enough to swallow 2.625 against 3 would leave
        // that claim certifying nothing.
        assertTrue(
            "the top rule at its exact ${hairline}px passes the comparison against the " +
                "${hairline.roundToInt()}px the card and the chip spend, so the claim above " +
                "cannot tell the two renderings apart",
            mismatches(
                listOf(Claim("`.ptabs` top rule", hairline.roundToInt().toFloat(), hairline)),
            ).isNotEmpty(),
        )
    }

    /** Every scale step a kit component spends, resolved the way the platform resolves it. */
    @Test
    fun `the components spend the whole pixels the platform would`() {
        val row = sessionRow(context, "quanthome/api", "claude", "Wants to run something", "working")
        val chips = chipRow(context)
        val label = sectionLabel(context, "Needs you")
        val header = navHeader(context, "Inbox", "3 LIVE")
        val bar = tabBar(context, listOf(TabItem("Inbox", selected = true, badgeCount = 3)))
        val pill = bar.getChildAt(0).kitRequire(KitTag.BADGE)

        assertEquals(
            emptyList<String>(),
            mismatches(
                listOf(
                    Claim("`.prow` padding-y", dimenPx("swarm_space_10"), row.paddingTop),
                    Claim("`.prow` padding-x", dimenPx("swarm_space_12"), row.paddingStart),
                    Claim("`.chips` padding-x", dimenPx("swarm_space_18"), chips.paddingStart),
                    Claim("`.chips` padding-bottom", dimenPx("swarm_space_12"), chips.paddingBottom),
                    Claim("`.plabel` padding-x", dimenPx("swarm_space_18"), label.paddingStart),
                    Claim("`.plabel` padding-bottom", dimenPx("swarm_space_8"), label.paddingBottom),
                    Claim("`.pnav` padding-top", dimenPx("swarm_space_4"), header.paddingTop),
                    // `.ptabs { padding-bottom }` is not here because the bar no longer spends
                    // one: that 14 px is the mock's home indicator and the real inset comes from
                    // `PhoneActivity.insetTheSystemBars`. There is no rounding to check in a
                    // padding that does not exist, and the bar's bottom air is asserted where the
                    // rest of its chrome is, in InboxChromeTest.
                    Claim("`.ptabs` height", dimenPx("swarm_tabbar_height"), bar.layoutParams.height),
                    Claim("badge padding-y", dimenPx("swarm_space_2"), pill.paddingTop),
                    Claim("badge padding-x", dimenPx("swarm_space_6"), pill.paddingStart),
                ),
            ),
        )
    }

    /**
     * The dp constants the resource table cannot carry, which are spent through [Kit.dp] and were
     * truncated at the same call sites.
     */
    @Test
    fun `the kit metrics are the whole pixels the platform would spend`() {
        val bar = row("working").kitRequire(KitTag.WORKBAR)
        val tabs = tabBar(context, listOf(TabItem("Inbox", selected = true, badgeCount = 3)))
        val icon = tabs.getChildAt(0).kitRequire(KitTag.TAB_ICON)
        val pill = tabs.getChildAt(0).kitRequire(KitTag.BADGE)

        assertEquals(
            emptyList<String>(),
            mismatches(
                listOf(
                    Claim(
                        "`.workbar` height",
                        px(KitOrigin.cssDp(".workbar", "height")).roundToInt(),
                        bar.layoutParams.height,
                    ),
                    Claim(
                        "`.ptabs svg` size",
                        px(KitOrigin.cssDp(".ptabs svg", "width")).roundToInt(),
                        icon.layoutParams.width,
                    ),
                    // Row 3's `height 16`, joined to the row itself by the Go gate.
                    Claim(
                        "badge height",
                        px(KitMetrics.BADGE_HEIGHT_DP).roundToInt(),
                        pill.layoutParams.height,
                    ),
                ),
            ),
        )
        assertNotEquals(
            "the design's 22dp tab icon is a whole number of pixels at density $density, so this " +
                "claim cannot tell a truncated size from a rounded one",
            px(KitOrigin.cssDp(".ptabs svg", "width")).toInt(),
            px(KitOrigin.cssDp(".ptabs svg", "width")).roundToInt(),
        )
    }

    /**
     * The dot's halo needs room in its layer at every density, and the compensation that keeps the
     * mark's footprint at 7 dp has to be exact in whole pixels -- not exact in dp and rounded
     * three times.
     */
    @Test
    fun `the status dot still occupies exactly its core, with room for the halo`() {
        val core = px(KitOrigin.cssDp(".pdot", "width")).roundToInt()
        val glow = px(KitOrigin.cssFirstPx(".pdot.att", "box-shadow")).roundToInt()
        val dot = statusDot(context, "needs_input")
        val params = dot.layoutParams as android.view.ViewGroup.MarginLayoutParams

        assertEquals(
            emptyList<String>(),
            mismatches(
                listOf(
                    Claim("layer width", core + 2 * glow, params.width),
                    Claim("layer height", core + 2 * glow, params.height),
                    Claim(
                        "footprint",
                        core,
                        params.width + params.marginStart + params.marginEnd,
                    ),
                ),
            ),
        )
    }

    private fun row(group: String) = sessionRow(
        context, "quanthome/api", "claude", "Wants to run something", group,
    )
}
