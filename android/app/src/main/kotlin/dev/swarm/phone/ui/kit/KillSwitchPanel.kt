package dev.swarm.phone.ui.kit

import android.content.Context
import android.view.View
import android.widget.LinearLayout
import dev.swarm.phone.R

/**
 * derived: docs/design/substrate-components.md #12 Kill-switch panel
 *
 * What the machine's own remote-control switch is doing, and where it lives.
 *
 * There is deliberately no `origin:` line. `.kills` is the retired mock's class and Substrate draws
 * no machines screen, so row 12 is the whole specification.
 *
 * **IT HAS NO TRAILING CONTROL, AND THE ABSENCE IS IN THE SIGNATURE.** Row 12 said "trailing
 * control is row 4" -- the toggle -- until its 2026-08-01 amendment, and the amendment is a
 * security decision rather than a styling one: `App.KillSwitchEngaged` is READ ONLY by design,
 * because `protocol/server.go handleRemoteSetControl` refuses the remote tier before the backend is
 * consulted, on the stated grounds that a remote device must never re-enable a switch its owner
 * turned off. A toggle here is a control that cannot act. So this factory takes no trailing view at
 * all: a later caller cannot supply one by accident, which a `trailing: View? = null` parameter
 * would let them do. The phone's real destructive action is Revoke, in row 13's device row, and it
 * is [denyChip].
 *
 * **IT IS THE ONE COMPONENT IN THIS KIT WITH A BORDER AND NO FILL** -- row 12's surface cell is
 * "none (the ground shows, as in the mock)". [panelSurface] is that surface; a `--p-card` fill here
 * would make the panel a card that happens to have a warm border, and cards in this skin are things
 * you act on.
 *
 * **ITS BORDER IS THE ATTENTION ROW'S RECIPE WITH ONE TOKEN SUBSTITUTED**, which is row 12's own
 * sentence: `color-mix(--p-err 36%, --p-hair)` against `.prow.attention`'s
 * `color-mix(--p-att 36%, --p-hair)`. [Kit.killSwitchBorder] spends the SAME declared share rather
 * than a second copy of it -- see there. An opaque mix rather than an alpha, so the panel
 * composites identically if it is ever moved off the ground.
 *
 * A 2 dp `--p-err` RAIL WAS CONSIDERED AND REJECTED, and row 12 says why: the rail is Substrate's
 * marker for "this row needs you", and a container reporting a state does not.
 *
 * ROW 12'S `gap space_10` IS NOT SPENT, and that is what the amendment costs rather than an
 * oversight. The gap is `.kills`'s flex gap between its text block and the toggle beside it; with
 * the toggle deleted there are no two things for it to separate. It comes back the day something
 * legitimately sits beside the text, and nothing does.
 *
 * @param title row 12's `Title.Row` / `--p-err` cell. The copy is the screen's (PB-DS-9); the mock
 *  writes `Remote access`.
 * @param body the subtitle, `Body.Secondary` / `--p-ink2`.
 * @param command the part of [body] carrying row 12's inline `Mono.InlineStrong` / `--p-ink` cell
 *  -- the daemon-side verb, `swarm remote off` in the mock. Named rather than passed separately for
 *  [Kit.emphasised]'s reason: a factory taking two fragments would own the separator between them,
 *  and a separator is copy. Null renders the line with no marked span.
 */
fun killSwitchPanel(
    context: Context,
    title: CharSequence,
    body: CharSequence,
    command: CharSequence? = null,
): View {
    val panel = LinearLayout(context).apply {
        orientation = LinearLayout.VERTICAL
        background = panelSurface(context, Kit.killSwitchBorder(context))
        setPaddingRelative(
            Kit.dimenPx(context, R.dimen.swarm_space_14),
            Kit.dimenPx(context, R.dimen.swarm_space_12),
            Kit.dimenPx(context, R.dimen.swarm_space_14),
            Kit.dimenPx(context, R.dimen.swarm_space_12),
        )
        // THE MARGIN IS THE COMPONENT'S, unlike every row in this kit, and row 12 is the reason:
        // the panel is the one block on the machines screen that sits on the ground rather than
        // inside a list, so nothing above it can carry its inset.
        layoutParams = LinearLayout.LayoutParams(MATCH, WRAP).apply {
            topMargin = Kit.dimenPx(context, R.dimen.swarm_space_8)
            marginStart = Kit.dimenPx(context, R.dimen.swarm_space_14)
            marginEnd = Kit.dimenPx(context, R.dimen.swarm_space_14)
        }
    }
    panel.addView(
        Kit.textView(context).apply {
            Kit.appearance(this, R.style.TextAppearance_Swarm_Title_Row)
            // `--p-err` on the TITLE and nowhere else in this panel. It is the one place the skin
            // spends the error token on something that is not a destructive control, and row 12
            // spends it deliberately: what the line reports is the machine refusing this phone.
            setTextColor(Kit.colour(context, R.color.swarm_state_error))
            text = title
            layoutParams = LinearLayout.LayoutParams(MATCH, WRAP)
            tag = KitTag.KILL_TITLE
        },
    )
    panel.addView(
        Kit.textView(context).apply {
            Kit.appearance(this, R.style.TextAppearance_Swarm_Body_Secondary)
            setTextColor(Kit.colour(context, R.color.swarm_text_secondary))
            text = Kit.emphasised(context, body, command)
            layoutParams = LinearLayout.LayoutParams(MATCH, WRAP)
            tag = KitTag.KILL_BODY
        },
    )
    return panel
}
