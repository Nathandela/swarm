package dev.swarm.phone.ui.kit

import android.content.Context
import android.view.Gravity
import android.view.View
import android.widget.LinearLayout
import dev.swarm.phone.R

/**
 * derived: docs/design/substrate-components.md #29 Gap divider
 *
 * The record has a hole, said at the place the record tore.
 *
 * **WHAT IT REPLACES IS A PARAGRAPH IN THE WRONG PLACE.** Today a proven gap draws
 * `SessionDetailPanel.kt:248-249`'s sentence and two full-width buttons ABOVE the conversation --
 * a notice with no position, standing where the reader is trying to read, on a screen the owner
 * photographed with roughly 150 dp left for the messages. A reader at a tear needs to know the
 * record is not continuous. They do not need an essay about why, and they certainly do not need it
 * before the first message.
 *
 * **IT IS [notice] WITH `NoticeKind.ERROR`, MINUS THE PARAGRAPH.** Same `Body.Secondary`, same
 * `--p-err`, same voice -- the machine is the one speaking -- and the sentence's width is given
 * back to two hairlines. That is the derivation rather than a resemblance: what changed is how
 * much of the screen the statement is allowed to take, not who is making it.
 *
 * **IT IS NOT THE SYNC NOTICE AND MAY NEVER BORROW ITS DRAWING.** A stale stream is a different
 * fact: it has no position, so it cannot claim one, and it stays the full-width sentence it
 * already is. This shape says *the record tore HERE*, and a component that drew both would be
 * making a claim about position on behalf of a state that has none.
 *
 * **THE RULES TAKE [Kit.errorBorder], WHICH IS ROW 12's DERIVATION SPENT A THIRD TIME.** The
 * drawing writes the rule as `rgba(--p-err, 0.32)`, and PB-TOK-7 forbids typing an alpha that is a
 * function of a token -- so what would look like the faithful implementation is the one the token
 * regime refuses. `color-mix(--p-err 36%, --p-hair)` is the same gesture already declared, already
 * checked by `internal/design.Derivations()`, and already spent by the kill-switch panel and the
 * refused bubble: a hairline warmed toward the error ink. A fourth hand mix would be a fourth
 * place for the share to drift from 36%.
 *
 * **THE LABEL CARRIES ITS OWN REPAIR AND THE MENU DELIBERATELY DOES NOT.** One affordance per
 * operation: two routes to one live-only act are two pending states competing over which is in
 * flight. The word is inside [label] because it is copy (PB-DS-9), and the whole line is the
 * control -- which is why the floor below is spent here rather than on a span nothing can size.
 *
 * IT SETS NO MARGIN, which is [notice]'s cell and its reason: the air belongs to the column that
 * composes it. What this component does own is 48 dp of its own height, and that height IS the
 * air at a tear.
 */
fun gapDivider(context: Context, label: CharSequence): LinearLayout = KitStack(
    context,
    LinearLayout.HORIZONTAL,
    Kit.dimenPx(context, R.dimen.swarm_space_8),
).apply {
    gravity = Gravity.CENTER_VERTICAL
    // PB-DS-12's floor on the LINE, because the line is the control: the repair is a word inside
    // the label and row 22 states the general case -- "an inline span cannot carry a 48 dp
    // target". A minimum and not a size, `syncStrip`'s terms: the rule and the word are drawn
    // exactly as the drawing draws them, and what grows is the air a finger may land in.
    minimumHeight = Kit.dpPx(context, KitMetrics.MIN_TARGET_DP)
    layoutParams = LinearLayout.LayoutParams(MATCH, WRAP)

    addView(gapRule(context))
    addView(
        Kit.textView(context).apply {
            Kit.appearance(this, R.style.TextAppearance_Swarm_Body_Secondary)
            setTextColor(Kit.colour(context, R.color.swarm_state_error))
            text = label
            // WRAP between two weighted rules, so the words keep their own width and the rules
            // divide whatever is left. A weighted label would let a long repair phrase eat the
            // rules until the divider stopped reading as one.
            layoutParams = LinearLayout.LayoutParams(WRAP, WRAP)
        },
    )
    addView(gapRule(context))
}

/**
 * One half of the rule, warmed to `--p-err` and taking the width the sentence used to have.
 *
 * `Kit.dpPx` AND NOT `Kit.dp`: this is a laid-out height rather than a stroke, which is exactly
 * where the distinction is easy to lose. Every other hairline in this kit is spent rounded, and at
 * density 2.625 the unrounded value lays out 2 px against the 3 px the header's own rule paints on
 * the same screen.
 */
private fun gapRule(context: Context): View = View(context).apply {
    setBackgroundColor(Kit.errorBorder(context))
    // WEIGHT 1 ON BOTH SIDES, so the words sit at the centre of the column whatever they are. A
    // fixed rule length would put a two-word label and a five-word one in different places, and
    // what this component draws is a break in the record rather than a heading.
    layoutParams = LinearLayout.LayoutParams(0, Kit.dpPx(context, KitMetrics.HAIRLINE_DP), 1f)
}
