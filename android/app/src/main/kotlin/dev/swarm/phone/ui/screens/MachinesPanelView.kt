package dev.swarm.phone.ui.screens

import android.content.Context
import android.view.View
import android.view.ViewGroup
import android.widget.LinearLayout
import dev.swarm.phone.ui.kit.denyChip
import dev.swarm.phone.ui.kit.killSwitchPanel
import dev.swarm.phone.ui.kit.machineRow
import dev.swarm.phone.ui.kit.navHeader
import dev.swarm.phone.ui.kit.sectionLabel
import dev.swarm.phone.ui.kit.sessionList
import dev.swarm.phone.ui.kit.settingsRow

/**
 * Phase B slice S25 -- PB-DS-6 and PB-DS-9: the machines screen, composed from the component kit.
 *
 * WHAT IT COMPOSES. Substrate's artifact draws no machines screen at all, so
 * `docs/design/substrate-components.md` rows 11, 12 and 13 are the whole specification, and two of
 * the three blocks are components of their own: [machineRow] for row 11 and [killSwitchPanel] for
 * row 12. Row 13's device row is a [settingsRow] -- with no fingerprint to render it is a name and
 * a trailing control, which is exactly what that component is -- and its Revoke is [denyChip], row
 * 13's "`.a2-no` treatment at chip metrics". This file spends them and decides nothing about how
 * any of them looks; `android/gate/s24_screens_test.go` fences this package to exactly that, so an
 * `R.color`, an `R.dimen`, an `R.style`, a `setTextAppearance`, a `setPadding` or a `background =`
 * here fails the build.
 *
 * **WHY THE ROWS ARE NOT `sessionRow`, WHICH IS WHAT THE DRAWING LOOKS LIKE.** A machine row and a
 * triage row are the same picture -- a mark, a name, a mono identifier, a secondary line -- and the
 * first build of this screen tried to reuse the triage row on exactly that evidence. `sessionRow`
 * builds its leading mark by calling `statusDot` with a `status.Group`, and a machine's
 * reachability is not one: reusing it means passing a Group the server never sent, and the two
 * whose colours happen to match are the fabrication `group-tokens.tsv`'s gate exists to refuse. A
 * reuse justified by identical pixels is not a reuse justified by a compatible seam. Row 11's
 * padding differs too, once it is read rather than remembered.
 *
 * **THE ROWS SIT IN `sessionList` AND THE PANEL DOES NOT.** The mock gives this screen `.cards`
 * (`padding: 0 14px; gap: 8px`), which is `.prows` with different numbers, and §6 is where that
 * difference is already settled -- so the rows take the container the kit ships, as the activity
 * screen's do. Row 12's panel carries its OWN margins (`space_8` top, `space_14` sides) because it
 * is the one block here that sits on the ground rather than in a list. The consequence is visible
 * and is recorded rather than papered over: the panel is inset 2 dp more than the rows above and
 * below it, because `space_14` is what row 12 states and `space_12` is what `.prows` spends.
 *
 * **THE AUDIT LOG IS NOT HERE.** The mock draws one on this screen and its component is row 14, the
 * activity row, which is a second agent's and now exists. Raising a second one here would be the
 * copy of a component §2's reuse rule exists to prevent, so the section arrives when the screen is
 * wired: [below] is where it goes.
 *
 * **NOTHING IN THIS FILE OWNS A CLICK**, which is the division `SettingsPanelView` and
 * `LaunchPanelView` are built on. Revoke deletes this device's push token and then issues a signed
 * command, and PB-SEC-12 clause 1's touch filter belongs on the control -- both are the surface's.
 * Row 13's >=48 dp target is NOT: `denyChip` carries its own floor, because a target is a property
 * of the control's box rather than of the click attached to it. The chip is tagged
 * [MachinesTag.REVOKE] so the surface reaches the one this screen composed instead of building a
 * second one that looks like it.
 */
object MachinesTag {
    /** The root header. `.pnav`, not the drill-down `.navhead`: machines is a tab. */
    const val NAV = "machines.nav"

