package dev.swarm.phone.ui.kit

import android.content.Context
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
 * WHAT IT DOES NOT SET IS THE TOUCH TARGET. Row 9 asks for a 48 dp target around a 36 dp visual,
 * and 48 has no expressible origin in this package's annotation grammar: rows 4, 10, 13, 15 and
 * 22 all write ">=48", and the metric reader cannot read a value behind a `>=`. It is a WCAG floor
 * rather than a design value, which may be the reason it needs a different mechanism. Reported
 * rather than typed, because typing it here is exactly the "a size somebody chose" this package
 * exists to prevent.
 */
fun textField(context: Context, hint: CharSequence): EditText = EditText(context).apply {
    setTextAppearance(R.style.TextAppearance_Swarm_Body_Message)
    setTextColor(Kit.colour(context, R.color.swarm_text_primary))
    setHintTextColor(Kit.colour(context, R.color.swarm_text_secondary))
    this.hint = hint
    background = wellSurface(context)
    setPaddingRelative(
        Kit.dimenPx(context, R.dimen.swarm_space_14),
        Kit.dimenPx(context, R.dimen.swarm_space_8),
        Kit.dimenPx(context, R.dimen.swarm_space_14),
        Kit.dimenPx(context, R.dimen.swarm_space_8),
    )
    layoutParams = LinearLayout.LayoutParams(MATCH, WRAP)
    tag = KitTag.TEXT_FIELD
}
