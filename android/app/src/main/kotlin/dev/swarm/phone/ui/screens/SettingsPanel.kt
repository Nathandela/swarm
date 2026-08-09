package dev.swarm.phone.ui.screens

import dev.swarm.phone.ui.ClockBanner
import dev.swarm.phone.ui.MachineFreshness
import dev.swarm.phone.ui.MachineLabel
import dev.swarm.phone.ui.MachinePane
import dev.swarm.phone.ui.PushCategory
import dev.swarm.phone.ui.PushToggle
import dev.swarm.phone.ui.SettingsScreen
import dev.swarm.phone.ui.StreamBadge
import dev.swarm.phone.ui.StreamView
import dev.swarm.phone.ui.kit.PresenceMark

/**
 * Phase B slice S24 -- PB-DS-9: the SETTINGS screen's model.
 *
 * WHY THERE IS A SECOND MODEL BESIDE [SettingsScreen]. That one answers what a push preference
 * IS: what survives a process death, what an unacknowledged change means, what a withheld
 * POST_NOTIFICATIONS makes true. This answers what the SCREEN says about it -- the section
 * heading, the label and the sublabel on each row, which preference a row is bound to, and what
 * a screen reader hears for a row that is one touch target. Every one of those is copy or
 * arrangement, and PB-DS-9 assigns both to the screen: the kit "takes data, not views or copy".
 *
 * IT IS A PURE FUNCTION OVER [SettingsScreen], which is the shape this module already uses for
 * screen logic ([TriageInboxScreen], `PermissionStateResolver`). No Android import, so it is
 * checkable without a device.
 *
 * ## Three rows inventory C6 draws and this panel does not
 *
 * The recorded settings screen has five rows. Two of them are the two this product has, and the
 * other three are recorded here rather than shipped, because a control wired to nothing is worse
 * than a gap -- it looks finished.
 *
 * - **`Quiet hours` / `23:00 - 07:30`.** `swarmmobile.PushPreference` carries `Alerts` and
 *   `Mentions` and no schedule, and the machine has never heard of one. A third switch would be
 *   a preference with no field, no wire form and no effect.
 * - **`Require Face ID to approve` / `Biometric gate on every approval sheet`.** VOID. ADR-007
 *   B133 removed phone-side user authentication on the grounds that the trust boundary is the
 *   wire; `docs/design/substrate-components.md` §8.8 flags this exact row as post-dating its own
 *   deletion. [SettingsScreen] already deleted the field it would have driven.
 * - **`End-to-end encryption` / `Noise XX - relay sees ciphertext only`, status `active`.** The
 *   claim is true of the transport by construction, and that is precisely why it cannot be
 *   rendered as a live status: nothing on this handset READS it, so "active" would be a word
 *   printed unconditionally beside a screen whose whole subject is what the machine has actually
 *   confirmed. It needs a fact on the wire before it can be a row.
 */
