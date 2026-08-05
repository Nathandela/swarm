package dev.swarm.phone.ui.kit

import android.content.Context
import android.graphics.Canvas
import android.graphics.Color
import android.graphics.ColorFilter
import android.graphics.LinearGradient
import android.graphics.Paint
import android.graphics.Path
import android.graphics.PixelFormat
import android.graphics.Rect
import android.graphics.RectF
import android.graphics.Shader
import android.graphics.drawable.Drawable
import android.graphics.drawable.GradientDrawable
import android.graphics.drawable.LayerDrawable
import dev.swarm.phone.R

/**
 * PB-DS-5: the surfaces every kit component is painted on, and the three effects that are easy to
 * get wrong.
 *
 * **`View.elevation` is the wrong implementation of `--p-card-fx` despite being the obvious one.**
 * Substrate bans drop shadows outright -- its own rule is that elevation is one ladder step
 * LIGHTER, never a shadow -- so a card that reached for `elevation` would render the single effect
 * the skin forbids while looking, in code, exactly like the effect it asks for. The key light is
 * an INSET 1 dp top-edge highlight at 4.5% white, clipped to the card's radius, and
 * `android/gate/s23_kit_test.go` fences `elevation`, `translationZ` and the outline shadow colours
 * out of this package entirely.
 *
 * THE SPEC IS EXPOSED BECAUSE THE PLATFORM HIDES WHAT IT WAS BUILT FROM. `GradientDrawable` has no
 * getter for its stroke and none for its corner radius, so an appearance test could otherwise only
 * assert the fill. [SurfaceSpec] is the single input the layers below are constructed from -- not a
 * parallel description of them -- so asserting against it asserts what is drawn.
 */
internal data class SurfaceSpec(
    val fill: Int,
    val stroke: Int,
    /**
     * WHOLE PIXELS, because `GradientDrawable.setStroke` takes an int and the conversion has to
     * happen somewhere it can be reviewed. It used to happen at the call to that setter, as
     * `strokeWidthPx.toInt()` -- and a cast truncates where the platform rounds, so the design's
     * 1 dp hairline rendered 2 px on a 2.625x handset instead of 3. Substrate bans drop shadows,
     * which makes this line the only depth cue every card, chip and row has; losing a third of it
     * is not a rounding detail. Held as the spent value so an appearance test can read it.
     */
    val strokeWidthPx: Int,
    val radiusPx: Float,
    /** `--p-card-fx`, or null on the surfaces the design gives no `box-shadow`. */
    val keyLight: Int?,
    val keyLightPx: Float,
    /** `.prow.attention::before`, or null on every surface but the NeedsInput row. */
    val rail: Int?,
    val railPx: Float,
)

/**
 * A card, chip or pill: fill and hairline, then the key light, then the rail.
 *
 * IT IS A LAYER LIST, which is what PB-DS-5 asks for, and the two upper layers are custom
 * drawables rather than more `GradientDrawable`s for a reason a reviewer should be able to check:
 * a 1 dp band clipped to a 9 dp corner is not a 1 dp rounded rectangle. The clipped band stops
 * about 4.9 dp short of each end, where the corner curve reaches it; a 1 dp `GradientDrawable`
 * carrying the card's radius degenerates to a capsule spanning the full width. At this size the
 * difference is small, and it is the difference between "clipped to the card radius" and not.
 */
internal class SubstrateSurface(val spec: SurfaceSpec, layers: Array<Drawable>) :
    LayerDrawable(layers)

/** `--p-card-fx`: `inset 0 1px 0 rgba(255,255,255,0.045)`, clipped to the surface's own radius. */
internal class EdgeHighlight(
    val colour: Int,
    val heightPx: Float,
    val radiusPx: Float,
) : Drawable() {

    private val paint = Paint(Paint.ANTI_ALIAS_FLAG).apply { color = colour }
    private val clip = Path()

    override fun draw(canvas: Canvas) {
        if (bounds.isEmpty) return
        val save = canvas.save()
        canvas.clipPath(roundedClip(clip, bounds, radiusPx))
        canvas.drawRect(
            bounds.left.toFloat(),
            bounds.top.toFloat(),
            bounds.right.toFloat(),
            bounds.top + heightPx,
            paint,
        )
        canvas.restoreToCount(save)
    }

    override fun setAlpha(alpha: Int) { paint.alpha = alpha }
    override fun setColorFilter(colorFilter: ColorFilter?) { paint.colorFilter = colorFilter }
    override fun getOpacity(): Int = PixelFormat.TRANSLUCENT
}

