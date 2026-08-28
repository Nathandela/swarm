package dev.swarm.phone.ui.kit

import android.content.Context
import android.graphics.Color
import android.provider.Settings
import android.util.TypedValue
import androidx.test.core.app.ApplicationProvider
import dev.swarm.phone.theme.SwarmTheme
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotEquals
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import kotlin.math.roundToInt

/**
 * FAILING-FIRST (TDD RED, GG-5) for PB-DS-10 over the maquette's `.tog`.
 *
 * **THIS FILE USED TO SAY THE TOGGLE HAD NO DESIGN SOURCE, AND ADR-009 D2 ENDED THAT.** Its header
 * opened "THE TOGGLE IS THE COMPONENT IN TWO DOCUMENTS' BLIND SPOTS ... Substrate draws no
 * `.toggle` rule at all, so row 4 is the whole specification -- there is no CSS for [KitOrigin] to
 * resolve, which is why every other appearance suite in this package can name a selector and this
 * one cannot." That was true of `docs/research/remote-control-design-directions.html`. It is false
 * of `docs/research/obsidian-maquette.html`, which draws the control in all three states and
 * labels the cell "Toggle - 3 states":
 *
 *	.tog      { width: 40px; height: 24px; border-radius: 12px; background: var(--p-elev);
 *	            border: 1px solid var(--p-hair); position: relative; }
 *	.tog i    { position: absolute; top: 3px; left: 3px; width: 16px; height: 16px;
 *	            border-radius: 50%; background: var(--p-ink3); }
 *	.tog.on   { background: var(--p-hero); border-color: var(--p-hero); }
 *	.tog.on i { left: auto; right: 3px; background: var(--p-hero-ink); }
 *
 * so every number and every colour below is RESOLVED FROM THE DESIGN through
 * [KitOrigin.maquetteFirstPx] and [KitOrigin.maquetteColour], the way every other suite in this
 * package works. The four assertions this file used to make against
 * `docs/design/substrate-components.md` row 4 are quoted where they are replaced.
 *
 * WHAT ACTUALLY MOVED, since "re-token it" would have been the cheap reading of the migration and
 * is not what the maquette asks for:
 *
 *   - track 46x28 -> 40x24, thumb 24 -> 16, inset `space_2` -> 3, travel 18 -> 16;
 *   - the off track was `color-mix(--p-ink3 40%, transparent)` with NO border and is now a solid
 *     `--p-elev` fill inside a 1 dp `--p-hair` hairline;
 *   - the thumb was `--p-ink` in BOTH states and now crosses `--p-ink3` -> `--p-hero-ink`.
 *
 * THE BORDER-BOX ARITHMETIC IS THE ONE SUBTLETY. The maquette sets `box-sizing: border-box`, so
 * `.tog`'s 40x24 INCLUDES its 1px border, and `.tog i`'s `top: 3px` is measured from the padding
 * box -- inside that border. The thumb therefore sits 4 px from the outer edge on every side
 * (`1 + 3`), the height checks out as `1 + 3 + 16 + 3 + 1 = 24`, and the travel is
 * `40 - 2 * (1 + 3) - 16 = 16`. That is why travel is no longer a constant: it is the width with
 * everything else taken out of it, and a constant would be a fifth number free to disagree with
 * the four the design states.
 *
 * THE >=48 dp TOUCH TARGET IS STILL NOT SET HERE, and the reason is unchanged: row 15 puts it on
 * the settings row ("the whole row is one >=48 dp target when it carries a toggle"), and a 40x24
 * control grown to 48 dp would meet the number by destroying the drawing.
 */
@RunWith(RobolectricTestRunner::class)
class ToggleTest {

    private val context: Context
        get() = SwarmTheme.applyTo(ApplicationProvider.getApplicationContext())

    @Before
    fun startFromUnreducedMotion() {
        // A prior test's zero scale must not leak into this one -- see MotionTest, same reason.
        setAnimatorScale(1f)
    }