data class SettingsPanel(
    /** Inventory C6.1. The settings tab's own `.pnav .big`. */
    val title: String,
    val sections: List<SettingsSection>,
    /**
     * What the panel says beside the switches, in the order a reader needs it: why they are dead
     * first, then what has not landed yet. Empty when there is nothing to report -- a blank line
     * of body copy under two switches reads as a warning nobody wrote.
     *
     * THE STRINGS ARE [SettingsScreen]'S OWN and are not re-worded here. Two files deciding what
     * a user reads about the same condition is how they drift.
     */
    val notices: List<String>,
    /**
     * agents-tracker-u7sl: ADR-007 B143's battery-saver/Doze push-delay disclosure.
     *
     * A SEPARATE FIELD FROM [notices] AND NOT A THIRD ENTRY IN IT. `notices` is filtered to what
     * a fault produced -- read [notices]'s own KDoc -- and this is not one: battery saver and
     * Doze delaying a push is the product working as B16 designed it, not a malfunction, so it
     * renders every time this section does rather than only when a switch is blocked.
     *
     * THE STRING IS [SettingsScreen]'S OWN, for [notices]'s reason: two files deciding what a
     * user reads about the same fact is how they drift.
     */
    val disclosure: String,
    /**
     * agents-tracker-64rf's pairing entry point, or null on a phone with no machine.
     *
     * IT IS A FIELD OF ITS OWN RATHER THAN A ROW SQUEEZED INTO A [SettingsSection], because that
     * type's `rows` is `List<SettingsRow>` -- a list of push toggles -- and a [PairedMachineRow] is
     * not one. Reshaping the section to hold both would change what every existing reader of
     * `sections` sees, for a reason that has nothing to do with what they read it for.
     */
    val machineSection: MachineSection? = null,
    /**
     * agents-tracker-nx44.3: the CONNECTION section, or null on a phone that cannot read its own
     * link (and on every caller written before this section existed).
     *
     * IT IS WHAT IS LEFT OF THE MACHINES DESTINATION. That tab drew four "The X view has a gap"
     * cards and a sentence saying this phone could not read its machine's details; field test 3
     * (2026-08-09) is the record of an owner reading it and asking what the page was for. The two
     * questions it was actually there to answer -- which computer am I attached to, and is what I
     * am looking at current -- are one row and one line, so they are a SECTION rather than a
     * screen, and they sit here because [machineSection] directly above already answers the first
     * half in the durable sense (which machine is this phone PINNED to) and this answers it in the
     * live one.
     *
     * A FIELD OF ITS OWN, for [machineSection]'s reason exactly: [SettingsSection.rows] is
     * `List<SettingsRow>` -- a list of push toggles -- and neither a machine row nor a fault line
     * is one.
     */
    val connection: ConnectionSection? = null,
    /**
     * agents-tracker-0dij: the words on the control that leads out of a permanently blocked
     * notification permission, or null on every other state.
     *
     * IT IS A LABEL AND NOT A CONTROL, for the reason [SettingsRow]'s toggle arrives as a parameter:
     * pressing it leaves the app, so the Intent, PB-SEC-12 clause 1's touch filter and an identity
     * that survives a redraw are the surface's. The words are [SettingsScreen]'s own and are not
     * re-spelled here.
     */
    val permissionRedirectLabel: String? = null,
    /**
     * agents-tracker-2yfn: the words on the control that leads to the WAKE CHANNEL's own page, or
     * null wherever that is not where the problem is.
     *
     * IT IS A SECOND FIELD AND NOT A SECOND VALUE FOR [permissionRedirectLabel], because the two
     * lead to two different system screens and the destination is fixed inside a listener installed
     * at construction -- so one control cannot be both, and a single label would leave the panel
     * unable to say which one it is asking for. What is shared is the reason both are labels rather
     * than controls: pressing either leaves the app, so the Intent, PB-SEC-12 clause 1's touch
     * filter and an identity that survives a redraw are the surface's.
     */
    val deliveryRedirectLabel: String? = null,
) {
    /**
     * The section headings top to bottom, which is where "the pairing section leads" is a fact
     * something can read rather than an accident of two fields being declared in a given order.
     *
     * "Which computer am I attached to" is the question this screen exists to answer for an owner
     * who could not find the pairing entry point at all, so it leads and the preferences follow.
     */
    val sectionHeadingsInOrder: List<String>
        get() = listOfNotNull(machineSection?.heading, connection?.heading) +
            sections.map { it.heading }
}

/** One `.seclabel` and the rows under it. */
data class SettingsSection(val heading: String, val rows: List<SettingsRow>)

/**
 * agents-tracker-nx44.3: the machine this phone is attached to, live.
 *
 * TWO OF ITS THREE FIELDS ARE SILENT WHEN THERE IS NOTHING WRONG, which is this app's standing
 * rule for a fault report ("online is the only quiet state", `ConnectionBanner` since S16) and the
 * thing the deleted destination got wrong: four cards that rendered on every visit, three of them
 * usually saying a channel was fine, taught the reader to skip the fourth.
 *
 * THE UNCONDITIONAL READOUT STILL EXISTS AND IS NOT HERE. `SyncDetail` draws every repair channel
 * including the healthy ones, because it is opened deliberately -- so "all four are fine" stays
 * distinguishable from "this screen forgot the reply channel", which is the argument `LinkPanel`
 * made for its four rows and which now belongs to the sheet that inherited them.
 */