/**
 * `.prow.attention::before`: a 2 dp full-height rail at the leading edge.
 *
 * Clipped to the card's radius for the same reason the CSS is -- `.prow` sets `overflow: hidden`,
 * so the rail stops at the corner curve instead of squaring off the card's top-left.
 */
internal class EdgeRail(
    val colour: Int,
    val widthPx: Float,
    val radiusPx: Float,
) : Drawable() {

    private val paint = Paint(Paint.ANTI_ALIAS_FLAG).apply { color = colour }
    private val clip = Path()

    override fun draw(canvas: Canvas) {
        if (bounds.isEmpty) return
        val save = canvas.save()
        canvas.clipPath(roundedClip(clip, bounds, radiusPx))
        canvas.drawRect(
            bounds.left.toFloat(),
            bounds.top.toFloat(),
            bounds.left + widthPx,
            bounds.bottom.toFloat(),
            paint,
        )
        canvas.restoreToCount(save)
    }

    override fun setAlpha(alpha: Int) { paint.alpha = alpha }
    override fun setColorFilter(colorFilter: ColorFilter?) { paint.colorFilter = colorFilter }
    override fun getOpacity(): Int = PixelFormat.TRANSLUCENT
}

/**
 * `--p-workbar`: `linear-gradient(90deg, #00c2d7, transparent 85%)`.
 *
 * **The transparent stop keeps its RGB.** `transparent` in CSS is `rgba(0,0,0,0)`, and writing that
 * literally -- `#00000000`, the obvious spelling -- makes the bar fade through BLACK, so its
 * visible half greys out toward the middle. The end colour is `--p-work` at alpha zero, which
 * dissolves in place. The two are indistinguishable in a diff and obvious on screen.
 *
 * A `<gradient>` in XML cannot express this either: it has no arbitrary stop positions, so the 85%
 * stop would have to become a `centerColor` at 50%.
 */
internal class WorkingBarShape(
    val startColour: Int,
    val endColour: Int,
    val fadeStop: Float,
    val radiusPx: Float,
) : Drawable() {

    private val paint = Paint(Paint.ANTI_ALIAS_FLAG)
    private val rect = RectF()

    override fun onBoundsChange(bounds: Rect) {
        super.onBoundsChange(bounds)
        if (bounds.isEmpty) return
        paint.shader = LinearGradient(
            bounds.left.toFloat(),
            0f,
            bounds.right.toFloat(),
            0f,
            intArrayOf(startColour, endColour),
            // CLAMP holds the last colour past the stop, which is what `transparent 85%` means:
            // the remaining 15% is already fully transparent.
            floatArrayOf(0f, fadeStop),
            Shader.TileMode.CLAMP,
        )
    }

    override fun draw(canvas: Canvas) {
        if (bounds.isEmpty) return
        rect.set(bounds)
        canvas.drawRoundRect(rect, radiusPx, radiusPx, paint)
    }

    override fun setAlpha(alpha: Int) { paint.alpha = alpha }
    override fun setColorFilter(colorFilter: ColorFilter?) { paint.colorFilter = colorFilter }
    override fun getOpacity(): Int = PixelFormat.TRANSLUCENT
}

/** `.ptabs`: a translucent fill with a 1 dp hairline along its top edge. */
internal class TopRule(val fill: Int, val rule: Int, val rulePx: Float) : Drawable() {

    private val paint = Paint(Paint.ANTI_ALIAS_FLAG)

    override fun draw(canvas: Canvas) {
        if (bounds.isEmpty) return
        paint.color = fill
        canvas.drawRect(bounds, paint)
        paint.color = rule
        canvas.drawRect(
            bounds.left.toFloat(),
            bounds.top.toFloat(),
            bounds.right.toFloat(),
            bounds.top + rulePx,
            paint,
        )
    }

    override fun setAlpha(alpha: Int) { paint.alpha = alpha }
    override fun setColorFilter(colorFilter: ColorFilter?) { paint.colorFilter = colorFilter }
    override fun getOpacity(): Int = PixelFormat.TRANSLUCENT
}

