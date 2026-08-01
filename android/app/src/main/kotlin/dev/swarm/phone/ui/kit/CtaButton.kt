package dev.swarm.phone.ui.kit

import android.content.Context
import android.graphics.Canvas
import android.graphics.ColorFilter
import android.graphics.Paint
import android.graphics.PixelFormat
import android.graphics.RectF
import android.graphics.drawable.Drawable
import android.graphics.drawable.GradientDrawable
import android.graphics.drawable.LayerDrawable
import android.view.Gravity
import android.view.View
import android.widget.LinearLayout
import android.widget.TextView
import dev.swarm.phone.R

/**
 * Which of Substrate's three action rules a CTA renders.
 *
 * THE THREE ARE NOT A HIERARCHY THIS FILE INVENTED -- they are `.a2-ok`, `.a2-no` and `.a2-more`,
 * three rules the artifact declares side by side over one shared `.acts2 button`. Naming them
 * rather than taking a fill and an ink is what keeps the three fills, the three inks, the one
 * border and the one bloom in the kit instead of at three call sites.
 */
enum class CtaKind { APPROVE, DENY, MORE }

/**
 * The single input a CTA's background is built from.
 *
 * [SurfaceSpec] exists for the reason this does, and it is the platform's rather than a preference:
 * `GradientDrawable` has no getter for its stroke and none for its corner radius, so an appearance
 * test could otherwise only assert the fill. Reading THIS asserts what is drawn, because the layers
 * are constructed from it rather than described by it.
 */
internal data class CtaSpec(
    val fill: Int,
    val stroke: Int,
    /** Whole pixels: `GradientDrawable.setStroke` takes an int, and a cast truncates where the
     * platform rounds -- the hairline is the only depth cue Substrate allows itself. */
    val strokeWidthPx: Int,
    val radiusPx: Float,
    /** `--p-cta-fx`, or null on the two variants and the one site the design gives no box-shadow. */
    val bloom: Int?,
    val bloomRadiusPx: Float,
    /**
     * The room the bloom needs INSIDE the view's own bounds, and 0 when there is no bloom.
     *
     * It is a property of the spec rather than a local because it is spent three times -- the layer
     * insets, the padding that makes room for it, and the negative margin that gives it back -- and
     * the three have to agree or the button moves.
     */
    val insetPx: Int,
)

/** A CTA's background: the bloom, then the fill and its border, both inset by the halo's room. */
internal class CtaSurface(val spec: CtaSpec, layers: Array<Drawable>) : LayerDrawable(layers)

/**
 * `--p-cta-fx`: `0 0 18px rgba(83, 206, 124, 0.20)`, symmetric, zero offset, no spread.
 *
 * THE SAME CONVERSION THE STATUS DOT ALREADY SOLVED, and the derivation table says so in as many
 * words -- its status-dot row ends "the same conversion as `--p-cta-fx`". Android has no other
 * primitive for a halo: `View.elevation` with `setOutlineSpotShadowColor` produces a DIRECTIONAL
 * light-source shadow and clamps saturation (ADR-007 B134 decision 4), and Substrate bans drop
 * shadows outright, so the one effect `elevation` would ship is the one the skin forbids.
 *
 * IT PAINTS THE FILL TOO, and that is not redundancy that can be removed. `setShadowLayer` renders
 * a blurred copy of what the paint DRAWS; a layer that drew nothing would cast nothing. The
 * `GradientDrawable` above it repaints the same rounded rect with the same fill and adds the
 * border, so what reaches the screen is one opaque button with a halo behind it.
 */
internal class CtaBloom(
    val fill: Int,
    val bloom: Int,
    val radiusPx: Float,
    val bloomRadiusPx: Float,
) : Drawable() {

    private val paint = Paint(Paint.ANTI_ALIAS_FLAG).apply {
        color = fill
        setShadowLayer(bloomRadiusPx, 0f, 0f, bloom)
    }
    private val rect = RectF()

    override fun draw(canvas: Canvas) {
        if (bounds.isEmpty) return
        rect.set(bounds)
        canvas.drawRoundRect(rect, radiusPx, radiusPx, paint)
    }

    override fun setAlpha(alpha: Int) { paint.alpha = alpha }
    override fun setColorFilter(colorFilter: ColorFilter?) { paint.colorFilter = colorFilter }
    override fun getOpacity(): Int = PixelFormat.TRANSLUCENT
}