data class ConnectionSection(
    /** The `.seclabel` over the row. */
    val heading: String,
    /** Derivation row 11's machine row: who this phone is talking to, and whether it is reachable. */
    val machine: MachineRow,
    /**
     * ONE line naming the repair channels with holes in them, or empty while every one is current.
     *
     * IT NAMES THEM AND DOES NOT DESCRIBE THEM. Which of the two unhealthy states a channel is in
     * -- an idle hole or one being repaired -- is [dev.swarm.phone.ui.StreamView.notice]'s
     * sentence, and the sync detail sheet prints it per channel. A section that reproduced that
     * here would be the four cards again with the borders taken off.
     */
    val health: String,
    /**
     * PB-TIME-1's verdict, verbatim from the daemon, or empty while the clock is in budget.
     *
     * IT IS HERE BECAUSE IT WAS DRAWN BY `linkPanelView` AND BY NOTHING ELSE. `ClockBanner` and
     * `StreamView` were fully modelled, unit-tested and reached no pixel until agents-tracker-ah2
     * built a section for them; deleting that section without carrying the verdict would put the
     * clock straight back into that state. It belongs with the link rather than with the machine:
     * a skewed clock gets a command refused as "not authorized", which sends a user to pair again
     * when the fix is their own clock.
     */
    val clockNotice: String,
)

/**
 * One machine: derivation row 11.
 *
 * MOVED HERE FROM `MachinesPanel.kt` BY agents-tracker-nx44.3, unchanged except for this
 * paragraph. That file was the Machines destination's model and the destination is deleted; this
 * row is the one part of it that answered a question a person asked, so it moves to the section
 * that asks it rather than being deleted and rewritten.
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
 * cell -- [dev.swarm.phone.ui.MachineLabel.of]'s fallback -- and an endpoint cell beside it would
 * print the same string twice, one of them labelled as though it were something else.
 */
data class MachineRow(
    /**
     * Row 11's `name`, `Title.Row` / `--p-ink`: the machine as this product names it -- which
     * since agents-tracker-ksvb.1 means the name the MACHINE published, and the endpoint id only
     * where it published none. [dev.swarm.phone.ui.MachineLabel.of] makes that choice; this field
     * is its answer.
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
     * Row 11's `meta` line -- [dev.swarm.phone.ui.MachinePane.explanationOf], the pane's OWN
     * sentence.
     *
     * It is not re-worded here. PB-APP-11's whole subject is that the relay answers the presence
     * query and the relay is the declared adversary, so this line is the one place the screen
     * says whose word `online` is; two files deciding that separately is how one of them ends up
     * telling a user their machine is fine.
     *
     * EMPTY FOR A HEALTHY MACHINE (agents-tracker-ksvb.6), which is when [presenceDescription]
     * stops being null.
     */
    val presenceLine: String,
    /**
     * What the presence dot announces, or null where [presenceLine] already says it in words.
     *
     * A HEALTHY MACHINE PRINTS NO LINE, so the dot -- otherwise decorative -- is the only thing
     * left on screen carrying the state, and [dev.swarm.phone.ui.MachinePane.announcementOf] is
     * what it reads out. Non-null exactly where [presenceLine] is empty: a described dot beside a
     * sentence that already says the same thing would have a screen reader announce presence
     * twice.
     */
    val presenceDescription: String?,
    /**
     * Which of the relay's THREE words the mark draws, carried rather than collapsed.
     *
     * `App.MachinePresence` returns `unknown`, `offline` or `online`, and the cheap implementation
     * is "not offline" -- which paints a machine nobody can vouch for as reachable. `unknown`
     * means the relay has no live record, or that this phone has lost the link and can no longer
     * ask (`presenceCache.forget`), and reporting the absence of evidence as evidence is the one
     * thing this field must not do.
     *
     * IT WAS A `Boolean` UNTIL ADR-009 D2. The maquette draws `.pdot.unknown` as a hollow ring, so
     * the third word now has a mark of its own; folding it onto `offline` here would put the
     * collapse one layer up from where it was fixed. [PresenceMark] is a closed set, so a `when`
     * over it cannot acquire a default arm to hide the third state in again.
     */
    val mark: PresenceMark,
)

/**
 * The paired machine's `.seclabel` and the one row under it.
 *
 * ITS HEADING IS THE WORD THIS APP ALREADY USES -- `PairingSurface`, `PairingFlow`,
 * `PairingPanel`, and the `Pair a computer` step title -- so an owner who came from that flow
 * meets the same word again rather than a new one invented for this heading alone.
 */
data class MachineSection(val heading: String, val row: PairedMachineRow)