/** `.prow`, and `.prow.attention` when the row is the one blocked on the user. */
internal fun cardSurface(context: Context, attention: Boolean): SubstrateSurface = surface(
    SurfaceSpec(
        fill = Kit.colour(context, R.color.swarm_surface_card),
        stroke = if (attention) {
            Kit.attentionBorder(context)
        } else {
            Kit.colour(context, R.color.swarm_hairline)
        },
        strokeWidthPx = Kit.dpPx(context, KitMetrics.HAIRLINE_DP),
        radiusPx = Kit.dimen(context, R.dimen.swarm_radius_card),
        // rgba(255, 255, 255, 0.045). The white is `--p-card-fx`'s own RGB -- the token IS a
        // translucent white -- and the alpha comes from KitMetrics, which the Go gate recomputes
        // from that token. Color.WHITE rather than a hex, so nothing in this package is a literal.
        keyLight = ColorMix.withAlpha(Color.WHITE, KitMetrics.KEY_LIGHT_ALPHA),
        keyLightPx = Kit.dp(context, KitMetrics.KEY_LIGHT_DP),
        rail = if (attention) Kit.colour(context, R.color.swarm_state_attention) else null,
        railPx = Kit.dp(context, KitMetrics.RAIL_DP),
    ),
)

/**
 * Derivation row 1's toast: the ELEVATED surface, with the card's hairline, radius and key light.
 *
 * **IT IS A SEPARATE RECIPE FROM [cardSurface] OVER ONE CELL, AND THAT CELL IS THE COMPONENT.**
 * Three of the four values are the card's, so the obvious implementation is to call `cardSurface`
 * and be done -- and the fill is the whole difference between a block sitting IN the page and one
 * floating over it. Substrate bans drop shadows outright, so "one ladder step lighter" is the only
 * elevation this skin has; a toast painted `--p-card` is a card that happens to be in the way.
 *
 * IT KEEPS THE KEY LIGHT, which row 1 states in as many words ("`--p-card-fx` key-light as on
 * every card; no drop shadow (banned)"). That is the one place the row asks for the card's
 * treatment rather than the elevated surface's own, and it is why this is not `wellSurface`'s
 * shape with a different fill.
 *
 * OPAQUE, which is §2's ruling rather than this file's: the mock makes the banner, the toast and
 * the quick chips 95% translucent over an 18-20 px blur, and over bright content that lets ghosted
 * text bleed through them. The two bars that carry `--p-tabbg` are the only translucency left.
 */
internal fun toastSurface(context: Context): SubstrateSurface = surface(
    SurfaceSpec(
        fill = Kit.colour(context, R.color.swarm_surface_elevated),
        stroke = Kit.colour(context, R.color.swarm_hairline),
        strokeWidthPx = Kit.dpPx(context, KitMetrics.HAIRLINE_DP),
        radiusPx = Kit.dimen(context, R.dimen.swarm_radius_card),
        // The card's own recipe: `--p-card-fx`'s RGB is white and its alpha is the checked
        // constant, exactly as `cardSurface` spends them.
        keyLight = ColorMix.withAlpha(Color.WHITE, KitMetrics.KEY_LIGHT_ALPHA),
        keyLightPx = Kit.dp(context, KitMetrics.KEY_LIGHT_DP),
        rail = null,
        railPx = 0f,
    ),
)

/**
 * `.chip`, and `.chip.on` when it is the selected scope.
 *
 * NO KEY LIGHT: the design gives `box-shadow: var(--p-card-fx)` to `.prow`, `.sheet2` and `.tcard`
 * and to nothing else. A chip that acquired one would be a card at chip size.
 */
internal fun chipSurface(context: Context, selected: Boolean): SubstrateSurface = surface(
    SurfaceSpec(
        fill = Kit.colour(
            context,
            if (selected) R.color.swarm_hero else R.color.swarm_surface_card,
        ),
        // `.chip.on { border-color: transparent }` -- the fill carries the state on its own.
        stroke = if (selected) ColorMix.TRANSPARENT else Kit.colour(context, R.color.swarm_hairline),
        strokeWidthPx = if (selected) 0 else Kit.dpPx(context, KitMetrics.HAIRLINE_DP),
        radiusPx = Kit.dimen(context, R.dimen.swarm_radius_chip),
        keyLight = null,
        keyLightPx = 0f,
        rail = null,
        railPx = 0f,
    ),
)