    private fun setAnimatorScale(scale: Float) {
        Settings.Global.putFloat(
            context.contentResolver, Settings.Global.ANIMATOR_DURATION_SCALE, scale,
        )
    }

    /** The design's px is Android dp at 1:1 -- the artifact is a 386x812 frame at device scale. */
    private fun dpPx(value: Float): Int = TypedValue.applyDimension(
        TypedValue.COMPLEX_UNIT_DIP, value, context.resources.displayMetrics,
    ).roundToInt()

    private val description = "End-to-end encryption"

    private fun off() = toggle(context, checked = false, description = description)

    private fun on() = toggle(context, checked = true, description = description)

    private fun trackOf(subject: ToggleSwitch) = subject.background as TogglePill

    private fun thumbOf(subject: ToggleSwitch) = subject.kitRequire(KitTag.TOGGLE_THUMB)

    private fun thumbFillOf(subject: ToggleSwitch) = thumbOf(subject).background as TogglePill

    /**
     * `.tog.on { background: var(--p-hero) }` -- the hero fill, not the ok green.
     *
     * The negative half is kept from the row-4 era because it is the mistake that would survive
     * review: after B134 `--p-ok` carries ReadyForReview, and a control's on-state is not a status
     * -- a green toggle and a green dot on the inbox would be one colour saying two unrelated
     * things. The maquette's `.chip.on` takes the same fill for the same reason.
     */
    @Test
    fun `the on track is the hero fill and not the ok green`() {
        val hero = KitOrigin.maquetteColour(".tog.on", "background")
        val claims = listOf(Claim("`.tog.on { background }`", hero, trackOf(on()).colour))

        assertEquals(mismatches(claims).joinToString("\n"), emptyList<String>(), mismatches(claims))
        assertNotEquals(
            "the on track is --p-ok, which after B134 is ReadyForReview's colour -- a status " +
                "colour on a control that reports no status",
            KitOrigin.token("--p-ok"),
            trackOf(on()).colour,
        )
    }

    /**
     * `.tog.on { border-color: var(--p-hero) }` -- the edge crosses with the fill.
     *
     * A track that swapped its fill and kept the `--p-hair` edge would draw a champagne capsule
     * outlined in linen-warm grey, which is not a state the design contains: the ON track is one
     * flat champagne object, and its border is champagne so that it vanishes into the fill.
     */
    @Test
    fun `the on track's edge is the hero, so the capsule reads as one object`() {
        val claims = listOf(
            Claim(
                "`.tog.on { border-color }`",
                KitOrigin.maquetteColour(".tog.on", "border-color"),
                trackOf(on()).borderColour,
            ),
        )

        assertEquals(mismatches(claims).joinToString("\n"), emptyList<String>(), mismatches(claims))
        assertNotEquals(
            "the on track keeps the off state's hairline edge, so the engaged control is a " +
                "champagne fill inside a grey outline",
            KitOrigin.maquetteColour(".tog", "border"),
            trackOf(on()).borderColour,
        )
    }

