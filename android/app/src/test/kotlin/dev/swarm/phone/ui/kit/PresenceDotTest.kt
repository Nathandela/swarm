package dev.swarm.phone.ui.kit

import android.content.Context
import android.view.View
import android.view.ViewGroup
import androidx.test.core.app.ApplicationProvider
import dev.swarm.phone.theme.SwarmTheme
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import kotlin.math.roundToInt

/**
 * FAILING-FIRST (TDD RED, GG-5) for PB-DS-10 over the presence mark in derivation row 11.
 *
 * WHAT THIS COMPONENT IS FOR, since it looks like a duplicate of [statusDot] and the whole of its
 * justification is that it is not one. A machine's reachability is not a `status.Group`: the four
 * Groups are the server's derived session states, the phone renders them verbatim and never
 * invents one (PB-TOK-8), and `TestPBDS7_TheStatusDotBindingIsTheCheckedInMapping` fails on any
 * bound key absent from `android/group-tokens.tsv` precisely so that stays true. The trap is that
 * the two colours row 11 gives presence -- `--p-ok` online, `--p-ink3` offline -- are the SAME
 * two tokens `ready_for_review` and `completed` already carry, so `statusDot(context, "completed")`
 * for an offline machine renders every pixel correctly and is a fabricated Group. This factory
 * exists so the right pixels are reachable without it.
 *
 * WHAT IT SHARES AND WHAT IT DOES NOT. The drawable, the 7 dp diameter and the flat treatment are
 * `.pdot`'s and are asserted against the design source here, exactly as `InboxRowTest` asserts
 * them for the status dot. The BINDING is the only thing that differs, and it has THREE values.
 *
 * **THE BINDING WAS A BOOLEAN AND ADR-009 D2 IS WHY IT IS NOT.** This file's header used to end
 * "The BINDING is the only thing that differs, and it is a boolean", and the factory took
 * `online: Boolean`: `App.Presence`'s third word, `unknown`, was folded onto the offline visual on
 * the reasoning that the caller states the word in copy beside the mark. That reasoning was sound
 * against the SUBSTRATE artifact, which draws no `.pdot.unknown` rule at all -- row 11 gives
 * presence two colours and two was all there was to render. The Obsidian maquette draws three:
 *
 *	.pdot.online  { background: var(--p-ok); }
 *	.pdot.offline { background: var(--p-ink3); }
 *	.pdot.unknown { background: transparent; border: 1px solid var(--p-ink3); }
 *
 * and its component sheet labels the cell "PresenceDot -- 3 states" with `unknown - hollow`. The
 * maquette is the normative design source (ADR-009 D2) and the migration plan's phase O1 lists
 * `PresenceDot x3` by name, so a component that renders two of the three is a fidelity miss and
 * not a judgement call. The failure it produces is exact: a machine whose presence is `unknown`
 * -- which is what a relay restart produces, since presence is never persisted -- renders
 * identically to one confirmed asleep.
 *
 * WHAT SURVIVES THE CHANGE, because the old reasoning was half right: `unknown` still must not
 * read as REACHABLE. A hollow ring in the recessive ink is not the online fill and could not be
 * mistaken for it; it is the absence of a mark, drawn.
 *
 * IT NEVER GLOWS, IN EITHER STATE, and that is row 11's own sentence rather than an omission:
 * "Flat in both states -- no glow. Nothing glows unless it is alive, and a reachable machine is
 * not a running agent." The plausible wrong answer is the live one -- online is the state that
 * feels alive -- so the negative half below is what holds it.
 */
@RunWith(RobolectricTestRunner::class)
class PresenceDotTest {

    private val context: Context
        get() = SwarmTheme.applyTo(ApplicationProvider.getApplicationContext())

    private val density: Float get() = context.resources.displayMetrics.density

    private fun px(dp: Float) = dp * density

    /** The dot's core in whole pixels: the design's 7 dp as the platform spends a dimension. */
    private val corePx: Int get() = px(KitOrigin.cssDp(".pdot", "width")).roundToInt()

    /** The space the mark occupies -- its box plus whatever it gives back. See [statusDot]. */
    private fun footprint(dot: View): Int {
        val params = dot.layoutParams as ViewGroup.MarginLayoutParams
        return params.width + params.marginStart + params.marginEnd
    }

    private fun drawableOf(dot: View): StatusDotDrawable {
        val background = dot.background
        assertTrue(
            "the presence dot's background is $background, not a StatusDotDrawable -- so whatever " +
                "it paints, it is not the mark `.pdot` specifies",
            background is StatusDotDrawable,
        )
        return background as StatusDotDrawable
    }