/**
 * origin: .acts2 button
 * origin: .a2-ok
 * origin: .a2-no
 * origin: .a2-more
 * derived: docs/design/substrate-components.md §4 In-card CTA pair
 *
 * The approval sheet's three actions: approve, deny, and the one that opens the session instead.
 *
 * **ALL FOUR RULES ARE REAL, WHICH IS WHY THIS IS AN `origin:` AND NOT A `derived:`.** `.acts2
 * button` carries the shape, the padding and the type; `.a2-ok`, `.a2-no` and `.a2-more` carry the
 * three fills and the three inks. Section 3 of the derivation table has no numbered row for this
 * component precisely because the artifact draws it -- row 24 is the DISABLED variant, which is a
 * different component and does not ship here. The `§4` citation above is for the one thing the
 * artifact does not say, which is the [bloom] parameter.
 *
 * THE DENY FILL IS A DERIVATION AND NOT A COLOUR. `.a2-no`'s background is
 * `color-mix(in srgb, --p-err 13%, transparent)`, so it has no `<color>` resource and PB-TOK-7
 * forbids typing what it resolves to. [Kit.denyFill] computes it from the share
 * `internal/design.Derivations()` declares, which is the same arrangement the attention row's
 * border and the two status-dot glows are already in.
 *
 * IT DOES NOT HANDLE ITS OWN TAP, and no component in this kit does -- PB-DS-6's sentence is that a
 * screen composes components and passes data. The consequence worth stating is that this is a
 * `TextView` and not a `Button`, so it does not announce itself as a button to a screen reader
 * until the screen that owns the click gives it a role. Recorded as a gap rather than half-solved
 * here, where there is no click to attach a role to.
 *
 * @param bloom false inside a card. §4's "In-card CTA pair" row: the card sets `overflow: hidden`,
 *  so an 18 dp bloom inside it is clipped at the card edge and looks broken -- the bloom belongs to
 *  the full-width sheet CTA. It suppresses the halo AND the room made for it, because a button that
 *  dropped the glow and kept the inflation would sit 18 dp out of place in every direction. It is
 *  a parameter rather than a fourth [CtaKind] because the fill, the ink and the type are `.a2-ok`
 *  unchanged: the site differs, not the variant.
 */
fun ctaButton(
    context: Context,
    label: CharSequence,
    kind: CtaKind,
    bloom: Boolean = true,
): TextView {
    val spec = ctaSpec(context, kind, bloom)
    val padPx = Kit.dimenPx(context, R.dimen.swarm_space_12)
    return TextView(context).apply {
        setTextAppearance(R.style.TextAppearance_Swarm_Label_Button)
        setTextColor(Kit.colour(context, ctaInk(kind)))
        text = label
        gravity = Gravity.CENTER
        background = ctaSurface(spec)
        // `.acts2 button { padding: 12px }` plus the halo's room. The room is not padding in the
        // design's sense -- it is the inflation the layers are inset by, and the negative margins
        // below hand every pixel of it back, so what the design fixes is unchanged.
        setPaddingRelative(
            padPx + spec.insetPx,
            padPx + spec.insetPx,
            padPx + spec.insetPx,
            padPx + spec.insetPx,
        )
        layoutParams = LinearLayout.LayoutParams(MATCH, WRAP).apply {
            marginStart = -spec.insetPx
            marginEnd = -spec.insetPx
            topMargin = -spec.insetPx
            bottomMargin = -spec.insetPx
        }
        // A SOFTWARE LAYER OR NO GLOW AT ALL. `setShadowLayer` is IGNORED under hardware
        // acceleration for everything but text, so an accelerated button draws a flat rectangle --
        // correct in every value a test could read off the Paint, and wrong on screen. The
        // variants that do not bloom stay on no layer at all: a software layer allocates a bitmap
        // per view, and paying that for a flat rounded rect is a cost nobody would find again.
        if (spec.bloom != null) setLayerType(View.LAYER_TYPE_SOFTWARE, null)
    }
}