    /**
     * `.tog { background: var(--p-elev); border: 1px solid var(--p-hair) }` -- a SURFACE, not a
     * blend, and it has an edge.
     *
     * THIS REPLACES THE TEST THAT REQUIRED A TRANSLUCENT BLEND. That one read
     *
     *	fun `the off track is the tertiary ink blended the premultiplied way`() {
     *	    val ink3 = KitOrigin.token("--p-ink3")
     *	    val resolved = trackOf(off()).colour
     *	    val share = Color.alpha(resolved) / 255f
     *	    ... Claim("row 4 off track over transparent",
     *	              KitOrigin.overTransparent(ink3, share), resolved)
     *	    assertTrue("the off track is fully opaque, so it is a flat --p-ink3 rather than a
     *	               share of it ...", Color.alpha(resolved) < 255)
     *
     * -- `color-mix(in srgb, --p-ink3 40%, transparent)`, taken from `substrate-components.md`
     * row 4 for a component the Substrate artifact drew no rule for. The maquette draws one and it
     * is not a blend at all: it is `--p-elev`, the ladder's one step above card, inside the same
     * hairline every other bordered surface in this design carries. So the assertion INVERTS --
     * the track must now be opaque, and the blend that used to be required is the failure.
     *
     * THE BORDER IS THE HALF THAT IS EASIEST TO SKIP, and the one worth having a claim of its own.
     * A solid `--p-elev` capsule with no edge looks like a plausible control and is the drawing
     * with its outline left off; over a warm near-black card the elevated surface is a few RGB
     * steps from the ground behind it, so the hairline is most of what makes the track visible.
     */
    @Test
    fun `the off track is the elevated surface inside a hairline, not a blend`() {
        val track = trackOf(off())
        val claims = listOf(
            Claim("`.tog { background }`", KitOrigin.maquetteColour(".tog", "background"), track.colour),
            Claim("`.tog { border }` colour", KitOrigin.maquetteColour(".tog", "border"), track.borderColour),
            Claim(
                "`.tog { border }` weight",
                dpPx(KitOrigin.maquetteFirstPx(".tog", "border")).toFloat(),
                track.borderPx,
            ),
        )

        assertEquals(
            "the off track is translucent, which is the retired row-4 blend " +
                "(`color-mix(--p-ink3 40%, transparent)`); the maquette gives it a solid surface",
            255,
            Color.alpha(track.colour),
        )
        assertNotEquals(
            "the off track is still a share of --p-ink3",
            KitOrigin.token("--p-ink3"),
            track.colour,
        )
        assertEquals(mismatches(claims).joinToString("\n"), emptyList<String>(), mismatches(claims))
    }

    /**
     * THE THUMB CHANGES COLOUR, and the test this replaces asserted that it never does.
     *
     * The old one read
     *
     *	// Row 4: "thumb `--p-ink` both states" -- the thumb's position carries the state, not
     *	// its ink.
     *	fun `the thumb is the primary ink in both states`() {
     *	    val ink = KitOrigin.token("--p-ink")
     *	    Claim("row 4 thumb, off", ink, thumbFillOf(off()).colour)
     *	    Claim("row 4 thumb, on",  ink, thumbFillOf(on()).colour)
     *
     * and Toggle.kt's own KDoc still carried that sentence in capitals. The maquette draws
     * `.tog i { background: var(--p-ink3) }` and `.tog.on i { background: var(--p-hero-ink) }` --
     * two inks, and the ON one is the near-black every other accent fill in this skin puts its
     * content in. It has to be: a pale thumb on the accent track is light-on-light, the one
     * contrast pair the fill's own ceiling makes unwinnable (ADR-009 D8.1's amendment measured the
     * maximum reachable |Lc| on champagne `#c9a876` at 59.73, with pure white reaching only 49;
     * ADR-020 re-measured it on slate `#8eb4e6` at 62.04, white reaching 46.58).
     */
    @Test
    fun `the thumb crosses between the two inks the maquette gives it`() {
        val claims = listOf(
            Claim(
                "`.tog i { background }`",
                KitOrigin.maquetteColour(".tog i", "background"),
                thumbFillOf(off()).colour,
            ),
            Claim(
                "`.tog.on i { background }`",
                KitOrigin.maquetteColour(".tog.on i", "background"),
                thumbFillOf(on()).colour,
            ),
        )

        assertEquals(mismatches(claims).joinToString("\n"), emptyList<String>(), mismatches(claims))
        assertNotEquals(
            "the thumb is one ink in both states, which is what row 4 specified (`thumb --p-ink " +
                "both states`) and what Toggle.kt's KDoc still says in capitals",
            thumbFillOf(off()).colour,
            thumbFillOf(on()).colour,
        )
    }

