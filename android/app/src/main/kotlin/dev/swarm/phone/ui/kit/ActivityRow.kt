package dev.swarm.phone.ui.kit

import android.content.Context
import android.text.SpannableStringBuilder
import android.text.Spanned
import android.text.style.ForegroundColorSpan
import android.text.style.TextAppearanceSpan
import android.widget.LinearLayout
import dev.swarm.phone.R

/**
 * derived: docs/design/substrate-components.md #14 Activity row
 *
 * One line of history: when it happened, what happened, and the one word in it worth the eye.
 *
 * There is deliberately no `origin:` line. `.arow` is the RETIRED MOCK's class -- Substrate's
 * artifact draws no activity screen at all, its demo phone renders the inbox and nothing else --
 * so the shared block declares no such rule and citing it as an origin would claim a join to
 * something that does not exist. [settingsRow] and [readOnlyNote] are in the same position and say
 * so the same way.
 *
 * ITS CARD IS `cardSurface` RATHER THAN A SECOND DERIVATION OF THE SAME FOUR VALUES. Row 14 asks
 * for `--p-card`, a 1 dp `--p-hair` border, `--p-card-r` and the `--p-card-fx` key light, which is
 * the session row's surface exactly -- and its `space_10` x `space_12` padding is the session
 * row's too. The mock drew this row at radius 12 with 11/13 padding and no border at all; none of
 * that survives, because §2's reuse rule is what stops the app having two card radii one screen
 * apart and a second place the card fill has to be changed. What is left for this file to decide
 * is what goes IN the card, which is three text roles and a gap.
 *
 * **THE TIMESTAMP CELL IS ROW 14'S; ITS BEING OPTIONAL IS THIS FILE'S.** The row states the cell
 * (`Mono.Meta` / `--p-ink3`) and one correction to the mock: the column is wrap-content, not a
 * fixed 52 dp, because a fixed column CLIPS at the 1.3x font scale PB-DS-12 requires. The row does
 * not say the timestamp may be absent -- that decision is taken here, and the wrap-content column
 * is what makes it cost nothing: an empty one occupies no width, so a caller with no time to show
 * renders a row with no gutter rather than a row with a blank one, and no layout shifts under it.
 * It is not a hypothetical slot. `swarmmobile.JournalEntry` is `(Cursor, SessionID, Type, Group)`
 * and carries NO TIMESTAMP, so the one caller this component has today passes null -- see
 * [dev.swarm.phone.ui.screens.ActivityPanel], which argues it at length. The parameter exists
 * because the row specifies the cell; it is not a slot waiting for an invented value.
 *
 * **THE EMPHASIS IS A SPAN AND NOT A SECOND VIEW.** Row 14 states three type roles for one row,
 * and the third is an INLINE emphasis -- `Mono.InlineStrong` / `--p-ink` inside a `Body.Message` /
 * `--p-ink` body, which is `.prow .ln` and `.ln b` one screen over. Two TextViews side by side
 * would break the wrap: a body that ran to two lines would leave the emphasised word stranded on
 * the first, and the row would read as a label and a value rather than as a sentence.
 *
 * **BOTH INKS ARE `--p-ink`, SO THE EMPHASIS IS CARRIED BY THE FACE.** That is row 14's decision
 * and it is worth naming, because the obvious reading of "emphasis" is a colour change and this is
 * not one: what separates the marked span from the sentence around it is a monospace face at 600
 * against a sans face at 400. It reads as an inline identifier, which is what the things worth
 * marking in this log are.
 *
 * @param body the whole sentence, which the SCREEN writes. PB-DS-9 gives copy to the screen and
 *  this component styles it.
 * @param emphasis the part of [body] that carries `.ln b`, named rather than passed as a separate
 *  fragment: a factory that took two pieces and joined them would own the separator between them,
 *  and a separator is copy. It must OCCUR in [body] -- a caller that names a span not in the
 *  sentence has a copy bug, and this fails loudly rather than rendering the row unemphasised,
 *  which is the failure nobody would see.
 * @param timestamp null renders no timestamp cell AT ALL rather than an empty one -- see above.
 */
