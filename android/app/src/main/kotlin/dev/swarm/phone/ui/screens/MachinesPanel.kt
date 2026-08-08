package dev.swarm.phone.ui.screens

import dev.swarm.phone.ui.MachineLabel
import dev.swarm.phone.ui.MachinePane
import dev.swarm.phone.ui.kit.PresenceMark

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
 * THE `endpoint <id>` SLOT IS HERE AS OF agents-tracker-ksvb.1, AND THE REASON IT WAS NOT IS
 * RECORDED RATHER THAN DELETED. This comment used to say row 11's mono cell had no source
 * because "this product has ONE identifier for a machine, not two" -- `MachinePane.machineId` IS
 * the endpoint id, so rendering it twice would have been a second copy of the name wearing the
 * mock's label rather than a second fact. That was true and it stopped being true when the
 * pairing payload's hostname started reaching the phone: there are now two facts about a machine,
 * a NAME a person chose and an ID the software derived, and row 11's two cells are exactly the
 * shape for them.
 *
 * THE OLD REASONING STILL GOVERNS THE UNNAMED CASE, which is why [MachineRow.endpoint] is
 * nullable rather than always the id. A machine that published no name renders its id in the NAME
 * cell -- [MachineLabel.of]'s fallback -- and an endpoint cell beside it would print the same
 * string twice, one of them labelled as though it were something else.
 *
 * THE PARALLEL THIS COMMENT USED TO DRAW IS GONE, and the correction is recorded rather than
 * quietly dropped. It said [InboxRow.agent] was "the same cell in the same position and is empty
 * for the same reason: `swarmmobile.Session` carries no agent, so the slot has no source on the
 * wire". Both halves are false as of 5f45f34: the facade carries `Agent` verbatim from the wire,
 * and the inbox row renders it. So that cell is empty only when the machine reported no agent,
 * which is a fact about one session rather than a gap in the wire. This row's own reason is
 * unaffected -- it never rested on the parallel, only borrowed it.
 */
