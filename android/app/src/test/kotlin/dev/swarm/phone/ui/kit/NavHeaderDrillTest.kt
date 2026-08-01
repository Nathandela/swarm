package dev.swarm.phone.ui.kit

import android.content.Context
import android.util.TypedValue
import android.widget.LinearLayout
import android.widget.TextView
import androidx.test.core.app.ApplicationProvider
import dev.swarm.phone.theme.SwarmTheme
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotEquals
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.GraphicsMode
import kotlin.math.roundToInt

/**
 * FAILING-FIRST (TDD RED, GG-5) for PB-DS-10 over derivation §4's drill-down nav header.
 *
 * WHAT THIS LANE CAN RESOLVE FROM THE ORIGIN AND WHAT IT CANNOT, stated first because the split
 * decides what every assertion below is worth:
 *
 *   - **THE TYPE AND THE INKS ARE FULLY RESOLVED.** §4 states two named styles and two named
 *     tokens -- `Title.Sheet` / `--p-ink` for the title, `Body.Message` / `--p-ink2` for the back
 *     label -- and every one of those four resolves from the staged artifact: [TypeScale] follows
 *     `type.xml`'s own `origin:` comment to the CSS rule (`.sheet2 h4`, `.m2`) and reads the size,
 *     the tracking and the family out of it, and [KitOrigin.token] reads the ARGB out of the token
 *     origin. Nothing here is a number recorded out of the kit.
 *   - **THE THREE PADDING STEPS AND THE GAP ARE NOT RESOLVED HERE**, for [ToggleTest]'s reason
 *     exactly: `docs/design/substrate-components.md` is not on this classpath. That join is
 *     `android/gate/s23_kit_test.go`'s -- `s23DerivedEdge` reads `space_6` top, `space_18` sides,
 *     `space_12` bottom and gap `space_10` out of §4's own row and requires the file to spend
 *     them. What is asserted here is what that reader cannot see: that the steps reach the VIEW,
 *     off the resource table Android actually merges, rather than being named in a source.
 *   - **THE CHEVRON'S PATH IS NOT ASSERTED ANYWHERE AND CANNOT BE.** Neither artifact draws one;
 *     `res/drawable/swarm_nav_back.xml` says so in its own comment. Its STROKE is joined to §4 by
 *     the Go lane. What this suite asks is the question a drawing cannot answer for itself:
 *     whether the glyph is on screen at all and whether it carries `--p-ink` rather than the
 *     label's `--p-ink2`, which is the one visual decision §4 makes about it.
 *
 * THE >=48 dp TOUCH TARGET IS NOT ASSERTED, for [textField]'s and [ToggleTest]'s reason: §4 writes
 * "48 dp target" and this package's annotation grammar reads no value behind a `>=` or in front of
 * a unit. It is a WCAG floor rather than a design value, and the component does not set one.
 * Recorded so the absence reads as a known boundary rather than as an oversight.
 */
