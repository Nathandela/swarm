package dev.swarm.phone.ui.kit

import android.content.Context
import android.graphics.Color
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
 * FAILING-FIRST (TDD RED, GG-5) for PB-DS-10 over the kit's FOUNDATION: the Group bindings, the
 * blend, and the three drawable factories every surfaced component is built out of.
 *
 * "For every component: a test inflates it and asserts the RESOLVED colour int, dimension px,
 *  typeface and letter-spacing against the token origin -- not against a constant recorded from
 *  the implementation. Each such test carries a negative control proving it fails when the value
 *  diverges."
 *
 * WHY THE FOUNDATION GETS ITS OWN SUITE. Three of the four values a card paints -- the derived
 * attention border, the inset key-light and the two dot glows -- are colours that exist in NO
 * resource and CANNOT: they are functions of tokens, so PB-TOK-7 forbids typing them and the
 * resource table has no form for them. What the kit carries instead is the SHARE, joined to
 * internal/design's derivation table by the Go gate, and the blend that turns a share into a
 * colour. If that blend is wrong every component below is wrong in the same direction, which is
 * why it is asserted here once rather than implied nine times.
 */
@RunWith(RobolectricTestRunner::class)
// NATIVE and not the default. Robolectric's LEGACY graphics stubs the text stack -- measureText
// returns one pixel per character -- which makes every font measure fixed-pitch and every
// typeface assertion in this file certify the opposite of the truth. The same annotation, for the
// same reason, as MonoBoxDrawingTest.
@GraphicsMode(GraphicsMode.Mode.NATIVE)
class KitFoundationTest {

    private val context: Context
        get() = SwarmTheme.applyTo(ApplicationProvider.getApplicationContext())

    private val density: Float get() = context.resources.displayMetrics.density

    private fun px(dp: Float) = dp * density

    /**
     * PB-TOK-8 and ADR-007 B134 decision 1, resolved.
     *
     * The expectation is followed out of the checked-in join -- group-tokens.tsv says which token
     * a Group is, the origin says what that token's value is -- so this test cannot agree with a
     * kit that binds green to Completed, which is what Substrate's own demo phone labels it.
     */
    @Test
    fun `every status Group resolves to the colour its checked-in token declares`() {
        val bindings = KitOrigin.groupTokens()
        assertEquals(
            "PB-TOK-8 binds four Groups; a Group the dot cannot colour is a whole inbox section " +
                "with no state",
            4,
            bindings.size,
        )
        assertEquals(
            emptyList<String>(),
            mismatches(
                bindings.map { (group, token) ->
                    Claim("$group -> $token", KitOrigin.token(token), Kit.groupColour(context, group))
                },
            ),
        )
    }

    /**
     * "Nothing glows unless it is alive." Exactly two Groups are.
     *
     * The share is read off the CSS rule whose fill IS the Group's token, so which Groups glow is
     * a property of the design rather than a list in this test: `.pdot.att` and `.pdot.wrk` carry
     * a `box-shadow` with a `color-mix` share, `.pdot.ok` sets `box-shadow: none` explicitly, and
     * `--p-ink3` has no `.pdot` rule at all.
     */
    @Test
    fun `the two live Groups glow at the design's share and the other two do not glow`() {
        val variants = KitOrigin.dotVariants()
        assertTrue(
            "the design declares no .pdot variants; every expectation here would be null",
            variants.isNotEmpty(),
        )

        val claims = KitOrigin.groupTokens().map { (group, token) ->
            Claim("$group ($token) glow", KitOrigin.dotGlow(token)?.colour, Kit.groupGlow(context, group))
        }
        assertEquals(2, claims.count { it.want != null })
        assertEquals(emptyList<String>(), mismatches(claims))
    }

    /**
     * The blend itself, in both the forms the artifact uses.
     *
     * They look alike and behave differently, and one function has to get both right or the four
     * derived values become four hand-transcriptions again.
     */
    @Test
    fun `the blend is the premultiplied one CSS specifies`() {
        val att = KitOrigin.token("--p-att")
        val hair = KitOrigin.token("--p-hair")
        val glowShare = KitOrigin.cssPercent(".pdot.att", "box-shadow")
        val borderShare = KitOrigin.cssPercent(".prow.attention", "border-color")

        assertEquals(
            emptyList<String>(),
            mismatches(
                listOf(
                    Claim(
                        "--p-att ${glowShare * 100}% over transparent",
                        KitOrigin.overTransparent(att, glowShare),
                        ColorMix.mix(att, glowShare, ColorMix.TRANSPARENT),
                    ),
                    Claim(
                        "--p-att ${borderShare * 100}% over --p-hair",
                        KitOrigin.overOpaque(att, borderShare, hair),
                        ColorMix.mix(att, borderShare, hair),
                    ),
                ),
            ),
        )
    }