/**
 * One settings row: derivation table row 15.
 *
 * `label` is `Title.Row` / `--p-ink`, `sublabel` is `Body.Secondary` / `--p-ink2`, and the
 * trailing control is the toggle of row 4. NEITHER COMPONENT EXISTS IN THE KIT, which is why
 * this file carries the row's data and not its appearance.
 */
data class SettingsRow(
    /** The preference this row drives. The mapping is a bijection; see [SettingsScreen]. */
    val toggle: PushToggle,
    val label: String,
    val sublabel: String,
    val checked: Boolean,
    val enabled: Boolean,
    /**
     * What a screen reader announces for the row.
     *
     * Row 15 makes the WHOLE ROW one >=48 dp target when it carries a toggle, so the row is one
     * accessibility node and the sublabel is inside it. Without this a screen reader user hears
     * `Needs your decision` and never `Approvals and blocked prompts`, which is the half that
     * says what the switch actually governs.
     */
    val description: String,
)

object SettingsPanelScreen {

    /**
     * Inventory C6.2's recorded copy: `Needs your decision` and `Task done`.
     *
     * IT IS NOT THE COPY THIS SCREEN SHIPPED. `SettingsSurface` wrote
     * "Tell me when an agent is waiting on me" and "Tell me when an agent has finished" -- longer,
     * first-person, and invented at the call site. The artifact's is shorter and it is paired with
     * a sublabel that carries the detail, which is the structure row 15 specifies.
     */
    private val ROW_LABELS: Map<PushToggle, String> = mapOf(
        PushToggle.FIRST to "Needs your decision",
        PushToggle.SECOND to "Task done",
    )

    /** The second line of each row, verbatim from C6.2. */
    private val ROW_SUBLABELS: Map<PushToggle, String> = mapOf(
        PushToggle.FIRST to "Approvals and blocked prompts",
        PushToggle.SECOND to "Completions and failures",
    )

    /**
     * The rows, in the artifact's order.
     *
     * DECLARED RATHER THAN TAKEN FROM `PushToggle.values()`, so the screen's order is the
     * artifact's rather than an enum's declaration order -- two facts that agree today and have
     * no reason to keep agreeing.
     */
    private val ROW_ORDER: List<PushToggle> = listOf(PushToggle.FIRST, PushToggle.SECOND)

    /** Inventory C6.1. */
    private const val TITLE = "Settings"

    /** Inventory C6.2's `.seclabel`. */
    private const val NOTIFICATIONS = "Notifications"

    /** The paired machine's `.seclabel`. See [MachineSection]. */
    private const val PAIRING = "Pairing"

    /**
     * The live link's `.seclabel` (agents-tracker-nx44.3).
     *
     * THE TRANSPORT'S WORD, DELIBERATELY. `LinkPanelScreen` headed the deleted section `Link` and
     * argued for it: "Connection" was the transport's word and that section was about what had
     * ARRIVED, per repair channel, which is a different fact. This section is both -- a machine
     * and whether frames from it are current -- and it sits under `Pairing` on a screen a person
     * opens asking about their computer. `Connection` is the word they arrive with.
     */
    private const val CONNECTION = "Connection"

    /**
     * The relay's words for a live authenticated connection and for a closed one
     * (`relay.PresenceOnline`, `relay.PresenceOffline`).
     *
     * COMPARED AGAINST, NEVER RENDERED. The line a user reads carries whatever the relay actually
     * said; these constants only decide which of the maquette's three marks the 7 dp dot takes.
     *
     * ANYTHING ELSE IS `unknown`, INCLUDING THE RELAY'S OWN THIRD WORD, and that is the safe
     * direction rather than a shrug: a word this phone does not recognise is a word it has learned
     * nothing from, which is exactly what `unknown` means. The failure the old `presence == ONLINE`
     * boolean could produce -- an unrecognised word reading as reachable -- is impossible here for
     * the same reason.
     */
    private const val ONLINE = "online"
    private const val OFFLINE = "offline"

    fun labelFor(toggle: PushToggle): String = checkNotNull(ROW_LABELS[toggle]) {
        "PB-DS-9: no settings label for $toggle. A switch with no words beside it is a control " +
            "nobody can identify, so this fails loudly rather than rendering a blank row."
    }

    fun sublabelFor(toggle: PushToggle): String = checkNotNull(ROW_SUBLABELS[toggle]) {
        "PB-DS-9: no settings sublabel for $toggle."
    }

