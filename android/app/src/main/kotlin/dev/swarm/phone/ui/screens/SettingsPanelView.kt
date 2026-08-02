package dev.swarm.phone.ui.screens

import android.content.Context
import android.view.View
import android.view.ViewGroup
import android.widget.LinearLayout
import android.widget.TextView
import dev.swarm.phone.ui.kit.KitTag
import dev.swarm.phone.ui.kit.denyChip
import dev.swarm.phone.ui.kit.navHeader
import dev.swarm.phone.ui.kit.sectionLabel
import dev.swarm.phone.ui.kit.settingsRow

/**
 * Phase B slice S24 -- PB-DS-6 and PB-DS-9: the settings screen, composed from the component kit.
 *
 * WHAT IT COMPOSES. Inventory C6 is a nav header, a section label and a stack of rows, and the
 * kit ships all three: `navHeader`, `sectionLabel` and `settingsRow`. This file spends them and
 * decides nothing about how any of them looks -- `android/gate/s24_screens_test.go` fences this
 * package to exactly that, so an `R.color`, an `R.dimen`, an `R.style`, a `setTextAppearance`, a
 * `setPadding` or a `background =` here fails the build.
 *
 * WHAT IT STILL CANNOT COMPOSE IS THE TOGGLE. Derivation row 4 has no factory, so the trailing
 * control arrives as a parameter: [rowFor] is supplied by `SettingsSurface`, which owns the switch
 * anyway -- the listener, the touch filter PB-SEC-12 clause 1 requires, and the fact that the same
 * control has to survive a redraw. `settingsRow` takes a trailing `View` rather than a `Boolean`
 * precisely so a caller can place one it did not build, which is what makes the gap a parameter in
 * a signature rather than a lookalike hand-built where nothing checks it.
 *
 * THE PAIRING SECTION IS NOT IN INVENTORY C6, and that is agents-tracker-64rf's decision rather
 * than a drift: the retired mock draws `Notifications` and a now-void `Security` and nothing about
 * a paired machine, because the entry point only moved onto this screen after an owner could not
 * find it on a real handset. It is composed from the same two components the preference sections
 * are, plus `denyChip` for its one control, and it leads -- see [SettingsPanel.machineSection].
 * NOTHING HERE OWNS ITS CLICK: replacing is revoke-then-pair, which reaches a facade verb and
 * carries PB-SEC-12 clause 1's touch filter, so the control is tagged [SettingsTag.REPLACE] for
 * the surface to reach rather than built a second time where the click is.
 *
 * THE NOTICE LINES ARE STILL BARE `TextView`s. There is no notice or body-copy component -- row 8's
 * empty state is centred with 48 dp of vertical padding and is a different thing -- so they carry
 * the model's copy and no appearance at all. That is the absence of a decision rather than one
 * made here; reaching for `Body.Secondary` directly would be a screen choosing type.
 */
object SettingsTag {
    /** C6.1 `.pnav`. */
    const val NAV = "settings.nav"

    /** C6.2 `.seclabel` -- one per section. */
    const val SECTION_LABEL = "settings.section.label"

    /**
     * One `.setrow`. The row's own parts are the kit's and carry `KitTag.SETTINGS_*`; this tag is
     * the screen's handle on the whole row, which is what the accessibility consolidation below
     * needs a subject for.
     */
    const val ROW = "settings.row"

    /** What the panel says about switches that are inert or unconfirmed. */
    const val NOTICE = "settings.notice"

    /**
     * agents-tracker-64rf's paired-machine row. It is NOT [ROW]: that tag marks the push toggles,
     * and a caller reaching for them must not be handed a row whose control revokes a pairing.
     */
    const val MACHINE_ROW = "settings.machine.row"

    /**
     * The replace control, tagged because the SURFACE owns its click -- the division this file and
     * `MachinesPanelView` are both built on. Replacing is revoke-then-pair, which reaches a facade
     * verb and carries PB-SEC-12 clause 1's touch filter; neither is a screen's.
     */
    const val REPLACE = "settings.machine.replace"

    /** The parts whose ON-SCREEN ORDER is the recorded composition. */
    val COMPOSITION: Set<String> = setOf(NAV, SECTION_LABEL, MACHINE_ROW, ROW)
}

