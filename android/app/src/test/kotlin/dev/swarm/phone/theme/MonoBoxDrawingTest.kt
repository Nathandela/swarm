package dev.swarm.phone.theme

import android.graphics.Paint
import android.graphics.Typeface
import android.widget.TextView
import androidx.test.core.app.ApplicationProvider
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotEquals
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.GraphicsMode

/**
 * FAILING-FIRST (TDD RED, GG-5) for PB-DS-3's second criterion: "a test renders a box-drawing
 * string through it so the residual is OBSERVED rather than assumed."
 *
 * WHAT THE RESIDUAL WAS RECORDED AS. ADR-007 B134 decision 2: "Android's `monospace` is Droid
 * Sans Mono, which does not cover U+2500-257F. A terminal peek carrying box-drawing from an agent
 * TUI may render TOFU." The upgrade path is bundling JetBrains Mono and it is deliberately not
 * taken until the peek is seen to need it.
 *
 * WHAT RENDERING IT ACTUALLY SHOWS -- and this is why the requirement asked for a test rather
 * than a paragraph. It is NOT tofu. Every box-drawing character resolves, through FONT FALLBACK,
 * to a glyph in a different font at a DIFFERENT ADVANCE WIDTH: 0.71em against the monospace
 * family's own 0.60em. The frame is drawn, and it is 18% wider per character than the text it
 * frames, so `┌─ claude · swarm ─┐` and the lines beneath it do not line up. A missing glyph
 * would have been obvious to anyone who looked at the screen once; a frame that is silently
 * 18% off is the failure that ships.
 *
 * WHY `Paint.hasGlyph` IS NOT THE TEST. It returns TRUE for every one of these characters,
 * because it consults the whole fallback chain rather than the named family. A test built on it
 * would be green, would look like coverage, and would certify the opposite of the truth. That is
 * the exact shape this repository has had to reject before, so the measurement below is an
 * ADVANCE WIDTH comparison and hasGlyph is asserted only to record that tofu is NOT what happens.
 *
 * GRAPHICS MODE IS NATIVE AND MUST BE. Robolectric's default LEGACY graphics stubs the whole
 * text stack: `measureText` returns one pixel per character, `hasGlyph` returns false for the
 * letter A, and `Typeface.getWeight` returns 0. Every assertion here would pass or fail for
 * reasons unconnected to any font. `TEXT_MEASUREMENT_IS_REAL` below is the guard that fails
 * loudly if this annotation is ever removed.
 *
 * THE LIMIT OF THIS EVIDENCE, stated because the point of the test is honesty about the residual:
 * what is measured is the font configuration in Robolectric's Android runtime, which is AOSP's --
 * the same Droid Sans Mono and Noto fallback set a stock handset ships. It is not a survey of
 * every OEM's font customisation. It is enough to settle the question the ADR left open, which
 * was whether the peek renders the frame at all.
 */
