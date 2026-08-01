package dev.swarm.phone.ui.kit

import android.content.Context
import android.util.TypedValue
import android.view.Gravity
import android.view.View
import android.widget.LinearLayout
import androidx.test.core.app.ApplicationProvider
import dev.swarm.phone.theme.DesignScale
import dev.swarm.phone.theme.SwarmTheme
import dev.swarm.phone.theme.TypeScale
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
 * FAILING-FIRST (TDD RED, GG-5) for PB-DS-10 over the approval sheet's three actions.
 *
 * ALL FOUR RULES ARE SUBSTRATE'S OWN, which is what makes this the best-resolved suite in the
 * package: `.acts2 button` carries the shape, the padding and the type; `.a2-ok`, `.a2-no` and
 * `.a2-more` carry the three fills and the three inks. Nothing here is a derivation citation
 * standing in for a missing rule -- every expected value below is read out of the artifact through
 * [KitOrigin] or out of the token origin, including the deny fill, whose 13% share is a `color-mix`
 * the artifact's own CSS writes and [KitOrigin.cssPercent] reads.
 *
 * THE BLOOM IS THE STATUS DOT'S CONVERSION, SECOND SITE. `--p-cta-fx` is `0 0 18px rgba(...)` and
 * is typed `effect` in `tokens.json`, so PB-TOK-6's converters produce no resource for it; the
 * derivation table's status-dot row says the two are one conversion in as many words ("the same
 * conversion as `--p-cta-fx`"). Which means the same three things have to be true here as on the
 * dot: a `Paint.setShadowLayer`, a SOFTWARE layer (the shadow is ignored under hardware
 * acceleration for everything but text), and room for the halo INSIDE the view's own bounds,
 * because a software layer's bitmap is the view's bounds and clips the glow before any parent's
 * `clipChildren` is consulted. The room is given back as negative margin, because a CSS
 * `box-shadow` does not participate in layout and the inflation must not either.
 *
 * AND IT IS SUPPRESSED INSIDE A CARD. §4's "In-card CTA pair" row: the card sets `overflow: hidden`,
 * so an 18 dp bloom inside it is clipped at the card edge and looks broken. That is the `bloom`
 * parameter, and the suite asserts the suppressed form resolves to the SAME geometry as a button
 * that never had a bloom -- not merely to a missing layer.
 */
@RunWith(RobolectricTestRunner::class)
// NATIVE, for the reason KitOrigin.isFixedPitch gives: LEGACY graphics stubs the text stack and
// returns one pixel per character, which makes every font measure fixed-pitch and would certify the
// opposite of the truth about this component's family.
@GraphicsMode(GraphicsMode.Mode.NATIVE)
class CtaButtonTest {

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

    private fun approve(bloom: Boolean = true) =
        ctaButton(context, "Approve once", CtaKind.APPROVE, bloom)

    private fun deny() = ctaButton(context, "Deny", CtaKind.DENY)

    private fun more() = ctaButton(context, "View session first", CtaKind.MORE)

    private fun specOf(button: View) = (button.background as CtaSurface).spec

    /** `.a2-ok`, `.a2-no`, `.a2-more`: three fills, each read from the rule that declares it. */
    @Test
    fun `each variant is the fill its own rule declares`() {
        val claims = listOf(
            Claim("`.a2-ok` background", KitOrigin.cssColour(".a2-ok", "background"), specOf(approve()).fill),
            Claim(
                "`.a2-no` background",
                // `color-mix(in srgb, var(--p-err) 13%, transparent)`: the share is the artifact's
                // own and is read out of the declaration rather than transcribed. This is the one
                // derived colour in this component, and PB-TOK-7 forbids typing its resolved hex --
                // so the check has to recompute it, which is what overTransparent is here for.
                KitOrigin.overTransparent(
                    KitOrigin.token("--p-err"),
                    KitOrigin.cssPercent(".a2-no", "background"),
                ),
                specOf(deny()).fill,
            ),
            Claim("`.a2-more` background", KitOrigin.cssColour(".a2-more", "background"), specOf(more()).fill),
        )

        assertEquals(mismatches(claims).joinToString("\n"), emptyList<String>(), mismatches(claims))
        assertNotEquals(
            "the deny fill IS --p-err, so the 13% mix never happened and the button is a solid " +
                "error-red block rather than a tint over whatever is behind it",
            KitOrigin.token("--p-err"),
            specOf(deny()).fill,
        )
    }