/**
 * The settings panel as a view.
 *
 * @param rowFor the trailing control for one row -- the toggle derivation row 4 specifies and the
 *  kit does not ship. It is a function of the row rather than a flat list so a caller cannot
 *  silently pair the wrong switch with the wrong preference, which is the one defect
 *  [SettingsPanel]'s bijection exists to prevent.
 * @param below views this slice has NOT recomposed, hosted under the panel. Null is the finished
 *  shape.
 */
fun settingsPanelView(
    context: Context,
    panel: SettingsPanel,
    rowFor: (SettingsRow) -> View,
    below: View? = null,
): View {
    val column = LinearLayout(context).apply {
        orientation = LinearLayout.VERTICAL
        layoutParams = LinearLayout.LayoutParams(MATCH, WRAP)
    }

    // C6.1. `navHeader`'s second argument is the live counter, and settings has none: the
    // counter is the inbox's in-context liveness readout (derivation §1.4) and a settings screen
    // has nothing in flight to report.
    column.addView(navHeader(context, panel.title, null).apply { tag = SettingsTag.NAV })

    // FIRST, above the preferences. This is where the pairing entry point lives once a phone is
    // paired (agents-tracker-64rf), and the defect it answers is an owner not finding it -- so it
    // is not put under two switches. The heading is a `.seclabel` like any other, so it carries the
    // same tag: the section order is then readable off the composition.
    panel.machineSection?.let { section ->
        column.addView(
            sectionLabel(context, section.heading).apply { tag = SettingsTag.SECTION_LABEL },
        )
        column.addView(
            settingsRow(
                context = context,
                label = section.row.label,
                sublabel = section.row.sublabel,
                // Row 13's arrangement, reused: `denyChip` is the `.a2-no` treatment at chip
                // metrics and this is the same class of action as Revoke -- it ends this phone's
                // pairing. The chip's own copy says what it does, so no description beside it.
                trailing = denyChip(context, section.row.replaceLabel)
                    .apply { tag = SettingsTag.REPLACE },
            ).apply { tag = SettingsTag.MACHINE_ROW },
        )
    }

    panel.sections.forEach { section ->
        column.addView(
            sectionLabel(context, section.heading).apply { tag = SettingsTag.SECTION_LABEL },
        )
        section.rows.forEach { row ->
            column.addView(
                settingsRow(
                    context = context,
                    label = row.label,
                    sublabel = row.sublabel,
                    trailing = rowFor(row),
                ).apply { announceAsOneRow(row) },
            )
        }
    }

    panel.notices.forEach { notice ->
        column.addView(
            TextView(context).apply {
                tag = SettingsTag.NOTICE
                text = notice
                layoutParams = LinearLayout.LayoutParams(MATCH, WRAP)
            },
        )
    }

    below?.let { column.addView(it) }
    return column
}

/**
 * Make the row ONE accessibility node, which is derivation row 15's "the whole row is one >=48 dp
 * target when it carries a toggle" expressed where it can be.
 *
 * Left alone a screen reader reads four things per row -- the label, the sublabel, the switch, and
 * nothing tying them together. With the description on the row and the kit's two lines excluded it
 * reads the row's words once and then the switch's state, which is two nodes and the right two.
 *
 * IT IS NOT A VISUAL DECISION AND SO IT IS NOT THE KIT'S. `importantForAccessibility` says nothing
 * about how anything looks; what it needs is the row's own [SettingsRow.description], which is copy
 * -- and copy is the screen's. `settingsRow` could not write this without being told the sentence.
 */
private fun View.announceAsOneRow(row: SettingsRow) {
    tag = SettingsTag.ROW
    isFocusable = true
    contentDescription = row.description
    listOf(KitTag.SETTINGS_LABEL, KitTag.SETTINGS_SUBLABEL).forEach { part ->
        findTagged(part)?.importantForAccessibility = View.IMPORTANT_FOR_ACCESSIBILITY_NO
    }
}

/**
 * The descendant the kit tagged with [tag], or null.
 *
 * It is not `kitFind`: that one lives in the TEST source set, and a production file cannot reach
 * it. Four lines rather than promoting it, because promoting a test helper into `ui/kit` is a
 * change to a package this slice may not touch.
 */
private fun View.findTagged(tag: String): View? {
    if (this.tag == tag) return this
    if (this !is ViewGroup) return null
    for (i in 0 until childCount) {
        getChildAt(i).findTagged(tag)?.let { return it }
    }
    return null
}

private const val MATCH = ViewGroup.LayoutParams.MATCH_PARENT
private const val WRAP = ViewGroup.LayoutParams.WRAP_CONTENT