    // ---- the mark ----------------------------------------------------------

    @Test
    fun `presence is the design's 7dp mark in the two tokens row 11 names`() {
        val online = drawableOf(presenceDot(context, PresenceMark.ONLINE))
        val offline = drawableOf(presenceDot(context, PresenceMark.OFFLINE))

        val claims = listOf(
            Claim("online fill", KitOrigin.maquetteColour(".pdot.online", "background"), online.fill),
            Claim("offline fill", KitOrigin.maquetteColour(".pdot.offline", "background"), offline.fill),
            Claim("`.pdot` diameter, online", px(KitOrigin.cssDp(".pdot", "width")), online.diameterPx),
            Claim("`.pdot` diameter, offline", px(KitOrigin.cssDp(".pdot", "width")), offline.diameterPx),
            Claim("`.pdot` height", px(KitOrigin.cssDp(".pdot", "height")), online.diameterPx),
            Claim("online is a disc", 0f, online.strokePx),
            Claim("offline is a disc", 0f, offline.strokePx),
            Claim(
                "online occupies 7px of layout",
                corePx,
                footprint(presenceDot(context, PresenceMark.ONLINE)),
            ),
            Claim(
                "offline occupies 7px of layout",
                corePx,
                footprint(presenceDot(context, PresenceMark.OFFLINE)),
            ),
        )
        assertEquals(mismatches(claims).joinToString("\n"), emptyList<String>(), mismatches(claims))

        assertNotEquals(
            "online and offline paint the same colour, so the dot reports nothing at all",
            online.fill,
            offline.fill,
        )
    }

    /**
     * The third state: `.pdot.unknown` is a HOLLOW RING, and it is the one the app never drew.
     *
     * THE ASSERTION IS ABOUT SHAPE AND NOT ABOUT COLOUR, and that is the design's doing rather
     * than a concession. The maquette gives the ring `--p-ink3`, which is the same ink the offline
     * DISC takes -- so a test that only compared fills would pass over the exact defect this
     * exists for. What separates the two states is that one is drawn and one is outlined, and the
     * outline is what "we have no record" looks like: the mark is there, and there is nothing in
     * it.
     *
     * THE STROKE IS INSIDE THE 7 dp AND THE FOOTPRINT DOES NOT MOVE. The maquette sets
     * `box-sizing: border-box` on every element it draws, so `.pdot.unknown`'s 1px border is
     * inside the 7px box rather than added to it. A ring that grew the mark by 2 dp would push
     * every machine row's text a hairline sideways on a relay restart.
     */
    @Test
    fun `the unknown machine is the maquette's hollow ring and not the offline disc`() {
        val unknown = drawableOf(presenceDot(context, PresenceMark.UNKNOWN))
        val offline = drawableOf(presenceDot(context, PresenceMark.OFFLINE))

        val claims = listOf(
            Claim("ring colour", KitOrigin.maquetteColour(".pdot.unknown", "border"), unknown.fill),
            Claim(
                // Rounded, because a 1 dp BORDER is quantised the way every other hairline in
                // this kit is (PB-DS-6's one-value-one-rendering rule, which
                // TestPBDS6_EveryKitMetricIsRenderedOneWay enforces over `HAIRLINE_DP`).
                "ring weight",
                px(KitOrigin.maquetteFirstPx(".pdot.unknown", "border")).roundToInt().toFloat(),
                unknown.strokePx,
            ),
            Claim("`.pdot` diameter", px(KitOrigin.cssDp(".pdot", "width")), unknown.diameterPx),
            Claim(
                "the ring occupies 7px of layout, like the two discs",
                corePx,
                footprint(presenceDot(context, PresenceMark.UNKNOWN)),
            ),
        )
        assertEquals(mismatches(claims).joinToString("\n"), emptyList<String>(), mismatches(claims))

        assertNotEquals(
            "`unknown` renders with the same stroke as `offline`, so the relay having no record " +
                "of a machine is indistinguishable from the relay reporting it asleep. That is " +
                "the state a relay restart produces, and the maquette draws a third mark for it",
            offline.strokePx,
            unknown.strokePx,
        )
        assertNotEquals(
            "the unknown ring is painted `--p-ok`, so a machine nothing has heard from reads as " +
                "reachable -- the absence of evidence rendered as evidence",
            KitOrigin.token("--p-ok"),
            unknown.fill,
        )
    }