    /**
     * `.acts2 button` binds ONE type style to three inks, which is why [KitOrigin.textClaims] takes
     * the ink separately: the metric style is shared and the colour is the variant's.
     */
    @Test
    fun `each variant is the button label in its own rule's ink`() {
        assertTrue(
            "the pitch probe cannot answer: ${KitOrigin.typefaceProbeFaults()}",
            KitOrigin.typefaceProbeFaults().isEmpty(),
        )

        val claims = KitOrigin.textClaims(
            view = approve(),
            selector = ".acts2 button",
            ink = KitOrigin.cssColour(".a2-ok", "color"),
            spScale = spScale,
        ) + KitOrigin.textClaims(
            view = deny(),
            selector = ".acts2 button",
            ink = KitOrigin.cssColour(".a2-no", "color"),
            spScale = spScale,
        ) + KitOrigin.textClaims(
            view = more(),
            selector = ".acts2 button",
            ink = KitOrigin.cssColour(".a2-more", "color"),
            spScale = spScale,
        )

        assertEquals(mismatches(claims).joinToString("\n"), emptyList<String>(), mismatches(claims))
    }

    /**
     * A KNOWN DIVERGENCE, RECORDED RATHER THAN PAPERED OVER.
     *
     * `.a2-more` restates `font-weight: 500` over `.acts2 button`'s 600, and PB-DS-2's scale is
     * eighteen closed `TextAppearance.Swarm.*` styles with exactly one `Label.Button` among them.
     * The kit may not set a weight at a call site -- `android/gate/s23_kit_test.go` fences
     * `setTypeface` out of this package for the reason PB-DS-2 gives -- so the tertiary action
     * ships at 600 and is one weight step heavier than the artifact draws it.
     *
     * Adding a nineteenth style is `res/values/type.xml`'s decision and that file belongs to the
     * type slice. This test is what keeps the divergence from becoming invisible: it fails if
     * either number moves, so whoever changes one has to come back here.
     */
    @Test
    fun `the tertiary action's lighter weight has no style and the gap is recorded`() {
        assertEquals(
            "the artifact no longer gives `.a2-more` a weight of its own; the divergence this " +
                "test records may have closed, and the KDoc above needs rewriting rather than " +
                "the assertion relaxing",
            "500",
            DesignScale.rule(".a2-more")["font-weight"],
        )
        assertEquals(
            "`.acts2 button`'s weight moved, so what the kit ships is no longer what this " +
                "divergence was measured against",
            600,
            TypeScale.designSpec(".acts2 button").weight,
        )
    }

    /**
     * `.acts2 button { border: none }`, and `.a2-more`'s `1px solid var(--p-hair) !important`.
     *
     * The hairline is the only thing separating the tertiary action from the card behind it -- both
     * are `--p-card` -- so losing it makes the button disappear rather than merely look flat.
     */
    @Test
    fun `only the tertiary action carries a border`() {
        val claims = listOf(
            Claim("`.a2-more` border colour", KitOrigin.cssColour(".a2-more", "border"), specOf(more()).stroke),
            Claim(
                "`.a2-more` border width",
                dp(KitOrigin.cssFirstPx(".a2-more", "border")).roundToInt(),
                specOf(more()).strokeWidthPx,
            ),
            Claim("`.a2-ok` border width", 0, specOf(approve()).strokeWidthPx),
            Claim("`.a2-no` border width", 0, specOf(deny()).strokeWidthPx),
        )

        assertEquals(mismatches(claims).joinToString("\n"), emptyList<String>(), mismatches(claims))
    }

    /** `.acts2 button { border-radius: var(--p-btn-r) }` -- the button step of PB-DS-4's ladder. */
    @Test
    fun `every variant takes the button radius`() {
        val radius = dp(DesignScale.tokenPx("--p-btn-r"))
        val claims = listOf(
            Claim("`.a2-ok` radius", radius, specOf(approve()).radiusPx),
            Claim("`.a2-no` radius", radius, specOf(deny()).radiusPx),
            Claim("`.a2-more` radius", radius, specOf(more()).radiusPx),
        )

        assertEquals(mismatches(claims).joinToString("\n"), emptyList<String>(), mismatches(claims))
    }

