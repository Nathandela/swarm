package dev.swarm.phone.ui.screens

import dev.swarm.phone.ui.MachinePane

/**
 * Phase B slice S25 -- PB-DS-9: the MACHINES screen's model.
 *
 * WHY THERE IS A SECOND MODEL BESIDE [MachinePane]. That one answers what the machine pane IS:
 * that presence is the relay's opinion and never evidence about the machine, that the phone's own
 * freshness has to be rendered beside it (PB-APP-11), and that the kill switch is read-only
 * because `protocol/server.go handleRemoteSetControl` refuses the remote tier before consulting
 * its backend. This answers what the SCREEN says about it -- the title, the words on each row,
 * which of the mock's elements survive, and which state the presence mark reads as reachable.
 * Every one of those is copy or arrangement, and PB-DS-9 assigns both to the screen: the kit
 * "takes data, not views or copy".
 *
 * IT IS A PURE FUNCTION OVER [MachinePane], which is the shape this module already uses for
 * screen logic ([SettingsPanel], [TriageInboxScreen]). No Android import, so it is checkable
 * without a device.
 *
 * ## What the mock draws that this panel does not
 *
 * `docs/research/remote-control-mock.html`'s `renderMachines()` is evidence of intent and not a
 * specification; `docs/design/substrate-components.md` rows 11, 12 and 13 are the specification.
 * The copy below is the mock's, because the derivation document is explicit that the mock stays
 * authoritative for structure, copy and behaviour -- it is the mock's colours and radii that were
 * replaced. Three of its elements do not ship, and each is recorded here rather than left to be
 * rediscovered by the next person to read the drawing:
 *
 * - **The kill switch's TOGGLE.** Row 12 as amended on 2026-08-01: `App.KillSwitchEngaged` is
 *   read-only by design -- `protocol/server.go handleRemoteSetControl` refuses the remote tier
 *   before the backend is consulted -- so a switch here would be a control that cannot act. The
 *   phone's real destructive action is Revoke, one section below, and it is real.
 * - **The device key fingerprint.** Row 13 gives the device row a fingerprint in `Mono.Agent`
 *   (the mock draws `key e7c2…9f31 · this device`), and no facade verb returns one:
 *   `mobile/screen_coverage.tsv` gives this screen `App.Presence`, `App.RevokeThisDevice` and
 *   `App.KillSwitchEngaged`, and [MachinePane] carries a device NAME and nothing else. A hash of
 *   something to hand would render exactly like the real thing, which is ADR-007 B135's defect
 *   class. The half this phone knows for certain survives.
 * - **The machine row's session count.** The mock's meta line is
 *   `N sessions · relay connected · outbound only`, and the machine pane cannot count sessions:
 *   `App.Roster` is the inbox's and attributes a session to a machine by parsing its id, which
 *   says which machines have sessions rather than what this pane is about. What replaces it is the
 *   line PB-APP-11 requires anyway -- presence, qualified by the phone's own freshness.
 *
 * ## And what this slice does not own
 *
 * **The audit log.** The mock draws an `Audit log` section here, `mobile/screen_coverage.tsv`'s
 * `journal.read` row says the same verb serves it, and its component is derivation row 14 -- the
 * activity row, which has its own factory being built beside this slice. A second one raised here
 * would be the copy of a component §2's reuse rule exists to prevent, so the section arrives with
 * the row rather than before it. Nothing in this file reads [MachinePane.activity].
 */
data class MachinesPanel(
    /** Inventory C1.4's tab name, which is this screen's own `.pnav .big`. */
    val title: String,
    /**
     * THE MACHINE, SINGULAR, because the facade has no verb that enumerates machines. `App.Presence`
     * answers for the destination this phone is paired to and pairing is to one machine; the
     * inbox's scope bar reads machine names off session ids, which says which machines have
     * SESSIONS rather than which are paired. A list here would be a screen shaped for an answer
     * nothing can give it, and multi-machine is a Phase C non-goal (requirements §7).
     */
    val machine: MachineRow,
    val remoteAccess: RemoteAccessRow,
    /** The mock's `.seclabel` over the device section, in the mock's own words. */
    val pairedDevicesHeading: String,
    /** Likewise singular: the phone can only ever see, and revoke, itself. */
    val pairedDevice: PairedDeviceRow,
)

/**
 * One machine: derivation row 11.
 *
 * THE `endpoint <id>` SLOT IS NOT HERE. Row 11 gives the row three text cells -- a name, a mono
 * `endpoint id` and a meta line -- and this product has ONE identifier for a machine, not two.
 * `MachinePane.machineId` IS the endpoint id (`machine-endpoint-0001` in the model's own test),
 * and rendering it twice would be a second copy of the name wearing the mock's label rather than
 * a second fact. [InboxRow.agent] is the same cell in the same position and is empty for the same
 * reason: `swarmmobile.Session` carries no agent, so the slot has no source on the wire.
 */