    /**
     * The maquette's geometry, as the border-box arithmetic it is.
     *
     * THIS REPLACES ROW 4's SUMS. The old test read
     *
     *	val inset = dimenPx("swarm_space_2")
     *	Claim("row 4 track height", thumb.height + inset + inset, track.height)
     *	Claim("row 4 track width",  thumb.width + travelPx() + inset + inset, track.width)
     *
     * because row 4 stated `track 46x28` as a sum of thumb, travel and two `space_2` insets, and
     * the two leaves lived in a document this classpath cannot read. The maquette states the
     * track's own width and height, so those are what the component must equal -- and the SUM is
     * kept as a consistency check, because a 40x24 track whose parts do not add up to 40x24 is a
     * drawing nobody could have rendered.
     */
    @Test
    fun `the track is the maquette's 40 by 24, and its parts add up to it`() {
        val subject = off()
        val thumb = thumbOf(subject).layoutParams
        val track = subject.layoutParams
        val inset = dpPx(KitOrigin.maquetteFirstPx(".tog i", "top"))
        val border = dpPx(KitOrigin.maquetteFirstPx(".tog", "border"))

        val claims = listOf(
            Claim("`.tog { width }`", dpPx(KitOrigin.maquetteFirstPx(".tog", "width")), track.width),
            Claim("`.tog { height }`", dpPx(KitOrigin.maquetteFirstPx(".tog", "height")), track.height),
            Claim("`.tog i { width }`", dpPx(KitOrigin.maquetteFirstPx(".tog i", "width")), thumb.width),
            Claim("`.tog i { height }`", dpPx(KitOrigin.maquetteFirstPx(".tog i", "height")), thumb.height),
            Claim("the thumb is square", thumb.height, thumb.width),
            // border-box: `.tog`'s 24 INCLUDES its 1px border, and `.tog i { top: 3px }` is
            // measured from the padding box -- inside that border. So the thumb sits `1 + 3` from
            // the outer edge and the height reads `1 + 3 + 16 + 3 + 1`.
            Claim(
                "height = border + inset + thumb + inset + border",
                border + inset + thumb.height + inset + border,
                track.height,
            ),
            Claim(
                "width = border + inset + thumb + travel + inset + border",
                border + inset + thumb.width + travelPx() + inset + border,
                track.width,
            ),
        )

        assertEquals(mismatches(claims).joinToString("\n"), emptyList<String>(), mismatches(claims))
    }

    /**
     * The pill exception: `.tog { border-radius: 12px }` on a 24 dp track, `50%` on the thumb.
     *
     * Substrate's shape ladder has no pill step and a squared track reads as a checkbox, which is
     * the argument row 4 made for the exception; the maquette draws it and states the number, so
     * the number is checked as well as the halving. Both drawables are asserted because getting
     * one radius right says nothing about the other.
     */
    @Test
    fun `the pill radius is half the track and half the thumb`() {
        val subject = off()
        val claims = listOf(
            Claim(
                "`.tog { border-radius }`",
                dpPx(KitOrigin.maquetteFirstPx(".tog", "border-radius")).toFloat(),
                trackOf(subject).radiusPx,
            ),
            Claim("which is half the track", subject.layoutParams.height / 2f, trackOf(subject).radiusPx),
            Claim(
                "`.tog i { border-radius: 50% }` is half the thumb",
                thumbOf(subject).layoutParams.height / 2f,
                thumbFillOf(subject).radiusPx,
            ),
        )

        assertEquals(mismatches(claims).joinToString("\n"), emptyList<String>(), mismatches(claims))
    }

    /** `.tog i { left: 3px }` and `.tog.on i { right: 3px }`: the thumb rests at each end. */
    @Test
    fun `the thumb rests at each end of its travel`() {
        val claims = listOf(
            Claim("thumb at rest, off", 0f, thumbOf(off()).translationX),
            Claim("thumb at rest, on", travelPx().toFloat(), thumbOf(on()).translationX),
        )

        assertEquals(mismatches(claims).joinToString("\n"), emptyList<String>(), mismatches(claims))
    }