    /**
     * @param settings what [SettingsScreen] says is true now. Read once, so the panel cannot
     *  disagree with itself between two rows.
     * @param machine the machine this phone is pinned to, or null when it is pinned to none. The
     *  two absences are different facts and are not collapsed: NULL is "there is no pairing to
     *  show a row about", and EMPTY is "there is one and this phone cannot read its name", which
     *  [PairedMachineRowScreen] renders as `Paired`. Defaulting to null is what lets every call
     *  site written before this row existed keep meaning what it meant.
     * @param connection [connectionOf]'s answer, or null on a phone that cannot read its own link
     *  -- which is the same null every call site written before agents-tracker-nx44.3 passes. It
     *  is COMPOSED rather than built here so that the facts it needs (the relay's presence, the
     *  phone's freshness, four stream verdicts and the clock) are read once, at the seam that can
     *  read them, instead of this function growing six parameters it only forwards.
     */
    fun of(
        settings: SettingsScreen,
        machine: String? = null,
        connection: ConnectionSection? = null,
    ): SettingsPanel = SettingsPanel(
        title = TITLE,
        sections = listOf(
            SettingsSection(
                heading = NOTIFICATIONS,
                rows = ROW_ORDER.map { toggle -> rowFor(settings, toggle) },
            ),
        ),
        // BLOCKED FIRST. It is the reason nothing will happen; the pending notice is about a
        // change that will not take effect until it is fixed, so read in the other order the
        // user is told what has been saved before being told it is inert.
        //
        // THE DELIVERY NOTICE IS BLOCKED-FIRST TOO AND SITS BESIDE THE PERMISSION'S
        // (agents-tracker-2yfn). The two cannot both be non-empty -- a permission notice suppresses
        // the delivery one, see `SettingsScreen.blockedDelivery` -- so this is one slot filled from
        // whichever of the two facts is the live one, rather than two paragraphs about one fault.
        notices = listOf(
            settings.notificationsBlockedNotice,
            settings.deliveryBlockedNotice,
            settings.pendingNotice,
        ).filter { it.isNotEmpty() },
        disclosure = settings.pushDelayDisclosure,
        machineSection = machine?.let {
            MachineSection(heading = PAIRING, row = PairedMachineRowScreen.of(it))
        },
        connection = connection,
        permissionRedirectLabel = settings.notificationRedirectLabel,
        deliveryRedirectLabel = settings.deliveryRedirectLabel,
    )

    /**
     * The CONNECTION section (agents-tracker-nx44.3).
     *
     * IT IS A PURE FUNCTION OVER FACTS THE ADAPTER CAN READ WITHOUT A ROUND TRIP, and that is what
     * makes it callable from a draw at all. `App.MachinePresence` is a cached O(1) read fed by the
     * relay goroutine on its own 15 s cadence -- NOT `App.Presence`, which is the blocking
     * round-trip android/unbound-verbs.tsv bars a render from -- and `App.MachineFreshness`,
     * `App.StreamState`, `App.ResyncPending` and `App.ClockVerdict` all read local state.
     *
     * @param machineId the endpoint id this phone is pinned to -- the machine's identity, and the
     *  fallback for its name.
     * @param machineName `App.MachineName`: the hostname the machine published, or empty where it
     *  published none. WHICH CELL EACH ENDS UP IN IS DECIDED HERE and not at the call site, so the
     *  one rule -- [dev.swarm.phone.ui.MachineLabel.of], and an endpoint cell only where it is a
     *  SECOND fact -- is in a function a JVM can check.
     * @param presence `App.MachinePresence`'s state, verbatim. It is the RELAY's opinion and never
     *  evidence about the machine, which is why [freshness] is a required parameter here rather
     *  than an option a caller may drop.
     * @param freshness `App.MachineFreshness` -- the phone's OWN evidence, the one thing a relay
     *  that answers every poll while withholding every frame cannot fake.
     * @param streams `FacadeBridge.streamViews()`, in `FacadeBridge.REPAIR_CHANNELS` order. The
     *  order is not decided here.
     * @param clock `FacadeBridge.clockBanner()` -- PB-TIME-1's verdict, pulled per draw and never
     *  latched, because a screen that opened after the measurement was never sent the event.
     * @param formatTime an Android formatter carrying the user's locale and time zone, passed
     *  through to the pane's own sentence so this stays checkable without one.
     */
    fun connectionOf(
        machineId: String,
        machineName: String,
        presence: String,
        freshness: MachineFreshness,
        streams: List<StreamView>,
        clock: ClockBanner,
        formatTime: (Long) -> String,
    ): ConnectionSection {
        // READ ONCE AND BRANCHED ON ONCE. A formatter is not guaranteed pure, so a second call
        // could in principle answer differently -- and a row whose line and description disagreed
        // about whether the machine is healthy is exactly the drift PB-APP-11 refuses.
        val line = MachinePane.explanationOf(presence, freshness, formatTime)
        return ConnectionSection(
            heading = CONNECTION,
            machine = MachineRow(
                name = MachineLabel.of(machineName, machineId),
                // THE ID KEEPS ITS OWN CELL, and only while the name cell is saying something
                // else. See [MachineRow.endpoint]: this is a second FACT beside the name, never a
                // second copy of it.
                endpoint = machineId.takeIf { machineName.isNotEmpty() },
                presenceLine = line,
                presenceDescription = if (line.isEmpty()) {
                    MachinePane.announcementOf(presence)
                } else {
                    null
                },
                mark = when (presence) {
                    ONLINE -> PresenceMark.ONLINE
                    OFFLINE -> PresenceMark.OFFLINE
                    else -> PresenceMark.UNKNOWN
                },
            ),
            health = healthOf(streams.filter { it.badge != StreamBadge.LIVE }.map { it.stream }),
            // [ClockBanner.visible] AND NOT `text.isNotEmpty()`. They agree today, and only one of
            // them is the model's verdict: `of` decides that a blank verdict is a HEALTHY clock,
            // and a screen re-deriving that from the string would be a second opinion able to
            // disagree.
            clockNotice = if (clock.visible) clock.text else "",
        )
    }

