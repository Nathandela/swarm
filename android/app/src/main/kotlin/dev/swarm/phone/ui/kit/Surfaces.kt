package dev.swarm.phone.ui.kit

import android.content.Context
import android.graphics.Canvas
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
    /**
     * ADR-009 D4.4's vertical gradient: the BOTTOM stop, with [fill] as the top one. Null on every
     * surface that is one flat colour, which is every surface but the approval sheet.
     *
     * **IT DEFAULTS TO NULL AND THAT IS THE `ONLY` IN D4.4.** "The approval sheet is the only
     * vertical gradient" is a property of the whole kit rather than of the sheet, so the recipe
     * that wants one has to say so and the seven that do not say nothing. A gradient reachable by
     * omission would be the opposite arrangement.
     *
     * TWO FIELDS AND NOT A STOP LIST, because the design has two stops and no third. A list would
     * be a place to put a third, and the moment a surface has three the "one light source" rule
     * this file opens with has quietly acquired a second.
     */
    val fillBottom: Int? = null,
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

/**
 * `.prow`, and `.slab.lit` -- ADR-009 D4's promoted slab -- when the row is the one blocked on the
 * user.
 *
 * **PROMOTION IS TWO CHANGES AT ONCE AND EITHER ALONE IS THE WRONG DRAWING.** The maquette says
 * `.slab.lit { background: var(--p-elev); box-shadow: var(--p-lit-fx) }`: the slab comes forward
 * one ladder step AND catches more of the one light source. Taking the stronger edge on the card
 * fill draws a card with a bright line on it; taking the elevated fill with the resting edge draws
 * a toast. Together they are the single statement D4 makes -- the same light, on material that has
 * come forward -- which is why they are one boolean here rather than two parameters a caller could
 * spend separately.
 *
 * ELEVATION IS STILL A LIGHTER SURFACE AND NEVER A SHADOW, so this is the ladder doing the work
 * that `View.elevation` would be the obvious and forbidden implementation of. The file comment
 * above says why; nothing about that changes because a second surface now uses the step.
 *
 * THE RAIL AND THE WARMED BORDER SURVIVE UNTOUCHED. ADR-009's amendment to PB-DS-5 adds the lit
 * key-light as a NEW effect -- "Two effects are added, not substituted" -- so the two Substrate
 * mechanisms that already said "this row needs you" keep saying it. The maquette's own `.slab.lit`
 * draws neither; that is a divergence between the drawing and the requirement, and O3's scope is
 * the material, so the requirement wins here and the drawing is left for a decision that names it.
 */
internal fun cardSurface(context: Context, attention: Boolean): SubstrateSurface = surface(
    SurfaceSpec(
        fill = Kit.colour(
            context,
            if (attention) R.color.swarm_surface_elevated else R.color.swarm_surface_card,
        ),
        stroke = if (attention) {
            Kit.attentionBorder(context)
        } else {
            Kit.colour(context, R.color.swarm_hairline)
        },
        strokeWidthPx = Kit.dpPx(context, KitMetrics.HAIRLINE_DP),
        radiusPx = Kit.dimen(context, R.dimen.swarm_radius_card),
        // rgba(246, 243, 236, 0.10) -- and the RGB is `swarm_text_primary`'s, not a hex and no
        // longer `Color.WHITE`. ADR-009 D4 states the material rule this implements: surfaces are
        // "gradients of LINEN over ground", one light source, top edge. `--p-card-fx`'s RGB is
        // therefore exactly `--p-ink`, which is what the resource below resolves; the alpha comes
        // from KitMetrics, which the Go gate recomputes from the same token.
        //
        // IT USED TO BE `Color.WHITE` AND THAT WAS RIGHT UNTIL IT WASN'T. Substrate's key light
        // was a neutral white, so the constant was both correct and literal-free. Obsidian's is
        // tinted, and a white key light over a warm ladder is the exact cool contamination the
        // direction exists to remove -- visible on device, invisible in a diff, which is why the
        // Kotlin appearance suite reads this colour out of the token rather than trusting it.
        keyLight = ColorMix.withAlpha(
            Kit.colour(context, R.color.swarm_text_primary),
            // THE ONLY DIFFERENCE BETWEEN THE TWO EFFECTS IS THE ALPHA, which is why one
            // expression serves both: `--p-card-fx` and `--p-lit-fx` are the same linen, at the
            // same 1 dp, on the same edge, and they differ by how much of the light the surface
            // catches. Two separate constructions here would be two chances to warm one and not
            // the other -- and a promoted slab lit by a white edge over a warm ladder is the cool
            // contamination the direction exists to remove, visible on device and invisible here.
            if (attention) KitMetrics.LIT_KEY_LIGHT_ALPHA else KitMetrics.KEY_LIGHT_ALPHA,
        ),
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
        // The card's own recipe: `--p-card-fx`'s RGB is `--p-ink`'s linen and its alpha is the
        // checked constant, exactly as `cardSurface` spends them.
        keyLight = ColorMix.withAlpha(
            Kit.colour(context, R.color.swarm_text_primary),
            KitMetrics.KEY_LIGHT_ALPHA,
        ),
        keyLightPx = Kit.dp(context, KitMetrics.KEY_LIGHT_DP),
        rail = null,
        railPx = 0f,
    ),
)