data class MachineRow(
    /**
     * Row 11's `name`, `Title.Row` / `--p-ink`: the machine as this product names it -- which
     * since agents-tracker-ksvb.1 means the name the MACHINE published, and the endpoint id only
     * where it published none. [MachineLabel.of] makes that choice; this field is its answer.
     */
    val name: String,
    /**
     * Row 11's mono `endpoint id` cell, or null where there is nothing a second cell could add.
     *
     * NULL IS NOT "NO ID". It is "the id is already in the name cell": a machine that published
     * no hostname falls back to its endpoint id for [name], and repeating it here would print one
     * string twice with the second copy labelled as a different fact. `ui/kit/MachineRow.kt`
     * draws no cell at all for null rather than an empty one, which is the same rule its
     * sublabel and the activity row's timestamp already follow.
     */
    val endpoint: String?,
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
     * Which of the relay's THREE words the mark draws, carried rather than collapsed.
     *
     * `App.Presence` returns `unknown`, `offline` or `online`, and the cheap implementation is
     * "not offline" -- which paints a machine nobody can vouch for as reachable. `unknown` means
     * the relay has no live record (presence is never persisted, so its own restart produces it),
     * and reporting the absence of evidence as evidence is the one thing this field must not do.
     *
     * IT WAS A `Boolean` UNTIL ADR-009 D2. The maquette draws `.pdot.unknown` as a hollow ring, so
     * the third word now has a mark of its own; folding it onto `offline` here would put the
     * collapse one layer up from where it was fixed. [PresenceMark] is a closed set, so a `when`
     * over it cannot acquire a default arm to hide the third state in again.
     */
    val mark: PresenceMark,
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
data class RemoteAccessRow(
    /** Row 12's `Title.Row` / `--p-err` cell. The mock's `.kills` title, verbatim. */
    val title: String,
    /**
     * Row 12's subtitle: what the switch is doing, and where it lives.
     *
     * The first sentence is [MachinePane.killSwitchExplanation] unchanged -- the pane's own words
     * about its own state, not re-worded here, for [MachineRow.presenceLine]'s reason. The second
     * is this screen's, and it exists because the first one ends by saying the switch is somebody
     * else's without saying where to find it.
     */
    val body: String,
    /**
     * The part of [body] that is row 12's inline `Mono.InlineStrong` cell: a real verb of the real
     * CLI (`cmd/swarm/remote.go`), and the one that applies to the state the switch is in. The
     * mock prints `swarm remote off` unconditionally, which is the wrong instruction to give
     * somebody whose remote control is already off.
     */
    val command: String,
)

/**
 * Derivation row 13's Paired-device row: a name, and the one destructive action this phone has.
 *
 * **ROW 13'S SECOND CELL IS ABSENT, NOT RE-WORDED.** The row states a `fingerprint` in
 * `Mono.Agent` / `--p-ink3` and the mock prints `key e7c2…9f31 · this device`; nothing on this
 * handset can compute a fingerprint. The tail of that string -- "this device" -- IS a fact, and an
 * earlier draft of this file rendered it as a sublabel, which was wrong twice over: the row's
 * second cell is mono at 10 sp and a sublabel is sans at 12, so it would have been the right words
 * in the wrong role, and it put a caption where the row has no data. The fact moves to
 * [revokeDescription], where it is load-bearing rather than decorative.
 *
 * The button's LABEL is here because copy is the screen's (PB-DS-9). Its click is not, and no model
 * field stands in for one: `App.RevokeThisDevice` deletes the push token and then issues a signed
 * command, PB-SEC-12 clause 1's touch filter belongs on the control, and both are the surface's.
 * `MachinesPanelView` tags the button so the surface can reach it.
 */
data class PairedDeviceRow(
    val name: String,
    val revokeLabel: String,
    /**
     * What a screen reader announces for the control, INSTEAD of its one-word label.
     *
     * `Revoke` alone does not say what it revokes, and this is the only irreversible thing on the
     * screen: it destroys this handset's pairing, and the reply to the command it sends comes back
     * over the path the command destroys.
     */
    val revokeDescription: String,
)

object MachinesPanelScreen {

    /**
     * What the Machines DESTINATION says while this phone cannot build a [MachinesPanel] at all.
     *
     * IT IS PUBLIC AND IT IS THE ONE PIECE OF THIS SCREEN'S COPY THAT CANNOT RIDE THE PANEL, which
     * is why it is a constant rather than a field. Every other string here reaches the view through
     * [of]; this one is read when [of] cannot be called, so there is no instance to carry it. The
     * surface spends it through the kit's `emptyState`, the same component the activity screen
     * shows under a heading with nothing beneath it.
     *
     * WHY IT EXISTS: two of [MachinePane]'s fields have no honest source on this handset today.
     * `presence` is `App.Presence`, a blocking relay round-trip that android/unbound-verbs.tsv
     * forbids calling from a render driven by the event stream; `pairedDeviceName` has no bound
     * accessor at all. Both are agents-tracker-xtj's, and neither may be invented (ADR-007 B135).
     *
     * THE SENTENCE IS BUILT THE WAY [ActivityPanelScreen]'s EMPTY COPY IS, and against the same
     * three failure modes. It says what is true of THIS PHONE -- that it cannot read the details --
     * rather than anything about the machine, because a phone that cannot ask has learned nothing:
     * "no machines" and "your machine is unreachable" are both claims this handset is in no
     * position to make, and the second is the one a user would otherwise infer from a bare empty
     * screen. The clause after the comma is there to refuse that inference explicitly. It does not
     * apologise and it promises no future version: a screen that says "coming soon" is making a
     * commitment the code cannot keep, and one that says "sorry" has told the user nothing.
     */
    const val UNAVAILABLE_COPY =
        "This phone cannot read your machine's details, so nothing here says whether it is " +
            "reachable."

    /** Inventory C1.4 names the tab `Machines`; the root header carries the same word. */
    private const val TITLE = "Machines"

    /** The mock's `.kills` title, verbatim. Copy is the mock's; row 12 is its appearance. */
    private const val REMOTE_ACCESS = "Remote access"

    private const val PAIRED_DEVICES = "Paired devices"

    /** The mock's `.rev`. Row 13 keeps the word and replaces the button; see the view. */
    private const val REVOKE = "Revoke"

    /**
     * The tail of row 13's fingerprint cell -- the half that is a fact -- said where it matters.
     *
     * The phone can revoke exactly one device: itself. `App.RevokeThisDevice` is the verb and
     * `mobile/screen_coverage.tsv` records why it is the phone's panic action rather than the kill
     * switch: the switch is owner-tier and this app can never set it.
     */
    private const val THIS_DEVICE = "this device"

    /**
     * The second sentence of row 12's subtitle, and the two real verbs it ends with.
     *
     * BOTH ARE SHIPPED CLI COMMANDS (`cmd/swarm/remote.go`: `swarm remote off` disables remote
     * control, `swarm remote on` clears the override). The one rendered is the one that applies to
     * the state the switch is IN -- telling a user whose remote control is already off to run
     * `swarm remote off`, which is what the mock's static copy does, is an instruction that does
     * nothing.
     */
    private const val SWITCH_LIVES = "The switch lives on the machine: "
    private const val COMMAND_DISABLE = "swarm remote off"
    private const val COMMAND_ENABLE = "swarm remote on"

    /**
     * The relay's words for a live authenticated connection and for a closed one
     * (`relay.PresenceOnline`, `relay.PresenceOffline`).
     *
     * COMPARED AGAINST, NEVER RENDERED. The line a user reads is the pane's, which carries
     * whatever the relay actually said; these constants only decide which of the maquette's three
     * marks the 7 dp dot takes.
     *
     * ANYTHING ELSE IS `unknown`, INCLUDING THE RELAY'S OWN THIRD WORD, and that is the safe
     * direction rather than a shrug: a word this phone does not recognise is a word it has
     * learned nothing from, which is exactly what `unknown` means. The failure the old
     * `presence == ONLINE` boolean could produce -- an unrecognised word reading as reachable --
     * is impossible here for the same reason.
     */
    private const val ONLINE = "online"
    private const val OFFLINE = "offline"

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
            name = MachineLabel.of(pane.machineName, pane.machineId),
            // THE ID KEEPS ITS OWN CELL, and only while the name cell is saying something else.
            // See [MachineRow.endpoint]: this is a second FACT beside the name, never a second
            // copy of it.
            endpoint = pane.machineId.takeIf { pane.machineName.isNotEmpty() },
            presenceLine = pane.presenceExplanation(formatTime),
            mark = when (pane.presence) {
                ONLINE -> PresenceMark.ONLINE
                OFFLINE -> PresenceMark.OFFLINE
                else -> PresenceMark.UNKNOWN
            },
        ),
        remoteAccess = remoteAccessOf(pane),
        pairedDevicesHeading = PAIRED_DEVICES,
        pairedDevice = PairedDeviceRow(
            name = pane.pairedDeviceName,
            revokeLabel = REVOKE,
            revokeDescription = "$REVOKE ${pane.pairedDeviceName}, $THIS_DEVICE",
        ),
    )

    private fun remoteAccessOf(pane: MachinePane): RemoteAccessRow {
        // The verb that MOVES the switch from where it is: `on` re-enables what the owner turned
        // off, `off` is what turns it off. Read off the same flag the sentence before it is read
        // from, so the two cannot disagree about which state they are describing.
        val command = if (pane.killSwitchEngaged) COMMAND_ENABLE else COMMAND_DISABLE
        return RemoteAccessRow(
            title = REMOTE_ACCESS,
            body = "${pane.killSwitchExplanation} $SWITCH_LIVES$command.",
            command = command,
        )
    }
}