    /**
     * The negative control for the blend, and the sharpest one in this package: it demonstrates
     * the specific WRONG implementation, not merely that two numbers can differ.
     *
     * Interpolating un-premultiplied gets the alpha right and the hue wrong -- the result is a
     * DARKER version of the token at the same alpha, which still reads as "a dimmer --p-att" in a
     * code review. So the control computes what that mistake produces and requires the kit's
     * answer not to be it.
     */
    @Test
    fun `the blend is not the un-premultiplied one that fades through black`() {
        val att = KitOrigin.token("--p-att")
        val share = KitOrigin.cssPercent(".pdot.att", "box-shadow")

        // What interpolating un-premultiplied toward rgba(0,0,0,0) gives: every channel scaled.
        val naive = Color.argb(
            (share * 255f).toInt(),
            (Color.red(att) * share).toInt(),
            (Color.green(att) * share).toInt(),
            (Color.blue(att) * share).toInt(),
        )
        assertNotEquals(
            "the reference blend in this suite is the naive one, so it cannot catch the kit " +
                "making the same mistake",
            naive,
            KitOrigin.overTransparent(att, share),
        )
        assertNotEquals(
            "the kit's glow fades --p-att through black instead of preserving its RGB at alpha " +
                "${share * 100}%. Both spellings look identical in a diff and only one is what " +
                "CSS computes.",
            naive,
            ColorMix.mix(att, share, ColorMix.TRANSPARENT),
        )
        assertTrue(
            "the origin's blend and the kit's agree on a value that is not the design's",
            mismatches(listOf(Claim("control", naive, ColorMix.mix(att, share, ColorMix.TRANSPARENT))))
                .isNotEmpty(),
        )
    }

    /** The plain card: `.prow`'s fill, hairline, radius and key-light, resolved. */
    @Test
    fun `the card surface resolves the design's card`() {
        val surface = cardSurface(context, attention = false)
        assertEquals(
            emptyList<String>(),
            mismatches(
                listOf(
                    Claim("`.prow` fill", KitOrigin.cssColour(".prow", "background"), surface.spec.fill),
                    Claim("`.prow` border colour", KitOrigin.cssColour(".prow", "border"), surface.spec.stroke),
                    // WHOLE PIXELS, ROUNDED. The stroke reaches GradientDrawable as an int, and
                    // truncating it renders a 1dp hairline 2px wide on a 2.625x handset where the
                    // platform's own getDimensionPixelSize gives 3 -- a third of the element
                    // Substrate leans on for depth, lost to a cast. KitDensityTest is where that
                    // is visible; here it is only asserted to be the design's own number.
                    Claim("`.prow` border width", px(KitOrigin.cssFirstPx(".prow", "border")).roundToInt(), surface.spec.strokeWidthPx),
                    Claim("`.prow` radius", px(KitOrigin.cssDp(".prow", "border-radius")), surface.spec.radiusPx),
                    Claim("--p-card-fx colour", KitOrigin.rgbaToken("--p-card-fx"), surface.spec.keyLight),
                    Claim("--p-card-fx band", px(1f), surface.spec.keyLightPx),
                    Claim("`.prow` has no rail", null, surface.spec.rail),
                ),
            ),
        )
    }

    /**
     * The key light is a LAYER, and the layer is what PB-DS-5 asks to see.
     *
     * `View.elevation` is the obvious implementation and the wrong one -- Substrate bans drop
     * shadows outright -- so the property asserted is the one elevation could never satisfy: a
     * 1dp band of a specific translucent white, drawn INSIDE the card and clipped to its radius.
     */
    @Test
    fun `the key light is an inset one-dp band clipped to the card radius`() {
        val surface = cardSurface(context, attention = false)
        val highlight = (0 until surface.numberOfLayers)
            .map { surface.getDrawable(it) }
            .filterIsInstance<EdgeHighlight>()
            .singleOrNull()
        assertTrue(
            "the card surface carries no EdgeHighlight layer, so --p-card-fx is either missing " +
                "or has been approximated by something that is not a layer",
            highlight != null,
        )
        assertEquals(
            emptyList<String>(),
            mismatches(
                listOf(
                    Claim("key light colour", KitOrigin.rgbaToken("--p-card-fx"), highlight!!.colour),
                    Claim("key light height", px(1f), highlight.heightPx),
                    Claim(
                        "key light clip radius",
                        px(KitOrigin.cssDp(".prow", "border-radius")),
                        highlight.radiusPx,
                    ),
                ),
            ),
        )
        // AUTHORIZED VALUE MIGRATION, ADR-009 O2. This is a KNOWN ANSWER, typed independently of
        // the reader so that a key light which had quietly become opaque is contradicted by a
        // number rather than by itself. What it said before:
        //
        //     (0.045f * 255f).toInt(),
        //
        // ADR-009 D3 strengthens and warms the key-light to
        // `inset 0 1px 0 rgba(246,243,236,0.10)`: one light source, top edge, linen-toned. The
        // assertion's point is unchanged and is still worth making at 0.10 -- an alpha this low
        // is exactly the one a missing alpha turns into 255 without anything else noticing.
        assertEquals(
            "the key light is a translucent linen, not the opaque one a missing alpha produces",
            (0.10f * 255f).toInt(),
            Color.alpha(highlight.colour),
        )
    }

