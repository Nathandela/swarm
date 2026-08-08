package dev.swarm.phone.ui.kit

import android.content.Context
import android.graphics.Canvas
import android.graphics.ColorFilter
import android.graphics.Paint
import android.graphics.PixelFormat
import android.graphics.RectF
import android.graphics.drawable.Drawable
import dev.swarm.phone.R

/**
 * The ring, and the room it needs between a component's paint and the component's own edge.
 *
 * THE SPEC IS EXPOSED FOR [SurfaceSpec]'s REASON: a `Paint` has no readable stroke geometry once it
 * is inside a `Drawable`, so an appearance test could otherwise assert the colour and nothing about
 * where the ring is. The layers are built FROM this rather than described by it.
 */
internal data class FocusRingSpec(
    val ink: Int,
    val strokePx: Float,
    /** `outline-offset`: the clear band between the focused paint and the ring. */
    val offsetPx: Float,
    /**
     * Row 23's "focused component's radius + 2", and the `+ 2` is [offsetPx] rather than a second
     * number that happens to share a digit. §1.1 gives the derivation: the ring stays CONCENTRIC
     * with what it surrounds, and a ring drawn at distance d outside a rounded rectangle of radius
     * r is concentric exactly when its own radius is r + d.
     */
    val radiusPx: Float,
    /**
     * What the focused component must keep clear inside its own bounds for the ring to be drawn
     * where the row puts it: the offset plus the stroke.
     *
     * IT IS A PROPERTY OF THE RING AND NOT OF THE COMPONENT, so a component cannot make room for a
     * ring of a different size than the one it will be given. `CtaSpec.insetPx` is the same
     * arrangement for the CTA's halo, and for the same reason: the room and the drawing have to
     * agree or the thing is drawn somewhere else.
     */
    val roomPx: Float,
)

/**
 * derived: docs/design/substrate-components.md #23 Focus ring
 *
 * `:focus-visible`: a 2 dp `--p-hero` ring, `space_2` clear of what it surrounds.
 *
 * **IT IS A FOREGROUND AND NOT A BACKGROUND, WHICH IS WHAT LETS IT BE ONE COMPONENT.** Row 23
 * applies to every focusable, and the focusables in this kit already spend their background on
 * `.a2-*`, `.chip` or a well -- a ring that arrived as a background would have to be merged into
 * each of those surfaces, which is five copies of one rule and the thing §2's reuse rule exists to
 * stop. A foreground composes with whatever is underneath.
 *
 * **IT DRAWS ONLY WHEN THE VIEW IS FOCUSED**, which is `:focus-visible` and not `:focus`: the state
 * arrives through the view's own drawable state, so the ring appears under keyboard and D-pad
 * traversal and stays absent under a finger, exactly as the pseudo-class it cites does. A ring
 * painted unconditionally would be a border on every control.
 *
 * **THE INK IS `--p-hero`, WHICH ADR-009 D3 DECIDED AND §1.1 ARGUES.** The ring was the one
 * component in this kit with no product origin at all: it had been specified only by the
 * documentation page's own chrome accent, which is a fact about a documentation page, and PB-DS-7
 * flagged it as a standing gap rather than pretending otherwise. D3 closes it with the champagne.
 *
 * WHY THE ACCENT, WHEN §1.1 ONCE REJECTED IT. Substrate's hero meant SELECTED and nothing else --
 * the fill of `.chip.on`, the ink of `.ptabs .on` -- so a hero ring around an unselected chip said
 * the opposite of what was true. Obsidian's accent means YOU: needs-you, CTA, focus, the live
 * counter, the brand, unified on purpose. Focus is the fifth thing the one accent says, not a
 * sixth meaning bolted onto a fill colour. What is still rejected is the neutral pair -- a ring in
 * `--p-ink` or `--p-ink2` over a warm ladder whose hairline is `--p-hair` reads as a heavier
 * border -- and the three status tokens that are not the accent, because they mean state.
 *
 * `--p-att` RESOLVES TO THESE SAME BYTES AND THAT IS THE ALIAS, NOT A COLLISION. ADR-009 D6 makes
 * the NeedsInput token value-alias the hero deliberately; both keep their own row in
 * `android/design-tokens.tsv` and their own `<color>`, so a future skin breaks either in one line.
 * This ring resolves `swarm_hero`, which is the token it MEANS; a ring that reached for
 * `swarm_state_attention` because the bytes matched would be a focus ring painted out of a status.
 *
 * Measured on Obsidian's ladder: 8.74 / 8.22 / 7.69:1 on `--p-bg` / `--p-card` / `--p-elev`, the
 * three surfaces focus lands on, against the 3:1 floor ADR-009 D8.1 holds a non-text indicator to.
 *
 * @param componentRadiusPx the radius of the thing being surrounded. A component with no surface
 *  passes 0 and gets a 2 dp radius, which is the same rule: concentric with a square corner.
 */
fun focusRing(context: Context, componentRadiusPx: Float): FocusRingDrawable {
    val offsetPx = Kit.dimen(context, R.dimen.swarm_space_2)
    val strokePx = Kit.dp(context, KitMetrics.FOCUS_RING_DP)
    return FocusRingDrawable(
        FocusRingSpec(
            ink = Kit.colour(context, R.color.swarm_hero),
            strokePx = strokePx,
            offsetPx = offsetPx,
            radiusPx = componentRadiusPx + offsetPx,
            roomPx = offsetPx + strokePx,
        ),
    )
}

/**
 * The ring as a drawable: nothing at all until the view it is on takes focus.
 *
 * THE STROKE IS DRAWN ON THE INSIDE OF THE BOUNDS RATHER THAN ASTRIDE THEM. `Canvas.drawRoundRect`
 * centres a stroke on the path, so a rectangle at the drawable's own bounds would lose half its
 * width to whatever clips the view -- a ring that is 2 dp in every value a test reads and 1 dp on
 * screen. Insetting by half the stroke is what makes the drawn width the stated width.
 */
class FocusRingDrawable internal constructor(internal val spec: FocusRingSpec) : Drawable() {

    private val paint = Paint(Paint.ANTI_ALIAS_FLAG).apply {
        style = Paint.Style.STROKE
        color = spec.ink
        strokeWidth = spec.strokePx
    }
    private val rect = RectF()

    /** Whether the view this is on is focused. Read by the appearance suite. */
    var focused: Boolean = false
        private set

    override fun isStateful(): Boolean = true

    override fun onStateChange(state: IntArray): Boolean {
        val next = state.contains(android.R.attr.state_focused)
        if (next == focused) return false
        focused = next
        invalidateSelf()
        return true
    }

    override fun draw(canvas: Canvas) {
        if (!focused || bounds.isEmpty) return
        val half = spec.strokePx / 2f
        rect.set(bounds)
        rect.inset(half, half)
        canvas.drawRoundRect(rect, spec.radiusPx, spec.radiusPx, paint)
    }

    override fun setAlpha(alpha: Int) { paint.alpha = alpha }
    override fun setColorFilter(colorFilter: ColorFilter?) { paint.colorFilter = colorFilter }
    override fun getOpacity(): Int = PixelFormat.TRANSLUCENT
}