    /**
     * `.a2-ok { box-shadow: var(--p-cta-fx) }`, converted the way the status dot's glow is.
     *
     * THE COLOUR COMES OUT OF THE TOKEN, NOT OUT OF THE FILL. `--p-cta-fx` is
     * `0 0 18px rgba(83, 206, 124, 0.20)`, whose RGB happens to equal `--p-cta-bg` in this skin --
     * and "happens to" is the operative phrase, because `--p-cta-bg` keeps its own row in
     * `android/design-tokens.tsv` precisely so a future skin can break that alias. Reading the
     * expected value out of the effect token rather than out of the fill is what would notice.
     */
    @Test
    fun `the approve action blooms with the effect token the artifact declares`() {
        val spec = specOf(approve())

        val claims = listOf(
            Claim("`--p-cta-fx` colour", KitOrigin.rgbaToken("--p-cta-fx"), spec.bloom),
            Claim("`--p-cta-fx` blur radius", dp(KitOrigin.cssFirstPx(".a2-ok", "box-shadow")), spec.bloomRadiusPx),
        )

        assertEquals(mismatches(claims).joinToString("\n"), emptyList<String>(), mismatches(claims))
    }

    /**
     * The three consequences of a `box-shadow` on a view, which the status dot paid for once already.
     *
     * THE LAYER TYPE IS THE ONE THAT LOOKS RIGHT IN EVERY VALUE AND IS WRONG ON SCREEN.
     * `setShadowLayer` is ignored under hardware acceleration for everything but text, so a button
     * that set the shadow and left itself accelerated draws a flat rectangle while every property a
     * test could read off the Paint stays correct.
     *
     * THE INFLATION IS GIVEN BACK. A CSS `box-shadow` does not participate in layout, so the room
     * the halo needs inside the view's bounds is returned as a negative margin on all four edges --
     * which means what the design fixes, the button's own box, is unchanged.
     */
    @Test
    fun `the bloom gets room inside the view and gives all of it back`() {
        val button = approve()
        val spec = specOf(button)
        val bloomPx = dp(KitOrigin.cssFirstPx(".a2-ok", "box-shadow")).roundToInt()
        val pad = dimenPx("swarm_space_12")
        val margins = button.layoutParams as LinearLayout.LayoutParams

        val claims = listOf(
            Claim("the halo's room", bloomPx, spec.insetPx),
            Claim("padding-start", pad + bloomPx, button.paddingStart),
            Claim("padding-top", pad + bloomPx, button.paddingTop),
            Claim("padding-end", pad + bloomPx, button.paddingEnd),
            Claim("padding-bottom", pad + bloomPx, button.paddingBottom),
            Claim("margin-start", -bloomPx, margins.marginStart),
            Claim("margin-end", -bloomPx, margins.marginEnd),
            Claim("margin-top", -bloomPx, margins.topMargin),
            Claim("margin-bottom", -bloomPx, margins.bottomMargin),
            Claim("layer type", View.LAYER_TYPE_SOFTWARE, button.layerType),
        )

        assertEquals(mismatches(claims).joinToString("\n"), emptyList<String>(), mismatches(claims))
        assertTrue(
            "the bloom needs room, and 0 px of it means the halo is clipped by the software " +
                "layer's own bitmap before any parent is consulted",
            bloomPx > 0,
        )
    }

    /**
     * §4's "In-card CTA pair": the approve action drops `--p-cta-fx` inside a card.
     *
     * IT IS ASSERTED AS AN IDENTITY WITH THE NON-BLOOMING VARIANTS rather than as a missing layer.
     * A button that dropped the shadow but kept the 18 dp of inflation would still have a null
     * bloom, and would sit 18 dp out of place in every direction inside the card it was suppressed
     * for -- so what is checked is that suppressing the bloom leaves the geometry a button that
     * never had one has.
     */
    @Test
    fun `suppressing the bloom leaves the geometry a button with no bloom has`() {
        val inCard = approve(bloom = false)
        val pad = dimenPx("swarm_space_12")
        val margins = inCard.layoutParams as LinearLayout.LayoutParams

        val claims = listOf(
            Claim("in-card bloom", null, specOf(inCard).bloom),
            Claim("in-card halo room", 0, specOf(inCard).insetPx),
            Claim("in-card padding-top", pad, inCard.paddingTop),
            Claim("in-card padding-start", pad, inCard.paddingStart),
            Claim("in-card margin-start", 0, margins.marginStart),
            Claim("in-card margin-top", 0, margins.topMargin),
            Claim("in-card layer type", View.LAYER_TYPE_NONE, inCard.layerType),
            // Row 250 says the fill is `.a2-ok` unchanged: only the box-shadow goes.
            Claim("in-card fill", specOf(approve()).fill, specOf(inCard).fill),
        )

        assertEquals(mismatches(claims).joinToString("\n"), emptyList<String>(), mismatches(claims))
    }

