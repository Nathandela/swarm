package dev.swarm.phone.ui.screens

import android.content.Context
import android.view.View
import android.view.ViewGroup
import android.widget.LinearLayout
import dev.swarm.phone.ui.kit.CtaKind
import dev.swarm.phone.ui.kit.ctaButton
import dev.swarm.phone.ui.kit.navHeader
import dev.swarm.phone.ui.kit.presenceDot
import dev.swarm.phone.ui.kit.sectionLabel
import dev.swarm.phone.ui.kit.settingsRow

/**
 * Phase B slice S25 -- PB-DS-6 and PB-DS-9: the machines screen, composed from the component kit.
 *
 * WHAT IT COMPOSES. Substrate's artifact draws no machines screen at all, so
 * `docs/design/substrate-components.md` rows 11, 12 and 13 are the whole specification. This file
 * spends four existing components and one new one -- the root `navHeader`, `settingsRow` for all
 * three rows, `sectionLabel` over the device section, `ctaButton(kind = DENY)` for Revoke, and
 * `presenceDot` for row 11's 7 dp mark -- and decides nothing about how any of them looks;
 * `android/gate/s24_screens_test.go` fences this package to exactly that, so an `R.color`, an
 * `R.dimen`, an `R.style`, a `setTextAppearance`, a `setPadding` or a `background =` here fails
 * the build.
 *
 * **WHY THE MACHINE ROW IS A `settingsRow`.** Row 11 states `--p-card`, 1 dp `--p-hair`,
 * `--p-card-r`, `padding space_12 x space_14`, a name in `Title.Row` / `--p-ink` and a meta line
 * in `Body.Secondary` / `--p-ink2`. Row 15's settings row states the same four values and the same
 * two type roles, and `settingsRow` already ships them -- so this is §2's reuse rule applied where
 * it lands exactly, not an approximation of a row that could not be built. Two residuals, recorded
 * because they are visible: row 11 puts `space_4` between the name and the meta line and
 * `settingsRow` stacks them with none, and row 11's mono `endpoint id` has no slot here because it
 * has no source (see [MachinesPanel]). The mark rides the trailing edge, which is where the row
 * has a slot; row 11 states its size, its two colours and its flatness and does not state a side.
 *
 * **AND WHY IT IS `presenceDot` AND NOT `statusDot`.** `sessionRow` builds its own leading mark by
 * calling `statusDot(context, group)`, and `Kit.groupColour` fails loudly on anything that is not
 * one of the four Groups -- so reaching for the triage row here would mean passing a Group the
 * server never sent, and the two that would render row 11's colours (`ready_for_review` for
 * online, `completed` for offline) are precisely the fabrication `group-tokens.tsv`'s gate exists
 * to refuse. Presence is `App.Presence`, the relay's opinion, and it is not a Group.
 *
 * **WHERE THIS DEVIATES FROM ROW 12, DELIBERATELY.** Row 12 draws the remote-access state as a
 * charged container: no fill, a 1 dp `color-mix(--p-err 36%, --p-hair)` border, and its title in
 * `--p-err`. Two reasons it ships as an ordinary `settingsRow` instead. First, that border is a
 * derived colour and `internal/design.Derivations()` declares five, none of them this one --
 * PB-TOK-7 forbids typing what it resolves to, so the container cannot be built at all until that
 * table grows a row, which is not this slice's to add. Second, row 12's own 2026-08-01 amendment
 * deletes its control on the grounds that the kill switch is read-only; a danger border and an
 * error-coloured title over a line the user cannot act on advertise the affordance that amendment
 * just removed. The state is reported, in the mock's own words, with nothing beside it.
 *
 * **THE AUDIT LOG IS NOT HERE.** The mock draws one on this screen and its component is row 14,
 * the activity row, which has a factory of its own being built beside this slice. A second one
 * raised here would be the copy of a component §2's reuse rule exists to prevent, so the section
 * arrives with the row: [below] is where it goes.
 *
 * **NOTHING IN THIS FILE OWNS A CLICK**, which is the same division `SettingsPanelView` and
 * `LaunchPanelView` are built on. Revoke deletes this device's push token and then issues a signed
 * command, and PB-SEC-12 clause 1's touch filter belongs on the control -- both are the surface's.
 * The button is tagged [MachinesTag.REVOKE] so the surface can reach the one this screen composed
 * instead of building a second one that looks like it.
 */
