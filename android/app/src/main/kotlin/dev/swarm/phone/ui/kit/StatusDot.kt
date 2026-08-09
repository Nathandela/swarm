package dev.swarm.phone.ui.kit

import android.content.Context
import android.graphics.Canvas
import android.graphics.ColorFilter
import android.graphics.Paint
import android.graphics.PixelFormat
import android.graphics.drawable.Drawable
import android.view.View
import android.widget.LinearLayout
import kotlin.math.roundToInt

/**
 * The 7 dp mark that carries a session's state, and the one glow that says NeedsInput needs you.
 *
 * ONE GLOW, NOT TWO, SINCE OWNER RULING R8 (2026-08-09, bead agents-tracker-oonj): Working is
 * still alive and shows it on its own workbar, but the maquette's `.sdot.work` declares no
 * `box-shadow` at all, so the dot itself stays flat there -- "one glow means one meaning: the
 * light marks the session that needs you."
 *
 * THE RADIUS IS NOT A TOKEN AND CANNOT BE. `--p-dot-r` is 4 px applied to a 7 px box: `2 x 4 >= 7`,
 * so CSS clamps the corner and the dot renders a full circle. The literal 4 is unreachable, which
 * is why `dimens.xml` declares no `swarm_radius_dot` and why this draws a circle rather than a
 * rounded rectangle. Transcribing the token would ship a rounded square the design does not
 * contain -- PB-DS-4 records the same degeneracy, and it is the obvious mistake here.
 */
internal class StatusDotDrawable(
    val fill: Int,
    /** The blended halo, or null for the three Groups the design leaves flat. */
    val glow: Int?,
    val diameterPx: Float,
    val glowRadiusPx: Float,
    /**
     * The maquette's `.pdot.unknown`: a RING rather than a disc. 0 is the disc every other mark
     * in this app draws, which is why it is the default.
     *
     * ONE DRAWABLE FOR BOTH SHAPES, and the argument is [TogglePill]'s: the ring is the disc with
     * a different Paint style, and a second class would be a second implementation of the 7 dp
     * circle that PB-DS-4's clamp degeneracy already makes delicate. The stroke is drawn INSIDE
     * the diameter (see [draw]) because the maquette sets `box-sizing: border-box` on everything
     * it draws -- a border there is inside the box, and a ring that grew the mark by its own
     * weight would shift every machine row's text sideways when a relay restarts.
     */
    val strokePx: Float = 0f,
) : Drawable() {

    val paint: Paint = Paint(Paint.ANTI_ALIAS_FLAG).apply {
        color = fill
        if (strokePx > 0f) {
            style = Paint.Style.STROKE
            strokeWidth = strokePx
        }
        // `box-shadow: 0 0 9px <colour>` -- symmetric, zero offset, no spread. Android has no
        // other primitive for it: View.elevation with setOutlineSpotShadowColor produces a
        // DIRECTIONAL light-source shadow, not a halo, and clamps saturation (ADR-007 B134
        // decision 4). This is the same conversion --p-cta-fx needs, solved once.
        if (glow != null) setShadowLayer(glowRadiusPx, 0f, 0f, glow)
    }

    override fun draw(canvas: Canvas) {
        if (bounds.isEmpty) return
        // A stroke straddles the path, half in and half out, so the path a RING follows is inset
        // by half its own weight -- that is what keeps the ring's outer edge on the design's 7 dp
        // rather than 1 dp beyond it. `strokePx` is 0 for a disc, so this is `diameterPx / 2f`
        // for every other mark and there is no second expression to keep in step.
        val radius = (diameterPx - strokePx) / 2f
        canvas.drawCircle(bounds.exactCenterX(), bounds.exactCenterY(), radius, paint)
    }

    override fun setAlpha(alpha: Int) { paint.alpha = alpha }
    override fun setColorFilter(colorFilter: ColorFilter?) { paint.colorFilter = colorFilter }
    override fun getOpacity(): Int = PixelFormat.TRANSLUCENT
    // Rounded, not truncated: a drawable's intrinsic size is a whole number of pixels, and the
    // platform's own rule for turning a dimension into one rounds half away from zero.
    override fun getIntrinsicWidth(): Int = diameterPx.roundToInt()
    override fun getIntrinsicHeight(): Int = diameterPx.roundToInt()
}

