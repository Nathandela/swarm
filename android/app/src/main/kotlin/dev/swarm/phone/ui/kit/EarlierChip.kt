package dev.swarm.phone.ui.kit

import android.content.Context
import android.view.Gravity
import android.widget.LinearLayout
import android.widget.TextView
import dev.swarm.phone.R

/**
 * derived: docs/design/substrate-components.md #30 Earlier chip
 *
 * The offer to fetch older messages, at the HEAD of the list and out of the reading path.
 *
 * **IT IS A CHIP AND NOT A BUTTON, AND THAT IS THE WHOLE OF THE ROW.** The obvious implementation
 * is a full-width tertiary CTA -- the app already has one, `ctaButton(kind = MORE)`, and it is
 * what a `Load earlier messages` control looks like in most clients. It is refused here because
 * the defect this whole surface is fixing is three full-width buttons standing above a
 * conversation with 150 dp left for it; a fourth one, at the top of the list, would be the same
 * mistake at the other end. A pill hugs its words, sits above the first message rather than in
 * front of it, and scrolls away with the history it belongs to.
 *
 * **IT IS ROW 10's FLOATING `.chip` IN A NEW POSITION AND NOT A NEW SURFACE.** [chipSurface] with
 * `selected = false`, `Label.Chip` / `--p-ink2`, the same construction [syncPill] already spends:
 * §2's reuse rule, and the reason this component's row states its paint by citing row 10 instead
 * of restating it. A second recipe would differ from that one in no cell.
 *
 * **THE HONESTY RULE IS THE NEW PART, AND IT IS THE CALLER'S TO OBEY.** The chip is drawn only
 * while the machine says there is genuinely something older to fetch; when it says nothing older
 * is retained, the chip is ABSENT rather than present and dead. A component cannot enforce that --
 * it never sees the fact -- so it is recorded here and in the row, and the screen that composes it
 * is where it lands. `navHeaderDrill`'s nullable back control is the same arrangement one
 * component over: a control that cannot act is worse than no control.
 *
 * IT SETS NO GRAVITY AND NO MARGIN. The drawing centres it over the first message, and centring is
 * layout -- `android/gate/s24_screens_test.go` leaves `Gravity` and `layoutParams` to the screen
 * precisely so a screen may place a component without authoring it. A chip that centred itself
 * would be right on this screen and wrong on the next one that wants it.
 */
fun earlierChip(context: Context, label: CharSequence): TextView = Kit.textView(context).apply {
    Kit.appearance(this, R.style.TextAppearance_Swarm_Label_Chip)
    setTextColor(Kit.colour(context, R.color.swarm_text_secondary))
    text = label
    background = chipSurface(context, selected = false)
    // `filterChip`'s treatment, because this IS that chip: a scope chip is one line ended with
    // the platform's mark, and a chip that wrapped would stop being a pill.
    Kit.identityCell(this)
    gravity = Gravity.CENTER
    setPaddingRelative(
        Kit.dimenPx(context, R.dimen.swarm_space_12),
        Kit.dimenPx(context, R.dimen.swarm_space_8),
        Kit.dimenPx(context, R.dimen.swarm_space_12),
        Kit.dimenPx(context, R.dimen.swarm_space_8),
    )
    // Row 10's own `targets >=48`, arriving with the shape rather than being decided here.
    // `syncPill`'s reasoning in both directions: a pill sized to one short line of copy is the
    // control that measures short by construction, and this is a minimum rather than a size, so
    // the drawing is unchanged wherever the metrics already clear it.
    minimumHeight = Kit.dpPx(context, KitMetrics.MIN_TARGET_DP)
    // Row 23, concentric with the chip's own radius -- unlike the header's controls, this one
    // paints a fill, so a ring at radius 0 would cut across the corners it surrounds.
    Kit.focusable(this, componentRadiusPx = Kit.dimen(context, R.dimen.swarm_radius_chip))
    layoutParams = LinearLayout.LayoutParams(WRAP, WRAP)
}
