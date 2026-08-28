package dev.swarm.phone.ui.screens

/**
 * The phone remote-launch flow's SCREEN MODEL (Wave R5, ADR-007 B144(b), playbook 4.3; bead
 * agents-tracker-hggx.6), in the module's established pure-function shape (PairOnlyScreen,
 * TriageInboxScreen, MachinesScreen -- PB-DS-9: logic lives here where the JVM suite can drive
 * it, views take data and copy from it).
 *
 * The phone SELECTS and CONFIRMS a machine-authored preset; it never composes argv, cwd, env,
 * or options -- no such field exists anywhere in this model. LaunchPanel.kt (Phase B S24's
 * free-form agent+cwd form) is NOT this flow and is retired from the REMOTE surface by this
 * wave's decision: R5's phone never supplies an arbitrary filesystem path, so the preset flow
 * is the one remote launch UX.
 */

/** The nameable controls of the launch flow (playbook 4.3). An affordance the model cannot
 * name cannot be asserted on, deep-linked to, or read by a screen reader. */
enum class LaunchAffordance {
    /** New session: offered only when [LaunchPresetScreen.launchAvailabilityFor] says AVAILABLE. */
    NEW_SESSION,

    /** Pick one machine-authored preset row. The phone selects; it never composes. */
    SELECT_PRESET,

    /** The explicit phone confirm ADR-007 D8 retains: one signed session_launch. */
    CONFIRM_LAUNCH,

    /** Backing out of the confirmation sheet is an affordance, not a gesture. */
    CANCEL_LAUNCH,
}

/**
 * Whether NEW_SESSION is offered, and if not, WHY -- every denial is a named state with its
 * own copy, because a missing button with no reason is the D3 defect class.
 */
enum class LaunchAvailability {
    AVAILABLE,

    /** Target machine unreachable: refused phone-side before anything is composed. */
    OFFLINE,

    /** Remote control is off machine-side; the phone can read the state, never set it. */
    KILL_SWITCH_OFF,

    /** read_only / read_approve tier: launch needs the full tier. A named reason, not a missing button. */
    TIER_FORBIDS,

    /** The machine authored zero presets: the remedy (author at the terminal) is its own. */
    NO_PRESETS,

    /**
     * FIRST-RUN (round 2): the machine has not answered a launch_presets reply yet, so the
     * phone knows neither its tier nor the list -- `App.LaunchCapability` is still empty. Its
     * own state because the two readings it replaces are both wrong: TIER_FORBIDS would
     * slander a phone whose only problem is that it has not asked, and AVAILABLE would be a
     * button granted on no fact at all. The remedy is the fetch control the view offers.
     */
    FETCHING,
}

/**
 * The launch verb's outcome states: every stable refusal code and delivery state is a nameable
 * model state with its own sentence -- interpolating wire strings into one notice is how
 * refusals become invisible (D3). PENDING / OUTCOME_UNKNOWN are ADR-017 T9's delivery
 * vocabulary: outcome_unknown is rendered honestly, never as success or failure.
 */
enum class LaunchDeliveryNotice {
    /** Visible success: the machine applied the launch and the session exists. */
    APPLIED,

    /** In flight, unresolved. */
    PENDING,

    /** The machine re-authored the preset between list and confirm; remedy: re-pick. */
    STALE_PRESET,

    /** The machine never authored this id; remedy: re-list. */
    UNKNOWN_PRESET,

    /** Remote control is off machine-side. */
    KILL_SWITCH,

    /** Wrong key, revoked pairing, or a read_only/read_approve tier. */
    NOT_AUTHORIZED,

    /** Target unreachable: refused phone-side before anything was composed. */
    OFFLINE,

    /** Died mid-flight and the machine could not prove the outcome. */
    OUTCOME_UNKNOWN,

    /**
     * A refusal code this screen has no named state for (round 2; e.g. `policy`: a forbidden
     * preset option, a root outside the allowed set). Still a VISIBLE refusal -- the machine's
     * own words ride [LaunchPresetScreen.noticeFor]'s detail slot -- never silence and never
     * an invented success (the codes are the machine's and this side must not claim to know
     * the whole set, CommandVerdict's own rule).
     */
    REFUSED,
}

