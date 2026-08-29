package dev.swarm.phone.ui.kit

import android.content.Context
import android.util.TypedValue
import android.view.View
import android.view.ViewGroup
import android.widget.LinearLayout
import android.widget.TextView
import androidx.test.core.app.ApplicationProvider
import dev.swarm.phone.theme.SwarmTheme
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotEquals
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.GraphicsMode
import kotlin.math.roundToInt

/**
 * FAILING-FIRST (TDD RED, GG-5) for PB-DS-10 over derivation row 15 -- the flat grouped
 * settings row and its caller-owned trailing control.
 *
 * WHAT THIS SUITE IS AND IS NOT ASSERTING. The row's surface is the signed Slate `.trow`:
 * screen ground with one bottom hairline, no rounded card or key light. This suite holds that
 * structural decision plus the row's content gutter; typography and controls remain kit-owned.
 *
 * EVERY OTHER EXPECTATION COMES FROM THE DESIGN. The type and the inks resolve through
 * [KitOrigin]; the padding is joined to row 15 by `s23DerivedSpacing` in the Go lane, which reads
 * the table rather than this file.
 */
@RunWith(RobolectricTestRunner::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
class SettingsRowTest {

    private val context: Context
        get() = SwarmTheme.applyTo(ApplicationProvider.getApplicationContext())

    private val spScale: Float
        get() = TypedValue.applyDimension(
            TypedValue.COMPLEX_UNIT_SP, 1f, context.resources.displayMetrics,
        )

    private fun dimenPx(name: String): Int {
        val id = context.resources.getIdentifier(name, "dimen", context.packageName)
        assertNotEquals("R.dimen.$name is not in the merged resource table", 0, id)
        return context.resources.getDimension(id).roundToInt()
    }

    private fun row(
        label: CharSequence = "Needs your decision",
        sublabel: CharSequence? = "Approvals and blocked prompts",
        trailing: View? = null,
    ) = settingsRow(context, label, sublabel, trailing)

    /** The design's px is Android dp at 1:1 -- the artifact is a 386x812 frame at device scale. */
    private fun dp(value: Float): Float = TypedValue.applyDimension(
        TypedValue.COMPLEX_UNIT_DIP, value, context.resources.displayMetrics,
    )

    /** A phone's content width, so a MATCH_PARENT component measures against something real. */
    private val PARENT_WIDTH_DP = 360f

    /**
     * Rows 4 and 15 are one instruction: "the whole row is one >=48 dp target when it carries a
     * toggle", which is also where row 4 puts the toggle's own ">=48 with the visual unchanged".
     *
     * THE ROW IS THE SUBJECT AND THE TOGGLE IS NOT, and that is the point rather than a shortcut. A
     * 46x28 control grown to 48 dp meets the floor by destroying the drawing the same clause
     * protects; a row that is one target keeps both. Asserted with the toggle in place, because
     * that is the configuration both rows are about.
     *
     * WITH NO SUBLABEL, which is the shape where the floor binds. Two text lines and 12 dp of
     * padding clear 48 dp on their own, so a row asserted in its tallest configuration would pass
     * whatever the component did -- and the single-line row is the one a caller reaches for when
     * the label says everything.
     */
    @Test
    fun `a row carrying a toggle is one target that clears PB-DS-12's floor`() {
        val faults = touchTargetFaults(
            row(
                sublabel = null,
                trailing = toggle(context, checked = true, description = "End-to-end encryption"),
            ),
            dp(KitMetrics.MIN_TARGET_DP).roundToInt(),
            dp(PARENT_WIDTH_DP).roundToInt(),
        )

        assertEquals(faults.joinToString("\n"), emptyList<String>(), faults)
    }

    // ---- the type and the inks -------------------------------------------

    @Test
    fun `the label and the sublabel are the design's two text roles`() {
        val subject = row()

        val claims = KitOrigin.textClaims(
            view = subject.kitRequire(KitTag.SETTINGS_LABEL) as TextView,
            // `.prow .pj` is what type.xml records as Title.Row's origin, and row 15 spends
            // Title.Row -- so the size, the tracking and the family come from the artifact.
            selector = ".prow .pj",
            ink = KitOrigin.token("--p-ink"),
            spScale = spScale,
        ) + KitOrigin.textClaims(
            view = subject.kitRequire(KitTag.SETTINGS_SUBLABEL) as TextView,
            selector = ".prow .ln",
            ink = KitOrigin.token("--p-ink2"),
            spScale = spScale,
        )

        assertTrue(
            "the pitch probe cannot answer: ${KitOrigin.typefaceProbeFaults()}",
            KitOrigin.typefaceProbeFaults().isEmpty(),
        )
        assertEquals(mismatches(claims).joinToString("\n"), emptyList<String>(), mismatches(claims))
    }

    // ROW 15'S STATUS-TEXT FORM IS RETIRED AND ITS CLAIM DIED WITH IT (agents-tracker-2pnu F5,
    // agents-tracker-zecs). The test here read:
    //
    //   `the status label is hero and not ok` -- "THE INK IS THE POINT OF THE ASSERTION. `--p-ok`
    //   is the plausible wrong answer ... and after ADR-007 B134 it carries ReadyForReview, so it
    //   would be one colour saying two unrelated things on two screens."
    //
    // The argument was right and it was about a component with no caller: `statusLabel`'s only
    // would-be one was inventory C6's `End-to-end encryption` row, which `SettingsPanelScreen`
    // records as deliberately unbuilt -- "nothing on this handset READS it". The ink rule it
    // protected is not lost: `--p-hero`'s meaning is stated in android/design-tokens.tsv and the
    // one production spend of it is asserted where that spend is.

    @Test
    fun `the row is flat ground with one bottom hairline rather than a rounded card`() {
        val subject = row()
        val surface = subject.background as? BottomRule

        assertNotNull("the settings row is still an individual card: ${subject.background}", surface)
        assertEquals(Kit.colour(context, dev.swarm.phone.R.color.swarm_background), surface!!.fill)
        assertEquals(Kit.colour(context, dev.swarm.phone.R.color.swarm_hairline), surface.rule)
        assertEquals(Kit.dpPx(context, KitMetrics.HAIRLINE_DP).toFloat(), surface.rulePx)
        assertTrue("a flat settings row cannot carry a card surface", subject.background !is SubstrateSurface)
    }

    @Test
    fun `the ruled row aligns its content to the signed Slate group`() {
        val subject = row()

        assertEquals(dimenPx("swarm_space_18"), subject.paddingStart)
        assertEquals(dimenPx("swarm_space_18"), subject.paddingEnd)
        assertEquals(dimenPx("swarm_space_14"), subject.paddingTop)
        assertEquals(dimenPx("swarm_space_14"), subject.paddingBottom)
    }

    // ---- composition ------------------------------------------------------

    @Test
    fun `a row with no sublabel renders no second line at all`() {
        val subject = row(sublabel = null)

        assertNull(
            "an absent sublabel still produced a view. A blank TextView occupies its line height " +
                "and its gap, so the row would sit taller than its neighbours for no visible reason",
            subject.kitFind(KitTag.SETTINGS_SUBLABEL),
        )
        assertNotNull(subject.kitFind(KitTag.SETTINGS_LABEL))
    }

    /**
     * A trailing control as the kit builds one: a view carrying its own
     * `LinearLayout.LayoutParams`. See the gap claim below for why that is the contract and not
     * scaffolding.
     */
    private fun trailingStandIn(): View = View(context).apply {
        layoutParams = LinearLayout.LayoutParams(
            ViewGroup.LayoutParams.WRAP_CONTENT,
            ViewGroup.LayoutParams.WRAP_CONTENT,
        )
    }

    @Test
    fun `the trailing control is placed with the row's own gap`() {
        // ANY VIEW THE KIT BUILDS, WHICH IS THE COMPONENT'S OWN CLAIM. The row takes a trailing
        // `View` rather than a variant it switches on, so the subject here is a stand-in: it used
        // to be `statusLabel(context, "active")`, and asserting the gap through a specific
        // factory made this read as a claim about that factory rather than about the slot.
        //
        // IT CARRIES ITS OWN LAYOUT PARAMS, and that is the contract rather than test scaffolding.
        // `settingsRow` writes the gap onto the child's existing `LinearLayout.LayoutParams`
        // BEFORE the addView that would otherwise mint them, so a trailing view with none takes
        // no gap at all. Every kit factory sets its own, which is what makes the rule hold at
        // every real call site.
        val control = trailingStandIn()
        val subject = row(trailing = control)

        assertEquals(
            "the trailing control is not in the row at all",
            control,
            subject.getChildAt(1),
        )
        assertEquals(
            dimenPx("swarm_space_10"),
            (control.layoutParams as LinearLayout.LayoutParams).marginStart,
        )
    }

    @Test
    fun `the text takes the slack so the control sits hard right`() {
        val subject = row(trailing = trailingStandIn())
        val text = subject.getChildAt(0)

        // `flex: 1`. Without it a long label pushes the control off the row rather than wrapping.
        assertEquals(1f, (text.layoutParams as LinearLayout.LayoutParams).weight, 0.001f)
        assertEquals(0, text.layoutParams.width)
    }

    @Test
    fun `the row says what it was given and invents nothing`() {
        val subject = row(label = "Quiet hours", sublabel = "23:00 - 07:30")

        assertEquals("Quiet hours", (subject.kitRequire(KitTag.SETTINGS_LABEL) as TextView).text)
        assertEquals(
            "23:00 - 07:30",
            (subject.kitRequire(KitTag.SETTINGS_SUBLABEL) as TextView).text,
        )
    }

    /**
     * The negative control PB-DS-10 requires, through the SAME comparison the assertions above
     * use. Each perturbation is the smallest one that matters, and the last is the specific
     * mistake this component invites: painting its own card instead of reusing the kit's.
     */
    @Test
    fun `the settings row assertions can actually fail`() {
        val hero = KitOrigin.token("--p-hero")
        val gap = dimenPx("swarm_space_10")

        assertTrue(
            "an ink one unit from the origin's passes the comparison",
            mismatches(listOf(Claim("status ink", hero, hero + 1))).isNotEmpty(),
        )
        assertTrue(
            "a gap one pixel from the row's passes the comparison",
            mismatches(listOf(Claim("gap", gap, gap + 1))).isNotEmpty(),
        )
    }
}
