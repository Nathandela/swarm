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

    @Test
    fun `the field says what it was given`() {
        assertEquals("Which agent to start", textField(context, "Which agent to start").hint)
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
