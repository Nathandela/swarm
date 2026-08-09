package dev.swarm.phone.ui.kit

import android.content.Context
import android.content.res.ColorStateList
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

/**
 * A CTA's background: the bloom, then the fill and its border, both inset by the halo's room.
 *
 * IT IS STATEFUL BECAUSE DERIVATION ROW 24 IS A STATE AND NOT A VARIANT. The disabled/stale CTA is
 * the same button after its request expired -- `isEnabled = false` on a control the screen already
 * holds -- so the pair has to follow the view's drawable state rather than a constructor argument
 * a caller would have to rebuild the button to change. `View.setBackground` seeds the state and
 * every later `setEnabled` delivers it, so nothing at a call site has to remember.
 *
 * WHAT MOVES IS THE PAINT AND NOT THE BOX. [enabled] and [disabled] differ in fill, ink and bloom
 * and agree on radius, stroke and the halo's room, which is row 24 restating `--p-btn-r` 9 and
 * `padding space_12`: a button that also changed size would reflow the sheet under the user at the
 * moment the request died.
 */
internal class CtaSurface(
    /** The live pair, from the variant's own `.a2-*` rule. */
    val enabled: CtaSpec,
    /** Row 24's dead pair: `--p-hair` fill, `--p-ink3` ink, no bloom. */
    val disabled: CtaSpec,
    layers: Array<Drawable>,
) : LayerDrawable(layers) {

    /** The spec being painted. It is [enabled] until the view's state says otherwise. */
    var spec: CtaSpec = enabled
        private set

    override fun isStateful(): Boolean = true

    /**
     * A DISABLED VIEW'S STATE SET OMITS `state_enabled` rather than carrying a disabled attribute,
     * which is why this asks what is present rather than what is absent.
     */
    override fun onStateChange(state: IntArray): Boolean {
        val next = if (state.contains(android.R.attr.state_enabled)) enabled else disabled
        if (next == spec) return false
        spec = next
        // The fill and its border are the last layer; the bloom, where there is one, sits beneath.
        (getDrawable(numberOfLayers - 1) as GradientDrawable).setColor(next.fill)
        // "`--p-cta-fx` removed -- nothing glows unless it is alive." The halo's LAYER cannot be
        // removed from a LayerDrawable, so it stops painting; the room it occupies is unchanged,
        // which is what keeps the button where it was.
        if (numberOfLayers > 1) getDrawable(0).alpha = if (next.bloom == null) 0 else 255
        invalidateSelf()
        return true
    }
}

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
 * derived: docs/design/substrate-components.md #24 Disabled / stale CTA
 *
 * The approval sheet's three actions: approve, deny, and the one that opens the session instead.
 *
 * **ALL FOUR RULES ARE REAL, WHICH IS WHY THIS IS AN `origin:` AND NOT A `derived:`.** `.acts2
 * button` carries the shape, the padding and the type; `.a2-ok`, `.a2-no` and `.a2-more` carry the
 * three fills and the three inks. Section 3 of the derivation table has no numbered row for this
 * component precisely because the artifact draws it. The `§4` citation above is for the one thing
 * the artifact does not say, which is the [bloom] parameter.
 *
 * **ROW 24 IS A STATE OF THIS BUTTON AND NOT A COMPONENT BESIDE IT**, which is why the second
 * `derived:` line is here rather than in a factory of its own. The row is reached by
 * `isEnabled = false` on a CTA a screen already holds -- the approval sheet's primary action going
 * dead when the daemon-side request expires -- so it is a drawable state, and [CtaSurface] and
 * [ctaInk] are where it lives. What it is NOT is the STALE NOTE the row pairs it with
 * (`Body.Secondary` / `--p-att`, centred, `space_10` above): that is a sentence saying why the
 * button died, and copy is the screen's (PB-DS-9). The greyed button is not the state's meaning;
 * the note is, and the row says so.
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
    return Kit.textView(context).apply {
        setTextAppearance(R.style.TextAppearance_Swarm_Label_Button)
        setTextColor(ctaInk(context, kind))
        text = label
        gravity = Gravity.CENTER
        background = ctaSurface(spec, ctaDisabledSpec(context, spec))
        // `.acts2 button { padding: 12px }` plus the halo's room. The room is not padding in the
        // design's sense -- it is the inflation the layers are inset by, and the negative margins
        // below hand every pixel of it back, so what the design fixes is unchanged.
        setPaddingRelative(
            padPx + spec.insetPx,
            padPx + spec.insetPx,
            padPx + spec.insetPx,
            padPx + spec.insetPx,
        )
        // Row 22's `min 48`, PLUS the room the halo takes and the margins below hand back. The
        // floor is about the button a user can SEE: measured against the view's own box, a
        // blooming CTA clears any minimum with 18 dp of transparent glow on every edge while its
        // visible box stays 12 dp short. Adding the room here means the floor applies to what is
        // left after the negative margins, which is the thing anyone aims at.
        minimumWidth = Kit.dpPx(context, KitMetrics.MIN_TARGET_DP) + 2 * spec.insetPx
        minimumHeight = Kit.dpPx(context, KitMetrics.MIN_TARGET_DP) + 2 * spec.insetPx
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

/**
 * origin: .acts2
 *
 * The column two or more CTAs stack in: `.acts2 { gap: 7px }`, which PB-DS-1's ledger absorbs
 * into `swarm_space_8`.
 *
 * [ctaButton]'s OWN INVENTORY ROW RECORDS WHY THIS WAS LEFT UNSPENT: "the `.acts2` container's
 * own `gap: 7px` is NOT here ... this slice ships the BUTTON and not the column it sits in ... a
 * container factory with no caller is the second spelling `EmptyStateTest`'s KDoc argues against,
 * and the gap belongs to whoever builds the sheet." `PairingPanelView`'s control loop and
 * `SessionDetailView`'s take-control/stop/kill stack are that caller now, and both used to
 * `addView` these buttons bare -- zero gap, and on `CtaKind.APPROVE`'s bloom variant, WORSE than
 * zero: [CtaSpec.insetPx]'s negative margin pulls an un-gapped neighbour closer than the button's
 * own visible edge (agents-tracker-nx44.1).
 *
 * `KitStack`, ON `sessionList`'s AND `chipRow`'s OWN PRECEDENT -- the container spends the gap and
 * nowhere else, so a caller never types it at its own two or three call sites.
 */
fun ctaStack(context: Context): LinearLayout = KitStack(
    context,
    LinearLayout.VERTICAL,
    Kit.dimenPx(context, R.dimen.swarm_space_8),
).apply {
    layoutParams = LinearLayout.LayoutParams(MATCH, WRAP)
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
 * Derivation row 24, the state the artifact never drew: `.sheet.stale .a-ok`.
 *
 * IT IS THE LIVE SPEC WITH THREE CELLS REPLACED, which is the row read literally. Row 24 restates
 * `--p-btn-r` 9 and `padding space_12` -- the enabled CTA's own radius and padding -- so what it
 * changes is the fill, the ink and the bloom, and what it keeps is everything that decides where
 * the button is. Deriving it FROM [live] rather than building a fourth spec is what makes that
 * true by construction: a radius added to the CTA arrives here too.
 *
 * **THE BLOOM IS THE LOAD-BEARING HALF, and §1.3 says so.** Substrate's stated rule is *nothing
 * glows unless it is alive*, an expired request is definitionally not alive, and a dead button
 * keeping its 18 dp phosphor halo would contradict the skin's one explicit rule about light.
 *
 * THE PAIR IS 2.66:1, BELOW THE 3:1 UI FLOOR, BY INTENT -- and a reviewer must not "fix" it. WCAG
 * 1.4.3 exempts inactive controls and PB-DS-12's floors are written for interactive elements; this
 * button is neither clickable nor focusable, which `isEnabled = false` is what makes true. The
 * check that matters is against its NEIGHBOURS: the tertiary `View session first` below it carries
 * `--p-ink` on `--p-card` at 17.91:1, so dead and alive are 6.7x apart in one stack. The rejected
 * alternative was ink `--p-ink2` at 4.72:1, which reads as merely another enabled control.
 */
private fun ctaDisabledSpec(context: Context, live: CtaSpec): CtaSpec = live.copy(
    fill = Kit.colour(context, R.color.swarm_hairline),
    bloom = null,
    bloomRadiusPx = 0f,
)

/**
 * The layers, inset by the halo's room so the button's visible box is where the design puts it.
 *
 * A software layer's bitmap is the VIEW's bounds, and the glow is clipped inside it before
 * `clipChildren` on any parent is consulted -- so the room has to come from the view itself, and
 * the drawing has to be inset by exactly as much. `setLayerInset` is that inset; with no bloom it
 * is zero on every edge and this is one rounded rectangle filling its view.
 *
 * THE ROOM IS THE LIVE SPEC'S ON BOTH STATES. [disabled] drops the halo and keeps its inflation,
 * which is the one place the two specs are deliberately allowed to disagree with each other's
 * meaning: a button that gave the room back as it died would jump 18 dp in every direction at the
 * moment the request expired.
 */
private fun ctaSurface(spec: CtaSpec, disabled: CtaSpec): CtaSurface {
    val layers = mutableListOf<Drawable>()
    spec.bloom?.let { layers += CtaBloom(spec.fill, it, spec.radiusPx, spec.bloomRadiusPx) }
    layers += GradientDrawable().apply {
        shape = GradientDrawable.RECTANGLE
        cornerRadius = spec.radiusPx
        setColor(spec.fill)
        setStroke(spec.strokeWidthPx, spec.stroke)
    }
    val surface = CtaSurface(spec, disabled, layers.toTypedArray())
    for (layer in 0 until surface.numberOfLayers) {
        surface.setLayerInset(layer, spec.insetPx, spec.insetPx, spec.insetPx, spec.insetPx)
    }
    return surface
}

/**
 * Each variant's ink, and row 24's for all three when the control is dead.
 *
 * IT IS A `ColorStateList` AND NOT A COLOUR, which is the ink half of the same decision the surface
 * makes: the disabled state arrives as `isEnabled = false` on a button the screen already built, so
 * the ink has to follow the view's state rather than a value chosen at construction. A `TextView`
 * consults this on every state change for free; a plain int would leave the fill dead and the label
 * alive, which is a dimmed button with a full-strength word on it.
 *
 * `.a2-ok` is `--p-cta-ink` rather than `--p-hero-ink`, and the two are the same near-black green
 * today: `android/design-tokens.tsv` keeps both rows on purpose, because a future skin can break
 * the alias and a join that deduplicated by value would not notice it had.
 */
private fun ctaInk(context: Context, kind: CtaKind): ColorStateList = ColorStateList(
    arrayOf(intArrayOf(-android.R.attr.state_enabled), intArrayOf()),
    intArrayOf(
        // Row 24's ink cell: `Label.Button` / `--p-ink3`, the same one on all three variants,
        // because a dead control has no variant left to be.
        Kit.colour(context, R.color.swarm_text_tertiary),
        Kit.colour(context, ctaInkRes(kind)),
    ),
)

private fun ctaInkRes(kind: CtaKind): Int = when (kind) {
    CtaKind.APPROVE -> R.color.swarm_cta_ink
    CtaKind.DENY -> R.color.swarm_state_error
    CtaKind.MORE -> R.color.swarm_text_primary
}