@RunWith(RobolectricTestRunner::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
class NavHeaderDrillTest {

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

    private fun header(
        back: CharSequence = "Inbox",
        title: CharSequence = "mbp/quanthome · 80x24",
    ): LinearLayout = navHeaderDrill(context, back, title)

    private fun backOf(header: LinearLayout) = header.kitRequire(KitTag.DRILL_BACK) as TextView

    private fun titleOf(header: LinearLayout) = header.kitRequire(KitTag.DRILL_TITLE) as TextView

    // ---- the type and the inks --------------------------------------------

    /**
     * §4: "Title `Title.Sheet` / `--p-ink`", and the back label "`Body.Message` / `--p-ink2`".
     *
     * THE TITLE IS THE HALF WORTH SPELLING OUT. The plausible wrong answer is `Display.NavTitle`
     * -- 27 sp at weight 650 -- because that is what the OTHER nav header renders and reusing
     * `navHeader` for a drill-down screen is the obvious implementation. §4 moves the mock's
     * 16/700 to 15.5/650, which is `Title.Sheet`, and the negative half below is what makes the
     * distinction hold.
     */
    @Test
    fun `the title and the back label are the two text roles section 4 names`() {
        val subject = header()

        val claims = KitOrigin.textClaims(
            // `.sheet2 h4` is what type.xml records as Title.Sheet's origin.
            view = titleOf(subject),
            selector = ".sheet2 h4",
            ink = KitOrigin.token("--p-ink"),
            spScale = spScale,
        ) + KitOrigin.textClaims(
            // `.m2` is what type.xml records as Body.Message's origin.
            view = backOf(subject),
            selector = ".m2",
            ink = KitOrigin.token("--p-ink2"),
            spScale = spScale,
        )

        assertTrue(
            "the pitch probe cannot answer: ${KitOrigin.typefaceProbeFaults()}",
            KitOrigin.typefaceProbeFaults().isEmpty(),
        )
        assertEquals(mismatches(claims).joinToString("\n"), emptyList<String>(), mismatches(claims))
    }

    @Test
    fun `the title is not the root header's display size`() {
        // The root header's `.pnav .big` is 27 sp; this is 15.5 sp. Sharing one factory between
        // the two headers would put that choice in a boolean, and a screen passing it would be a
        // screen choosing type.
        val rootSize = KitOrigin.quantisedTextSize(
            dev.swarm.phone.theme.TypeScale.designSpec(".pnav .big").sizePx * spScale,
        )
        assertNotEquals(
            "the drill-down title renders at the ROOT header's size, so §4's 15.5 sp Title.Sheet " +
                "has been replaced by `.pnav .big`'s 27 sp Display.NavTitle",
            rootSize,
            titleOf(header()).textSize,
        )
    }

    // ---- the chevron -------------------------------------------------------

    /**
     * §4: "24 dp chevron glyph, stroke 1.7, `--p-ink`".
     *
     * THE INK IS THE ASSERTION AND THE PATH IS NOT. `--p-ink` for the glyph beside `--p-ink2` for
     * its label is the one visual decision §4 makes that only this component can get wrong -- the
     * obvious implementation tints the drawable with the TextView's own colour and quietly renders
     * a two-tone control in one tone.
     */
    @Test
    fun `the chevron is on screen and carries the primary ink, not the label's`() {
        val back = backOf(header())
        val glyph = back.compoundDrawablesRelative[0]

        assertNotNull(
            "§4 gives the back control a chevron glyph and the component drew none, so the back " +
                "control is a bare word with nothing saying it goes back",
            glyph,
        )
        val tint = requireNotNull(back.compoundDrawableTintList) {
            "the chevron carries no tint, so it renders in whatever colour the asset happens to " +
                "ship -- which is the platform's white placeholder, not a token"
        }
        val claims = listOf(
            Claim("§4 chevron ink", KitOrigin.token("--p-ink"), tint.defaultColor),
        )
        assertEquals(mismatches(claims).joinToString("\n"), emptyList<String>(), mismatches(claims))
        assertNotEquals(
            "the chevron is tinted with the back LABEL's ink, so §4's two-tone back control " +
                "renders in one tone",
            KitOrigin.token("--p-ink2"),
            tint.defaultColor,
        )
    }

    /**
     * The glyph takes the ASSET's own box rather than a size typed in the component.
     *
     * `.ptabs svg` paid for this once: the drawn box and the viewBox the path is written in are
     * different coordinate spaces, and a component that set its own bounds is a second statement
     * of a number the asset already makes.
     */
    @Test
    fun `the chevron is drawn at the size the asset declares`() {
        val glyph = requireNotNull(backOf(header()).compoundDrawablesRelative[0])
        val claims = listOf(
            Claim("chevron width", glyph.intrinsicWidth, glyph.bounds.width()),
            Claim("chevron height", glyph.intrinsicHeight, glyph.bounds.height()),
        )
        assertEquals(mismatches(claims).joinToString("\n"), emptyList<String>(), mismatches(claims))
    }

    // ---- the frame ---------------------------------------------------------

    /**
     * §4's three padding steps and its gap, resolved off the merged resource table.
     *
     * THE GO LANE READS THE ROW AND THIS READS THE VIEW, and neither is sufficient alone: a source
     * can name `R.dimen.swarm_space_6` and still spend it on the wrong edge, which is exactly the
     * mistake a four-argument `setPaddingRelative` invites.
     */
    @Test
    fun `the header spends section 4's three steps on the three edges it names`() {
        val subject = header()
        val claims = listOf(
            Claim("§4 padding top", dimenPx("swarm_space_6"), subject.paddingTop),
            Claim("§4 padding start", dimenPx("swarm_space_18"), subject.paddingStart),
            Claim("§4 padding end", dimenPx("swarm_space_18"), subject.paddingEnd),
            Claim("§4 padding bottom", dimenPx("swarm_space_12"), subject.paddingBottom),
            Claim(
                "§4 gap, between the back control and the title",
                dimenPx("swarm_space_10"),
                (titleOf(subject).layoutParams as LinearLayout.LayoutParams).marginStart,
            ),
            Claim(
                "§4 gap, between the chevron and its label",
                dimenPx("swarm_space_10"),
                backOf(subject).compoundDrawablePadding,
            ),
        )
        assertEquals(mismatches(claims).joinToString("\n"), emptyList<String>(), mismatches(claims))
    }

    @Test
    fun `the title takes the remaining width rather than a trailing gravity`() {
        // A trailing gravity works until the title grows long enough to meet what is beside it,
        // and then they overlap rather than push. `navHeader` records the same reasoning.
        val params = titleOf(header()).layoutParams as LinearLayout.LayoutParams
        assertEquals(0, params.width)
        assertEquals(1f, params.weight, 0.001f)
    }

    // ---- the negative control ----------------------------------------------

    /**
     * PB-DS-10's control, fed to the SAME function every assertion above calls.
     *
     * A control that rebuilds the comparison inline proves something about the copy and nothing
     * about the assertion -- this package has shipped that mistake once, as an `assertEquals(got,
     * got)` with a green control beside it. So the perturbation goes through [mismatches].
     */
    @Test
    fun `the comparison fails when a value diverges`() {
        val subject = header()
        val ink = KitOrigin.token("--p-ink")

        assertTrue(
            "a perturbed ink produced no mismatch, so every ink claim in this suite would hold " +
                "against a component painted in any colour at all",
            mismatches(listOf(Claim("§4 title ink", ink + 1, titleOf(subject).currentTextColor)))
                .isNotEmpty(),
        )
        assertTrue(
            "a perturbed padding produced no mismatch, so the frame claims are about nothing",
            mismatches(
                listOf(Claim("§4 padding top", subject.paddingTop + 1, subject.paddingTop)),
            ).isNotEmpty(),
        )
        assertTrue(
            "the comparison reports a mismatch for a value that agrees, so it fails on everything " +
                "-- which is as useless as failing on nothing",
            mismatches(listOf(Claim("§4 title ink", ink, ink))).isEmpty(),
        )
    }
}
