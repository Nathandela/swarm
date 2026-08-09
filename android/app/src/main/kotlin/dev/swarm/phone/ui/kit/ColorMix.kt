package dev.swarm.phone.ui.kit

import android.graphics.Color
import kotlin.math.roundToInt

/**
 * `color-mix(in srgb, x <fraction>%, y)`: the ONE blend the design uses, on the platform that has
 * to render it.
 *
 * WHY THE KIT COMPUTES ITS DERIVED COLOURS INSTEAD OF SPENDING THEM AS RESOURCES. Three colours in
 * this design are FUNCTIONS of tokens rather than tokens -- the attention row's warmed border, the
 * deny button's tint and the status-dot glow -- so no `<color>` can carry them and PB-TOK-7
 * forbids typing their resolved values. `android/gate/s22_derived_test.go` enforces that by
 * scanning every shipped source for the three hexes. This file is the supported way to obtain them:
 * the SHARE is what the kit carries (joined to `internal/design.Derivations()` by
 * `android/gate/s23_kit_test.go`), and the arithmetic happens here.
 *
 * IT IS A PORT OF `internal/design.Mix` AND IT HAS TO STAY ONE. That function is asserted against
 * the artifact in Go; this one is asserted against the design in `KitFoundationTest`, through a
 * reference implementation written the obvious way rather than copied from here. Two independent
 * checks of the same arithmetic is the arrangement, because a port that drifted would be a fifth
 * copy of the palette that every existing fence is blind to.
 *
 * THE TWO CSS FORMS LOOK ALIKE AND BEHAVE DIFFERENTLY, and one expression has to get both right:
 *
 *  - `color-mix(in srgb, --p-att 36%, --p-hair)` blends toward a second OPAQUE colour. Both alphas
 *    are 1, so it is the plain weighted average of the channels.
 *  - `color-mix(in srgb, --p-att 50%, transparent)` blends toward `rgba(0, 0, 0, 0)`. This does
 *    NOT darken the colour. CSS interpolates in PREMULTIPLIED space, transparent's premultiplied
 *    contribution is zero on every channel, and un-premultiplying by the resulting alpha returns
 *    the base's RGB untouched at alpha 0.50 -- the status dot's glow, ruling R8 (2026-08-09).
 *
 * Interpolating un-premultiplied gets the alpha right and the hue wrong, and the result still
 * reads as "a dimmer version of the token" in a diff -- so the mistake survives review. Doing the
 * premultiplied form uniformly gets both cases from one expression, because when both alphas are 1
 * the premultiply and the divide cancel.
 */
internal object ColorMix {

    /**
     * The CSS keyword, which is `rgba(0, 0, 0, 0)` -- a colour with zero alpha, NOT the absence of
     * one. The distinction is the whole of [mix]'s second branch.
     */
    const val TRANSPARENT: Int = 0

    fun mix(x: Int, fraction: Float, y: Int): Int {
        val ax = Color.alpha(x) / 255f
        val ay = Color.alpha(y) / 255f
        val a = fraction * ax + (1 - fraction) * ay
        if (a == 0f) {
            // Fully transparent: CSS serialises rgba(0,0,0,0), and there is no colour to recover
            // because the divide below would be by zero.
            return TRANSPARENT
        }
        fun channel(cx: Int, cy: Int): Int {
            val premultiplied = fraction * ax * cx + (1 - fraction) * ay * cy
            return quantise(premultiplied / a)
        }
        return Color.argb(
            quantise(a * 255f),
            channel(Color.red(x), Color.red(y)),
            channel(Color.green(x), Color.green(y)),
            channel(Color.blue(x), Color.blue(y)),
        )
    }

    /** The same colour at a stated alpha -- `--p-tabbg` is `--p-bg` at 88%. */
    fun withAlpha(colour: Int, alpha: Float): Int = Color.argb(
        quantise(alpha * 255f),
        Color.red(colour),
        Color.green(colour),
        Color.blue(colour),
    )

    /**
     * Quantises one channel to 8 bits, ONCE, at the end of the blend.
     *
     * Half rounds away from zero, which is `roundToInt`'s rule and is CSS's own rule too. It used
     * to be load-bearing in a way this file could show directly: before owner ruling R8
     * (2026-08-09) the needs-input glow's alpha was `0.70 * 255 = 178.5`, an exact tie, and the
     * artifact rendered `0xB3` = 179 -- away from zero. Rounding half to even would have given
     * 178, wrong by one on the one derivation whose arithmetic landed on the boundary. R8 moved
     * that derivation's share to 50%, whose own tie (`0.50 * 255 = 127.5`) rounds to 128 either
     * way, so no value in this kit exercises the disagreement today; `roundToInt`'s rule is kept
     * because it is still the CSS behaviour a future derivation may need.
     */
    private fun quantise(v: Float): Int = v.roundToInt().coerceIn(0, 255)
}