/**
 * One selectable preset row. Every field is a wire fact from the machine's launch_presets
 * reply; the row carries the REVISION it displayed because that is what the confirm signs --
 * the phone echoes, it never derives.
 */
data class PresetRowModel(
    val id: String,
    val displayName: String,
    val provider: String,
    val workspacePath: String,
    val revision: String,
    /** The preset's worktree-isolation default (round 2): the confirm sheet renders it as a
     * behavior sentence, never an implicit default. Defaulted so the round-1 suite's rows --
     * which predate the field -- keep constructing. */
    val worktree: Boolean = false,
)

/**
 * The preset flow AS DRAWN (round 2): what one redraw of the launch section needs, carried as a
 * value so the surface's redraw guard can compare (the [LaunchPanel]/MachinesPanel idiom --
 * re-parenting live controls per journal event takes the keyboard away).
 */
data class LaunchPresetPanel(
    val availability: LaunchAvailability,
    /** [LaunchPresetScreen.noticeFor] of [availability]: "" exactly when AVAILABLE. */
    val availabilityNotice: String,
    /** [LaunchPresetScreen.commandFor] of [availability]: the well under the notice, or "". */
    val availabilityCommand: String = "",
    val rows: List<PresetRowModel>,
    /** The confirm verb's resolved delivery/refusal sentence, "" while none is claimable. */
    val deliveryNotice: String,
    /**
     * The FETCH PRESETS verb's own resolved refusal sentence ([LaunchPresetScreen.fetchNoticeFor]),
     * "" while none is claimable (round 4, review MEDIUM 3).
     *
     * IT IS A SECOND SLOT AND THAT IS THE WHOLE FIX. Round 3 gave the fetch verb its own COPY but
     * left both verbs writing one line, with the launch write unconditional while a launch was in
     * flight -- so for the entire pending window of any launch, every fetch refusal was replaced
     * by "The launch is on its way to the machine and has not resolved yet." Two verbs, two
     * answers, two slots: neither verb's sentence can now be a function of the other's state.
     * Defaulted so suites that predate the field keep constructing.
     */
    val fetchNotice: String = "",
)

/**
 * The confirmation sheet's model: exactly the five facts playbook 4.3 puts on the sheet --
 * machine, provider, resolved workspace display path, worktree behavior, initial-prompt
 * presence -- plus the echoed preset revision the signed confirm carries. Nothing else is
 * editable: no cwd field, no env field, no argv field EXISTS in the model.
 */
data class LaunchConfirmationModel(
    val machineName: String,
    val provider: String,
    val workspacePath: String,
    val worktreeBehavior: String,
    val hasInitialPrompt: Boolean,
    val presetRevision: String,
)

/** The launch flow's pure logic: the availability resolver, the confirm sheet, the notices. */
object LaunchPresetScreen {

    /** Every control the launch flow offers, nameable by the model. */
    val affordances: List<LaunchAffordance> = LaunchAffordance.entries

    /** The section heading the launch flow sits under (the NEW_SESSION affordance's area). */
    const val HEADING = "New session"

    /** The control that asks the machine for its current preset list (and this phone's tier). */
    const val FETCH_LABEL = "Fetch presets"

    /** The confirm sheet's CONFIRM_LAUNCH control. */
    const val CONFIRM_LABEL = "Start session"

    /** The confirm sheet's CANCEL_LAUNCH control: an affordance, not a gesture. */
    const val CANCEL_LABEL = "Cancel"

    /** The one free-text field the phone may contribute (playbook 4.3). */
    const val PROMPT_HINT = "First instruction (optional)"

