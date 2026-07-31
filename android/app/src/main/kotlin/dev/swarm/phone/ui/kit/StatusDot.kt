package dev.swarm.phone.ui.kit

import android.content.Context
import android.graphics.Canvas
import android.graphics.ColorFilter
import android.graphics.Paint
import android.graphics.PixelFormat
import android.graphics.drawable.Drawable
import android.view.View
import android.widget.LinearLayout

/**
 * The 7 dp mark that carries a session's state, and the two glows that say it is alive.
 *
 * THE RADIUS IS NOT A TOKEN AND CANNOT BE. `--p-dot-r` is 4 px applied to a 7 px box: `2 x 4 >= 7`,
 * so CSS clamps the corner and the dot renders a full circle. The literal 4 is unreachable, which
 * is why `dimens.xml` declares no `swarm_radius_dot` and why this draws a circle rather than a
 * rounded rectangle. Transcribing the token would ship a rounded square the design does not
 * contain -- PB-DS-4 records the same degeneracy, and it is the obvious mistake here.
 */
internal class StatusDotDrawable(
    val fill: Int,
    /** The blended halo, or null for the two Groups the design leaves flat. */
    val glow: Int?,
    val diameterPx: Float,
    val glowRadiusPx: Float,
) : Drawable() {

    val paint: Paint = Paint(Paint.ANTI_ALIAS_FLAG).apply {
        color = fill
        // `box-shadow: 0 0 9px <colour>` -- symmetric, zero offset, no spread. Android has no
        // other primitive for it: View.elevation with setOutlineSpotShadowColor produces a
        // DIRECTIONAL light-source shadow, not a halo, and clamps saturation (ADR-007 B134
        // decision 4). This is the same conversion --p-cta-fx needs, solved once.
        if (glow != null) setShadowLayer(glowRadiusPx, 0f, 0f, glow)
    }

    override fun draw(canvas: Canvas) {
        if (bounds.isEmpty) return
        canvas.drawCircle(bounds.exactCenterX(), bounds.exactCenterY(), diameterPx / 2f, paint)
    }

    override fun setAlpha(alpha: Int) { paint.alpha = alpha }
    override fun setColorFilter(colorFilter: ColorFilter?) { paint.colorFilter = colorFilter }
    override fun getOpacity(): Int = PixelFormat.TRANSLUCENT
    override fun getIntrinsicWidth(): Int = diameterPx.toInt()
    override fun getIntrinsicHeight(): Int = diameterPx.toInt()
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
 * the Paint, and wrong on screen. The two Groups that do not glow stay on no layer at all: a
 * software layer allocates a bitmap per view, and paying that on rows drawing a flat 7 dp circle
 * is a cost nobody would find again.
 */
fun statusDot(context: Context, group: String): View {
    val size = Kit.dp(context, KitMetrics.DOT_DP)
    val glow = Kit.groupGlow(context, group)
    return View(context).apply {
        background = StatusDotDrawable(
            fill = Kit.groupColour(context, group),
            glow = glow,
            diameterPx = size,
            glowRadiusPx = Kit.dp(context, KitMetrics.GLOW_RADIUS_DP),
        )
        layoutParams = LinearLayout.LayoutParams(size.toInt(), size.toInt())
        if (glow != null) setLayerType(View.LAYER_TYPE_SOFTWARE, null)
        tag = KitTag.DOT
    }
}
