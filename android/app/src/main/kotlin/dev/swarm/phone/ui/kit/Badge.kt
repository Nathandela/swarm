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
 * Inbox tab, and the only thing that survives the user going to Activity or Settings.
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
 *
 * NULL IS A DESCRIPTION AND THE EMPTY STRING IS NOT. A non-null content description is what a
 * screen reader reads INSTEAD of a view's own text, so `""` asks it to say nothing -- on the one
 * signal this product promises means something, and on a view whose text is already the count.
 * `null` leaves "3" announceable. The tab bar filled a missing description with `""`, which is why
 * this parameter is nullable rather than defensive.
 */
fun badge(context: Context, count: Int, description: CharSequence?): TextView =
    Kit.textView(context).apply {
        setTextAppearance(R.style.TextAppearance_Swarm_Mono_Agent)
        setTextColor(Kit.colour(context, R.color.swarm_hero_ink))
        // Three digits either overflow a 16 dp pill or shrink the type below the 10 sp floor
        // PB-DS-12 already flags, so the count saturates rather than the box growing.
        text = if (count >= 100) "99+" else count.toString()
        contentDescription = description
        background = pillSurface(context, Kit.colour(context, R.color.swarm_state_attention))
        gravity = Gravity.CENTER
        setPaddingRelative(
            Kit.dimenPx(context, R.dimen.swarm_space_6),
            Kit.dimenPx(context, R.dimen.swarm_space_2),
            Kit.dimenPx(context, R.dimen.swarm_space_6),
            Kit.dimenPx(context, R.dimen.swarm_space_2),
        )
        // THE PILL IS AT LEAST AS WIDE AS IT IS TALL, and that is a fact about being ANCHORED
        // rather than a shape somebody liked. The badge hangs off the Inbox tab's glyph at
        // `END or TOP` with a negative margin, so it grows LEFTWARD across the icon it is pinned
        // to -- and a box sized to its text alone is a different width for every count. The ninth
        // session arriving turned a narrow lozenge into a wide one and moved the mark against the
        // glyph underneath. One digit in a square is the box row 3 draws.
        //
        // A FLOOR AND NOT A FIXED WIDTH: two digits and `99+` still have to fit, and a fixed box
        // would clip the count this component exists to show.
        minimumWidth = Kit.dpPx(context, KitMetrics.BADGE_HEIGHT_DP)
        layoutParams = LinearLayout.LayoutParams(
            WRAP,
            Kit.dpPx(context, KitMetrics.BADGE_HEIGHT_DP),
        )
    }
