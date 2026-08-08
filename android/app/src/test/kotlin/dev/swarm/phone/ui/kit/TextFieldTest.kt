package dev.swarm.phone.ui.kit

import android.content.Context
import android.util.TypedValue
import androidx.test.core.app.ApplicationProvider
import dev.swarm.phone.theme.SwarmTheme
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotEquals
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.GraphicsMode
import kotlin.math.roundToInt

/**
 * FAILING-FIRST (TDD RED, GG-5) for PB-DS-10 over derivation row 9's field.
 *
 * THE PLACEHOLDER'S INK IS THE ASSERTION THAT MATTERS. `--p-ink3` is the obvious choice for a hint
 * -- it is what "de-emphasised" looks like everywhere else in this kit -- and row 9 rejects it in
 * numbers: 3.50:1 on the well, under the 4.5:1 text floor, against `--p-ink2`'s 6.21:1. It is also
 * the site where that matters most, because `PhoneSurface` has no XML layouts and every field on
 * it is identified by its hint alone. So the wrong answer here is not a shade, it is a field a
 * user cannot read the name of.
 */
@RunWith(RobolectricTestRunner::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
class TextFieldTest {

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

    /** The design's px is Android dp at 1:1 -- the artifact is a 386x812 frame at device scale. */
    private fun dp(value: Float): Float = TypedValue.applyDimension(
        TypedValue.COMPLEX_UNIT_DIP, value, context.resources.displayMetrics,
    )

    /** A phone's content width, so a MATCH_PARENT field measures against something real. */
    private val PARENT_WIDTH_DP = 360f

    private fun field() = textField(context, "Paste the pairing code your machine printed")

    @Test
    fun `the field is the design's body copy in the primary ink`() {
        val claims = KitOrigin.textClaims(
            view = field(),
            // `.m2` is what type.xml records as Body.Message's origin, which row 9 spends.
            selector = ".m2",
            ink = KitOrigin.token("--p-ink"),
            spScale = spScale,
        )

        assertTrue(
            "the pitch probe cannot answer: ${KitOrigin.typefaceProbeFaults()}",
            KitOrigin.typefaceProbeFaults().isEmpty(),
        )
        assertEquals(mismatches(claims).joinToString("\n"), emptyList<String>(), mismatches(claims))
    }

    @Test
    fun `the placeholder is the secondary ink and not the tertiary`() {
        assertEquals(KitOrigin.token("--p-ink2"), field().currentHintTextColor)
        assertNotEquals(
            "the hint is --p-ink3, which is 3.50:1 on this well -- under the text floor, on the " +
                "one string that names the field",
            KitOrigin.token("--p-ink3"),
            field().currentHintTextColor,
        )
    }

    @Test
    fun `the field is a well and shares the one recessed surface`() {
        val spec = (field().background as SubstrateSurface).spec

        // Row 9 inverts the mock: the field is `--p-well`, not a lighter fill. Compared against a
        // freshly built wellSurface rather than against transcribed values, so what is asserted is
        // REUSE -- a transcription passes even after the two have diverged.
        assertEquals(wellSurface(context).spec, spec)
        assertEquals(KitOrigin.token("--p-well"), spec.fill)
    }

    @Test
    fun `the field spends row 9's steps`() {
        val subject = field()
        val claims = listOf(
            Claim("row 9 padding-y (top)", dimenPx("swarm_space_8"), subject.paddingTop),
            Claim("row 9 padding-y (bottom)", dimenPx("swarm_space_8"), subject.paddingBottom),
            Claim("row 9 padding-x (start)", dimenPx("swarm_space_14"), subject.paddingStart),
            Claim("row 9 padding-x (end)", dimenPx("swarm_space_14"), subject.paddingEnd),
        )

        assertEquals(mismatches(claims).joinToString("\n"), emptyList<String>(), mismatches(claims))
    }

    /**
     * Row 9 states a target and a smaller visual in the same cell, and it is the only row that does:
     * "field padding `space_8` x `space_14`, visual height 36, touch target 48".
     *
     * BOTH NUMBERS OR NEITHER. A field grown to 48 dp meets the floor and loses the 36 dp well the
     * same sentence specifies; a 36 dp field keeps the well and cannot be hit. What the row asks for
     * is 48 dp of target around 36 dp of paint, so the well is inset inside the target rather than
     * filling it -- and the assertion reads the PAINTED band, not the view, because a well that
     * quietly grew to 48 would satisfy every other check in this file.
     */
    @Test
    fun `the field is 48 dp of target around row 9's 36 dp well`() {
        val subject = field()
        // The two numbers are the KIT's constants and not this file's: `s23_kit_test.go` is what
        // holds each of them to the row it cites, which is the arrangement every metric in this
        // package is already in.
        val faults = touchTargetFaults(
            subject,
            dp(KitMetrics.MIN_TARGET_DP).roundToInt(),
            dp(PARENT_WIDTH_DP).roundToInt(),
        )

        assertEquals(faults.joinToString("\n"), emptyList<String>(), faults)

        val well = subject.background as SubstrateSurface
        val painted = subject.measuredHeight - well.getLayerInsetTop(0) - well.getLayerInsetBottom(0)
        assertEquals(
            "row 9's well is 36 dp and the field is 48; a painted band of any other height is " +
                "either the target eating the visual or the visual eating the target",
            dp(KitMetrics.WELL_HEIGHT_DP).roundToInt(),
            painted,
        )
    }

    @Test
    fun `the field says what it was given`() {
        assertEquals("Which agent to start", textField(context, "Which agent to start").hint)
    }

    // ---- the mono variant (agents-tracker-ksvb.7) ---------------------------

    /**
     * FAILING-FIRST for agents-tracker-ksvb.7's part (b): the terminal composer types directly
     * under `Mono.Code` grid content and has always taken `Body.Message` -- 12.5sp proportional
     * Roboto -- like every other field on the surface. The one field that must render what a
     * person types the way the terminal will echo it is the one field that did not.
     *
     * `MONO = TRUE` SPENDS THE SAME `.sheet2 .cmd` ORIGIN [monoWell] DOES, and the claim is built
     * the same way MonoWellTest's is -- one design rule, checked at its second call site rather
     * than transcribed into a second one.
     */
    @Test
    fun `the mono variant is the well's own Mono Code face, not row 9's Body Message`() {
        val claims = KitOrigin.textClaims(
            view = textField(context, "Type into the session you hold", mono = true),
            selector = ".sheet2 .cmd",
            ink = KitOrigin.token("--p-ink"),
            spScale = spScale,
        )

        assertTrue(
            "the pitch probe cannot answer: ${KitOrigin.typefaceProbeFaults()}",
            KitOrigin.typefaceProbeFaults().isEmpty(),
        )
        assertEquals(mismatches(claims).joinToString("\n"), emptyList<String>(), mismatches(claims))
    }

    /**
     * Row 9's target/well split does not belong to the type choice: `WELL_HEIGHT_DP` and
     * `MIN_TARGET_DP` come from `minimumHeight` and the background's layer insets, neither of
     * which this variant touches. Asserted anyway, because "the mono variant fits in the well" is
     * exactly the claim a font swap could quietly break.
     */
    @Test
    fun `the mono variant keeps row 9's 36 dp well inside the 48 dp target`() {
        val subject = textField(context, "Type into the session you hold", mono = true)
        val faults = touchTargetFaults(
            subject,
            dp(KitMetrics.MIN_TARGET_DP).roundToInt(),
            dp(PARENT_WIDTH_DP).roundToInt(),
        )

        assertEquals(faults.joinToString("\n"), emptyList<String>(), faults)

        val well = subject.background as SubstrateSurface
        val painted = subject.measuredHeight - well.getLayerInsetTop(0) - well.getLayerInsetBottom(0)
        assertEquals(
            "the mono variant's well is not row 9's 36 dp; a taller line box ate into the " +
                "48 dp target or the well shrank inside it",
            dp(KitMetrics.WELL_HEIGHT_DP).roundToInt(),
            painted,
        )
    }

    /** The negative control, through the same comparison the assertions above use. */
    @Test
    fun `the text field assertions can actually fail`() {
        val ink2 = KitOrigin.token("--p-ink2")
        val step = dimenPx("swarm_space_8")

        assertTrue(
            "an ink one unit from the origin's passes the comparison",
            mismatches(listOf(Claim("hint ink", ink2, ink2 + 1))).isNotEmpty(),
        )
        assertTrue(
            "a padding one pixel from row 9's passes the comparison",
            mismatches(listOf(Claim("padding-y", step, step + 1))).isNotEmpty(),
        )
        assertNotEquals(
            "the tertiary ink compares equal to the secondary, so the placeholder assertion is " +
                "about nothing",
            KitOrigin.token("--p-ink2"),
            KitOrigin.token("--p-ink3"),
        )
        assertNotEquals(
            "the well surface compares equal to the card's, so the reuse assertion would accept " +
                "a field painted as a card -- which is exactly the mock's arrangement row 9 inverts",
            wellSurface(context).spec,
            cardSurface(context, attention = false).spec,
        )
    }
}
