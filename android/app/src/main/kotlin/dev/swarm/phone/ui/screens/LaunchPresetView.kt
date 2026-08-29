package dev.swarm.phone.ui.screens

import android.content.Context
import android.view.View
import android.view.ViewGroup
import android.widget.LinearLayout
import dev.swarm.phone.ui.kit.monoWell
import dev.swarm.phone.ui.kit.notice
import dev.swarm.phone.ui.kit.scrolledHorizontally
import dev.swarm.phone.ui.kit.screenAir
import dev.swarm.phone.ui.kit.sectionLabel
import dev.swarm.phone.ui.kit.sessionList
import dev.swarm.phone.ui.kit.settingsRow

/**
 * Wave R5's preset launch flow AS DRAWN (round 2; bead agents-tracker-hggx.6): the companion
 * view of [LaunchPresetScreen], in the module's established shape (screen model + View beside it
 * -- MachinesPanelView, LaunchPanelView, TriageInboxView).
 *
 * EVERY SENTENCE IS THE MODEL'S. The availability notice is [LaunchPresetScreen.noticeFor] of
 * the resolver's state -- so a denied NEW_SESSION is a NAMED reason on screen, never a missing
 * button (the D3 defect class the R5 bead calls out) -- and the delivery line is
 * [LaunchPresetScreen.noticeFor] of a claimed outcome's state: visible success AND visible
 * refusal for the one verb.
 *
 * A ROW IS THE SELECT CONTROL (SELECT_PRESET): tapping it hands the row's OWN model back to
 * [onSelect], which opens the surface's confirmation sheet (CONFIRM_LAUNCH / CANCEL_LAUNCH live
 * there -- a signed launch must never ride a bare row tap; ADR-007 D8 retains the explicit
 * confirm). The phone selects; it never composes -- no cwd, env or argv field exists anywhere in
 * this composition.
 *
 * THE FETCH CONTROL IS SUPPLIED BY THE SURFACE (the [launchPanelView] `submit` arrangement, for
 * its recorded reasons: it carries a facade call and the touch filter). It is composed
 * UNCONDITIONALLY: a fetch pressed against an unreachable machine earns the routed offline
 * refusal out loud, which is a better answer than a button that vanishes without a word.
 */
object LaunchPresetTag {
    /** The section heading ([LaunchPresetScreen.HEADING]). */
    const val HEADING = "launch.preset.heading"

    /** The availability resolver's named denial, drawn only when NEW_SESSION is not offered. */
    const val AVAILABILITY = "launch.preset.availability"

    /** The well under the availability notice, carrying the command a denial points at (W5.1). */
    const val COMMAND = "launch.preset.command"

    /** One selectable machine-authored preset row (SELECT_PRESET). */
    const val ROW = "launch.preset.row"

    /** The control that asks the machine for its current list ([LaunchPresetScreen.FETCH_LABEL]). */
    const val FETCH = "launch.preset.fetch"

    /** The confirm verb's resolved delivery/refusal sentence, claimed by operation id. */
    const val DELIVERY = "launch.preset.delivery"

    /**
     * The FETCH verb's OWN resolved refusal sentence (round 4, review MEDIUM 3).
     *
     * A LINE OF ITS OWN, beside [FETCH] rather than sharing [DELIVERY]: while a launch was in
     * flight the launch verb overwrote the shared line unconditionally, so a refused fetch was
     * invisible for the whole pending window. One slot cannot carry two verbs' answers.
     */
    const val FETCH_DELIVERY = "launch.preset.fetch.delivery"
}

/**
 * The preset launch flow as a view.
 *
 * @param fetch the control that issues the signed launch_presets read; the SURFACE owns the verb.
 * @param onSelect a preset row was tapped: open the confirmation sheet over EXACTLY this row --
 *  the revision it displayed is what the confirm signs (echo, never derive).
 */
fun launchPresetView(
    context: Context,
    panel: LaunchPresetPanel,
    fetch: View,
    onSelect: (PresetRowModel) -> Unit,
): View {
    val column = LinearLayout(context).apply {
        orientation = LinearLayout.VERTICAL
        layoutParams = LinearLayout.LayoutParams(MATCH, WRAP)
    }

    column.addView(sectionLabel(context, LaunchPresetScreen.HEADING).apply { tag = LaunchPresetTag.HEADING })

    // The resolver's named denial -- OFFLINE, KILL_SWITCH_OFF, TIER_FORBIDS, NO_PRESETS, or the
    // first-run FETCHING -- with its own remedy. Empty exactly when AVAILABLE.
    if (panel.availabilityNotice.isNotEmpty()) {
        column.addView(
            notice(context, panel.availabilityNotice)
                .apply { tag = LaunchPresetTag.AVAILABILITY }
                .screenAir(),
        )
    }
    // A command is a well's text and never a sentence's (phone refit W5.1): the kit's mono well,
    // under the sentence that points at it, scrollable so a long verb is reachable.
    if (panel.availabilityCommand.isNotEmpty()) {
        column.addView(
            monoWell(context, panel.availabilityCommand)
                .apply { tag = LaunchPresetTag.COMMAND }
                .scrolledHorizontally(),
        )
    }

    // The machine-authored rows, offered only while the resolver says AVAILABLE: a row rendered
    // under a denial would be a select control for a launch every press must refuse.
    if (panel.availability == LaunchAvailability.AVAILABLE && panel.rows.isNotEmpty()) {
        val list = sessionList(context)
        panel.rows.forEach { row ->
            list.addView(
                settingsRow(
                    context = context,
                    label = row.displayName,
                    // The row's second line carries the WIRE FACTS the confirm will show again:
                    // provider and the machine's canonical workspace path. Nothing invented here.
                    sublabel = row.provider + "  " + row.workspacePath,
                ).apply {
                    tag = LaunchPresetTag.ROW
                    // PB-SEC-12 clause 1: selecting what leads to a signed launch must not take
                    // a tap the user could not see.
                    filterTouchesWhenObscured = true
                    setOnClickListener { onSelect(row) }
                },
            )
        }
        column.addView(list)
    }

    column.addView(fetch.apply { tag = LaunchPresetTag.FETCH }.screenAir())

    // The FETCH verb's own refusal, immediately under the control that earned it, on its own
    // line (round 4, review MEDIUM 3). Drawn independently of the launch verb's line below, so
    // an in-flight launch can no longer make a refused fetch invisible.
    if (panel.fetchNotice.isNotEmpty()) {
        column.addView(
            notice(context, panel.fetchNotice)
                .apply { tag = LaunchPresetTag.FETCH_DELIVERY }
                .screenAir(),
        )
    }

    // The one verb's resolved answer: APPLIED, PENDING, each stable refusal, OUTCOME_UNKNOWN, or
    // the catch-all REFUSED with the machine's words. Drawn only when an operation was issued --
    // a status line about an operation nobody issued is LaunchPanelView's recorded anti-pattern.
    if (panel.deliveryNotice.isNotEmpty()) {
        column.addView(
            notice(context, panel.deliveryNotice)
                .apply { tag = LaunchPresetTag.DELIVERY }
                .screenAir(),
        )
    }

    return column
}

private const val MATCH = ViewGroup.LayoutParams.MATCH_PARENT
private const val WRAP = ViewGroup.LayoutParams.WRAP_CONTENT
