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
 * PB-DS-3's second criterion ("a test renders a box-drawing string through it so the residual is
 * OBSERVED rather than assumed"), now measuring the residual's END rather than its size.
 *
 * WHAT THIS FILE USED TO PIN, and what it says now.
 *
 * ADR-007 B134 predicted that Android's `monospace` would render U+2500-257F as tofu. This test
 * measured what actually happened and the truth was worse: every box-drawing character resolves,
 * through FONT FALLBACK, to a glyph in another family at a DIFFERENT ADVANCE WIDTH -- 0.71em
 * against the monospace family's own 0.60em. The frame is drawn, and it is 18% wider per
 * character than the text it frames, so `┌─ claude · swarm ─┐` and the lines beneath it do not
 * line up. A missing glyph would have been obvious to anyone who looked at the screen once; a
 * frame that is silently 18% off is the failure that ships.
 *
 * AUTHORIZED REWRITE, ADR-009 D8.3 / D7 (phase O5). The old assertion, quoted so the pin's move
 * is visible rather than inferred -- and quoted from a test that asked for exactly this in its
 * own words ("If someone bundles JetBrains Mono -- the recorded upgrade path -- the advances
 * converge and this fails, which is the intended behaviour"):
 *
 *     val boxAdvance = paint.measureText("┌")
 *     assertNotEquals(
 *         "box drawing measures the same advance as the mono family, so it is covered by the " +
 *             "family itself and the frame aligns. The residual ADR-007 B134 records no longer " +
 *             "exists -- rewrite decision 2 rather than deleting this test.",
 *         monoAdvance,
 *         boxAdvance,
 *     )
 *
 * It is now an EQUALITY, on the same measurement, at the same size, through the same path. The
 * assertion did not weaken; the app did. `assertNotEquals` is satisfied by any difference at all,
 * including a second wrong font; `assertEquals` at 0f tolerance names one outcome.
 *
 * THE PLATFORM HALF IS KEPT AND IS NOW THE NEGATIVE CONTROL. `the platform mono family still
 * lays box drawing at a different advance` asserts the defect still reproduces on
 * [Typeface.MONOSPACE]. Without it, an equality that passed because the whole text stack had
 * stopped distinguishing anything -- a stubbed measurement, a fallback chain that collapsed --
 * would read as a fix. The two tests together say: the defect is real, it is still there in the
 * platform family, and it is not there in ours.
 *
 * WHY `Paint.hasGlyph` IS NOT THE TEST. It returns TRUE for every one of these characters, on
 * both families, because it consults the whole fallback chain rather than the named family. A
 * test built on it would have been green through the entire life of the defect. It is asserted
 * below only to record that tofu is not what happens.
 *
 * GRAPHICS MODE IS NATIVE AND MUST BE. Robolectric's default LEGACY graphics stubs the whole text
 * stack: `measureText` returns one pixel per character, so every advance would be equal and this
 * file's central assertion would pass for a reason unconnected to any font. `TEXT_MEASUREMENT_IS_REAL`
 * is the guard that fails loudly if this annotation is ever removed.
 *
 * THE LIMIT OF THIS EVIDENCE, stated because the point of the test is honesty about the residual:
 * what is measured is Robolectric's Android runtime, which is AOSP's -- the same Droid Sans Mono
 * and Noto fallback set a stock handset ships -- plus this module's own resource table. It is not
 * a survey of every OEM's font customisation, and it is not the handset. ADR-009's phase O7
 * device pass is where the frame is looked at on glass.
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
     * what the APP renders the peek with: the path from an `android:fontFamily` value to a
     * Typeface runs through TextView, and a family Android cannot resolve does not throw -- it
     * falls back to the default sans, silently. That path is longer now than it was: the value is
     * a resource reference, so it also crosses the resource table, the font-family XML and two
     * TTFs, and every one of those is a place the bundle can fail to arrive.
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
                "fixed-pitch. A font family Android cannot resolve falls back to the default " +
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
     * THE DEFECT IS DEAD: the frame lays out on the same grid as the text it frames.
     *
     * This is the assertion the whole of phase O5 exists to make true, and it is one equality:
     * a box-drawing glyph advances exactly as far as an ASCII one, so a rule drawn out of U+2500
     * lines up with the characters beneath it.
     */
    @Test
    fun `box drawing lays out at the same advance as ASCII in the bundled family`() {
        val paint = paintFor(MONO_STYLE)
        val asciiAdvance = paint.measureText("M")

        // Negative control FIRST. Under Robolectric's default LEGACY graphics every measurement
        // is one pixel per character, which would make the equality below true for a reason that
        // has nothing to do with fonts -- and unlike the inequality it replaced, an equality is
        // the thing a stubbed measurement produces.
        assertTrue(
            "text measurement is stubbed: `M` at ${MEASURE_SIZE}px measures $asciiAdvance. This " +
                "test needs @GraphicsMode(NATIVE); without it Paint.measureText returns one " +
                "pixel per character, every advance is equal, and this test certifies nothing.",
            asciiAdvance > TEXT_MEASUREMENT_IS_REAL,
        )

        // 1. Every box-drawing character in the peek's frame resolves. Kept from the version of
        //    this test that measured the defect: it is what ruled out ADR-007 B134's original
        //    tofu prediction, and a bundled family that lost the block would bring it back.
        boxDrawing.filter { !it.isWhitespace() }.forEach { c ->
            assertTrue(
                "U+%04X does not resolve to any glyph".format(c.code) +
                    ", so the bundled family does not cover the block it was bundled for",
                paint.hasGlyph(c.toString()),
            )
        }

        // 2. THE ASSERTION. Each box-drawing character, individually, at the ASCII advance.
        //    Individually rather than as a total, because a sum can be right while two glyphs are
        //    wrong in opposite directions -- and a frame with one wide corner is still a frame
        //    that does not line up.
        boxDrawing.filter { !it.isWhitespace() }.forEach { c ->
            assertEquals(
                "U+%04X advances %.4f against ASCII's %.4f at ${MEASURE_SIZE}px"
                    .format(c.code, paint.measureText(c.toString()), asciiAdvance) +
                    ". The glyph is coming from somewhere other than the bundled family -- font " +
                    "fallback, or a family that did not load -- and the terminal peek's frame is " +
                    "back to being drawn on a different grid than its own text.",
                asciiAdvance,
                paint.measureText(c.toString()),
                0f,
            )
        }

        // 3. And the whole string, so a per-character equality that held could not hide a
        //    shaping rule that closed the gaps up when the characters are laid out together.
        val glyphs = boxDrawing.filter { !it.isWhitespace() }
        assertEquals(
            "the frame measures a different width than its own characters do one at a time",
            glyphs.length * asciiAdvance,
            paint.measureText(glyphs),
            0f,
        )

        // 4. The middle dot the peek's own copy uses (`esc to interrupt · ctrl+q detach`), kept
        //    for the reason it was added: it is the character that made the box-drawing result a
        //    finding about a missing BLOCK rather than about "non-ASCII falls back".
        assertEquals(
            "U+00B7 no longer measures the family advance",
            asciiAdvance,
            paint.measureText("·"),
            0f,
        )

        println(
            "ADR-009 D7 / O5 measured at ${MEASURE_SIZE}px: ASCII advance $asciiAdvance, " +
                "box-drawing advance ${paint.measureText("┌")}, " +
                "features `${paint.fontFeatureSettings}`. The frame aligns.",
        )
    }

    /**
     * THE DEFECT WAS REAL AND STILL IS, one family over.
     *
     * The equality above has a failure mode that looks exactly like success: a text stack that
     * stopped distinguishing anything measures every glyph the same and reports the frame as
     * fixed. So the same measurement is run against [Typeface.MONOSPACE] -- Droid Sans Mono, the
     * family every mono style in this app rendered in until O5 -- and the 18% mismatch must still
     * be there. If this test ever goes green, the platform gained the block, the bundle proves
     * nothing about the fix, and ADR-007 B134's recorded residual needs rewriting again.
     */
    @Test
    fun `the platform mono family still lays box drawing at a different advance`() {
        val platform = Paint().apply {
            typeface = Typeface.MONOSPACE
            textSize = MEASURE_SIZE
        }
        val asciiAdvance = platform.measureText("M")
        assertTrue(
            "text measurement is stubbed: `M` at ${MEASURE_SIZE}px measures $asciiAdvance",
            asciiAdvance > TEXT_MEASUREMENT_IS_REAL,
        )
        val boxAdvance = platform.measureText("┌")
        assertNotEquals(
            "Typeface.MONOSPACE now lays U+250C at its own advance, so the residual ADR-007 B134 " +
                "records has been fixed by the PLATFORM rather than by ADR-009 D7's bundle. The " +
                "equality in this file would then be passing for a reason that has nothing to do " +
                "with anything this repository did.",
            asciiAdvance,
            boxAdvance,
        )
        println(
            "ADR-007 B134 residual, still reproducing on Typeface.MONOSPACE at ${MEASURE_SIZE}px: " +
                "ASCII $asciiAdvance, box-drawing $boxAdvance " +
                "(${"%.1f".format(100 * (boxAdvance / asciiAdvance - 1))}% wider).",
        )
    }

    /**
     * ADR-009 D7's other half, on the Paint the app actually renders with.
     *
     * The Go gate asserts the attribute is declared in type.xml. That is a claim about a file;
     * this is the claim that matters, which is that the declaration survives
     * `setTextAppearance` and reaches the paint that lays out the text. They are different
     * questions and this repository has already paid once for assuming the first implies the
     * second.
     */
    @Test
    fun `the mono style's font features reach the paint`() {
        assertEquals(
            "the mono style's paint carries no font features, so tabular figures and the slashed " +
                "zero are declared in type.xml and switched on nowhere",
            MONO_FONT_FEATURES,
            paintFor(MONO_STYLE).fontFeatureSettings,
        )
        assertEquals(
            "a sans style carries the machine-data font features; `tnum` on a proportional face " +
                "is a width nobody asked for",
            null,
            paintFor(SANS_STYLE).fontFeatureSettings,
        )
    }

    private companion object {
        /** The style the terminal peek and every command line render through. */
        const val MONO_STYLE = "TextAppearance.Swarm.Mono.Code"

        /** Any sans style: the contrast that proves android:fontFamily reaches the typeface. */
        const val SANS_STYLE = "TextAppearance.Swarm.Display.NavTitle"

        /** ADR-009 D7's feature string, verbatim. */
        const val MONO_FONT_FEATURES = "tnum, zero, calt"

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