/**
 * `.sheet2 .cmd`: the recessed well a mono block sits in.
 *
 * NO KEY LIGHT, and the reason is the same one `chipSurface` gives: the design gives
 * `box-shadow: var(--p-card-fx)` to `.prow`, `.sheet2` and `.tcard` and to nothing else. A well is
 * the opposite gesture anyway -- `--p-well` is DARKER than the ground, so a highlight along its
 * top edge would light the one surface in the skin that is meant to read as cut into the page.
 */
internal fun wellSurface(context: Context): SubstrateSurface = surface(
    SurfaceSpec(
        fill = Kit.colour(context, R.color.swarm_surface_well),
        stroke = Kit.colour(context, R.color.swarm_hairline),
        strokeWidthPx = Kit.dpPx(context, KitMetrics.HAIRLINE_DP),
        radiusPx = Kit.dimen(context, R.dimen.swarm_radius_card),
        keyLight = null,
        keyLightPx = 0f,
        rail = null,
        railPx = 0f,
    ),
)

/**
 * The badge: a saturated fill, no border, `--p-chip-r` on a 16 dp box so it renders a pill.
 *
 * There is no fifth radius. The pill is a consequence of `2 x 8 >= 16` -- the same degeneracy
 * PB-DS-4 records for the status dot -- rather than a new step on the shape scale.
 */
internal fun pillSurface(context: Context, fill: Int): SubstrateSurface = surface(
    SurfaceSpec(
        fill = fill,
        stroke = ColorMix.TRANSPARENT,
        strokeWidthPx = 0,
        radiusPx = Kit.dimen(context, R.dimen.swarm_radius_chip),
        keyLight = null,
        keyLightPx = 0f,
        rail = null,
        railPx = 0f,
    ),
)

/**
 * Derivation row 12's charged container: a border on the ground, with no fill of its own.
 *
 * **IT IS THE ONE SURFACE IN THIS KIT THAT PAINTS NO FILL**, and that is row 12's first cell --
 * "none (the ground shows, as in the mock)" -- rather than an omission. A `--p-card` fill here
 * would make the panel a card that happens to have a warm border, which is a different statement:
 * cards are things you act on and this one reports a state the phone cannot change.
 *
 * NO KEY LIGHT, for the reason [chipSurface] gives. The design puts `box-shadow: var(--p-card-fx)`
 * on `.prow`, `.sheet2` and `.tcard` and nowhere else, and an inset highlight along the top edge of
 * a container with no fill would be a 1 dp white line floating on the ground.
 *
 * @param stroke the border, which the CALLER computes. It is [Kit.killSwitchBorder] at the one site
 *  this has -- a blend, not a token, so it has no `<color>` resource and cannot be looked up here.
 */
internal fun panelSurface(context: Context, stroke: Int): SubstrateSurface = surface(
    SurfaceSpec(
        fill = ColorMix.TRANSPARENT,
        stroke = stroke,
        strokeWidthPx = Kit.dpPx(context, KitMetrics.HAIRLINE_DP),
        radiusPx = Kit.dimen(context, R.dimen.swarm_radius_card),
        keyLight = null,
        keyLightPx = 0f,
        rail = null,
        railPx = 0f,
    ),
)

private fun surface(spec: SurfaceSpec): SubstrateSurface {
    val layers = mutableListOf<Drawable>(
        GradientDrawable().apply {
            shape = GradientDrawable.RECTANGLE
            cornerRadius = spec.radiusPx
            setColor(spec.fill)
            setStroke(spec.strokeWidthPx, spec.stroke)
        },
    )
    spec.keyLight?.let { layers += EdgeHighlight(it, spec.keyLightPx, spec.radiusPx) }
    spec.rail?.let { layers += EdgeRail(it, spec.railPx, spec.radiusPx) }
    return SubstrateSurface(spec, layers.toTypedArray())
}

/** The surface's own rounded rectangle, reused as a clip so a band stops where the corner does. */
private fun roundedClip(path: Path, bounds: Rect, radiusPx: Float): Path {
    path.reset()
    path.addRoundRect(RectF(bounds), radiusPx, radiusPx, Path.Direction.CW)
    return path
}