fun activityRow(
    context: Context,
    body: CharSequence,
    emphasis: CharSequence? = null,
    timestamp: CharSequence? = null,
): LinearLayout {
    val row = KitStack(
        context,
        LinearLayout.HORIZONTAL,
        Kit.dimenPx(context, R.dimen.swarm_space_10),
    ).apply {
        // The timestamp is one short line and the body may be several. Aligned to the BOX the
        // timestamp would float against a two-line body's centre; aligned to the BASELINE it sits
        // on the body's first line, which is the relationship the mock's line-heights approximate
        // and `navHeader` already spends between its title and its counter.
        isBaselineAligned = true
        background = cardSurface(context, attention = false)
        setPaddingRelative(
            Kit.dimenPx(context, R.dimen.swarm_space_12),
            Kit.dimenPx(context, R.dimen.swarm_space_10),
            Kit.dimenPx(context, R.dimen.swarm_space_12),
            Kit.dimenPx(context, R.dimen.swarm_space_10),
        )
        layoutParams = LinearLayout.LayoutParams(MATCH, WRAP)
    }

    if (timestamp != null) {
        row.addView(
            Kit.textView(context).apply {
                setTextAppearance(R.style.TextAppearance_Swarm_Mono_Meta)
                setTextColor(Kit.colour(context, R.color.swarm_text_tertiary))
                text = timestamp
                // WRAP, which is the derivation's one correction to the mock: a fixed column
                // clips at the 1.3x font scale PB-DS-12 requires.
                layoutParams = LinearLayout.LayoutParams(WRAP, WRAP)
                tag = KitTag.ACTIVITY_TIME
            },
        )
    }
    row.addView(
        Kit.textView(context).apply {
            setTextAppearance(R.style.TextAppearance_Swarm_Body_Message)
            // `--p-ink`, which is row 14's cell and NOT the `--p-ink2` a body line takes on the
            // session row. `.prow .ln` is a session's need -- a subordinate second line under a
            // project name -- and this row has no first line for it to be subordinate to: the
            // sentence IS the row, so it takes the primary ink. The retired mock says the same
            // thing structurally: `.arow` declares a size and NO colour, so its body inherits the
            // screen's primary, and the only cell that dims itself is `.arow .when`. Which is the
            // timestamp -- row 14's `--p-ink3` -- so the two inks the row states are the two the
            // drawing actually spends.
            setTextColor(Kit.colour(context, R.color.swarm_text_primary))
            text = emphasised(context, body, emphasis)
            // The body takes the slack, so the timestamp keeps its own width and the sentence
            // wraps inside what is left rather than pushing the row wider than the screen.
            layoutParams = LinearLayout.LayoutParams(0, WRAP, 1f)
            tag = KitTag.ACTIVITY_BODY
        },
    )
    return row
}

/**
 * [body] with `.ln b` applied over [emphasis], or [body] unchanged when there is none.
 *
 * TWO SPANS AND NOT ONE, even though the ink span is the same colour the body already carries.
 * `Mono.InlineStrong` holds the family, the size and the weight; no text appearance in this app
 * holds a colour, because Substrate binds one style to several inks and every component here sets
 * its colour beside its appearance. Row 14 states the body's ink and the emphasis's ink as TWO
 * cells that today read the same token, and the span is what keeps them two: without it the
 * emphasis inherits whatever the body is painted, so a later change to one cell would silently
 * move the other, and `ActivityRowTest` would have no emphasis colour to read at all -- it would
 * be asserting the body's ink twice under two names.
 *
 * THE FAILURE MESSAGE NAMES NO REQUIREMENT ID, which is a fence talking rather than a style
 * choice: the literal accounting blanks string contents before it counts numbers, so a digit
 * inside one is a quantity nothing in `android/gate` can see, and it refuses both a metric hidden
 * in copy and a harmless `PB-DS-9`. The citation belongs in a comment, where it is free -- this is
 * PB-DS-9, the clause that gives copy to the screen and styling to the kit.
 */
private fun emphasised(
    context: Context,
    body: CharSequence,
    emphasis: CharSequence?,
): CharSequence {
    if (emphasis == null) return body
    val start = body.toString().indexOf(emphasis.toString())
    if (start < 0) {
        error(
            "`$emphasis` is not in `$body`, so the row was asked to emphasise a span its own " +
                "sentence does not contain. This fails loudly rather than dropping the emphasis, " +
                "which would render a correct-looking row that had quietly lost the one word the " +
                "design puts the eye on.",
        )
    }
    val end = start + emphasis.length
    return SpannableStringBuilder(body).apply {
        setSpan(
            TextAppearanceSpan(context, R.style.TextAppearance_Swarm_Mono_InlineStrong),
            start,
            end,
            Spanned.SPAN_EXCLUSIVE_EXCLUSIVE,
        )
        setSpan(
            ForegroundColorSpan(Kit.colour(context, R.color.swarm_text_primary)),
            start,
            end,
            Spanned.SPAN_EXCLUSIVE_EXCLUSIVE,
        )
    }
}
