package dev.swarm.phone.ui.kit

import android.content.Context
import android.util.TypedValue
import android.view.Gravity
import android.widget.LinearLayout
import androidx.test.core.app.ApplicationProvider
import dev.swarm.phone.theme.SwarmTheme
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.GraphicsMode
import kotlin.math.roundToInt

/**
 * FAILING-FIRST (TDD RED, GG-5) for PB-DS-10 over derivation row 22 -- the read-only note.
 *
 * WHAT RESOLVES FROM THE ORIGIN. Row 22 states one type role and one token, `Body.Secondary` /
 * `--p-ink2`, and both resolve from the staged artifact: [TypeScale] follows `type.xml`'s own
 * `origin:` comment to `.prow .ln` and reads the size, the tracking and the family out of the
 * design source; [KitOrigin.token] reads the ARGB out of the token origin. The two margin steps
 * are joined to row 22 by `s23DerivedEdge` in the Go lane, which reads the table; what is asserted
 * here is that they reach the VIEW off the resource table Android actually merges.
 *
 * **THE INK IS THE ASSERTION THAT MATTERS AND IT HAS A PLAUSIBLE WRONG ANSWER.** `--p-ink3` is
 * what "de-emphasised" looks like everywhere else in this kit -- the section label, the agent
 * name, the endpoint id, the timestamp column -- and this note is a caption under a terminal, so
 * it is exactly the component a careless edit would recede. Row 22 says `--p-ink2` and PB-DS-12
 * says why: `--p-ink3` is 3.17 to 3.50:1 on every surface in this product, under the 4.5:1 floor
 * for text a user is meant to read. The negative half below is what holds that.
 *
 * WHAT IS DELIBERATELY NOT HERE: `[Take control]`. Row 22's substance is that the inline span
 * becomes a STANDALONE tertiary button, which is `ctaButton(kind = MORE)` unchanged -- already
 * asserted by `CtaButtonTest` against `.a2-more`. A second opinion here could disagree with the
 * first, and a note that built its own button would be the copy of `.a2-more` §2's reuse rule
 * exists to prevent.
 */
@RunWith(RobolectricTestRunner::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
class ReadOnlyNoteTest {

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

    private val copy = "Read-only · escape-filtered VT snapshot"

    private fun note() = readOnlyNote(context, copy)

    @Test
    fun `the note is row 22's text role in row 22's ink`() {
        val claims = KitOrigin.textClaims(
            // `.prow .ln` is what type.xml records as Body.Secondary's origin.
            view = note(),
            selector = ".prow .ln",
            ink = KitOrigin.token("--p-ink2"),
            spScale = spScale,
        )

        assertTrue(
            "the pitch probe cannot answer: ${KitOrigin.typefaceProbeFaults()}",
            KitOrigin.typefaceProbeFaults().isEmpty(),
        )
        assertEquals(mismatches(claims).joinToString("\n"), emptyList<String>(), mismatches(claims))
        assertNotEquals(
            "the note is `--p-ink3`, which is under 3.5:1 on every surface in this product. It " +
                "is the ink every other de-emphasised label in this kit takes, and row 22 says " +
                "this one is prose a user is meant to read",
            KitOrigin.token("--p-ink3"),
            note().currentTextColor,
        )
    }

    @Test
    fun `the note is centred and spends row 22's two margin steps`() {
        val subject = note()
        val params = subject.layoutParams as LinearLayout.LayoutParams
        val claims = listOf(
            Claim("row 22 margin top", dimenPx("swarm_space_10"), params.topMargin),
            Claim("row 22 margin start", dimenPx("swarm_space_18"), params.marginStart),
            Claim("row 22 margin end", dimenPx("swarm_space_18"), params.marginEnd),
        )
        assertEquals(mismatches(claims).joinToString("\n"), emptyList<String>(), mismatches(claims))
        assertEquals(
            "row 22 centres the note. Left-aligned it reads as a caption belonging to the line " +
                "above it rather than as a statement about the whole block",
            Gravity.CENTER,
            subject.gravity,
        )
    }

    /**
     * Row 22 states no surface, no border and no radius: three cells reading `none`.
     *
     * The plausible wrong answer is a card. Every other block-level component in this kit sits on
     * `cardSurface` or `wellSurface`, and a note that acquired one would be a card containing one
     * sentence -- which is a different statement from a sentence under a terminal.
     */
    @Test
    fun `the note paints no surface of its own`() {
        assertNull(
            "the note carries a background. Row 22's surface, border and radius cells all say " +
                "`none` -- the ground shows through, the way it does for the empty state",
            note().background,
        )
    }

    /**
     * PB-DS-10's control, fed to the SAME function every assertion above calls.
     */
    @Test
    fun `the comparison fails when a value diverges`() {
        val subject = note()
        val params = subject.layoutParams as LinearLayout.LayoutParams

        assertTrue(
            "a perturbed ink produced no mismatch, so the ink claim above would hold against a " +
                "note painted in any colour at all",
            mismatches(
                listOf(Claim("row 22 ink", KitOrigin.token("--p-ink2") + 1, subject.currentTextColor)),
            ).isNotEmpty(),
        )
        assertTrue(
            "a perturbed margin produced no mismatch, so the spacing claims are about nothing",
            mismatches(
                listOf(Claim("row 22 margin top", params.topMargin + 1, params.topMargin)),
            ).isNotEmpty(),
        )
        assertTrue(
            "the comparison reports a mismatch for a value that agrees, so it fails on everything",
            mismatches(listOf(Claim("row 22 margin top", params.topMargin, params.topMargin)))
                .isEmpty(),
        )
    }
}
