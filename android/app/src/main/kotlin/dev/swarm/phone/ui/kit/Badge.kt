package dev.swarm.phone.ui.kit

import android.view.Gravity
import android.content.Context
import android.widget.LinearLayout
import android.widget.TextView
import dev.swarm.phone.R

/**
 * derived: docs/design/substrate-components.md #3 Badge
 *
 * The cross-screen attention carrier: a count of the sessions blocked on the user, anchored to the
 * Inbox tab, and the only thing that survives the user going to Machines, Activity or Settings.
 *
 * `--p-att`, NOT THE MOCK'S `#ff453a`. Red is retired here on semantics rather than on taste:
 * `--p-err` means denial, failure and destruction in this product, and a session waiting for a
 * human is none of those. NeedsInput is already `--p-att` in the row dot, the row rail and the
 * warmed border; the badge is the fourth site of the same state and takes the same token.
 *
 * The ink is `--p-hero-ink`, which is Substrate's one ink-for-saturated-fills token (8.79:1 on
 * attention). `--p-bg` was the alternative at 9.34:1; the two are indistinguishable at 10 sp and a
 * second rule -- "hero fills take hero ink, attention fills take the ground" -- is one nobody
 * remembers.
 *
 * The content description is the CALLER'S, because it is copy and copy is the screen's (PB-DS-9).
 * Row 3 states the form: "N sessions need you".
 */
fun badge(context: Context, count: Int, description: CharSequence): TextView =
    TextView(context).apply {
        setTextAppearance(R.style.TextAppearance_Swarm_Mono_Agent)
        setTextColor(Kit.colour(context, R.color.swarm_hero_ink))
        // Three digits either overflow a 16 dp pill or shrink the type below the 10 sp floor
        // PB-DS-12 already flags, so the count saturates rather than the box growing.
        text = if (count >= 100) "99+" else count.toString()
        contentDescription = description
        background = pillSurface(context, Kit.colour(context, R.color.swarm_state_attention))
        gravity = Gravity.CENTER
        setPaddingRelative(
            Kit.dimen(context, R.dimen.swarm_space_6).toInt(),
            Kit.dimen(context, R.dimen.swarm_space_2).toInt(),
            Kit.dimen(context, R.dimen.swarm_space_6).toInt(),
            Kit.dimen(context, R.dimen.swarm_space_2).toInt(),
        )
        layoutParams = LinearLayout.LayoutParams(
            WRAP,
            Kit.dp(context, KitMetrics.BADGE_HEIGHT_DP).toInt(),
        )
    }
