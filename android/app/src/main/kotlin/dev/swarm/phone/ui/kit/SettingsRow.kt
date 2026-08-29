package dev.swarm.phone.ui.kit

import android.content.Context
import android.view.Gravity
import android.view.View
import android.widget.LinearLayout
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
 * ITS SURFACE IS THE SIGNED SLATE `.trow`: the screen ground with one bottom hairline. Row 15
 * deliberately has no card fill, radius, key light or outer tile inset; consecutive rows make one
 * flat group. The row owns its 18 dp content gutter and 14 dp vertical rhythm, while its caller
 * leaves the group edge to edge. Row 13 reuses this composition for the paired-device action.
 *
 * THE TRAILING CONTROL IS A VIEW THE CALLER PASSES, not a variant this component switches on. Row
 * 15 keeps row 4 as a caller-owned trailing slot, and a factory that took a `Boolean` and built
 * one itself
 * would own two components' worth of decisions and force a third parameter the first time a row
 * needed something else. The screen composes; this places.
 *
 * **ROW 15'S STATUS-TEXT FORM IS WITHDRAWN AND THE TRAILING SLOT IS NOT** (agents-tracker-2pnu
 * F5, agents-tracker-zecs). `statusLabel` shipped this file's other half and its only would-be
 * caller was inventory C6's `End-to-end encryption` row, which `SettingsPanelScreen` records as
 * deliberately unbuilt: "nothing on this handset READS it, so \"active\" would be a word printed
 * unconditionally beside a screen whose whole subject is what the machine has actually
 * confirmed." A factory whose own justification rests on a row the screen model refuses to build
 * is a component with no future caller, so it is retired rather than left to be rediscovered.
 * The slot itself is unchanged: [toggle] fills it on every preference row and `denyChip` on the
 * pairing row. The day a fact on the wire earns a live status, the form comes back with it.
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
        Kit.textView(context).apply {
            Kit.appearance(this, R.style.TextAppearance_Swarm_Title_Row)
            setTextColor(Kit.colour(context, R.color.swarm_text_primary))
            this.text = label
            layoutParams = LinearLayout.LayoutParams(MATCH, WRAP)
            tag = KitTag.SETTINGS_LABEL
        },
    )
    if (sublabel != null) {
        text.addView(
            Kit.textView(context).apply {
                Kit.appearance(this, R.style.TextAppearance_Swarm_Body_Secondary)
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
        background = BottomRule(
            fill = Kit.colour(context, R.color.swarm_background),
            rule = Kit.colour(context, R.color.swarm_hairline),
            rulePx = Kit.dpPx(context, KitMetrics.HAIRLINE_DP).toFloat(),
        )
        setPaddingRelative(
            Kit.dimenPx(context, R.dimen.swarm_space_18),
            Kit.dimenPx(context, R.dimen.swarm_space_14),
            Kit.dimenPx(context, R.dimen.swarm_space_18),
            Kit.dimenPx(context, R.dimen.swarm_space_14),
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
