package dev.swarm.phone.ui.screens

import android.content.Context
import android.view.View
import android.view.ViewGroup
import android.widget.LinearLayout
import android.widget.TextView
import dev.swarm.phone.ui.kit.navHeader
import dev.swarm.phone.ui.kit.sectionLabel

/**
 * Phase B slice S24 -- PB-DS-6 and PB-DS-9: the settings screen, composed from the component kit.
 *
 * WHAT IT COMPOSES AND WHAT IT CANNOT. Inventory C6 is a nav header, a section label and a stack
 * of rows. The kit ships the first two and this file spends both. It does NOT ship the third:
 * derivation table row 15 (settings row) and row 4 (toggle) have no factory, and PB-DS-6 assigns
 * every visual decision to `ui/kit`. Building either here would contradict the requirement in the
 * same breath as claiming it, and `android/gate/s24_screens_test.go` fences this package to
 * exactly that -- an `R.color`, an `R.dimen`, an `R.style`, a `setTextAppearance`, a `setPadding`
 * or a `background =` fails the build.
 *
 * SO THE ROW ARRIVES AS A PARAMETER. [rowFor] is supplied by `SettingsSurface`, which owns the
 * switch anyway: the listener, the touch filter PB-SEC-12 clause 1 requires, and the fact that
 * the same control has to survive a redraw. That keeps the gap VISIBLE -- a missing component is
 * a parameter in a signature rather than a lookalike hand-built where nothing checks it -- and it
 * is the same seam `triageInboxView` already uses for `below`.
 *
 * WHAT IS UNSTYLED, SAID PLAINLY. The label, the sublabel and the notice lines below are bare
 * `TextView`s. They carry the recorded copy and no appearance at all, so they render at whatever
 * the theme's default is. That is not a design decision made here; it is the absence of one,
 * waiting for rows 15 and 4. The alternative -- reaching for `Title.Row` and `Body.Secondary`
 * directly -- is a screen choosing type, which is the thing this package is fenced against.
 */
object SettingsTag {
    /** C6.1 `.pnav`. */
    const val NAV = "settings.nav"

    /** C6.2 `.seclabel` -- one per section. */
    const val SECTION_LABEL = "settings.section.label"

    /** One `.setrow`: its copy, and the control the surface supplied. */
    const val ROW = "settings.row"

    /** `.setrow .sl` -- the row's own line. */
    const val ROW_LABEL = "settings.row.label"

    /** `.setrow .sl span` -- the qualifier under it. */
    const val ROW_SUBLABEL = "settings.row.sublabel"

    /** What the panel says about switches that are inert or unconfirmed. */
    const val NOTICE = "settings.notice"

    /** The parts whose ON-SCREEN ORDER is the recorded composition. */
    val COMPOSITION: Set<String> = setOf(NAV, SECTION_LABEL, ROW)
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

    panel.sections.forEach { section ->
        column.addView(
            sectionLabel(context, section.heading).apply { tag = SettingsTag.SECTION_LABEL },
        )
        section.rows.forEach { row -> column.addView(rowView(context, row, rowFor(row))) }
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
 * One row: its two lines, then the control.
 *
 * THE ROW IS ONE ACCESSIBILITY NODE AND THE TWO LINES ARE NONE, which is derivation row 15's
 * "the whole row is one >=48 dp target" expressed in the only way this package can express it.
 * Left alone, a screen reader reads four things per row -- the label, the sublabel, the switch,
 * and nothing tying them together. With the description on the row and the two `TextView`s
 * excluded, it reads the row's words once and then the switch's state, which is two nodes and
 * the right two.
 */
private fun rowView(context: Context, row: SettingsRow, control: View): View =
    LinearLayout(context).apply {
        tag = SettingsTag.ROW
        orientation = LinearLayout.HORIZONTAL
        layoutParams = LinearLayout.LayoutParams(MATCH, WRAP)
        isFocusable = true
        contentDescription = row.description

        addView(
            LinearLayout(context).apply {
                orientation = LinearLayout.VERTICAL
                // Weight 1: the copy takes whatever is left beside the control.
                layoutParams = LinearLayout.LayoutParams(0, WRAP, 1f)
                addView(
                    TextView(context).apply {
                        tag = SettingsTag.ROW_LABEL
                        text = row.label
                        importantForAccessibility = View.IMPORTANT_FOR_ACCESSIBILITY_NO
                    },
                )
                addView(
                    TextView(context).apply {
                        tag = SettingsTag.ROW_SUBLABEL
                        text = row.sublabel
                        importantForAccessibility = View.IMPORTANT_FOR_ACCESSIBILITY_NO
                    },
                )
            },
        )
        addView(control)
    }

private const val MATCH = ViewGroup.LayoutParams.MATCH_PARENT
private const val WRAP = ViewGroup.LayoutParams.WRAP_CONTENT
