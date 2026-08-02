package dev.swarm.phone.ui.kit

import android.content.Context
import android.view.Gravity
import android.view.View
import android.widget.LinearLayout
import android.widget.TextView
import dev.swarm.phone.R

/**
 * derived: docs/design/substrate-components.md #15 Settings row
 *
 * A labelled row with an optional second line and an optional trailing control.
 *
 * There is deliberately no `origin:` line. `.setrow` is row 15's MOCK class and the shared
 * Substrate block declares no such rule -- the same reason [emptyState] cites a row rather than a
 * selector. Substrate's directions page has no settings screen to draw one on.
 *
 * ITS SURFACE IS `cardSurface` AND NOT A SECOND DERIVATION OF THE SAME FOUR VALUES. Row 15 asks
 * for `--p-card`, a 1 dp `--p-hair` border, `--p-card-r` and the `--p-card-fx` key light, which is
 * the session row's surface exactly. §2's reuse rule is the whole reason the remaining 24
 * components are tractable: a second recipe here would be a third place the card fill has to be
 * changed, and the two would drift on the first edit. What differs between a session row and a
 * settings row is what is IN it, which is this file.
 *
 * THE TRAILING CONTROL IS A VIEW THE CALLER PASSES, not a variant this component switches on. Row
 * 15 says it is "row 4, or status text" -- a [toggle] or a [statusLabel] -- and a factory that
 * took a `Boolean` and built one itself would own two components' worth of decisions and force a
 * third parameter the first time a row needed something else. The screen composes; this places.
 *
 * @param sublabel null renders no second line AT ALL rather than an empty one. A blank TextView
 *  still occupies its line height and its gap, so a row with no sublabel would sit taller than
 *  its neighbours for no reason a reader could see.
 */
fun settingsRow(
    context: Context,
    label: CharSequence,
    sublabel: CharSequence? = null,
    trailing: View? = null,
): LinearLayout {
    val text = LinearLayout(context).apply {
        orientation = LinearLayout.VERTICAL
        // `flex: 1` -- the text takes the slack so the trailing control sits hard right.
        layoutParams = LinearLayout.LayoutParams(0, WRAP, 1f)
    }
    text.addView(
        TextView(context).apply {
            setTextAppearance(R.style.TextAppearance_Swarm_Title_Row)
            setTextColor(Kit.colour(context, R.color.swarm_text_primary))
            this.text = label
            layoutParams = LinearLayout.LayoutParams(MATCH, WRAP)
            tag = KitTag.SETTINGS_LABEL
        },
    )
    if (sublabel != null) {
        text.addView(
            TextView(context).apply {
                setTextAppearance(R.style.TextAppearance_Swarm_Body_Secondary)
                setTextColor(Kit.colour(context, R.color.swarm_text_secondary))
                this.text = sublabel
                layoutParams = LinearLayout.LayoutParams(MATCH, WRAP)
                tag = KitTag.SETTINGS_SUBLABEL
            },
        )
    }

    return LinearLayout(context).apply {
        orientation = LinearLayout.HORIZONTAL
        gravity = Gravity.CENTER_VERTICAL
        background = cardSurface(context, attention = false)
        setPaddingRelative(
            Kit.dimenPx(context, R.dimen.swarm_space_14),
            Kit.dimenPx(context, R.dimen.swarm_space_12),
            Kit.dimenPx(context, R.dimen.swarm_space_14),
            Kit.dimenPx(context, R.dimen.swarm_space_12),
        )
        // Rows 4 and 15 are one instruction: "the whole row is one >=48 dp target when it carries
        // a toggle", which is also where row 4 puts the TOGGLE's ">=48 with the visual unchanged".
        // The floor belongs here rather than on the control, because a 46x28 toggle grown to 48
        // would meet the number by destroying the drawing the same clause protects. A row with a
        // sublabel already clears it; a single-line one does not.
        minimumHeight = Kit.dpPx(context, KitMetrics.MIN_TARGET_DP)
        layoutParams = LinearLayout.LayoutParams(MATCH, WRAP)
        addView(text)
        trailing?.let {
            addView(
                it.apply {
                    (layoutParams as? LinearLayout.LayoutParams)?.marginStart =
                        Kit.dimenPx(context, R.dimen.swarm_space_10)
                },
            )
        }
    }
}

/**
 * derived: docs/design/substrate-components.md #15 Settings row
 *
 * The settings row's OTHER trailing form: a state the row reports rather than a control that
 * changes it. Row 15 names it "status text `Label.CardHead` / `--p-hero`".
 *
 * IT IS `--p-hero` AND NOT `--p-ok`, WHICH IS THE WHOLE REASON THE ROW SPELLS IT OUT. The one
 * shipped caller says "active" about end-to-end encryption, and that is a LIVENESS statement,
 * which is what hero means in this skin. `--p-ok` would read as a status, and after ADR-007 B134
 * it carries ReadyForReview -- so a green "active" here and a green dot on the inbox would be the
 * same colour saying two unrelated things.
 *
 * IT IS A SEPARATE FACTORY FROM [settingsRow] rather than a parameter on it, because the row would
 * otherwise take two mutually exclusive arguments -- a trailing View and a status String -- and
 * the caller could pass both. What it must NOT be is a TextView the screen styles: an ink and a
 * text appearance chosen in `ui/screens/` is the PB-DS-6 violation this package exists to prevent.
 */
fun statusLabel(context: Context, text: CharSequence): TextView = TextView(context).apply {
    setTextAppearance(R.style.TextAppearance_Swarm_Label_CardHead)
    setTextColor(Kit.colour(context, R.color.swarm_hero))
    this.text = text
    layoutParams = LinearLayout.LayoutParams(WRAP, WRAP)
    tag = KitTag.SETTINGS_STATUS
}