    /** The machine itself: derivation row 11. */
    const val MACHINE = "machines.machine"

    /** Row 12 as amended -- the state of the daemon-side switch, with no control. */
    const val REMOTE_ACCESS = "machines.remote"

    /** `.seclabel` over the device section. */
    const val SECTION_LABEL = "machines.section.label"

    /** Derivation row 13. */
    const val DEVICE = "machines.device"

    /**
     * The revoke control, tagged because the SURFACE owns its click.
     *
     * It is the phone's only destructive action and the only one it legitimately has: the kill
     * switch is owner-tier and this app can never set it (mobile/screen_coverage.tsv), so the
     * answer to a lost handset is revoking the handset.
     */
    const val REVOKE = "machines.revoke"

    /** The parts whose ON-SCREEN ORDER is the recorded composition. */
    val COMPOSITION: Set<String> = setOf(NAV, MACHINE, REMOTE_ACCESS, SECTION_LABEL, DEVICE)
}

/**
 * The machines panel as a view.
 *
 * @param below views this slice has NOT recomposed, hosted under the panel -- the audit-log
 *  section and the tab bar. Null is the finished shape.
 */
fun machinesPanelView(
    context: Context,
    panel: MachinesPanel,
    below: View? = null,
): View {
    val column = LinearLayout(context).apply {
        orientation = LinearLayout.VERTICAL
        layoutParams = LinearLayout.LayoutParams(MATCH, WRAP)
    }

    // `navHeader`'s second argument is the live counter, and machines has none: the counter is the
    // inbox's in-context liveness readout (derivation §1.4) and it counts sessions in flight,
    // which is not what this screen is about.
    column.addView(navHeader(context, panel.title, null).apply { tag = MachinesTag.NAV })

    column.addView(
        sessionList(context).apply {
            addView(
                machineRow(
                    context = context,
                    machine = panel.machine.name,
                    presence = panel.machine.presenceLine,
                    mark = panel.machine.mark,
                    // Row 11's `endpoint id` cell, or nothing where the panel says the id is
                    // already in the name cell. The decision is the panel's; this carries it.
                    endpoint = panel.machine.endpoint,
                    // THE MODEL'S OWN CALL, carried rather than re-decided here. Null while the
                    // line under the mark still says presence in words, including whose word it
                    // is -- a described dot there would say it twice. Non-null exactly where a
                    // healthy machine has printed nothing (agents-tracker-ksvb.6), which is the
                    // one case left where the dot is the only thing on screen carrying the state.
                    presenceDescription = panel.machine.presenceDescription,
                ).apply { tag = MachinesTag.MACHINE },
            )
        },
    )

    column.addView(
        killSwitchPanel(
            context = context,
            title = panel.remoteAccess.title,
            body = panel.remoteAccess.body,
            command = panel.remoteAccess.command,
        ).apply { tag = MachinesTag.REMOTE_ACCESS },
    )

    column.addView(
        sectionLabel(context, panel.pairedDevicesHeading)
            .apply { tag = MachinesTag.SECTION_LABEL },
    )
    column.addView(
        sessionList(context).apply {
            addView(
                settingsRow(
                    context = context,
                    label = panel.pairedDevice.name,
                    // Row 13's second cell is a fingerprint in `Mono.Agent`, and nothing on this
                    // handset can compute one. `settingsRow`'s sublabel is a different type role
                    // anyway, so there is no cell here to put a substitute in -- see
                    // MachinesPanel, and the description on the control below.
                    sublabel = null,
                    trailing = denyChip(
                        context = context,
                        label = panel.pairedDevice.revokeLabel,
                        description = panel.pairedDevice.revokeDescription,
                    ).apply { tag = MachinesTag.REVOKE },
                ).apply { tag = MachinesTag.DEVICE },
            )
        },
    )

    below?.let { column.addView(it) }
    return column
}

private const val MATCH = ViewGroup.LayoutParams.MATCH_PARENT
private const val WRAP = ViewGroup.LayoutParams.WRAP_CONTENT
