package dev.swarm.phone.ui.screens

import dev.swarm.phone.ui.PushCategory
import dev.swarm.phone.ui.PushToggle
import dev.swarm.phone.ui.SettingsScreen

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
     * agents-tracker-64rf's pairing entry point, or null on a phone with no machine.
     *
     * IT IS A FIELD OF ITS OWN RATHER THAN A ROW SQUEEZED INTO A [SettingsSection], because that
     * type's `rows` is `List<SettingsRow>` -- a list of push toggles -- and a [PairedMachineRow] is
     * not one. Reshaping the section to hold both would change what every existing reader of
     * `sections` sees, for a reason that has nothing to do with what they read it for.
     */
    val machineSection: MachineSection? = null,
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
        get() = listOfNotNull(machineSection?.heading) + sections.map { it.heading }
}

/** One `.seclabel` and the rows under it. */
data class SettingsSection(val heading: String, val rows: List<SettingsRow>)

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
     */
    fun of(settings: SettingsScreen, machine: String? = null): SettingsPanel = SettingsPanel(
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
        machineSection = machine?.let {
            MachineSection(heading = PAIRING, row = PairedMachineRowScreen.of(it))
        },
        permissionRedirectLabel = settings.notificationRedirectLabel,
        deliveryRedirectLabel = settings.deliveryRedirectLabel,
    )

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
            enabled = !settings.togglesDisabled,
            description = "$label. $sublabel",
        )
    }
}
