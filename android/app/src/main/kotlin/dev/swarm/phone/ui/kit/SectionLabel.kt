package dev.swarm.phone.ui.kit

import android.widget.LinearLayout
import android.content.Context
import android.widget.TextView
import dev.swarm.phone.R

/**
 * origin: .plabel
 *
 * A Group heading: mono, 10.5 sp, tracked to 0.09 em, uppercase, in the tertiary ink.
 *
 * `text-transform: uppercase` IS THE COMPONENT'S, NOT THE COPY'S. `isAllCaps` rather than an
 * uppercased string, so the screen keeps passing "Needs you" and the accessibility tree keeps
 * reading a phrase rather than nine letters -- and so the transform stays a design decision that
 * can be changed in one place.
 *
 * The ink is `--p-ink3`, which fails the 4.5:1 body-text floor on every surface in the product
 * (3.17 to 3.50:1). That is a property of the pinned token rather than of this component;
 * PB-DS-12 asks for it to be recorded with the sites it affects, and this is one of them.
 */
fun sectionLabel(context: Context, text: CharSequence): TextView = Kit.textView(context).apply {
    setTextAppearance(R.style.TextAppearance_Swarm_Label_Section)
    setTextColor(Kit.colour(context, R.color.swarm_text_tertiary))
    isAllCaps = true
    this.text = text
    setPaddingRelative(
        Kit.dimenPx(context, R.dimen.swarm_space_18),
        Kit.dimenPx(context, R.dimen.swarm_space_12),
        Kit.dimenPx(context, R.dimen.swarm_space_18),
        Kit.dimenPx(context, R.dimen.swarm_space_8),
    )
    layoutParams = LinearLayout.LayoutParams(MATCH, WRAP)
}
