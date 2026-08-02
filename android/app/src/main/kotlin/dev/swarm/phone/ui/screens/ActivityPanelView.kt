package dev.swarm.phone.ui.screens

import android.content.Context
import android.view.View
import android.view.ViewGroup
import android.widget.LinearLayout
import android.widget.TextView
import dev.swarm.phone.ui.kit.activityRow
import dev.swarm.phone.ui.kit.emptyState
import dev.swarm.phone.ui.kit.navHeader
import dev.swarm.phone.ui.kit.sectionLabel
import dev.swarm.phone.ui.kit.sessionList

/**
 * Phase B slice S25 -- PB-DS-6 and PB-DS-9: the activity screen, composed from the component kit.
 *
 * WHAT IT COMPOSES. A `navHeader`, one `sectionLabel`, and an `activityRow` per journal record --
 * or row 8's `emptyState` under the heading when there are none. It decides nothing about how any
 * of them looks: `android/gate/s24_screens_test.go` fences this package to exactly that, so an
 * `R.color`, an `R.dimen`, an `R.style`, a `setTextAppearance`, a `setPadding` or a `background =`
 * here fails the build.
 *
 * **IT PASSES NO TIMESTAMP, AND THAT IS THE WHOLE OF WHAT THIS FILE HAS TO SAY ABOUT THE MOCK.**
 * `activityRow` takes one because derivation row 14 specifies the cell; [ActivityEntry] has none
 * to give because the wire carries none. The call below therefore spends the parameter's default,
 * and the row renders with no gutter rather than with an empty one -- which is only free because
 * row 14 makes that column wrap-content instead of the mock's fixed 52 dp. [ActivityPanel] argues
 * all of it; this file is where the argument becomes an argument that is not passed.
 *
 * THE STALE LINE IS A BARE `TextView`, for the reason `SettingsPanelView`'s notices are: there is
 * no notice or body-copy component in the kit -- row 8's empty state is centred with 48 dp of
 * vertical padding and is a different thing -- so it carries the model's copy and no appearance at
 * all. That is the absence of a decision rather than one made here; reaching for `Body.Secondary`
 * directly would be a screen choosing type.
 *
 * IT SITS ABOVE THE HEADING RATHER THAN INSIDE THE LIST, which is the one placement decision here
 * and it differs from `PeekPanel`'s deliberately. There the banner goes INSIDE the mono well,
 * because a stale snapshot is one object and the warning belongs on it. Here the hole is in the
 * CHRONOLOGY -- it is about records that are not on screen at all -- so there is no row it could
 * attach to, and it has to be read before the list rather than found somewhere in it.
 */
object ActivityTag {
    /** The activity tab's own `.pnav`. */
    const val NAV = "activity.nav"

    /** `.plabel` -- one, and see [ActivityPanel] for why the mock's two are not reproduced. */
    const val SECTION_LABEL = "activity.section.label"

    /**
     * One `.arow`. The row's own parts are the kit's and carry `KitTag.ACTIVITY_*`; this tag is
     * the screen's handle on the whole row.
     */
    const val ROW = "activity.row"

    /** Row 8's block, under a heading whose section holds nothing. */
    const val EMPTY = "activity.empty"

    /** PB-APP-8: what the screen says when the log has a hole in it. */
    const val STALE = "activity.stale"

    /** The parts whose ON-SCREEN ORDER is the composition. */
    val COMPOSITION: Set<String> = setOf(NAV, STALE, SECTION_LABEL, ROW, EMPTY)
}

/**
 * The activity panel as a view.
 *
 * @param below views this slice has NOT recomposed, hosted under the panel. Null is the finished
 *  shape. `SettingsPanelView` takes the same parameter for the same reason.
 */
fun activityPanelView(
    context: Context,
    panel: ActivityPanel,
    below: View? = null,
): View {
    val column = LinearLayout(context).apply {
        orientation = LinearLayout.VERTICAL
        layoutParams = LinearLayout.LayoutParams(MATCH, WRAP)
    }

    // `navHeader`'s second argument is the live counter, and activity has none: the counter is the
    // inbox's in-context liveness readout (derivation §1.4), and a log of what has already
    // happened has nothing in flight to count.
    column.addView(navHeader(context, panel.title, null).apply { tag = ActivityTag.NAV })

    if (panel.staleNotice.isNotEmpty()) {
        column.addView(
            TextView(context).apply {
                tag = ActivityTag.STALE
                text = panel.staleNotice
                layoutParams = LinearLayout.LayoutParams(MATCH, WRAP)
            },
        )
    }

    panel.sections.forEach { section ->
        column.addView(
            sectionLabel(context, section.heading).apply { tag = ActivityTag.SECTION_LABEL },
        )
        if (section.rows.isEmpty()) {
            column.addView(
                emptyState(context, section.emptyCopy).apply { tag = ActivityTag.EMPTY },
            )
        } else {
            // `sessionList` IS `.prows`, AND USING IT HERE IS A REUSE RATHER THAN A BORROWING.
            // The mock gives the activity rows their own container, `.cards` (`padding: 0 14px;
            // gap: 8px`), which is `.prows` (`0 12px`, gap 7) with different numbers -- and §6 is
            // where that difference is already settled, since every mock geometry in this app
            // moves onto Substrate's. Row 14 does the same to the row itself, putting it on
            // `--p-card-r` 9 against the mock's 12. A second container factory would be the copy
            // §2's reuse rule exists to prevent, and building the gap and the side padding HERE
            // would be the PB-DS-6 violation the kit exists to stop: a screen typing spacing.
            val list = sessionList(context)
            section.rows.forEach { entry ->
                list.addView(
                    activityRow(
                        context = context,
                        body = entry.body,
                        emphasis = entry.emphasis,
                    ).apply { tag = ActivityTag.ROW },
                )
            }
            column.addView(list)
        }
    }

    below?.let { column.addView(it) }
    return column
}

private const val MATCH = ViewGroup.LayoutParams.MATCH_PARENT
private const val WRAP = ViewGroup.LayoutParams.WRAP_CONTENT