    /**
     * The one health line, over the channels [names] says have holes.
     *
     * THE CHANNEL NAMES ARE THE WIRE'S OWN WORDS. `journal`, `terminal`, `reply` and `grant` are
     * `internal/phonecore`'s strings and a table turning them into English would have to invent a
     * phrase for a fifth channel it had never seen -- which is `ChannelRow.stream`'s rule,
     * inherited rather than re-decided. `android/gate/pbapp8_repairchannels_test.go` set-compares
     * the four this app asks about against the four the core repairs.
     *
     * A REPAIR IN FLIGHT IS STILL A GAP, which is why the caller filters on `badge != LIVE` rather
     * than on `STALE`. `App.Resync` marks the request and the mark clears when the repair LANDS,
     * so a channel being repaired is a channel with a hole in it that is being worked on --
     * reporting it as current is PB-SYNC-3's optimistic clear.
     */
    private fun healthOf(names: List<String>): String = when (names.size) {
        0 -> ""
        1 -> "The ${names.single()} view has a gap."
        else -> "The ${names.dropLast(1).joinToString(", ")} and ${names.last()} views have gaps."
    }

    private fun rowFor(settings: SettingsScreen, toggle: PushToggle): SettingsRow {
        val label = labelFor(toggle)
        val sublabel = sublabelFor(toggle)
        return SettingsRow(
            toggle = toggle,
            label = label,
            sublabel = sublabel,
            // THE ROW READS THE PREFERENCE THROUGH THE CATEGORY, never through the toggle's
            // position. `toggleCategory` is the bijection [SettingsScreen] argues for, and going
            // round it here would be a second, silent mapping -- the exact defect that leaves one
            // category unreachable with nothing on screen to say so.
            checked = when (settings.toggleCategory(toggle)) {
                PushCategory.NEEDS_INPUT -> settings.alerts
                PushCategory.FINISHED -> settings.mentions
            },
            // AND IT ASKS ONE QUESTION RATHER THAN LISTING THE REASONS (agents-tracker-ix2v).
            // This read `!settings.togglesDisabled`, which is the PERMANENT block alone, so both
            // rows stayed live while a push_prefs was unanswered and a second flip could be
            // issued over the top of the first -- `SettingsSurface` watches one operation id, so
            // the first answer then becomes unclaimable and a refusal of the second reverts to a
            // snapshot taken before either, while Go has durably persisted both writes.
            // [SettingsScreen.togglesAcceptTaps] is where the two reasons are held apart.
            enabled = settings.togglesAcceptTaps,
            description = "$label. $sublabel",
        )
    }
}
