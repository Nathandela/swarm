package dev.swarm.phone.ui.kit

import android.content.Context
import android.view.View
import android.view.ViewGroup
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
 * factory takes no content: what varies inside the column is everything, never the column's own
 * padding.
 *
 * **THE CALLER IS THE SCREEN THAT HOSTS, AND THERE IS EXACTLY ONE** (agents-tracker-2pnu F2).
 * `pairingPanelView` spent this too, and `PhoneSurface.drawPairOnly` hosts that panel INSIDE
 * `pairOnlyView`'s column -- so the started pairing path paid row 18's cell twice and rendered at
 * 48 dp sides. A padding is spent by whoever owns the screen edge, and the pairing flow does not
 * own one: it is always somebody's content.
 */
fun screenColumn(context: Context): LinearLayout = LinearLayout(context).apply {
    orientation = LinearLayout.VERTICAL
    layoutParams = LinearLayout.LayoutParams(MATCH, WRAP)
    val vertical = Kit.dimenPx(context, R.dimen.swarm_space_10)
    val horizontal = Kit.dimenPx(context, R.dimen.swarm_space_24)
    setPaddingRelative(horizontal, vertical, horizontal, vertical)
}

/**
 * derived: docs/design/substrate-components.md #20 Screen scaffold
 * derived: docs/design/substrate-components.md §4 Notice line
 *
 * The screen's own side air, given to ONE child that carries none: `swarm_space_12`, the step the
 * Inbox's own row container already spends.
 *
 * **IT IS THE OWNER'S RULING OF 2026-08-09 (agents-tracker-nx44.10), AND THE DEFECT IS THAT NINE
 * COMPONENTS OWN THEIR EDGE AND FOUR DO NOT.** `navHeader` and `sectionLabel` hold themselves
 * `space_18` off the glass, `sessionList` 12, `settingsRow` and `machineRow` 14, `emptyState` 24 --
 * so the Inbox reads correctly and so do the parts of every other screen built out of those. What
 * has no air at all is §4's notice line, which says so in as many words -- *"no margin, no padding
 * and no gravity of its own ... the air is the composing column's"* -- along with `ctaStack`,
 * a loose `ctaButton` and `textField`. On the Inbox and on pairing the composing column pays;
 * on Activity, Settings, the session detail and the launch form it was a bare `MATCH_PARENT`
 * column paying nothing, and the owner photographed the result: text and buttons on the glass.
 *
 * **IT IS A PER-CHILD SEAM AND NOT A PADDING ON THE COLUMN, WHICH IS THE WHOLE OF THE DESIGN.**
 * A column that padded its own sides would add 12 to every child that already holds itself off the
 * edge: the nav title would render at 30 against the Inbox's 18, the section labels with it, and
 * the row cards at 24 -- three numbers the owner-signed maquette states directly (`.nav`,
 * `.sect` at `0 18px`, `.slab` at `margin: 0 14px`), on screens that would then disagree with the
 * Inbox across a tab switch. That is agents-tracker-2pnu F2 wearing a different pair of columns,
 * and the ruling's own words are that the air is spent EXACTLY ONCE. So the column stays bare and
 * the air goes to the children that arrive without one, where it can only be spent once.
 *
 * **IT SETS RATHER THAN ADDS, BECAUSE THE SLOTS OUTLIVE THE DRAW.** `PhoneSurface` builds Stop,
 * Kill, the resync control and the launch form's fields once and re-parents them on every
 * redraw -- so a seam that ADDED a margin would walk the controls off the screen at the rate the
 * machine produces journal events. Setting an absolute margin is idempotent by construction, which
 * is also what makes a second call at a second site harmless rather than a doubling.
 *
 * **A BLOOMING CTA'S ROOM IS READ AND NOT GUESSED.** `ctaButton(APPROVE)` inflates itself by
 * [CtaSpec.insetPx] and hands every pixel back with a negative margin, so its VIEW is 18 dp wider
 * on each side than the button anyone aims at. The air is measured to the VISIBLE box: the margin
 * this sets is the air minus that room, which leaves the painted edge exactly `swarm_space_12`
 * from the screen's. Reading the room off the drawable rather than off the margin is what keeps
 * the second call idempotent -- the margin has changed by then and the room has not.
 */
fun View.screenAir(): View = apply {
    val room = (background as? CtaSurface)?.spec?.insetPx ?: 0
    val air = Kit.dimenPx(context, R.dimen.swarm_space_12) - room
    val params = (layoutParams as? ViewGroup.MarginLayoutParams)
        ?: LinearLayout.LayoutParams(MATCH, WRAP).also { layoutParams = it }
    params.marginStart = air
    params.marginEnd = air
    layoutParams = params
}