    /**
     * ADR-009 D5's 150 ms toggle, and B134 decision 3's routing.
     *
     * FOUR THINGS MOVE NOW, AND THE COUNT USED TO BE TWO. The assertion read
     *
     *	assertEquals("row 4 moves two things: the thumb and the track", 2, motion.size)
     *
     * when the track crossed one colour and the thumb kept one ink. The maquette gives the ON and
     * OFF states four differences -- the thumb's position, the track's fill, the track's border
     * and the thumb's own ink -- and every one of them is a `transition` in the design's own CSS.
     * A control that crossed three of the four and jumped the last would read as two unrelated
     * events on a 16 dp travel, which is the defect the original two-animator assertion existed to
     * catch, at the size the maquette actually draws.
     */
    @Test
    fun `flipping the toggle carries the thumb, the fill, the edge and the ink together`() {
        val subject = off()
        val motion = subject.moveTo(checked = true)

        assertEquals(
            "the toggle moves four things: the thumb's position, the track's fill, the track's " +
                "edge and the thumb's ink. Anything the design changes between states and this " +
                "control jumps is a state change that reads as a glitch",
            4,
            motion.size,
        )
        motion.forEach {
            assertEquals(
                "ADR-009 D5 keeps the toggle at 150 ms; Motion.TOGGLE_DURATION_MS is where that " +
                    "number lives",
                Motion.TOGGLE_DURATION_MS,
                it.duration,
            )
            it.end()
        }

        val claims = listOf(
            Claim("thumb after the move", travelPx().toFloat(), thumbOf(subject).translationX),
            Claim(
                "track fill after the move",
                KitOrigin.maquetteColour(".tog.on", "background"),
                trackOf(subject).colour,
            ),
            Claim(
                "track edge after the move",
                KitOrigin.maquetteColour(".tog.on", "border-color"),
                trackOf(subject).borderColour,
            ),
            Claim(
                "thumb ink after the move",
                KitOrigin.maquetteColour(".tog.on i", "background"),
                thumbFillOf(subject).colour,
            ),
            Claim("state after the move", true, subject.checked),
        )
        assertEquals(mismatches(claims).joinToString("\n"), emptyList<String>(), mismatches(claims))
    }

    /**
     * ADR-007 B134 decision 3: the toggle inherits reduced-motion coverage from [Motion].
     *
     * THE END STATE STILL HAS TO LAND. "Reduced motion" is not "the control stops working" -- a
     * toggle that collapsed its duration and left the thumb where it was would be a switch that
     * does nothing for the users who asked for less movement. The sibling above, which leaves the
     * platform setting alone and expects the full 150 ms, is this assertion's negative control:
     * without it an implementation that always built a zero-duration animator would pass here.
     */
    @Test
    fun `reduced motion collapses the duration and still lands the end state`() {
        setAnimatorScale(0f)
        val subject = off()
        val motion = subject.moveTo(checked = true)

        motion.forEach {
            assertEquals(
                "an animator built under ANIMATOR_DURATION_SCALE 0 still carries its full " +
                    "duration, so the toggle bypassed Motion's reduced-motion check",
                0L,
                it.duration,
            )
            it.end()
        }

        val claims = listOf(
            Claim("thumb under reduced motion", travelPx().toFloat(), thumbOf(subject).translationX),
            Claim(
                "track under reduced motion",
                KitOrigin.maquetteColour(".tog.on", "background"),
                trackOf(subject).colour,
            ),
            Claim(
                "thumb ink under reduced motion",
                KitOrigin.maquetteColour(".tog.on i", "background"),
                thumbFillOf(subject).colour,
            ),
        )
        assertEquals(mismatches(claims).joinToString("\n"), emptyList<String>(), mismatches(claims))
    }

    /**
     * PB-DS-12: every non-text control has a content description.
     *
     * The toggle is the extreme case of it -- four dp of moving thumb and no text anywhere -- so a
     * screen reader gets nothing at all from the visual. The words are the screen's (PB-DS-9); what
     * the kit owes is that they arrive and that the empty string is never what arrives instead,
     * which is the platform's idiom for "decorative, skip me".
     */
    @Test
    fun `the toggle announces the description it was given`() {
        assertEquals(description, off().contentDescription)
        assertEquals(
            listOf<String?>(null),
            listOf(announcementFault(off().contentDescription, null)),
        )
    }