    /**
     * The availability RESOLVER for NEW_SESSION (playbook 4.3: "available only on a selected,
     * online machine whose paired phone has the `full` authorization tier and whose remote
     * kill switch is on" -- and, first-run, whose machine authored at least one preset).
     *
     * Precedence: OFFLINE wins over every other denial (the user's first problem is
     * reachability, and the refusal happens phone-side before anything is composed), then the
     * kill switch, then the tier, then the empty-preset setup state. An unknown tier string is
     * refused as TIER_FORBIDS: fail closed, never a button granted by a typo.
     */
    fun launchAvailabilityFor(
        online: Boolean,
        tier: String,
        killSwitchOn: Boolean,
        presetCount: Int,
    ): LaunchAvailability = when {
        !online -> LaunchAvailability.OFFLINE
        !killSwitchOn -> LaunchAvailability.KILL_SWITCH_OFF
        // An EMPTY tier is the first-run state -- the machine has not stamped
        // device_capability on any launch_presets reply this phone adopted yet -- and it
        // still offers no button (fail closed). A NON-EMPTY unrecognised tier is a typo and
        // fails closed as TIER_FORBIDS: never a button granted by a typo.
        tier.isEmpty() -> LaunchAvailability.FETCHING
        tier != "full" -> LaunchAvailability.TIER_FORBIDS
        presetCount <= 0 -> LaunchAvailability.NO_PRESETS
        else -> LaunchAvailability.AVAILABLE
    }

    /**
     * The confirmation sheet for one selected preset: the five playbook facts plus the echoed
     * revision. Worktree behavior is a RENDERED fact, never an implicit default -- the user
     * confirms what will actually happen to their working tree.
     */
    fun confirmationFor(
        preset: PresetRowModel,
        machineName: String,
        worktreeIsolation: Boolean,
        promptPresent: Boolean,
    ): LaunchConfirmationModel = LaunchConfirmationModel(
        machineName = machineName,
        provider = preset.provider,
        workspacePath = preset.workspacePath,
        worktreeBehavior = if (worktreeIsolation) {
            "Runs in an isolated worktree of this workspace"
        } else {
            "Runs directly in this workspace"
        },
        hasInitialPrompt = promptPresent,
        presetRevision = preset.revision,
    )

    /** Copy for an availability denial. A denial whose remedy is a command names it in [commandFor]. */
    fun noticeFor(state: LaunchAvailability): String = when (state) {
        LaunchAvailability.AVAILABLE -> ""
        LaunchAvailability.OFFLINE -> "Your computer is offline."
        LaunchAvailability.KILL_SWITCH_OFF -> "Remote control is off on your computer."
        LaunchAvailability.TIER_FORBIDS -> "This phone can't start sessions. Pair again with full access."
        LaunchAvailability.NO_PRESETS -> "No presets yet. On your computer:"
        LaunchAvailability.FETCHING -> "Tap Fetch to see presets."
    }

    /**
     * The command a denial points at, or "" where there is none. It is the well's text and never
     * part of the sentence (phone refit W5.1): a person retypes it into a shell off a phone screen.
     */
    fun commandFor(state: LaunchAvailability): String = when (state) {
        LaunchAvailability.KILL_SWITCH_OFF -> "swarm remote on"
        LaunchAvailability.NO_PRESETS -> "swarm remote presets add"
        else -> ""
    }

