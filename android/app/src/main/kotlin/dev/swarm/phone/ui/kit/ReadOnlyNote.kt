package dev.swarm.phone.ui.kit

import android.content.Context
import android.view.Gravity
import android.widget.LinearLayout
import android.widget.TextView
import dev.swarm.phone.R

/**
 * derived: docs/design/substrate-components.md #22 Read-only note
 *
 * The centred sentence under a block a user cannot type into.
 *
 * There is deliberately no `origin:` line. `.ro-note` is row 22's MOCK class -- it does not appear
 * in the shared CSS block at all, which is why row 22 had to derive this component. Citing it as
 * an origin would claim a join to a rule that does not exist. [emptyState] and [textField] are in
 * the same position and say so the same way.
 *
 * **ITS BUTTON IS NOT ITS BUTTON.** Row 22's whole substance is that `[Take control]` STOPS being
 * an inline span and becomes a standalone tertiary button below the note -- "an inline span cannot
 * carry a 48 dp target, and the mock's inline button was painted in the retired doc-chrome
 * accent". The button it becomes is `.a2-more` unchanged, which is [ctaButton] with
 * [CtaKind.MORE]: same fill, same hairline, same radius, same `Label.Button` in `--p-ink`, same
 * `space_12`. So this factory is the NOTE and the screen composes the button under it. Building a
 * second `.a2-more` in here would be the copy of an existing component that §2's reuse rule exists
 * to prevent, and it would put a control inside a component that cannot own a click.
 *
 * THE MARGIN IS A MARGIN AND NOT A PADDING, which is row 22's own word and matters on this one:
 * the note sits directly under the terminal well, and side PADDING would leave the note's own
 * background -- it has none, so nothing would show -- while a margin insets the text block itself
 * against a full-bleed well. The row states two edges and this spends two; the bottom and the
 * trailing air belong to whatever the screen puts next.
 *
 * The ink is `--p-ink2` rather than `--p-ink3`: row 22 says `Body.Secondary` / `--p-ink2`, and it
 * is prose a user is meant to read -- `--p-ink3` fails the 4.5:1 body-text floor on every surface
 * in this product.
 */
fun readOnlyNote(context: Context, text: CharSequence): TextView = Kit.textView(context).apply {
    setTextAppearance(R.style.TextAppearance_Swarm_Body_Secondary)
    setTextColor(Kit.colour(context, R.color.swarm_text_secondary))
    gravity = Gravity.CENTER
    this.text = text
    layoutParams = LinearLayout.LayoutParams(MATCH, WRAP).apply {
        topMargin = Kit.dimenPx(context, R.dimen.swarm_space_10)
        marginStart = Kit.dimenPx(context, R.dimen.swarm_space_18)
        marginEnd = Kit.dimenPx(context, R.dimen.swarm_space_18)
    }
    tag = KitTag.READ_ONLY_NOTE
}
