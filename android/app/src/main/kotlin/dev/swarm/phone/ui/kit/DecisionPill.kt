package dev.swarm.phone.ui.kit

import android.content.Context
import android.view.Gravity
import android.widget.LinearLayout
import android.widget.TextView
import dev.swarm.phone.R

/**
 * derived: docs/design/substrate-components.md #32 Decision pill
 *
 * The one persistent affordance in the reading flow, and it is persistent for exactly as long as
 * an unanswered decision is off screen.
 *
 * **WHAT MAKES IT LEGITIMATE IS THE CONDITION AND NOT THE PAINT.** A floating champagne pill over
 * a conversation is a strong thing to draw, and the argument for it is that the alternative is
 * worse: a decision the agent is blocked on, scrolled past, on a stream that keeps arriving. The
 * drawing states the condition twice -- it appears only while an unanswered decision is off
 * screen, and auto-scroll to the newest message is suppressed while one is unanswered -- and both
 * halves belong to the screen, because a component never knows where it is. What this file can do
 * is refuse to be anything else, and it does: no count, no dismissal, no second state.
 *
 * **IT IS [pillSurface] AT `--p-hero`, WHICH MINTS NOTHING.** Row 3's badge already spends that
 * recipe, and the champagne is what "you" looks like in this skin (ADR-009 D6) -- a question the
 * agent is blocked on is the strongest reading of that there is. The ink is `--p-hero-ink`,
 * because a saturated fill takes the ground ink and not the linen; that pairing is row 3's too.
 *
 * **NO `--p-cta-fx` BLOOM, AND THAT IS THE STANDING RULE RATHER THAN RESTRAINT.** Nothing glows
 * unless it is alive, and owner ruling R8 narrowed the one glow this app has to the single session
 * that needs you. A second champagne halo on the same screen would dilute the first, which is the
 * exact argument R8 made when it took the glow off the Working dot.
 *
 * **THE ARROW IS NOT HERE.** The drawing puts one after the words and the copy table records
 * `Decision needed`; a string not on that sheet is not on the screen. Where the decision IS gets
 * said by the scroll the tap performs, which is a better answer than a character.
 *
 * IT SETS NO GRAVITY AND NO MARGIN, for [earlierChip]'s reason: the drawing centres it and
 * centring is the screen's half of the fence.
 */
fun decisionPill(context: Context, label: CharSequence): TextView = Kit.textView(context).apply {
    setTextAppearance(R.style.TextAppearance_Swarm_Label_Chip)
    setTextColor(Kit.colour(context, R.color.swarm_hero_ink))
    text = label
    background = pillSurface(context, fill = Kit.colour(context, R.color.swarm_hero))
    // One line, [earlierChip]'s reason: a pill that wrapped would be a card. Two words never
    // will, and the guard is what stops a caller's longer copy quietly turning this into one.
    Kit.identityCell(this)
    gravity = Gravity.CENTER
    setPaddingRelative(
        Kit.dimenPx(context, R.dimen.swarm_space_14),
        Kit.dimenPx(context, R.dimen.swarm_space_6),
        Kit.dimenPx(context, R.dimen.swarm_space_14),
        Kit.dimenPx(context, R.dimen.swarm_space_6),
    )
    // `denyChip`'s terms: a minimum and not a size. This is the one control on the screen a person
    // reaches for while the agent is working, which is when a thumb is least careful.
    minimumHeight = Kit.dpPx(context, KitMetrics.MIN_TARGET_DP)
    Kit.focusable(this, componentRadiusPx = Kit.dimen(context, R.dimen.swarm_radius_chip))
    layoutParams = LinearLayout.LayoutParams(WRAP, WRAP)
}