object MachinesTag {
    /** The root header. `.pnav`, not the drill-down `.navhead`: machines is a tab. */
    const val NAV = "machines.nav"

    /** The machine itself: derivation row 11. */
    const val MACHINE = "machines.machine"

    /** Row 12 as amended -- a statement of the daemon-side switch, with no control. */
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
        settingsRow(
            context = context,
            label = panel.machine.name,
            sublabel = panel.machine.presenceLine,
            // No description: the line directly under the mark already says what it says, in
            // words, including whose word it is. A described dot would have the row read its
            // presence twice.
            trailing = presenceDot(context, online = panel.machine.online),
        ).apply { tag = MachinesTag.MACHINE },
    )

    column.addView(
        settingsRow(
            context = context,
            label = panel.remoteAccess.label,
            sublabel = panel.remoteAccess.sublabel,
            // Row 12 as amended: no trailing control, and this is a security property rather
            // than a styling decision. `App.KillSwitchEngaged` is read-only -- protocol/server.go
            // handleRemoteSetControl refuses the remote tier before consulting its backend, on the
            // stated grounds that a remote device must never re-enable a switch its owner turned
            // off -- so a toggle here would be a control that cannot act.
            trailing = null,
        ).apply { tag = MachinesTag.REMOTE_ACCESS },
    )

    column.addView(
        sectionLabel(context, panel.pairedDevicesHeading)
            .apply { tag = MachinesTag.SECTION_LABEL },
    )
    column.addView(
        settingsRow(
            context = context,
            label = panel.pairedDevice.name,
            sublabel = panel.pairedDevice.sublabel,
            trailing = revokeControl(context, panel.pairedDevice),
        ).apply { tag = MachinesTag.DEVICE },
    )

    below?.let { column.addView(it) }
    return column
}

/**
 * Row 13's Revoke: "the `.a2-no` treatment", which is `ctaButton` at [CtaKind.DENY].
 *
 * ROW 13 ASKS FOR IT AT CHIP METRICS -- `--p-chip-r` 8, padding `space_8` x `space_10`,
 * `Label.Chip` -- and what ships is `.a2-no` at its own button metrics: radius 9, padding
 * `space_12`, `Label.Button`. The fill and the ink, which are what make a control read as
 * destructive, are exactly row 13's. Closing the rest means a chip-metrics variant of `ctaButton`,
 * which is a change to a component this slice does not own, and §2's reuse rule is explicit that a
 * second denial control must not exist -- so the button is the shipped one.
 *
 * IT IS RE-LAID-OUT AND THAT IS LAYOUT RATHER THAN APPEARANCE. `ctaButton` sizes itself
 * MATCH_PARENT, which is right for the full-width sheet CTA it was written for and wrong inside a
 * horizontal row: LinearLayout measures an unweighted MATCH_PARENT child against the whole row and
 * leaves the weighted text column zero, so the device's name would vanish and the row would be a
 * button. Nothing about the button's look is touched here -- the fill, the ink, the radius and the
 * type are the kit's, and a screen may not name any of them.
 */
private fun revokeControl(context: Context, device: PairedDeviceRow): View =
    ctaButton(context, device.revokeLabel, CtaKind.DENY).apply {
        tag = MachinesTag.REVOKE
        layoutParams = LinearLayout.LayoutParams(WRAP, WRAP)
    }

private const val MATCH = ViewGroup.LayoutParams.MATCH_PARENT
private const val WRAP = ViewGroup.LayoutParams.WRAP_CONTENT