    /**
     * The negative control PB-DS-10 requires, through the SAME comparison every assertion above
     * uses: one unit of colour, one pixel of geometry, one state.
     *
     * A control that rebuilt the comparison inline would prove the copy works and say nothing about
     * the assertion. Every claim above goes through [mismatches], and so does every perturbation
     * here.
     */
    @Test
    fun `the toggle assertions can actually fail`() {
        val hero = KitOrigin.maquetteColour(".tog.on", "background")
        val inset = dpPx(KitOrigin.maquetteFirstPx(".tog i", "top"))
        val border = dpPx(KitOrigin.maquetteFirstPx(".tog", "border"))
        val thumb = thumbOf(off()).layoutParams.height

        assertTrue(
            "a track colour one unit from the design's passes the comparison",
            mismatches(listOf(Claim("on track", hero, hero + 1))).isNotEmpty(),
        )
        assertTrue(
            "a track one pixel taller than the design's sum passes the comparison",
            mismatches(
                listOf(
                    Claim(
                        "track height",
                        border + inset + thumb + inset + border,
                        border + inset + thumb + inset + border + 1,
                    ),
                ),
            ).isNotEmpty(),
        )
        assertTrue(
            "spending ONE inset where the border-box sum states two passes the comparison, which " +
                "is the mistake a reader who skimmed `.tog { height: 24px }` would make",
            mismatches(
                listOf(
                    Claim("track height", border + inset + thumb + inset + border, thumb + inset),
                ),
            ).isNotEmpty(),
        )
        assertTrue(
            "a thumb that never moved passes the travel comparison",
            mismatches(listOf(Claim("thumb", travelPx().toFloat(), 0f))).isNotEmpty(),
        )
        assertTrue(
            "a toggle left in its old state passes the state comparison",
            mismatches(listOf(Claim("checked", true, false))).isNotEmpty(),
        )
        assertNotEquals(
            "--p-ok compares equal to --p-hero, so the argument for the hero fill is about " +
                "nothing and the on-state assertion would accept either",
            KitOrigin.token("--p-ok"),
            hero,
        )
        assertNotEquals(
            "`.tog { background }` compares equal to `.tog.on { background }`, so every fill " +
                "assertion in this file would accept a track that never changes",
            KitOrigin.maquetteColour(".tog", "background"),
            hero,
        )
        assertNotEquals(
            "`.tog i { background }` compares equal to `.tog.on i { background }`, so the two-ink " +
                "thumb assertion would accept the single-ink thumb it replaced",
            KitOrigin.maquetteColour(".tog i", "background"),
            KitOrigin.maquetteColour(".tog.on i", "background"),
        )
        assertNotEquals(
            "`.tog { border }` compares equal to `.tog.on { border-color }`, so the edge " +
                "assertions would accept a border that never crosses",
            KitOrigin.maquetteColour(".tog", "border"),
            KitOrigin.maquetteColour(".tog.on", "border-color"),
        )
    }

    /**
     * The travel, in pixels, as the DESIGN states it: the track's width with everything else in
     * the row taken out of it.
     *
     * IT IS NO LONGER A CONSTANT AND THAT IS THE POINT. It used to read
     *
     *	private fun travelPx(): Int = dpPx(KitMetrics.TOGGLE_TRAVEL_DP)
     *
     * against `derived: docs/design/substrate-components.md #4 Toggle { travel }` -- row 4's 18,
     * recomputed from that row by the Go gate. The maquette states four numbers (40, 24, 16, 3)
     * and the travel is not among them: it is what is left of the width after the two borders, the
     * two insets and the thumb. A fifth constant would be a number free to disagree with the four,
     * which is exactly how 46x28 and thumb 24 survived a skin change that redrew both.
     */
    private fun travelPx(): Int =
        dpPx(KitOrigin.maquetteFirstPx(".tog", "width")) -
            2 * dpPx(KitOrigin.maquetteFirstPx(".tog", "border")) -
            2 * dpPx(KitOrigin.maquetteFirstPx(".tog i", "top")) -
            dpPx(KitOrigin.maquetteFirstPx(".tog i", "width"))
}
