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
 * them for the status dot. The BINDING is the only thing that differs, and it is a boolean.
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
        val online = drawableOf(presenceDot(context, online = true))
        val offline = drawableOf(presenceDot(context, online = false))

        val claims = listOf(
            Claim("online fill", KitOrigin.token("--p-ok"), online.fill),
            Claim("offline fill", KitOrigin.token("--p-ink3"), offline.fill),
            Claim("`.pdot` diameter, online", px(KitOrigin.cssDp(".pdot", "width")), online.diameterPx),
            Claim("`.pdot` diameter, offline", px(KitOrigin.cssDp(".pdot", "width")), offline.diameterPx),
            Claim("`.pdot` height", px(KitOrigin.cssDp(".pdot", "height")), online.diameterPx),
            Claim("online occupies 7px of layout", corePx, footprint(presenceDot(context, true))),
            Claim("offline occupies 7px of layout", corePx, footprint(presenceDot(context, false))),
        )
        assertEquals(mismatches(claims).joinToString("\n"), emptyList<String>(), mismatches(claims))

        assertNotEquals(
            "online and offline paint the same colour, so the dot reports nothing at all",
            online.fill,
            offline.fill,
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
        val offline = drawableOf(presenceDot(context, online = false))
        assertNotEquals(
            "the offline machine is painted `--p-err`. Red means denial, failure and destruction " +
                "in this product, and a machine that is asleep is none of those -- row 11 gives " +
                "it `--p-ink3`, the same recessive ink `completed` takes: both mean not active",
            KitOrigin.token("--p-err"),
            offline.fill,
        )
    }

    /**
     * Row 11: flat in both states.
     *
     * BOTH HALVES ARE ASSERTED because either one alone renders nothing. `setShadowLayer` is
     * ignored under hardware acceleration for everything but text, so a glow set without a
     * software layer draws flat while every value a test could read off the Paint says otherwise;
     * and a software layer with no glow is a bitmap allocated per row for nothing.
     */
    @Test
    fun `neither state glows, and neither pays for a software layer`() {
        listOf(true, false).forEach { online ->
            val dot = presenceDot(context, online = online)
            val drawable = drawableOf(dot)
            val claims = listOf(
                Claim("online=$online glow", null, drawable.glow),
                Claim("online=$online glow radius", 0f, drawable.glowRadiusPx),
                Claim("online=$online layer", View.LAYER_TYPE_NONE, dot.layerType),
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
        val silent = presenceDot(context, online = true)
        assertEquals(
            "an undescribed dot is left to a platform heuristic rather than marked decorative",
            View.IMPORTANT_FOR_ACCESSIBILITY_NO,
            silent.importantForAccessibility,
        )
        assertNull("a decorative dot carries words it was never given", silent.contentDescription)

        val spoken = presenceDot(context, online = false, description = "quanthome, offline")
        assertEquals("quanthome, offline", spoken.contentDescription)
        assertNull(
            "a described dot announces nothing a screen reader can use",
            announcementFault(spoken.contentDescription, text = null),
        )
    }

    // ---- PB-DS-10's control -------------------------------------------------

    @Test
    fun `the comparison fails when a value diverges`() {
        val online = drawableOf(presenceDot(context, online = true))

        assertTrue(
            "a perturbed fill produced no mismatch, so the colour claims above would hold against " +
                "a dot painted in anything at all",
            mismatches(listOf(Claim("online fill", KitOrigin.token("--p-ok") + 1, online.fill)))
                .isNotEmpty(),
        )
        assertTrue(
            "a perturbed glow produced no mismatch, so \"flat in both states\" is asserted " +
                "against nothing and a glowing presence dot would pass",
            mismatches(listOf(Claim("online glow", KitOrigin.token("--p-ok"), online.glow)))
                .isNotEmpty(),
        )
        assertTrue(
            "the comparison reports a mismatch for a value that agrees, so it fails on everything",
            mismatches(listOf(Claim("online fill", online.fill, online.fill))).isEmpty(),
        )
    }
}
