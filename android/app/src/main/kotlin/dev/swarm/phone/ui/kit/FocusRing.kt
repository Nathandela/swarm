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
 * `:focus-visible`: a 2 dp `--p-ink` ring, `space_2` clear of what it surrounds.
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
 * **THE INK IS `--p-ink` AND §1.1 SPENDS A PAGE ON WHY.** The artifact's own `:focus-visible` uses
 * `#e2a33b`, which is the DOCUMENTATION PAGE's chrome accent and not a product token at all. The
 * four status tokens are out because they mean state, `--p-hero` is out because it means selected
 * (a hero ring around an unselected chip says the opposite of what is true), and `--p-ink2` is out
 * because it is the resting colour of chip labels and row summaries, so a ring in it reads as a
 * border. `--p-ink` is 18.73 / 17.91 / 17.19:1 on the three surfaces focus lands on.
 *
 * @param componentRadiusPx the radius of the thing being surrounded. A component with no surface
 *  passes 0 and gets a 2 dp radius, which is the same rule: concentric with a square corner.
 */
fun focusRing(context: Context, componentRadiusPx: Float): FocusRingDrawable {
    val offsetPx = Kit.dimen(context, R.dimen.swarm_space_2)
    val strokePx = Kit.dp(context, KitMetrics.FOCUS_RING_DP)
    return FocusRingDrawable(
        FocusRingSpec(
            ink = Kit.colour(context, R.color.swarm_text_primary),
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