/**
 * origin: .pdot
 * derived: docs/design/substrate-components.md §4 Status dots, B134 mapping
 *
 * The dot for one `status.Group`, sized and coloured from the design and glowing only if that
 * Group is alive.
 *
 * IT SETS A SOFTWARE LAYER WHEN IT GLOWS, and that is not an optimisation detail. `setShadowLayer`
 * is IGNORED under hardware acceleration for everything but text, so a dot that set the shadow and
 * left the view accelerated draws a flat circle -- correct in every value a test could read off
 * the Paint, and wrong on screen. The three Groups that do not glow stay on no layer at all: a
 * software layer allocates a bitmap per view, and paying that on rows drawing a flat 7 dp circle
 * is a cost nobody would find again.
 *
 * **AND A SOFTWARE LAYER'S BITMAP IS THE VIEW'S OWN BOUNDS, WHICH IS WHY THE VIEW IS BIGGER THAN
 * THE MARK.** A 7 dp view on a software layer gets a 7 dp bitmap, and the 9 dp halo is clipped
 * INSIDE the layer -- before `clipChildren` on any parent is consulted, so setting that (which the
 * row and its line both do, and which a test asserted) cannot help: it governs the parent's clip,
 * not the layer's. THE BUG THIS FIXED PREDATES RULING R8: when this was written two Groups were
 * meant to glow, and the shipped dot had a correct Paint, a correct shadow radius, a correct layer
 * type and no visible glow on either of them, which was half of everything Substrate did with an
 * effect. So a dot THAT GLOWS is inflated by the halo's own radius on every side and gives exactly
 * that back as a NEGATIVE MARGIN: a CSS `box-shadow` does not participate in layout, and the
 * inflation must not either. `dot.layoutParams.width` is therefore no longer 7 dp on a glowing
 * dot, and the measurement that stays 7 dp on all four Groups regardless is the one the design
 * actually fixes -- what the mark occupies, which is `width + marginStart + marginEnd`.
 *
 * The inflation is the design's own 9 dp, so a Gaussian's outermost tail is still clipped: Skia
 * spreads a blur further than its stated radius. What is inside 9 dp is the halo the design
 * describes, and the alternative is a spread multiplier that no design source declares.
 *
 * THE DESCRIPTION IS THE CALLER'S, AND `null` IS A DECISION RATHER THAN AN OMISSION. This 7 dp
 * mark is the only thing distinguishing the four Groups -- four hues, no text -- so a screen
 * reader user gets nothing from it. The words are copy and copy is the screen's (PB-DS-9), so the
 * kit takes them; when it is given none it marks the view not-important-for-accessibility rather
 * than leaving whether the state is announced to a platform heuristic. What it must never do is
 * pass the EMPTY string, which is the platform's idiom for "decorative, skip me".
 */
fun statusDot(context: Context, group: String, description: CharSequence? = null): View {
    val corePx = Kit.dpPx(context, KitMetrics.DOT_DP)
    val glow = Kit.groupGlow(context, group)
    // Room for the halo, and none at all for the three Groups the design leaves flat.
    val haloPx = if (glow == null) 0 else Kit.dpPx(context, KitMetrics.GLOW_RADIUS_DP)
    return View(context).apply {
        background = StatusDotDrawable(
            fill = Kit.groupColour(context, group),
            glow = glow,
            diameterPx = Kit.dp(context, KitMetrics.DOT_DP),
            glowRadiusPx = if (glow == null) 0f else Kit.dp(context, KitMetrics.GLOW_RADIUS_DP),
        )
        layoutParams = LinearLayout.LayoutParams(corePx + 2 * haloPx, corePx + 2 * haloPx).apply {
            marginStart = -haloPx
            marginEnd = -haloPx
            topMargin = -haloPx
            bottomMargin = -haloPx
        }
        if (glow != null) setLayerType(View.LAYER_TYPE_SOFTWARE, null)
        contentDescription = description
        if (description == null) importantForAccessibility = View.IMPORTANT_FOR_ACCESSIBILITY_NO
        tag = KitTag.DOT
    }
}
