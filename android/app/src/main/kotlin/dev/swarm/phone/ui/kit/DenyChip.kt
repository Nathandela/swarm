package dev.swarm.phone.ui.kit

import android.content.Context
import android.view.Gravity
import android.widget.LinearLayout
import android.widget.TextView
import dev.swarm.phone.R

/**
 * derived: docs/design/substrate-components.md #13 Paired-device row
 *
 * The destructive action inside a row: `.a2-no`'s treatment at `.chip`'s metrics.
 *
 * There is deliberately no `origin:` line, and this component is the reason the distinction is
 * worth having. BOTH of its halves are drawn by Substrate -- `.a2-no` declares the fill and the
 * ink, `.chip` declares the radius, the padding and the label style -- and neither declares THIS.
 * The pairing of the two is row 13's, in five words: "the `.a2-no` treatment at chip metrics". An
 * `origin:` naming either rule would claim the artifact draws a control it does not.
 *
 * **IT IS NOT A SECOND DENIAL IDIOM, WHICH §2 FORBIDS.** The reuse rule's whole point is that this
 * product has ONE way of saying destructive -- a tinted fill, never an outline -- and the mock's
 * bespoke `.rev` button (its own border, its own radius, its own padding) is dropped for exactly
 * that reason. What this is, is the one idiom at a second SIZE: [Kit.denyFill] is the same blend
 * [ctaButton] paints with, [pillSurface] is the same surface the badge sits on, and nothing here
 * restates either.
 *
 * WHY NOT `ctaButton(kind = DENY)`, WHICH IS THE OBVIOUS ANSWER. It has the colours and the wrong
 * metrics: `--p-btn-r` 9, `space_12` on all four edges and `Label.Button` at 13.5 sp, against row
 * 13's `--p-chip-r` 8, `space_8` x `space_10` and `Label.Chip` at 11 sp. A sheet CTA is the primary
 * thing on its sheet; this is a control inside a row, and it must not out-weigh the device name
 * beside it.
 *
 * WHY NOT [filterChip], WHICH IS THE OTHER OBVIOUS ANSWER. It has the metrics and hard-codes its
 * own two fills -- `--p-card` and `--p-hero` -- because a scope chip's fill IS its selected state.
 * A parameter for a third fill would make it a chip that can be any colour, which is a component
 * with no opinion at all.
 *
 * **THE >=48 dp TOUCH TARGET IS NOT SET HERE**, and [toggle] and [textField] are in the same
 * position for the same reason: rows 4, 10, 13, 15 and 22 all write ">=48", this package's
 * annotation grammar cannot read a value behind a `>=`, and it is a WCAG floor rather than a design
 * value. It belongs to whoever attaches the click -- which for this control is the surface, because
 * revoking a device deletes a push token and issues a signed command. Recorded here as a gap rather
 * than half-solved with a number nothing can check.
 *
 * @param description what a screen reader announces INSTEAD of [label]. Null is the ordinary case:
 *  a chip carrying the word `Revoke` says what it is. It is here because one word is not always
 *  enough -- the caller with two devices on screen has to say WHICH one this destroys, and that is
 *  copy, which is the screen's (PB-DS-9).
 */
fun denyChip(
    context: Context,
    label: CharSequence,
    description: CharSequence? = null,
): TextView = TextView(context).apply {
    setTextAppearance(R.style.TextAppearance_Swarm_Label_Chip)
    // `.a2-no { color: var(--p-err) }`. The ink is the token straight, over a 13% tint of itself:
    // 5.3:1 on that fill, which row 13 states and §9 records.
    setTextColor(Kit.colour(context, R.color.swarm_state_error))
    text = label
    this.contentDescription = description
    // The fill is a BLEND and not a token, so it has no `<color>` resource and PB-TOK-7 forbids
    // typing what it resolves to -- `Kit.denyFill` computes it from the share
    // `internal/design.Derivations()` declares, which is where `ctaButton` gets it too.
    background = pillSurface(context, Kit.denyFill(context))
    gravity = Gravity.CENTER
    setPaddingRelative(
        Kit.dimenPx(context, R.dimen.swarm_space_10),
        Kit.dimenPx(context, R.dimen.swarm_space_8),
        Kit.dimenPx(context, R.dimen.swarm_space_10),
        Kit.dimenPx(context, R.dimen.swarm_space_8),
    )
    // HUGGING, unlike `ctaButton`, which lays itself out MATCH_PARENT for the full-width sheet it
    // was written for. This one sits at the trailing edge of a row whose text column is weighted,
    // and a MATCH_PARENT child there is measured against the whole row -- which leaves the name
    // beside it at zero width and turns the row into a button.
    layoutParams = LinearLayout.LayoutParams(WRAP, WRAP)
    tag = KitTag.DENY_CHIP
}