@RunWith(RobolectricTestRunner::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
class MonoBoxDrawingTest {

    /**
     * The frame the terminal peek actually draws, verbatim from the design's §C3 content:
     * U+250C, U+2500, U+252C, U+2510, U+2502, U+2514, U+2534, U+2518.
     */
    private val boxDrawing = "┌─┬─┐ │ └─┴─┘"

    /**
     * The Paint a real TextView ends up holding after the style is applied.
     *
     * Through [TextView.setTextAppearance] and not a hand-built Paint, because the question is
     * what the APP renders the peek with: the path from an `android:fontFamily` string to a
     * Typeface runs through TextView, and a family name Android cannot resolve does not throw --
     * it falls back to the default sans, silently.
     */
    private fun paintFor(style: String): Paint {
        val context = SwarmTheme.applyTo(ApplicationProvider.getApplicationContext())
        val res = context.resources.getIdentifier(style, "style", context.packageName)
        assertNotEquals(
            "R.style.${style.replace('.', '_')} is not in the resource table; this test would " +
                "measure a default typeface rather than the app's",
            0,
            res,
        )
        return TextView(context).apply { setTextAppearance(res) }.paint
            .apply { textSize = MEASURE_SIZE }
    }

    /**
     * The mono style really is fixed-pitch, and the sans style really is not.
     *
     * ASSERTED ON BEHAVIOUR, NOT ON IDENTITY. Comparing the resolved typeface against
     * [Typeface.MONOSPACE] fails even when everything is correct: TextView applies
     * `android:textFontWeight` by way of `Typeface.create(base, weight, italic)`, so what a view
     * holds is a derived instance rather than the family singleton. Fixed advance is the property
     * the terminal peek actually depends on, and it survives that derivation.
     *
     * THE SANS HALF IS THE NEGATIVE CONTROL. Without it, an app in which android:fontFamily was
     * ignored entirely -- and every style fell back to one default -- would pass the mono
     * assertion if that default happened to be fixed-pitch, and would pass it vacuously if the
     * measurement were stubbed. Requiring the two styles to measure DIFFERENTLY proves the family
     * attribute is reaching the typeface at all.
     */
    @Test
    fun `the mono style is fixed-pitch and the sans style is not`() {
        val mono = paintFor(MONO_STYLE)
        val narrow = mono.measureText("i")
        val wide = mono.measureText("W")
        assertEquals(
            "the mono style's typeface gives `i` and `W` different advances, so it is not " +
                "fixed-pitch. A font family name Android cannot resolve falls back to the default " +
                "sans without failing, and the terminal peek stops being a terminal.",
            narrow,
            wide,
            0f,
        )
        assertEquals(
            "five characters do not measure five advances; the peek's alignment cannot survive that",
            5 * narrow,
            mono.measureText("ABCDE"),
            0f,
        )

        val sans = paintFor(SANS_STYLE)
        assertNotEquals(
            "the sans style measures `i` and `W` identically too, so either every style resolves " +
                "to one family -- android:fontFamily is not reaching the typeface -- or text " +
                "measurement is stubbed. Either way the assertion above proves nothing.",
            sans.measureText("i"),
            sans.measureText("W"),
        )
    }

    /**
     * The requirement's own words: render the box-drawing string and observe what happens.
     *
     * This test PINS the residual. If someone bundles JetBrains Mono -- the recorded upgrade path
     * -- the advances converge and this fails, which is the intended behaviour: the ADR's
     * "recorded residual" paragraph has to be rewritten at the moment the residual stops being
     * real, and this is the thing that says so.
     */
    @Test
    fun `box drawing renders through fallback at a different advance than the mono family`() {
        val paint = paintFor(MONO_STYLE)
        val monoAdvance = paint.measureText("M")

        // Negative control FIRST. Under Robolectric's default LEGACY graphics every measurement
        // is one pixel per character, which would make the inequality below true for a reason
        // that has nothing to do with fonts.
        assertTrue(
            "text measurement is stubbed: `M` at ${MEASURE_SIZE}px measures $monoAdvance. This " +
                "test needs @GraphicsMode(NATIVE); without it Paint.measureText returns one " +
                "pixel per character and every assertion here is about nothing.",
            monoAdvance > TEXT_MEASUREMENT_IS_REAL,
        )

        // 1. It is NOT tofu. The glyphs resolve -- which is the half of the ADR's recorded
        //    residual that turns out to be wrong.
        boxDrawing.filter { !it.isWhitespace() }.forEach { c ->
            assertTrue(
                "U+%04X does not resolve to any glyph, so the peek really does render tofu and "
                    .format(c.code) +
                    "the ADR's recorded residual is exactly right. Bundling JetBrains Mono is " +
                    "the recorded upgrade path.",
                paint.hasGlyph(c.toString()),
            )
        }

        // 2. And they do not come from the mono family: the advance differs.
        val boxAdvance = paint.measureText("┌")
        assertNotEquals(
            "box drawing measures the same advance as the mono family, so it is covered by the " +
                "family itself and the frame aligns. The residual ADR-007 B134 records no longer " +
                "exists -- rewrite decision 2 rather than deleting this test.",
            monoAdvance,
            boxAdvance,
        )

        // 3. The whole frame is internally consistent -- every box character shares the fallback
        //    advance -- which is what makes the misalignment a uniform 18% rather than ragged.
        val glyphs = boxDrawing.filter { !it.isWhitespace() }
        assertEquals(
            "the box-drawing characters do not share one advance, so the frame is ragged rather " +
                "than merely mismatched",
            glyphs.length * boxAdvance,
            paint.measureText(glyphs),
            0f,
        )

        // 4. And the middle dot the peek's own copy uses (`esc to interrupt · ctrl+q detach`) IS
        //    in the mono family, which is what makes the box-drawing result a real finding rather
        //    than "non-ASCII falls back".
        assertEquals(
            "U+00B7 falls back too, so the finding above is `the mono family covers only ASCII` " +
                "rather than `it lacks the box-drawing block`",
            monoAdvance,
            paint.measureText("·"),
            0f,
        )

        println(
            "PB-DS-3 observed residual at ${MEASURE_SIZE}px: mono advance $monoAdvance, " +
                "box-drawing advance $boxAdvance " +
                "(${"%.1f".format(100 * (boxAdvance / monoAdvance - 1))}% wider), " +
                "tofu: none. The frame renders and does not align.",
        )
    }

    private companion object {
        /** The style the terminal peek and every command line render through. */
        const val MONO_STYLE = "TextAppearance.Swarm.Mono.Code"

        /** Any sans style: the contrast that proves android:fontFamily reaches the typeface. */
        const val SANS_STYLE = "TextAppearance.Swarm.Display.NavTitle"

        /**
         * Large enough that an advance difference is bigger than any rounding. The design renders
         * this style at 11.5sp; measuring there would put the two advances 1.7px apart and invite
         * a tolerance that swallows the finding.
         */
        const val MEASURE_SIZE = 100f

        /**
         * Legacy graphics returns exactly 1.0 per character. Anything above this is a real
         * measurement; the bound is deliberately far below any plausible advance at 100px.
         */
        const val TEXT_MEASUREMENT_IS_REAL = 2f
    }
}
