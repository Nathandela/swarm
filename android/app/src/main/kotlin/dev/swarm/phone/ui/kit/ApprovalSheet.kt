package dev.swarm.phone.ui.kit

import android.content.Context
import android.view.View
import android.widget.LinearLayout
import dev.swarm.phone.R

/**
 * derived: docs/design/substrate-components.md §4 Approval sheet pull-quote
 *
 * The approval sheet's contents, in the order ADR-009's maquette draws them: the machine and the
 * project in mono caps, then the blocking question as a pull-quote, then the literal in the well,
 * then the actions.
 *
 * **THE WHOLE COMPONENT IS AN ORDERING.** Substrate's `.sheet2` put an `h4` question first and the
 * context under it; the maquette reverses the two and grows the question. Three parts before,
 * three parts after -- no new information, which is the plan's own clause (O6.1: "no new
 * information, reordered hierarchy"). The reason is what a person does with a sheet: they read it
 * top-down, and the first line they meet should be *what they are being asked*. Who is asking is
 * what they check second, once they know the question is worth checking.
 *
 * **IT IS THE FIRST CALLER `sheetSurface` HAS EVER HAD.** O3 built that recipe and recorded in as
 * many words that it had no screen -- "a recipe waiting for its screen, not a rendered surface".
 * D4.4 calls this the heaviest material in the app, reserved for the moment of decision, and this
 * is the moment.
 *
 * **THE WELL AND THE ACTIONS ARE SLOTS, and that is `pairingStep`'s precedent rather than a
 * convenience.** `monoWell` already is every mono block in this app and `ctaButton` already is
 * every action; a sheet that built its own would be a second copy of both, drifting from them on
 * the first edit. What this factory owns is the arrangement and the two lines of type that exist
 * nowhere else.
 *
 * @param contextLine who is asking: the project, the agent and the machine, as the screen composes
 *  them. Uppercased HERE and not in the copy -- `sectionLabel`'s own ruling, so the accessibility
 *  tree still reads a phrase rather than a run of letters.
 * @param question the blocking question, verbatim from the screen. This component does not phrase
 *  it and must never grow a way to: `ui/screens/ApprovalSheetPanel.kt` records where the sentence
 *  comes from and what the wire does not yet carry.
 * @param well the literal the machine is showing, as `monoWell`. Null when there is none, and the
 *  sheet then draws no well at all rather than an empty recessed box -- `SessionDetailPanel
 *  .hasSnapshot` decides the same question the same way one screen over.
 * @param actions the decisions, as `ctaButton`s, sharing the width. Empty draws no row: an actions
 *  bar with nothing in it is 48 dp of sheet that looks broken.
 */
fun approvalSheet(
    context: Context,
    contextLine: CharSequence,
    question: CharSequence,
    well: View? = null,
    actions: List<View> = emptyList(),
): View = LinearLayout(context).apply {
    orientation = LinearLayout.VERTICAL
    layoutParams = LinearLayout.LayoutParams(MATCH, WRAP)
    background = sheetSurface(context)
    val pad = Kit.dimenPx(context, R.dimen.swarm_space_14)
    setPaddingRelative(pad, pad, pad, pad)

    addView(
        Kit.textView(context).apply {
            setTextAppearance(R.style.TextAppearance_Swarm_Mono_Meta)
            setTextColor(Kit.colour(context, R.color.swarm_text_tertiary))
            isAllCaps = true
            text = contextLine
            layoutParams = LinearLayout.LayoutParams(MATCH, WRAP)
        },
    )

    addView(
        Kit.textView(context).apply {
            // `Display.NavTitle` IS THE APP'S DISPLAY STYLE AT `--p-display-wt`, and the §4 row
            // records why it is this one rather than the maquette's 19 px: that drawing is on a
            // 300 px gallery phone, and this app's type ladder is deliberately still Substrate's.
            setTextAppearance(R.style.TextAppearance_Swarm_Display_NavTitle)
            setTextColor(Kit.colour(context, R.color.swarm_text_primary))
            text = question
            layoutParams = LinearLayout.LayoutParams(MATCH, WRAP).apply {
                topMargin = Kit.dimenPx(context, R.dimen.swarm_space_10)
            }
        },
    )

    well?.let { view ->
        view.layoutParams = LinearLayout.LayoutParams(MATCH, WRAP).apply {
            topMargin = Kit.dimenPx(context, R.dimen.swarm_space_12)
        }
        addView(view)
    }

    if (actions.isEmpty()) return@apply
    addView(
        LinearLayout(context).apply {
            orientation = LinearLayout.HORIZONTAL
            layoutParams = LinearLayout.LayoutParams(MATCH, WRAP).apply {
                topMargin = Kit.dimenPx(context, R.dimen.swarm_space_12)
            }
            actions.forEachIndexed { index, action ->
                // EQUAL WEIGHT, which the §4 in-card CTA pair already rules for two buttons of
                // the same importance. A sheet whose Allow is wider than its Deny has decided for
                // the user, and this is the one surface in the app where it must not.
                action.layoutParams = LinearLayout.LayoutParams(0, WRAP, 1f).apply {
                    if (index > 0) marginStart = Kit.dimenPx(context, R.dimen.swarm_space_10)
                }
                addView(action)
            }
        },
    )
}
