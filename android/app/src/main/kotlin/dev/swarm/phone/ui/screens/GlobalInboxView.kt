package dev.swarm.phone.ui.screens

import android.content.Context
import android.view.View
import android.view.ViewGroup
import android.widget.LinearLayout
import dev.swarm.phone.ui.kit.KitTag
import dev.swarm.phone.ui.kit.emptyState
import dev.swarm.phone.ui.kit.navHeaderDrill
import dev.swarm.phone.ui.kit.sessionList
import dev.swarm.phone.ui.kit.settingsRow

/**
 * The GLOBAL_INBOX destination AS DRAWN (inbox.global, bead agents-tracker-0ox9): the companion
 * view of [GlobalInboxScreen], reached from the machine switcher's
 * [MachinesPanelScreen.GLOBAL_INBOX_LABEL] entry.
 *
 * WHAT A ROW SAYS IS THE TUPLE'S NEWS: the session's title on the first line and the MACHINE that
 * serves it on the second ([GlobalInboxScreen.meta]), because the aggregate surface is the one
 * place a title alone cannot identify anything (MM4). The rows are [GlobalInboxScreen.rows]'s own
 * fold -- this file keys nothing itself.
 */
object GlobalInboxTag {
    /** The drill header. */
    const val NAV = "global.inbox.nav"

    /** One (machine_id, session_id) row. */
    const val ROW = "global.inbox.row"

    /** Row 8's block when no pairing holds a session. */
    const val EMPTY = "global.inbox.empty"
}

/**
 * @param onBack where the drill chevron goes, or null for a composition with no way back to wire,
 *  which draws no dead control (`navHeaderDrill(back = null)`'s own ruling).
 */
fun globalInboxView(
    context: Context,
    rows: List<GlobalInboxRowModel>,
    onBack: (() -> Unit)? = null,
): View {
    val column = LinearLayout(context).apply {
        orientation = LinearLayout.VERTICAL
        layoutParams = LinearLayout.LayoutParams(MATCH, WRAP)
    }

    column.addView(
        navHeaderDrill(
            context,
            back = if (onBack == null) null else GlobalInboxScreen.BACK,
            title = MachinesPanelScreen.GLOBAL_INBOX_LABEL,
        ).apply {
            tag = GlobalInboxTag.NAV
            onBack?.let { back ->
                findViewWithTag<View>(KitTag.DRILL_BACK)?.setOnClickListener { back() }
            }
        },
    )

    if (rows.isEmpty()) {
        column.addView(
            emptyState(context, GlobalInboxScreen.EMPTY_COPY).apply { tag = GlobalInboxTag.EMPTY },
        )
        return column
    }

    val list = sessionList(context)
    rows.forEach { row ->
        list.addView(
            settingsRow(
                context = context,
                label = row.title,
                sublabel = GlobalInboxScreen.meta(row),
            ).apply { tag = GlobalInboxTag.ROW },
        )
    }
    column.addView(list)
    return column
}

private const val MATCH = ViewGroup.LayoutParams.MATCH_PARENT
private const val WRAP = ViewGroup.LayoutParams.WRAP_CONTENT