/** Each variant's fill, border and bloom, read from the rule that declares it. */
private fun ctaSpec(context: Context, kind: CtaKind, bloom: Boolean): CtaSpec {
    // `box-shadow` is declared on `.a2-ok` and on neither of the other two, and §4 removes it from
    // `.a2-ok` inside a card. Both conditions produce the same absence, so they are one flag.
    val blooms = kind == CtaKind.APPROVE && bloom
    val ctaFill = Kit.colour(context, R.color.swarm_cta_background)
    return CtaSpec(
        fill = when (kind) {
            CtaKind.APPROVE -> ctaFill
            CtaKind.DENY -> Kit.denyFill(context)
            CtaKind.MORE -> Kit.colour(context, R.color.swarm_surface_card)
        },
        // `.acts2 button { border: none }`, overridden by `.a2-more`'s `1px solid var(--p-hair)`.
        // The tertiary action and the card behind it are both `--p-card`, so that hairline is the
        // only thing that makes the button a button rather than a sentence on the card.
        stroke = if (kind == CtaKind.MORE) {
            Kit.colour(context, R.color.swarm_hairline)
        } else {
            ColorMix.TRANSPARENT
        },
        strokeWidthPx = if (kind == CtaKind.MORE) Kit.dpPx(context, KitMetrics.HAIRLINE_DP) else 0,
        radiusPx = Kit.dimen(context, R.dimen.swarm_radius_button),
        // The RGB is `--p-cta-bg`'s own -- the effect token IS that colour at 20% -- so the fill
        // resource carries it and nothing here is a literal. This is `--p-card-fx`'s arrangement
        // with Color.WHITE, one token over.
        bloom = if (blooms) ColorMix.withAlpha(ctaFill, KitMetrics.CTA_BLOOM_ALPHA) else null,
        bloomRadiusPx = if (blooms) Kit.dp(context, KitMetrics.CTA_BLOOM_DP) else 0f,
        insetPx = if (blooms) Kit.dpPx(context, KitMetrics.CTA_BLOOM_DP) else 0,
    )
}

/**
 * The layers, inset by the halo's room so the button's visible box is where the design puts it.
 *
 * A software layer's bitmap is the VIEW's bounds, and the glow is clipped inside it before
 * `clipChildren` on any parent is consulted -- so the room has to come from the view itself, and
 * the drawing has to be inset by exactly as much. `setLayerInset` is that inset; with no bloom it
 * is zero on every edge and this is one rounded rectangle filling its view.
 */
private fun ctaSurface(spec: CtaSpec): CtaSurface {
    val layers = mutableListOf<Drawable>()
    spec.bloom?.let { layers += CtaBloom(spec.fill, it, spec.radiusPx, spec.bloomRadiusPx) }
    layers += GradientDrawable().apply {
        shape = GradientDrawable.RECTANGLE
        cornerRadius = spec.radiusPx
        setColor(spec.fill)
        setStroke(spec.strokeWidthPx, spec.stroke)
    }
    val surface = CtaSurface(spec, layers.toTypedArray())
    for (layer in 0 until surface.numberOfLayers) {
        surface.setLayerInset(layer, spec.insetPx, spec.insetPx, spec.insetPx, spec.insetPx)
    }
    return surface
}

/**
 * Each variant's ink.
 *
 * `.a2-ok` is `--p-cta-ink` rather than `--p-hero-ink`, and the two are the same near-black green
 * today: `android/design-tokens.tsv` keeps both rows on purpose, because a future skin can break
 * the alias and a join that deduplicated by value would not notice it had.
 */
private fun ctaInk(kind: CtaKind): Int = when (kind) {
    CtaKind.APPROVE -> R.color.swarm_cta_ink
    CtaKind.DENY -> R.color.swarm_state_error
    CtaKind.MORE -> R.color.swarm_text_primary
}