    /**
     * The two fills are the tokens' own values, read from the token origin.
     *
     * THE JOIN IS THROUGH THE TOKEN AND NOT THROUGH THE GROUP TABLE, which is the whole point of
     * this component. `--p-ok` is also `ready_for_review`'s colour and `--p-ink3` is also
     * `completed`'s, so an expectation read out of `group-tokens.tsv` would pass identically for a
     * presence dot implemented by handing [statusDot] a fabricated Group -- and would then be
     * asserting the defect rather than the design.
     */
    @Test
    fun `the offline mark is the recessive ink and not a second error colour`() {
        val offline = drawableOf(presenceDot(context, PresenceMark.OFFLINE))
        assertNotEquals(
            "the offline machine is painted `--p-err`. Red means denial, failure and destruction " +
                "in this product, and a machine that is asleep is none of those -- row 11 gives " +
                "it `--p-ink3`, the same recessive ink `completed` takes: both mean not active",
            KitOrigin.token("--p-err"),
            offline.fill,
        )
    }

    /**
     * Row 11: flat in every state.
     *
     * BOTH HALVES ARE ASSERTED because either one alone renders nothing. `setShadowLayer` is
     * ignored under hardware acceleration for everything but text, so a glow set without a
     * software layer draws flat while every value a test could read off the Paint says otherwise;
     * and a software layer with no glow is a bitmap allocated per row for nothing.
     *
     * The loop was `listOf(true, false)` and walks [PresenceMark.entries] now: the ring is the
     * state most likely to acquire a glow by accident, since "we cannot reach it" is the one a
     * designer would be tempted to make anxious.
     */
    @Test
    fun `no state glows, and none pays for a software layer`() {
        PresenceMark.entries.forEach { mark ->
            val dot = presenceDot(context, mark)
            val drawable = drawableOf(dot)
            val claims = listOf(
                Claim("$mark glow", null, drawable.glow),
                Claim("$mark glow radius", 0f, drawable.glowRadiusPx),
                Claim("$mark layer", View.LAYER_TYPE_NONE, dot.layerType),
            )
            assertEquals(mismatches(claims).joinToString("\n"), emptyList<String>(), mismatches(claims))
        }
    }

    // ---- what a screen reader gets -----------------------------------------

    /**
     * The words are the screen's (PB-DS-9) and `null` is a decision rather than an omission --
     * [statusDot]'s arrangement, for [statusDot]'s reason. What must never happen is the EMPTY
     * string, which is the platform's idiom for "decorative, skip me" and is what a caller writing
     * `description ?: ""` ships.
     */
    @Test
    fun `an undescribed dot is decorative and a described one is announced`() {
        val silent = presenceDot(context, PresenceMark.ONLINE)
        assertEquals(
            "an undescribed dot is left to a platform heuristic rather than marked decorative",
            View.IMPORTANT_FOR_ACCESSIBILITY_NO,
            silent.importantForAccessibility,
        )
        assertNull("a decorative dot carries words it was never given", silent.contentDescription)

        val spoken = presenceDot(context, PresenceMark.OFFLINE, description = "quanthome, offline")
        assertEquals("quanthome, offline", spoken.contentDescription)
        assertNull(
            "a described dot announces nothing a screen reader can use",
            announcementFault(spoken.contentDescription, text = null),
        )
    }

    // ---- PB-DS-10's control -------------------------------------------------

    @Test
    fun `the comparison fails when a value diverges`() {
        val online = drawableOf(presenceDot(context, PresenceMark.ONLINE))

        assertTrue(
            "a perturbed fill produced no mismatch, so the colour claims above would hold against " +
                "a dot painted in anything at all",
            mismatches(listOf(Claim("online fill", KitOrigin.token("--p-ok") + 1, online.fill)))
                .isNotEmpty(),
        )
        assertTrue(
            "a perturbed glow produced no mismatch, so \"flat in every state\" is asserted " +
                "against nothing and a glowing presence dot would pass",
            mismatches(listOf(Claim("online glow", KitOrigin.token("--p-ok"), online.glow)))
                .isNotEmpty(),
        )
        assertTrue(
            "a perturbed stroke produced no mismatch, so the ring assertion holds against a disc " +
                "-- which is the exact defect the third state exists to fix",
            mismatches(listOf(Claim("online stroke", 1f, online.strokePx))).isNotEmpty(),
        )
        assertTrue(
            "the comparison reports a mismatch for a value that agrees, so it fails on everything",
            mismatches(listOf(Claim("online fill", online.fill, online.fill))).isEmpty(),
        )
    }
}
