package dev.swarm.phone.ui.kit

import android.content.Context
import android.widget.LinearLayout
import dev.swarm.phone.R

/**
 * derived: docs/design/substrate-components.md #18 Pairing scaffold
 *
 * The pairing screen's own outer column: row 18's `.pair` padding, `space_10` vertical x
 * `space_24` horizontal.
 *
 * `swarm_space_24` (dimens.xml) EXISTS FOR THIS EXACT CELL AND HAD NO CALLER: the dimens.xml
 * comment names it "the mock's pairing-scaffold padding" and the gate asserts both directions --
 * the step is on the scale and unspent is not the same as unjustified. `pairingPanelView` and
 * `pairOnlyView` built their outer column as a bare `LinearLayout` with no padding at all, which
 * is the row's cell unspent rather than an omission nobody noticed: `ui/screens` is fenced
 * against `R.dimen` and `setPadding` (PB-DS-6), so neither file could have spent it directly.
 *
 * IT IS A CONTAINER FACTORY, ON `sessionList`'s PRECEDENT: "the rows' container ... exists so a
 * screen never types the 12 dp side padding or the gap between rows" (SessionRow.kt). This is the
 * same shape one level out -- the SCREEN's own inset rather than a list's -- and it is why the
 * factory takes no content: what varies between the pairing screen and the pair-only screen is
 * everything inside the column, never the column's own padding.
 */
fun screenColumn(context: Context): LinearLayout = LinearLayout(context).apply {
    orientation = LinearLayout.VERTICAL
    layoutParams = LinearLayout.LayoutParams(MATCH, WRAP)
    val vertical = Kit.dimenPx(context, R.dimen.swarm_space_10)
    val horizontal = Kit.dimenPx(context, R.dimen.swarm_space_24)
    setPaddingRelative(horizontal, vertical, horizontal, vertical)
}