/**
 * `.sheet`: ADR-009 D4.4's approval sheet -- the ONE vertical gradient in the app, and the only
 * surface besides a promoted slab carrying the strong 0.22 edge.
 *
 * **IT IS THE HEAVIEST MATERIAL THE SKIN HAS AND IT IS SPENT ON ONE MOMENT.** D4.4's word is
 * "reserved": the sheet is where a person decides something a machine is blocked on, and every
 * other surface in this kit is one flat colour under the resting key-light precisely so that this
 * one reads as a different KIND of object rather than as a bigger card. That is why the gradient
 * lives here rather than in a parameter [cardSurface] could take.
 *
 * **THE TWO STOPS ARE TWO RESOURCES AND NOT ONE PLUS AN ALPHA**, which is `colors.xml`'s own
 * ruling and worth repeating at the point of use: a gradient's endpoints are CHOSEN, not computed
 * from a base, so folding them into one resource would move the choice into whichever drawable
 * consumed it -- the "decided at the point of use" this whole regime exists to forbid.
 *
 * **THE RADIUS IS `--p-sheet-r` AND NOTHING ELSE IN THE APP SPENDS IT.** Substrate set it to 14px
 * and `--p-card-r` to 9px; ADR-009 separates them at 18 and 14, and until this recipe existed the
 * sheet radius was a `<dimen>` in the resource table with no component behind it.
 *
 * NO CALLER YET, AND SAYING SO IS PART OF THE COMMIT RATHER THAN AN ADMISSION. The migration plan
 * gives O3 the material and O6 the sheet itself (the pull-quote headline over the mono well), so
 * this is the recipe arriving before its screen. `TestPBDS5_EveryColourResourceIsSpentBySomething
 * ThatDraws` fences the half that can be fenced -- the two gradient stops now reach a surface --
 * and records in as many words that it cannot see composition.
 */
internal fun sheetSurface(context: Context): SubstrateSurface = surface(
    SurfaceSpec(
        fill = Kit.colour(context, R.color.swarm_sheet_gradient_top),
        fillBottom = Kit.colour(context, R.color.swarm_sheet_gradient_bottom),
        stroke = Kit.colour(context, R.color.swarm_hairline),
        strokeWidthPx = Kit.dpPx(context, KitMetrics.HAIRLINE_DP),
        radiusPx = Kit.dimen(context, R.dimen.swarm_radius_sheet),
        // THE PROMOTED EDGE, NOT THE RESTING ONE. D4.4 names exactly two surfaces that carry
        // `--p-lit-fx` -- a promoted slab and this -- so the alpha here is the lit constant and the
        // linen RGB is the same `--p-ink` every key light in this file resolves.
        keyLight = ColorMix.withAlpha(
            Kit.colour(context, R.color.swarm_text_primary),
            KitMetrics.LIT_KEY_LIGHT_ALPHA,
        ),
        keyLightPx = Kit.dp(context, KitMetrics.KEY_LIGHT_DP),
        // A SHEET IS NOT A ROW. The rail says "this row needs you" on a list of rows; on the sheet
        // the whole surface is the thing that needs you, and a leading bar would be the statement
        // made twice at two scales.
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
        // THE BUTTON RADIUS, NOT THE CARD'S, AND IT USED TO BE THE CARD'S BY COINCIDENCE.
        // Substrate set --p-card-r and --p-btn-r to the same 9px, and `.sheet2 .cmd` wrote a
        // literal 9px rather than either token -- so every one of those three numbers agreed and
        // nothing could tell which one the well was actually spending. ADR-009 separates them
        // (14 and 10), and the maquette answers the question the coincidence had been hiding:
        // it draws `.well { border-radius: var(--p-btn-r) }`. A well is a control-sized recess,
        // not a slab.
        radiusPx = Kit.dimen(context, R.dimen.swarm_radius_button),
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
            // ONE FILL OR TWO, and the two are set through `colors` rather than `setColor` because
            // those are different drawables: `setColor` clears any gradient the drawable had, so
            // an implementation that called both would render whichever came last and look, in a
            // diff, like it did both.
            //
            // ORIENTATION BEFORE COLOURS, and TOP_BOTTOM is `linear-gradient(180deg, ...)`.
            // GradientDrawable's default is LEFT_RIGHT, so a sheet that set the stops and left the
            // orientation alone would run the design's vertical gradient sideways -- correct in
            // every value a test could read off the spec, and wrong on screen.
            if (spec.fillBottom == null) {
                setColor(spec.fill)
            } else {
                orientation = GradientDrawable.Orientation.TOP_BOTTOM
                colors = intArrayOf(spec.fill, spec.fillBottom)
            }
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