data class MachineRow(
    /** Row 11's `name`, `Title.Row` / `--p-ink`: the machine as this product names it. */
    val name: String,
    /**
     * Row 11's `meta` line -- [MachinePane.presenceExplanation], the pane's OWN sentence.
     *
     * It is not re-worded here. PB-APP-11's whole subject is that the relay answers the presence
     * query and the relay is the declared adversary, so this line is the one place the screen
     * says whose word `online` is; two files deciding that separately is how one of them ends up
     * telling a user their machine is fine.
     */
    val presenceLine: String,
    /**
     * Whether the mark reads as reachable, which is TRUE FOR ONE OF THE RELAY'S THREE WORDS.
     *
     * `App.Presence` returns `unknown`, `offline` or `online` and row 11 gives the dot two
     * colours, so the cheap implementation is "not offline". `unknown` means the relay has no live
     * record -- presence is never persisted, so its own restart produces it -- and painting that
     * as reachable reports the absence of evidence as evidence. Both non-online words take the
     * recessive ink, which is §10's reading of the same token on `completed`: not active.
     */
    val online: Boolean,
)

/**
 * Derivation row 12, as amended: the kill switch's state, and no control.
 *
 * IT CARRIES NO CONTROL STATE AND THAT IS THE TYPE MAKING THE DECISION STICK. There is no
 * `checked` and no `enabled` here, so a later screen cannot put a switch on this row without first
 * adding a field and arguing for it -- which is the point at which someone would have to read
 * `handleRemoteSetControl` and discover that the remote tier is refused before the backend is
 * consulted. Row 12's own amendment says it in the same words: "a toggle here is a control that
 * cannot act". [MachinePane.canSetKillSwitch] is the pane's statement of the same fact, and it is
 * a `val` rather than a parameter for the same reason this row has no field.
 */
data class RemoteAccessRow(val label: String, val sublabel: String)

/**
 * Derivation row 13's Paired-device row: a name, a sublabel, and Revoke.
 *
 * The button's LABEL is here because copy is the screen's (PB-DS-9). Its click is not, and no
 * model field stands in for one: `App.RevokeThisDevice` deletes the push token and then issues a
 * signed command, PB-SEC-12 clause 1's touch filter belongs on the control, and both are the
 * surface's. `MachinesPanelView` tags the button so the surface can reach it.
 */
data class PairedDeviceRow(
    val name: String,
    /** The mock's `key <fingerprint> · this device` with its unsourceable half removed. */
    val sublabel: String,
    val revokeLabel: String,
)

object MachinesPanelScreen {

    /** Inventory C1.4 names the tab `Machines`; the root header carries the same word. */
    private const val TITLE = "Machines"

    /** The mock's `.kills` title, verbatim. Copy is the mock's; row 12 is its appearance. */
    private const val REMOTE_ACCESS = "Remote access"

    private const val PAIRED_DEVICES = "Paired devices"

    /**
     * What is left of §4's `key <fingerprint> · this device` once the half with no source is
     * dropped. It is worth keeping: the Revoke button beside it acts on the phone in the reader's
     * hand, and a destructive control should say what it destroys.
     */
    private const val THIS_DEVICE = "This device"

    /** The mock's `.rev`. Row 13 keeps the word and replaces the button; see the view. */
    private const val REVOKE = "Revoke"

    /**
     * The relay's word for a live authenticated connection (`relay.PresenceOnline`).
     *
     * COMPARED AGAINST, NEVER RENDERED. The line a user reads is the pane's, which carries
     * whatever the relay actually said; this constant only decides which of two inks the 7 dp
     * mark takes.
     */
    private const val ONLINE = "online"

    /**
     * @param pane what [MachinePane] says is true now. Read once, so the panel cannot disagree
     *  with itself between the dot and the sentence under it.
     * @param formatTime an Android formatter carrying the user's locale and time zone. The model
     *  states WHAT to say and never has to be right about a time zone to be testable -- it is
     *  [dev.swarm.phone.ui.MachineFreshness]'s own arrangement, passed straight through.
     */
    fun of(pane: MachinePane, formatTime: (Long) -> String): MachinesPanel = MachinesPanel(
        title = TITLE,
        machine = MachineRow(
            name = pane.machineId,
            presenceLine = pane.presenceExplanation(formatTime),
            online = pane.presence == ONLINE,
        ),
        remoteAccess = RemoteAccessRow(
            label = REMOTE_ACCESS,
            sublabel = pane.killSwitchExplanation,
        ),
        pairedDevicesHeading = PAIRED_DEVICES,
        pairedDevice = PairedDeviceRow(
            name = pane.pairedDeviceName,
            sublabel = THIS_DEVICE,
            revokeLabel = REVOKE,
        ),
    )
}