    /**
     * Copy for one delivery/refusal state: visible success AND visible refusal for the one
     * verb, each stable refusal code with its own sentence (shared copy collapses distinct
     * remedies). OUTCOME_UNKNOWN deliberately claims neither success nor failure: the machine
     * could not prove the outcome and the screen must not invent one. A non-empty [detail]
     * (the machine's own words) is appended after the state's sentence.
     */
    fun noticeFor(state: LaunchDeliveryNotice, detail: String = ""): String {
        val copy = when (state) {
            LaunchDeliveryNotice.APPLIED -> "Started."
            LaunchDeliveryNotice.PENDING -> "Starting…"
            LaunchDeliveryNotice.STALE_PRESET -> "That setup changed. Check it and confirm again."
            LaunchDeliveryNotice.UNKNOWN_PRESET -> "That setup is gone. Pick another."
            LaunchDeliveryNotice.KILL_SWITCH -> "Remote control is off on your computer."
            LaunchDeliveryNotice.NOT_AUTHORIZED -> "This phone can't start sessions."
            LaunchDeliveryNotice.OFFLINE -> "Your computer is offline."
            // ROUND 2 (review BLOCKER 2): this sentence promised "re-sends the same operation
            // and can never create a second session" -- a guarantee the code does not make.
            // App.SessionLaunch mints a FRESH operation id on every call, so a re-confirm is a
            // genuinely new operation and can duplicate; the honest copy warns instead of
            // promising, and sends the user to the session list FIRST.
            LaunchDeliveryNotice.OUTCOME_UNKNOWN -> "Not sure it started. Check the Inbox before trying again."
            LaunchDeliveryNotice.REFUSED -> "Couldn't start."
        }
        return if (detail.isEmpty()) copy else "$copy ($detail)"
    }

    /**
     * Copy for the FETCH PRESETS verb's refusal states (round 3, D3 class): the fetch is a
     * READ, so its refusal must say the preset list could not be fetched -- never the launch
     * verb's sentence about a verb the user did not press ("not authorized to launch sessions"
     * for a refused fetch prescribes the wrong fact and the wrong remedy). Same named states,
     * the fetch verb's own words; a non-empty [detail] (the machine's) is appended.
     */
    @Suppress("UNUSED_PARAMETER")
    fun fetchNoticeFor(state: LaunchDeliveryNotice, detail: String = ""): String {
        // ONE SENTENCE FOR EVERY STATE (phone refit W5.4): the fetch is a read, its refusal says
        // the presets could not be loaded, and the machine's own words in the detail say which
        // refusal this was.
        val copy = "Couldn't load presets."
        return if (detail.isEmpty()) copy else "$copy ($detail)"
    }

    /**
     * The wire outcome code of one claimed operation, resolved to its named notice state
     * (round 2) -- the mapping the composition uses, kept in the model where the JVM suite
     * drives it (PB-DS-9). The codes are wire literals held here for CommandVerdict's /
     * SwarmErrorTokens' stated reason: the unit-test JVM does not load the gomobile AAR, so
     * this side cannot read the Go constants; each literal names the constant it pins.
     *
     * An empty code is PENDING (no reply claimed yet -- `mobile/app.go Outcome` answers an
     * empty Outcome until one lands); the reply op `session_launch` is the applied success
     * (`outcomeOf` falls back to `Control.Op` when `ErrorCode` is empty); every recognised
     * refusal code gets its own state; anything else is REFUSED with the machine's words in
     * the detail slot -- never silence, never an invented success.
     */
    fun noticeStateFor(code: String): LaunchDeliveryNotice = when (code) {
        "" -> LaunchDeliveryNotice.PENDING
        "session_launch" -> LaunchDeliveryNotice.APPLIED // protocol.OpSessionLaunch, the success reply op
        "stale_preset" -> LaunchDeliveryNotice.STALE_PRESET // schema.CodeStalePreset
        "unknown_preset" -> LaunchDeliveryNotice.UNKNOWN_PRESET // schema.CodeUnknownPreset
        "kill_switch" -> LaunchDeliveryNotice.KILL_SWITCH // schema.CodeKillSwitch
        "not_authorized" -> LaunchDeliveryNotice.NOT_AUTHORIZED // schema.CodeNotAuthorized
        // ROUND 4 (review MAJOR 2): the machine answers a launch it CANNOT DECIDE -- the signed
        // operation is in flight under another driver and may yet apply or roll back -- with this
        // stable code (schema.CodeOutcomeUnknown). Falling through to REFUSED told the user the
        // machine turned the launch away; the state that says neither has existed here since
        // round 1 with nothing reaching it.
        "outcome_unknown" -> LaunchDeliveryNotice.OUTCOME_UNKNOWN // schema.CodeOutcomeUnknown
        else -> LaunchDeliveryNotice.REFUSED
    }
}
