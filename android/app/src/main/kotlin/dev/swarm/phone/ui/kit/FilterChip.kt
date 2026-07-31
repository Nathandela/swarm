package dev.swarm.phone.ui.kit

import android.content.Context
import android.view.Gravity
import android.widget.LinearLayout
import android.widget.TextView
import dev.swarm.phone.R

/**
 * origin: .chip
 *
 * The scope-bar chip, with its selected state and its optional machine-presence dot.
 *
 * THE OFF STATE OF THE PRESENCE DOT IS DERIVED, NOT DRAWN. Substrate declares only the online
 * colour (`.chip .pd { background: var(--p-ok) }`). Offline takes `--p-ink3`, the same recessive
 * ink the offline machine dot and the Completed Group carry, because all three mean "not active"
 * -- the collision is semantically correct rather than accidental.
 *
 * NEITHER STATE GLOWS. Substrate's rule is that nothing glows unless it is alive, and a machine
 * that is merely reachable is not a running agent.
 *
 * `--p-ok` NOW MEANS TWO THINGS ON THIS SCREEN, and it is worth knowing rather than discovering:
 * B134 moved ReadyForReview to `--p-ok`, and this presence dot was already `--p-ok`, so a green
 * 5 dp dot inside a chip and a green 7 dp dot at the head of a row are both visible on the inbox.
 * Different sizes, different containers, and the chip dot is always immediately left of a machine
 * name -- recorded as open at §8.2 of the derivation table.
 */
fun filterChip(
    context: Context,
    label: CharSequence,
    selected: Boolean,
    /** null when the chip names no machine -- the "All machines" scope has no presence. */
    present: Boolean?,
): TextView = TextView(context).apply {
    setTextAppearance(R.style.TextAppearance_Swarm_Label_Chip)
    setTextColor(
        Kit.colour(context, if (selected) R.color.swarm_hero_ink else R.color.swarm_text_secondary),
    )
    text = label
    background = chipSurface(context, selected)
    gravity = Gravity.CENTER_VERTICAL
    setPaddingRelative(
        Kit.dimen(context, R.dimen.swarm_space_10).toInt(),
        Kit.dimen(context, R.dimen.swarm_space_8).toInt(),
        Kit.dimen(context, R.dimen.swarm_space_10).toInt(),
        Kit.dimen(context, R.dimen.swarm_space_8).toInt(),
    )
    layoutParams = LinearLayout.LayoutParams(WRAP, WRAP)

    if (present != null) {
        val size = Kit.dp(context, KitMetrics.PRESENCE_DOT_DP)
        // `display: inline-block` before the label. A compound drawable is the platform's inline
        // leading mark, and `margin-right: 5px` is its drawable padding.
        val dot = StatusDotDrawable(
            fill = Kit.colour(
                context,
                if (present) R.color.swarm_state_ok else R.color.swarm_text_tertiary,
            ),
            glow = null,
            diameterPx = size,
            glowRadiusPx = 0f,
        ).apply { setBounds(0, 0, size.toInt(), size.toInt()) }
        setCompoundDrawablesRelative(dot, null, null, null)
        compoundDrawablePadding = Kit.dimen(context, R.dimen.swarm_space_4).toInt()
    }
}

/**
 * origin: .chips
 *
 * The scope bar. Built scrollable-ready as a plain row: Substrate's `.chips` does not scroll and
 * with two machines it does not need to, but the scope bar is the one container whose child count
 * is data rather than design (§8.6 of the derivation table).
 */
fun chipRow(context: Context): LinearLayout = KitStack(
    context,
    LinearLayout.HORIZONTAL,
    Kit.dimen(context, R.dimen.swarm_space_8).toInt(),
).apply {
    setPaddingRelative(
        Kit.dimen(context, R.dimen.swarm_space_18).toInt(),
        0,
        Kit.dimen(context, R.dimen.swarm_space_18).toInt(),
        Kit.dimen(context, R.dimen.swarm_space_12).toInt(),
    )
}
