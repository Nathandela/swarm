package dev.swarm.phone.ui.kit

import android.content.Context
import android.view.Gravity
import android.widget.LinearLayout
import android.widget.TextView
import dev.swarm.phone.R

/**
 * derived: docs/design/substrate-components.md #8 Empty state
 *
 * There is deliberately no `origin:` line. `.empty` is row 8's MOCK class, not a Substrate rule --
 * it does not appear in the shared CSS block at all, which is precisely why row 8 had to derive
 * this component. Citing it as an origin would claim a join to a rule that does not exist.
 *
 * The block a section shows when it holds nothing: `Body.Message` in the secondary ink, centred,
 * with generous vertical air so an empty section reads as a deliberate statement rather than as a
 * heading someone forgot to fill.
 *
 * WHY THIS COMPONENT EXISTS AT ALL, which is the whole of PB-DS-9's most-argued clause.
 * `TriageInbox.kt` records it: an empty section is still a section and says so, because dropping
 * it is the obvious implementation and it is wrong for a triage surface -- the sections then move
 * under the user as sessions change group, and "nothing is waiting on me", the most useful fact
 * this screen can report, becomes indistinguishable from "that section scrolled away".
 *
 * A heading over nothing is the same defect wearing a heading. So the copy is the component's
 * reason to exist, and the screen passes it: "Nothing is waiting on you." is a sentence, not the
 * absence of rows.
 *
 * THE INVERSE DEFECT IS THE ONE THAT NEARLY SHIPPED. Drawing this block unconditionally is the
 * obvious over-correction, and it tells a user holding two live sessions that the section has
 * nothing in it. `TriageInboxViewTest` asserts both directions; the untested direction is where
 * that bug lives.
 *
 * The ink is `--p-ink2` rather than `--p-ink3`: this is prose a user is meant to read, and
 * `--p-ink3` fails the 4.5:1 body-text floor on every surface in the product (3.17 to 3.50:1).
 * The derivation table makes the same call for the same reason.
 */
fun emptyState(context: Context, text: CharSequence): TextView = Kit.textView(context).apply {
    setTextAppearance(R.style.TextAppearance_Swarm_Body_Message)
    setTextColor(Kit.colour(context, R.color.swarm_text_secondary))
    gravity = Gravity.CENTER
    this.text = text
    // Row 8 states "padding 48 (2 x `space_24`) vertical, `space_24` horizontal". Spent as
    // `side + side` rather than through a `2` multiplier, because that adds NO number to the
    // package: no metric to annotate, no tenth row on the literal exemptions -- and, the reason
    // that matters, no borrowing of the exemption written for `2`, which is the status dot's two
    // halo sides. An exemption keyed by literal text lends its justification to any other
    // quantity spelled the same, which is how a reviewed number becomes an unreviewed one.
    val side = Kit.dimenPx(context, R.dimen.swarm_space_24)
    setPaddingRelative(side, side + side, side, side + side)
    layoutParams = LinearLayout.LayoutParams(MATCH, WRAP)
}