    /**
     * `.prow.attention`: the warmed border and the 2dp rail.
     *
     * Both are the NeedsInput state's, and both are derived rather than declared -- the border is
     * a `color-mix` PB-TOK-7 forbids typing, and the rail is `--p-att` at full strength on a
     * pseudo-element the design gives no other home.
     */
    @Test
    fun `the attention card carries the derived border and the attention rail`() {
        val surface = cardSurface(context, attention = true)
        val rail = (0 until surface.numberOfLayers)
            .map { surface.getDrawable(it) }
            .filterIsInstance<EdgeRail>()
            .singleOrNull()
        assertTrue("the attention card carries no EdgeRail layer", rail != null)

        val att = KitOrigin.token("--p-att")
        val expectedBorder = KitOrigin.overOpaque(
            att,
            KitOrigin.cssPercent(".prow.attention", "border-color"),
            KitOrigin.token("--p-hair"),
        )
        assertEquals(
            emptyList<String>(),
            mismatches(
                listOf(
                    Claim("`.prow.attention` border", expectedBorder, surface.spec.stroke),
                    Claim("rail colour", KitOrigin.cssColour(".prow.attention::before", "background"), rail!!.colour),
                    Claim("rail width", px(KitOrigin.cssDp(".prow.attention::before", "width")), rail.widthPx),
                    Claim("the fill is unchanged", KitOrigin.cssColour(".prow", "background"), surface.spec.fill),
                ),
            ),
        )
        assertNotEquals(
            "the attention border is the plain hairline, so the warmed border is not being " +
                "computed at all",
            KitOrigin.token("--p-hair"),
            surface.spec.stroke,
        )
    }

    /** `.chip` and `.chip.on`: the only component whose selected state changes three values. */
    @Test
    fun `the chip surface resolves both chip states`() {
        val off = chipSurface(context, selected = false)
        val on = chipSurface(context, selected = true)
        assertEquals(
            emptyList<String>(),
            mismatches(
                listOf(
                    Claim("`.chip` fill", KitOrigin.cssColour(".chip", "background"), off.spec.fill),
                    Claim("`.chip` border", KitOrigin.cssColour(".chip", "border"), off.spec.stroke),
                    Claim("`.chip` radius", px(KitOrigin.cssDp(".chip", "border-radius")), off.spec.radiusPx),
                    Claim("`.chip` has no key light", null, off.spec.keyLight),
                    Claim("`.chip.on` fill", KitOrigin.cssColour(".chip.on", "background"), on.spec.fill),
                    Claim("`.chip.on` border is transparent", 0, Color.alpha(on.spec.stroke)),
                    Claim("`.chip.on` radius is unchanged", off.spec.radiusPx, on.spec.radiusPx),
                ),
            ),
        )
    }

    /**
     * The negative control PB-DS-10 requires, over the comparison every test above runs.
     *
     * It feeds [mismatches] a claim whose resolved value has been moved by one unit -- the
     * smallest divergence any of these assertions could be asked to catch -- and requires it to
     * object. If this ever passes, every `assertEquals(emptyList(), mismatches(...))` above is
     * certifying nothing.
     */
    @Test
    fun `the appearance comparison can actually fail`() {
        // The typeface probe is validated against two faces whose pitch is not in question,
        // BEFORE any component is blamed for reporting the wrong one. A probe that cannot tell
        // Typeface.MONOSPACE from Typeface.SANS_SERIF produces the same symptom as a component
        // that lost its family, and the two have opposite fixes.
        assertEquals(emptyList<String>(), KitOrigin.typefaceProbeFaults())

        val fill = KitOrigin.cssColour(".prow", "background")
        assertTrue(
            "mismatches() accepted a colour that differs by one unit in the blue channel, so " +
                "every appearance assertion in this package passes over values that differ",
            mismatches(listOf(Claim("fill", fill, fill + 1))).isNotEmpty(),
        )
        assertTrue(
            "mismatches() accepted a dimension that differs by a whole dp",
            mismatches(listOf(Claim("radius", px(9f), px(10f)))).isNotEmpty(),
        )
        assertTrue(
            "mismatches() accepted a null where the design states a colour, which is what a " +
                "component that never applied its background would resolve to",
            mismatches(listOf(Claim("fill", fill, null))).isNotEmpty(),
        )
        assertEquals(
            "mismatches() reports a difference between a value and itself",
            emptyList<String>(),
            mismatches(listOf(Claim("fill", fill, fill), Claim("radius", px(9f), px(9f)))),
        )

        // And the readers it compares against must distinguish the design's own values, or the
        // equalities above hold over one number repeated.
        assertNotEquals(
            "the CSS colour reader returns the same value for the card fill and its hairline",
            KitOrigin.cssColour(".prow", "background"),
            KitOrigin.cssColour(".prow", "border"),
        )
        assertNotEquals(
            "the CSS dimension reader returns the same value for a 9px radius and an 8px one",
            KitOrigin.cssDp(".prow", "border-radius"),
            KitOrigin.cssDp(".chip", "border-radius"),
        )
    }
}
