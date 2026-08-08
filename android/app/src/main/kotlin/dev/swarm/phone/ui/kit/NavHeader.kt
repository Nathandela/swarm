package dev.swarm.phone.ui.kit

import android.content.Context
import android.widget.LinearLayout
import android.widget.TextView
import dev.swarm.phone.R

/**
 * origin: .pnav
 *
 * The root-screen header: the display title, and the live counter pushed to the far right.
 *
 * BASELINE ALIGNMENT IS ASKED FOR, because 27 sp beside 10 sp is exactly the pair that looks wrong
 * centred. The design says `align-items: baseline`; LinearLayout will do it, but only when the
 * container is told to.
 *
 * `margin-left: auto` HAS NO ANDROID FORM. The equivalent is giving the title the remaining width,
 * which is what a weight of 1 does. Writing it as a trailing gravity instead would work until the
 * title grew long enough to meet the counter, and then they would overlap rather than push.
 */
fun navHeader(context: Context, title: CharSequence, live: CharSequence?): LinearLayout =
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
