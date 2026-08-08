package dev.swarm.phone.ui.kit

import android.content.Context
import android.view.Gravity
import android.widget.EditText
import android.widget.LinearLayout
import dev.swarm.phone.R

/**
 * derived: docs/design/substrate-components.md #9 Composer
 *
 * A single-line input: the pairing code field, and the launch form's three.
 *
 * There is deliberately no `origin:` line. Substrate's directions page has no composer and no
 * form, so row 9 is the whole specification -- the same position [emptyState] and [settingsRow]
 * are in, and the distinction that rejected a kit commit when it was guessed at.
 *
 * IT IS A WELL, WHICH INVERTS THE MOCK. Row 9 is explicit that the field is `--p-well` rather than
 * a lighter fill: "the field is a *well*, inverting the mock's lighter-than-its-bar field --
 * `--p-well` is the token for recessed input, and against a translucent bar a `--p-card` field
 * would barely separate". So this shares [wellSurface] with [monoWell]: one recessed surface in
 * the skin, spent twice, rather than two recipes that drift.
 *
 * **THE PLACEHOLDER IS `--p-ink2` AND NOT `--p-ink3`, AND ROW 9 SAYS WHY IN NUMBERS.** `--p-ink3`
 * on the well is 3.50:1, under the 4.5:1 floor for text; `--p-ink2` gives 6.21:1. The tertiary ink
 * is the obvious choice for a hint -- it is what "de-emphasised" looks like everywhere else in
 * this kit -- and it is the wrong one here, because a hint IS the field's label on a surface with
 * no other label (`PhoneSurface` has no XML layouts, so every field is identified by its hint
 * alone). PB-DS-12 records the contrast; this is the site.
 *
 * **IT IS 48 dp OF TARGET AROUND 36 dp OF WELL, AND ROW 9 STATES BOTH**: "field padding `space_8`
 * x `space_14`, visual height 36, touch target 48". It is the only row in the table that states a
 * target and a smaller visual in the same cell, and the two numbers are not interchangeable -- a
 * field grown to 48 dp meets PB-DS-12's floor and loses the well the same sentence specifies, and a
 * 36 dp field keeps the well and cannot be hit. So the minimum is the target and the surface is
 * inset inside it by the difference, split between the two edges it is short on.
 *
 * THE TEXT IS CENTRED VERTICALLY FOR THAT REASON AND NO OTHER. `TextView` puts its content at the
 * top of whatever box it is given; with 12 dp more box than paint, a top-aligned line would sit
 * against the well's upper edge and leave the difference below it, which is the same defect the
 * inset exists to prevent, one layer in.
 */
fun textField(context: Context, hint: CharSequence): EditText = EditText(context).apply {
    // [Kit.textView]'s property, on the one kit view that cannot come from that constructor: an
    // EditText is a TextView and takes the same slack, and a field whose text sat on a different
    // line from the read-only well beside it would be the delta this kit just spent a commit
    // removing, in the one place a user is typing.
    includeFontPadding = false
    setTextAppearance(R.style.TextAppearance_Swarm_Body_Message)
    setTextColor(Kit.colour(context, R.color.swarm_text_primary))
    setHintTextColor(Kit.colour(context, R.color.swarm_text_secondary))
    this.hint = hint
    // The room between the target and the paint, on each of the two edges the well is short on --
    // the same `2` that counts the sides of the status dot's halo.
    val roomPx = (Kit.dpPx(context, KitMetrics.MIN_TARGET_DP) -
        Kit.dpPx(context, KitMetrics.WELL_HEIGHT_DP)) / 2
    background = wellSurface(context).apply {
        for (layer in 0 until numberOfLayers) setLayerInset(layer, 0, roomPx, 0, roomPx)
    }
    minimumHeight = Kit.dpPx(context, KitMetrics.MIN_TARGET_DP)
    gravity = Gravity.CENTER_VERTICAL
    setPaddingRelative(
        Kit.dimenPx(context, R.dimen.swarm_space_14),
        Kit.dimenPx(context, R.dimen.swarm_space_8),
        Kit.dimenPx(context, R.dimen.swarm_space_14),
        Kit.dimenPx(context, R.dimen.swarm_space_8),
    )
    layoutParams = LinearLayout.LayoutParams(MATCH, WRAP)
    tag = KitTag.TEXT_FIELD
}
