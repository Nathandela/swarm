package dev.swarm.phone.ui.kit

import android.content.Context
import android.view.View
import android.widget.LinearLayout
import android.widget.TextView
import dev.swarm.phone.R

/**
 * origin: .pnav
 *
 * The root-screen header: the display title, and the live counter pushed to the far right.
 *
 * BASELINE ALIGNMENT IS ASKED FOR, because the display rung beside 10 sp is the pair that looks wrong
 * centred. The design says `align-items: baseline`; LinearLayout will do it, but only when the
 * container is told to.
 *
 * `margin-left: auto` HAS NO ANDROID FORM. The equivalent is giving the title the remaining width,
 * which is what a weight of 1 does. Writing it as a trailing gravity instead would work until the
 * title grew long enough to meet the counter, and then they would overlap rather than push.
 *
 * @param status the sync mark, or null while the phone has nothing to report (agents-tracker-nx44
 *  .2). IT IS A SLOT AND NOT A MODEL, on [approvalSheet]'s and [pairingStep]'s precedent: the pill
 *  opens a detail, so it carries a click and a destination, and both of those are the surface's.
 *
 *  **WHY THE NAV ROW IS WHERE IT GOES.** The sync state is a property of what the title names --
 *  is this screen showing you the current thing -- and this row is where a reader's eye already is.
 *  The alternative it replaces was a band of its own above the destination, which is a fifth bar
 *  on a screen that already has a status bar, this row, a scroll and a tab bar; the four sentences
 *  it drew overlapped this row's title on a real handset (field test 3).
 *
 *  IT SITS AFTER THE LIVE COUNTER, so the two trailing readouts read outward from the screen: the
 *  counter is about what is happening IN this screen, the pill about whether the screen can be
 *  trusted at all.
 */
fun navHeader(
    context: Context,
    title: CharSequence,
    live: CharSequence?,
    status: View? = null,
): LinearLayout =
    LinearLayout(context).apply {
        orientation = LinearLayout.HORIZONTAL
        isBaselineAligned = true
        setPaddingRelative(
            Kit.dimenPx(context, R.dimen.swarm_space_18),
            Kit.dimenPx(context, R.dimen.swarm_space_4),
            Kit.dimenPx(context, R.dimen.swarm_space_18),
            Kit.dimenPx(context, R.dimen.swarm_space_10),
        )
        layoutParams = LinearLayout.LayoutParams(MATCH, WRAP)

        addView(
            Kit.textView(context).apply {
                setTextAppearance(R.style.TextAppearance_Swarm_Display_NavTitle)
                // `.pnav .big` declares no colour: it inherits `.pscreen { color: var(--p-ink) }`.
                setTextColor(Kit.colour(context, R.color.swarm_text_primary))
                text = title
                layoutParams = LinearLayout.LayoutParams(0, WRAP, 1f)
                tag = KitTag.TITLE
                Kit.identityCell(this)
            },
        )
        if (live != null) {
            addView(
                liveCounter(context, live).apply {
                    (layoutParams as LinearLayout.LayoutParams).marginStart =
                        Kit.dimenPx(context, R.dimen.swarm_space_10)
                },
            )
        }
        status?.let {
            addView(
                it.apply {
                    (layoutParams as? LinearLayout.LayoutParams)?.marginStart =
                        Kit.dimenPx(context, R.dimen.swarm_space_10)
                },
            )
        }
    }

/**
 * origin: .pnav .live
 *
 * The in-context liveness readout: mono, tracked, phosphor green.
 *
 * IT IS A SEPARATE COMPONENT FROM THE BADGE ON PURPOSE. §1.4 of the derivation table ships both
 * counters, on the argument that they count different things -- this one says how much is in
 * flight, the tab badge says what needs you and is the only one that survives leaving this screen.
 * That argument only holds while they stay visually distinct, which is why this is `--p-hero` and
 * the badge is `--p-att`.
 */
fun liveCounter(context: Context, text: CharSequence): TextView = Kit.textView(context).apply {
    setTextAppearance(R.style.TextAppearance_Swarm_Label_Live)
    setTextColor(Kit.colour(context, R.color.swarm_hero))
    this.text = text
    layoutParams = LinearLayout.LayoutParams(WRAP, WRAP)
    tag = KitTag.LIVE
}
