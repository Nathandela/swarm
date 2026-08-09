package dev.swarm.phone.ui.kit

import android.content.Context
import android.graphics.Color
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
 * FAILING-FIRST (TDD RED, GG-5) for PB-DS-10 over the kit's FOUNDATION: the Group bindings, the
 * blend, and the three drawable factories every surfaced component is built out of.
 *
 * "For every component: a test inflates it and asserts the RESOLVED colour int, dimension px,
 *  typeface and letter-spacing against the token origin -- not against a constant recorded from
 *  the implementation. Each such test carries a negative control proving it fails when the value
 *  diverges."
 *
 * WHY THE FOUNDATION GETS ITS OWN SUITE. Three of the four values a card paints -- the derived
 * attention border, the inset key-light and the one dot glow -- are colours that exist in NO
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
     * "Nothing glows unless it is alive" is still the rule; since ruling R8 it no longer runs
     * both ways. Two Groups are alive (NeedsInput, Working) and exactly one glows.
     *
     * AUTHORIZED REWRITE, ruling R8 (2026-08-09, bead agents-tracker-oonj). The share used to be
     * read off the shared block's `.pdot.att`/`.pdot.wrk`, both carrying a `color-mix` share; the
     * maquette's own `.sdot.att` carries a literal `rgba()` and `.sdot.work`/`.sdot.ok`/
     * `.sdot.done` declare no `box-shadow` at all -- one glow, not two. The share is still read
     * off the rule whose fill IS the Group's token, so which Group glows is a property of the
     * design rather than a list in this test.
     */
    @Test
    fun `the one live Group that glows does so at the maquette's share, and the other three do not glow`() {
        val variants = KitOrigin.maquetteDotVariants()
        assertTrue(
            "the maquette declares no .sdot variants; every expectation here would be null",
            variants.isNotEmpty(),
        )

        val claims = KitOrigin.groupTokens().map { (group, token) ->
            Claim("$group ($token) glow", KitOrigin.dotGlow(token)?.colour, Kit.groupGlow(context, group))
        }
        assertEquals(1, claims.count { it.want != null })
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
        //
        // `.toInt()` BECAME `.roundToInt()` IN THE SAME BREATH, and that is a correction rather
        // than an accommodation. `.toInt()` TRUNCATES, and 0.045 * 255 = 11.475 truncates and
        // rounds to the same 11 -- so the control agreed with ColorMix.quantise by luck and
        // nobody could see that the two used different arithmetic. At 0.10 the product is exactly
        // 25.5 and the two answers separate: truncation says 25, rounding says 26, and rounding
        // is what an 8-bit alpha quantisation must do (it is what ColorMix does, once, at the end
        // of the blend, for the reason its own doc gives).
        assertEquals(
            "the key light is a translucent linen, not the opaque one a missing alpha produces",
            (0.10f * 255f).roundToInt(),
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
                    // AUTHORIZED CLAIM MIGRATION, ADR-009 D3/D4. What this said before:
                    //
                    //     Claim("the fill is unchanged",
                    //           KitOrigin.cssColour(".prow", "background"), surface.spec.fill)
                    //
                    // It was Substrate's decision stated as a property: the attention row differed
                    // from a resting one by a border and a rail and by nothing else. ADR-009 adds
                    // `--p-lit-fx` as a NEW effect for exactly this row -- "the promoted card: a
                    // NeedsInput slab carries the stronger key-light" -- and the maquette draws it
                    // one ladder step up, `.slab.lit { background: var(--p-elev) }`. So the fill
                    // moves, and the claim is re-pointed at the source that moved it rather than
                    // deleted: `.slab.lit` is read here, not transcribed.
                    Claim(
                        "the promoted fill is the maquette's",
                        KitOrigin.maquetteColour(".slab.lit", "background"),
                        surface.spec.fill,
                    ),
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

    /**
     * `.slab.lit`: ADR-009 D4's promoted slab, which is a MATERIAL statement and not a decoration.
     *
     * The maquette gives a promoted row two things at once and either alone is the wrong drawing.
     * It sits one ladder step up -- `background: var(--p-elev)` -- and it catches more of the one
     * light source -- `box-shadow: var(--p-lit-fx)`, 0.22 against the resting 0.10. A slab that
     * took the stronger edge on the card fill is a card with a bright line on it; a slab that took
     * the elevated fill with the resting edge is a toast. Together they are the single thing D4
     * describes: the same light, on material that has come forward.
     *
     * EVERY EXPECTATION IS READ FROM THE MAQUETTE, which is what makes this an assertion about the
     * design rather than about itself. `.slab.lit`'s two declarations name tokens; the reader
     * resolves them. A suite that wrote `--p-elev` and `0.22f` here would agree with the kit
     * forever and could not tell a promoted slab from a toast.
     */
    @Test
    fun `the promoted card is the elevated slab under the stronger key light`() {
        val surface = cardSurface(context, attention = true)
        val highlight = (0 until surface.numberOfLayers)
            .map { surface.getDrawable(it) }
            .filterIsInstance<EdgeHighlight>()
            .singleOrNull()
        assertTrue(
            "the promoted card carries no EdgeHighlight layer at all, so --p-lit-fx is either " +
                "missing or has been approximated by something that is not a layer",
            highlight != null,
        )
        assertEquals(
            emptyList<String>(),
            mismatches(
                listOf(
                    Claim(
                        "`.slab.lit` fill",
                        KitOrigin.maquetteColour(".slab.lit", "background"),
                        surface.spec.fill,
                    ),
                    Claim(
                        "`.slab.lit` key light",
                        KitOrigin.maquetteRgba(".slab.lit", "box-shadow"),
                        highlight!!.colour,
                    ),
                    Claim(
                        "`.slab.lit` key-light band",
                        px(KitOrigin.maquetteFirstPx(".slab.lit", "box-shadow")),
                        highlight.heightPx,
                    ),
                    Claim(
                        "the promoted key light is `--p-lit-fx` and not `--p-card-fx`",
                        KitOrigin.rgbaToken("--p-lit-fx"),
                        highlight.colour,
                    ),
                ),
            ),
        )

        // THE TWO SURFACES MUST DIFFER, or every claim above holds over one value repeated. The
        // resting slab is the subject of `the card surface resolves the design's card`; what is
        // asserted here is that promotion is VISIBLE -- a lit slab whose fill and edge happened to
        // equal the resting one would satisfy each equality above and render no promotion at all.
        val resting = cardSurface(context, attention = false)
        assertNotEquals(
            "the promoted slab and the resting one paint the same fill, so ADR-009 D4's ladder " +
                "step does not exist on screen",
            resting.spec.fill,
            surface.spec.fill,
        )
        assertNotEquals(
            "the promoted slab and the resting one carry the same key light, so `--p-lit-fx` is " +
                "`--p-card-fx` under another name",
            resting.spec.keyLight,
            surface.spec.keyLight,
        )
    }

    /**
     * `.sheet`: ADR-009 D4.4's approval sheet -- the ONE vertical gradient in the app, and the
     * heaviest material there is, reserved for the moment of decision.
     *
     * FOUR THINGS AT ONCE, AND THE GRADIENT IS ONLY THE OBVIOUS ONE. The maquette draws
     * `background: linear-gradient(180deg, var(--p-sheet-hi), var(--p-sheet-lo))`, the hairline
     * every surface carries, `box-shadow: var(--p-lit-fx)` -- the strong 0.22 edge, which D4.4
     * gives to this and to a promoted slab and to nothing else -- and `border-radius:
     * var(--p-sheet-r)`, the one radius in the scale that had no component spending it.
     *
     * THE TWO STOPS ARE READ SEPARATELY. They are two tokens because a gradient's endpoints are
     * chosen rather than computed from a base; a reader that took the first colour and let the
     * second follow from an alpha would accept the exact collapse `colors.xml`'s own comment says
     * the two resources exist to prevent.
     *
     * THE UNIQUENESS IS ASSERTED, not assumed. "The only vertical gradient" is a property of the
     * whole kit and it is the half a test of this component alone cannot see, so every other
     * surface recipe is asked whether it has a second stop.
     */
    @Test
    fun `the approval sheet is the one vertical gradient, under the strong edge`() {
        val surface = sheetSurface(context)
        val stops = KitOrigin.maquetteColours(".sheet", "background")
        val highlight = (0 until surface.numberOfLayers)
            .map { surface.getDrawable(it) }
            .filterIsInstance<EdgeHighlight>()
            .singleOrNull()
        assertTrue("the sheet carries no EdgeHighlight layer, so `--p-lit-fx` is not on it", highlight != null)

        assertEquals(
            "`.sheet { background }` resolves to a number of colours other than two, so the " +
                "expectations below are reading something that is not the design's gradient",
            2,
            stops.size,
        )
        assertEquals(
            emptyList<String>(),
            mismatches(
                listOf(
                    Claim("`.sheet` gradient top", stops[0], surface.spec.fill),
                    Claim("`.sheet` gradient bottom", stops[1], surface.spec.fillBottom),
                    Claim("`.sheet` border", KitOrigin.maquetteColour(".sheet", "border"), surface.spec.stroke),
                    Claim(
                        "`.sheet` border width",
                        px(KitOrigin.maquetteFirstPx(".sheet", "border")).roundToInt(),
                        surface.spec.strokeWidthPx,
                    ),
                    Claim(
                        "`.sheet` radius",
                        px(KitOrigin.maquetteFirstPx(".sheet", "border-radius")),
                        surface.spec.radiusPx,
                    ),
                    Claim("`.sheet` key light", KitOrigin.maquetteRgba(".sheet", "box-shadow"), highlight!!.colour),
                    Claim("the sheet is not a row", null, surface.spec.rail),
                ),
            ),
        )
        assertNotEquals(
            "the sheet's two gradient stops are the same colour, so it renders as a flat surface " +
                "and the one vertical gradient in the app does not exist",
            surface.spec.fill,
            surface.spec.fillBottom,
        )
        assertEquals(
            "the sheet's radius is the card's, which is the coincidence `--p-sheet-r` exists to " +
                "stop being one",
            emptyList<String>(),
            mismatches(
                listOf(
                    Claim(
                        "the sheet radius differs from the card's",
                        true,
                        surface.spec.radiusPx != cardSurface(context, attention = false).spec.radiusPx,
                    ),
                ),
            ),
        )

        // D4.4's word is ONLY, and that is a claim about every other surface in this kit.
        listOf(
            "cardSurface(resting)" to cardSurface(context, attention = false),
            "cardSurface(promoted)" to cardSurface(context, attention = true),
            "toastSurface" to toastSurface(context),
            "chipSurface(idle)" to chipSurface(context, selected = false),
            "chipSurface(selected)" to chipSurface(context, selected = true),
            "wellSurface" to wellSurface(context),
            "pillSurface" to pillSurface(context, KitOrigin.token("--p-att")),
            "panelSurface" to panelSurface(context, Kit.killSwitchBorder(context)),
        ).forEach { (name, other) ->
            assertNull(
                "$name carries a second gradient stop. ADR-009 D4.4 gives the app exactly one " +
                    "vertical gradient and reserves it for the moment of decision; a second one " +
                    "spends the heaviest material the skin has on something that is not a choice.",
                other.spec.fillBottom,
            )
        }
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