    /** `box-shadow` is declared on `.a2-ok` and on neither of the other two. */
    @Test
    fun `the deny and tertiary actions never bloom`() {
        assertNull("`.a2-no` declares no box-shadow", specOf(deny()).bloom)
        assertNull("`.a2-more` declares no box-shadow", specOf(more()).bloom)
        assertEquals(View.LAYER_TYPE_NONE, deny().layerType)
        assertEquals(View.LAYER_TYPE_NONE, more().layerType)
    }

    /** `.acts2 button { padding: 12px }`, on all four edges, on the variants with no halo to house. */
    @Test
    fun `a button with no bloom spends the step and nothing else`() {
        val pad = dimenPx("swarm_space_12")
        val claims = listOf(
            Claim("`.a2-no` padding-top", pad, deny().paddingTop),
            Claim("`.a2-no` padding-start", pad, deny().paddingStart),
            Claim("`.a2-more` padding-bottom", pad, more().paddingBottom),
            Claim("`.a2-more` padding-end", pad, more().paddingEnd),
        )

        assertEquals(mismatches(claims).joinToString("\n"), emptyList<String>(), mismatches(claims))
    }

    /** `.acts2 { flex-direction: column }`: the buttons are full width and their labels centred. */
    @Test
    fun `the button fills its column and centres its label`() {
        val button = deny()

        assertEquals(LinearLayout.LayoutParams.MATCH_PARENT, button.layoutParams.width)
        assertEquals(
            Gravity.CENTER,
            button.gravity and (Gravity.HORIZONTAL_GRAVITY_MASK or Gravity.VERTICAL_GRAVITY_MASK),
        )
    }

    /** The copy is the screen's (PB-DS-9); the component decides what it looks like. */
    @Test
    fun `the button says what it was given`() {
        assertEquals("Approve once", approve().text.toString())
    }

    /**
     * The negative control PB-DS-10 requires, through the SAME comparison the assertions above use.
     *
     * A control that rebuilt the comparison inline would prove the copy works. Every claim above
     * goes through [mismatches], and so does every perturbation here.
     */
    @Test
    fun `the CTA assertions can actually fail`() {
        val fill = KitOrigin.cssColour(".a2-ok", "background")
        val pad = dimenPx("swarm_space_12")
        val bloomPx = dp(KitOrigin.cssFirstPx(".a2-ok", "box-shadow")).roundToInt()

        assertTrue(
            "a fill one unit from the artifact's passes the comparison",
            mismatches(listOf(Claim("fill", fill, fill + 1))).isNotEmpty(),
        )
        assertTrue(
            "a padding one pixel from the rule's passes the comparison",
            mismatches(listOf(Claim("padding", pad, pad + 1))).isNotEmpty(),
        )
        assertTrue(
            "a bloom inflation the button never gives back passes the margin comparison, which is " +
                "the mistake that leaves the CTA 18 dp out of place in every direction",
            mismatches(listOf(Claim("margin-start", -bloomPx, 0))).isNotEmpty(),
        )
        assertTrue(
            "a hardware-accelerated button passes the layer comparison, which is the failure that " +
                "reads correct in every value and draws no glow at all",
            mismatches(listOf(Claim("layer", View.LAYER_TYPE_SOFTWARE, View.LAYER_TYPE_NONE)))
                .isNotEmpty(),
        )
        assertTrue(
            "a bloom that was never suppressed passes the in-card comparison",
            mismatches(listOf(Claim("in-card bloom", null, fill))).isNotEmpty(),
        )
        assertNotEquals(
            "the deny fill compares equal to --p-err itself, so the 13% mix assertion would " +
                "accept a solid error-red button",
            KitOrigin.token("--p-err"),
            KitOrigin.overTransparent(
                KitOrigin.token("--p-err"),
                KitOrigin.cssPercent(".a2-no", "background"),
            ),
        )
        assertNotEquals(
            "`--p-cta-fx` resolves to the same ARGB as `--p-cta-bg`, so reading the bloom out of " +
                "the effect token rather than out of the fill would notice nothing",
            KitOrigin.token("--p-cta-bg"),
            KitOrigin.rgbaToken("--p-cta-fx"),
        )
    }
}
