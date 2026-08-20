package dev.swarm.phone.ui

import dev.swarm.phone.keys.GoCustodyFailure

/**
 * Phase B slice S16 -- PB-APP-9's taxonomy, on the side of the boundary that has to render it.
 *
 * The authorities:
 *   - PB-APP-9 (errors reach the user), PB-APP-10 (revoked and grant-loss states)
 *   - mobile/error_taxonomy.tsv, which is the join between the Go `ErrClass*` set and the
 *     [ErrorState] constants below and is checked as SET EQUALITY in both directions by
 *     android/gate/s16_ui_test.go
 *
 * WHY A TOKEN AND NOT A TYPE. gomobile leaves nothing of a Go error at the JNI boundary but
 * its MESSAGE, so the facade stamps the class onto the message as a prefix and this side reads
 * it back out. dev.swarm.phone.keys.GoCustodyFailure established the arrangement for the two
 * custody verdicts in S14; PB-APP-9 generalises it to every class the facade can produce.
 */

/**
 * The class tokens the facade stamps. Verbatim from mobile/error_taxonomy.tsv.
 *
 * THE CUSTODY VERDICT IS NOT RE-SPELLED HERE. It is already owned by [GoCustodyFailure], and
 * mobile/s14_custody_test.go checks that literal against the Go constant in the direction that
 * matters -- Go is authoritative, Kotlin is checked against it. A third copy of a discriminator
 * string is a third thing to get wrong, and the failure is silent: an unrecognised token falls
 * through to [ErrorState.UNKNOWN], which for a PERMANENT invalidation means the user is told to
 * try again forever (PB-KEY-6's recorded defect).
 *
 * The other fourteen are literals for the reason that one is: the unit-test JVM does not load
 * the AAR, so the constants cannot be read from it.
 */
object SwarmErrorTokens {
    const val UNKNOWN: String = "swarm/unknown"
    const val INTERNAL: String = "swarm/internal"
    const val INVALID_REQUEST: String = "swarm/invalid-request"
    const val NOT_FOUND: String = "swarm/not-found"
    const val APP_CLOSED: String = "swarm/closed"
    const val OFFLINE: String = "swarm/offline"
    const val NOT_PAIRED: String = "swarm/not-paired"
    const val STATE_CORRUPT: String = "swarm/state-corrupt"
    const val DEVICE_UNSUPPORTED: String = "swarm/device-unsupported"
    const val SYNCING: String = "swarm/unreconciled"
    const val AWAITING_KEY: String = "swarm/awaiting-key"
    const val GRANT_LOST: String = "swarm/grant-lost"
    const val REPAIR_REQUIRED: String = GoCustodyFailure.KEY_INVALIDATED_TOKEN
    const val REVOKED: String = "swarm/revoked"
    const val NEEDS_LEASE: String = "swarm/no-lease"

    /**
     * Wave R6 (IS-LIFE-5): the daemon refused a composer send because the conversation moved
     * on between the phone rendering the turn and the tap landing. An ordinary race with a
     * mild remedy -- re-read and send again, the draft retained -- never a bug report.
     */
    const val STALE_TURN: String = "swarm/stale-turn"
    const val RATE_LIMITED: String = "swarm/rate-limited"
    const val PAIRING_FAILED: String = "swarm/pairing-failed"

    /**
     * The three ways a pairing ENTRY is wrong, split off [PAIRING_FAILED] (agents-tracker-ksvb.5).
     *
     * `mobile/pairing.go` has authored a specific sentence for each of them since
     * agents-tracker-3fkm and all three were stamped `swarm/pairing-failed`, whose row says "The
     * pairing call itself failed. Start the pairing again from your machine's code." None of the
     * three ever reached a call, so that row's advice is wrong for all of them -- and the acts
     * that fix them differ by exactly the fact the reader already has: the code in front of them,
     * no relay address at all, or one typed in the wrong shape.
     */
    const val PAIRING_CODE_INVALID: String = "swarm/pairing-code"
    const val RELAY_UNKNOWN: String = "swarm/relay-unknown"
    const val RELAY_ADDRESS_INVALID: String = "swarm/relay-address"
}

/**
 * The state a failure is RENDERED as. Set-equal to the `rendered_state` column of
 * mobile/error_taxonomy.tsv, enforced in both directions: a class with no state has nowhere to
 * be shown, and a state no class produces is a dead branch whose message a later reader trusts
 * because it is there.
 *
 * The enum carries no data on purpose -- the remedy and the wording live in [ErrorRouter]'s one
 * table, so there is a single place a row can be got wrong.
 */
enum class ErrorState {
    UNKNOWN,
    INTERNAL,
    INVALID_REQUEST,
    NOT_FOUND,
    APP_CLOSED,
    OFFLINE,
    NOT_PAIRED,
    STATE_CORRUPT,
    DEVICE_UNSUPPORTED,
    SYNCING,
    AWAITING_KEY,
    GRANT_LOST,
    REPAIR_REQUIRED,
    REVOKED,
    NEEDS_LEASE,
    STALE_TURN,
    RATE_LIMITED,
    PAIRING_FAILED,

    /**
     * The three pairing-ENTRY states (agents-tracker-ksvb.5). They are three and not one for
     * [SwarmErrorTokens.PAIRING_CODE_INVALID]'s reason: two states that read identically are one
     * state, and the reader's next move differs between all three.
     */
    PAIRING_CODE_INVALID,
    RELAY_UNKNOWN,
    RELAY_ADDRESS_INVALID,
}

/**
 * What the user -- or the MACHINE -- has to do about it.
 *
 * PB-APP-10 NARROWS FROM THREE REMEDIES TO TWO (ADR-007 B133). [AUTHENTICATE] is gone with its
 * subject: there is no local authentication on this handset, so a remedy telling the user to
 * perform one would be advice that cannot be carried out. The two that remain must still never
 * collapse into each other -- [RE_PAIR] is permanent but the user CAN carry it out, while
 * [MACHINE_REGRANT] is the one they cannot perform from the handset at all, and sending a
 * grant-loss user to the pairing flow is a BRICK rather than a wording mistake: BeginPairing
 * fail-fasts while this device is still registered (PB-STATE-10), so the advice cannot be
 * followed and the only exit is physical access to the machine.
 *
 * [FIX_CLOCK] is not in the taxonomy: PB-TIME-1's verdict is a separate pull surface, not an
 * error class. It is here because it is the remedy a user is shown, and it must not be
 * confused with [RE_PAIR] -- the daemon's refusal of a skewed command reads "not authorized".
 */
enum class Remedy {
    NONE,
    REPORT_BUG,
    REFRESH,
    RESTART_APP,
    WAIT_FOR_CONNECTION,
    PAIR,
    WAIT_FOR_MACHINE,
    MACHINE_REGRANT,
    RE_PAIR,

    /**
     * PB-STATE-10's owner-side recovery, and it is its OWN remedy because it is TWO acts and
     * neither alone works: the user clears the app's data, and the OWNER unregisters this
     * device at the machine. [RE_PAIR] alone is the brick -- `swarm remote pair` is refused
     * while the device is still registered (single-device v1), so the advice cannot be
     * carried out and the only exit is physical access to the machine.
     *
     * It is deliberately NOT one of [RoutedError.offersPairing]'s two: there is no App to
     * offer a pairing flow from -- the durable blob failed Resume, which is how the user got
     * here -- and offering one before the machine side is done leads to the same refusal.
     */
    CLEAR_DATA_AND_RE_PAIR,
    TAKE_CONTROL,
    WAIT_AND_RETRY,
    RETRY_PAIRING,
    FIX_CLOCK,
    ;

    /**
     * Whether carrying this remedy out MEANS opening the pairing flow (agents-tracker-agre).
     *
     * IT LIVES ON THE REMEDY AND NOT ON ITS TWO HOLDERS, because both hold one. [RoutedError] asked
     * it of itself and [ConnectionBanner] could not ask it at all, so the transport's remedy -- the
     * one a still-paired handset in `RELAY_UNTRUSTED` gets -- had no way to become a control
     * without a second copy of this comparison. Two copies of a two-row predicate is two rows to
     * get wrong, and the failure is silent in the direction that matters: a screen offering the
     * pairing flow for [MACHINE_REGRANT] sends the user to a `BeginPairing` that fail-fasts while
     * this device is still registered (PB-STATE-10).
     *
     * [CLEAR_DATA_AND_RE_PAIR] IS STILL NOT ONE OF THEM, for the reason its own KDoc gives.
     */
    val offersPairing: Boolean get() = this == PAIR || this == RE_PAIR

    /**
     * Whether carrying this remedy out MEANS pressing the take-control this app already has.
     *
     * It is one row rather than two, and it is named anyway for the reason above: the caller that
     * needs it is [PressFeedback], which sees a refusal and must decide whether the screen owes the
     * user a control. A bare `== TAKE_CONTROL` at that seam is a routing decision written where
     * nothing reviews it against this table.
     */
    val offersTakeControl: Boolean get() = this == TAKE_CONTROL
}

/**
 * One routed failure: the state to render, what to tell the user, and what they can do.
 *
 * [offersPairing] is DERIVED from the remedy rather than stored, so a screen cannot end up
 * offering the pairing flow for a remedy that is not one. That is the property PB-APP-10
 * needs: the grant-loss screen's remedy is [Remedy.MACHINE_REGRANT], so there is no row to get
 * wrong -- it cannot offer a flow that would fail-fast the moment the user pressed it.
 */
data class RoutedError(
    val state: ErrorState,
    val remedy: Remedy,
    val message: String,
) {
    val offersPairing: Boolean get() = remedy.offersPairing

    /**
     * Whether this failure's remedy is the take-control press (agents-tracker-agre).
     *
     * WHY IT IS ASKED OF THE FAILURE AND NOT OF THE SCREEN. `swarm/no-lease` is what the MACHINE
     * answers a keystroke it will not carry, and it is newer information than anything the screen
     * knows: `ControlLease` records that a lease which lapsed at its horizon "still reads as
     * confirmed", because the horizon does not ride the take_control's outcome. So the screen goes
     * on showing Stop, the user presses it, and the same refusal comes back. The refusal itself is
     * the only thing on the phone that knows the lease is gone.
     */
    val offersTakeControl: Boolean get() = remedy.offersTakeControl
}

/**
 * The classifier the screens route on.
 *
 * MATCHING IS BY OUTERMOST TOKEN, NOT BY DECLARATION ORDER. The class rides the message as a
 * PREFIX ("swarm/grant-lost: swarmmobile: ..."), and errors nest -- a wrapped failure carries
 * the inner class further along the same string. The outer stamp is the one the facade decided
 * the call failed with, so the earliest index wins. A scan in table order would answer with
 * whichever row happened to be written first, which is a routing decision made by an ordering
 * nobody reviews.
 */
object ErrorRouter {

    private val byToken: Map<String, RoutedError> = mapOf(
        SwarmErrorTokens.UNKNOWN to RoutedError(
            ErrorState.UNKNOWN, Remedy.NONE,
            "Something failed in a way the app does not recognise. Try again, and report it if " +
                "it keeps happening.",
        ),
        SwarmErrorTokens.INTERNAL to RoutedError(
            ErrorState.INTERNAL, Remedy.REPORT_BUG,
            "The app hit an internal fault. Nothing you did caused it; please report it.",
        ),
        SwarmErrorTokens.INVALID_REQUEST to RoutedError(
            ErrorState.INVALID_REQUEST, Remedy.REPORT_BUG,
            "This screen asked for something the phone core cannot serve. The app is otherwise " +
                "healthy; please report it.",
        ),
        SwarmErrorTokens.NOT_FOUND to RoutedError(
            ErrorState.NOT_FOUND, Remedy.REFRESH,
            "That is no longer there. Refresh to see what your machine has now.",
        ),
        SwarmErrorTokens.APP_CLOSED to RoutedError(
            ErrorState.APP_CLOSED, Remedy.RESTART_APP,
            "The app was shut down while this screen was open. Reopen it to carry on.",
        ),
        SwarmErrorTokens.OFFLINE to RoutedError(
            ErrorState.OFFLINE, Remedy.WAIT_FOR_CONNECTION,
            "No link to your machine right now. This resumes on its own once the connection is " +
                "back.",
        ),
        SwarmErrorTokens.NOT_PAIRED to RoutedError(
            ErrorState.NOT_PAIRED, Remedy.PAIR,
            "This phone is not paired with a machine yet. Nothing is broken -- pair it to begin.",
        ),
        SwarmErrorTokens.STATE_CORRUPT to RoutedError(
            ErrorState.STATE_CORRUPT, Remedy.CLEAR_DATA_AND_RE_PAIR,
            "This phone's saved state cannot be read, so it has stopped rather than guess. " +
                "Clear this app's data, then on your machine run `swarm remote devices` to " +
                "find this device and `swarm remote revoke <device-id>` to unregister it -- " +
                "`swarm remote pair` is refused until you do -- and pair again.",
        ),
        SwarmErrorTokens.DEVICE_UNSUPPORTED to RoutedError(
            ErrorState.DEVICE_UNSUPPORTED, Remedy.REPORT_BUG,
            "This handset cannot protect keys the way this app requires. Nothing you do fixes " +
                "it and pairing again would land here; please report it.",
        ),
        SwarmErrorTokens.SYNCING to RoutedError(
            ErrorState.SYNCING, Remedy.WAIT_FOR_MACHINE,
            "Waiting for your machine to publish its current state. Reading works throughout; " +
                "changes are held until it does.",
        ),
        SwarmErrorTokens.AWAITING_KEY to RoutedError(
            ErrorState.AWAITING_KEY, Remedy.WAIT_FOR_MACHINE,
            "Waiting for your machine to send this phone its content key. This is the ordinary " +
                "first-launch wait.",
        ),
        SwarmErrorTokens.GRANT_LOST to RoutedError(
            ErrorState.GRANT_LOST, Remedy.MACHINE_REGRANT,
            "This phone's key grant is gone, and only the machine can issue a new one. Go to " +
                "your machine and grant this device access again; pairing again cannot work " +
                "while the machine still has this device registered.",
        ),
        SwarmErrorTokens.REPAIR_REQUIRED to RoutedError(
            ErrorState.REPAIR_REQUIRED, Remedy.RE_PAIR,
            "This phone's key was destroyed and no authentication brings it back. Pair this " +
                "device again.",
        ),
        SwarmErrorTokens.REVOKED to RoutedError(
            ErrorState.REVOKED, Remedy.RE_PAIR,
            "The owner removed this device. Clear its registration on the machine first, then " +
                "pair again.",
        ),
        SwarmErrorTokens.NEEDS_LEASE to RoutedError(
            ErrorState.NEEDS_LEASE, Remedy.TAKE_CONTROL,
            "Your machine has not given this phone control of that session. Take control to " +
                "type or to stop it; retrying is the one thing that cannot help.",
        ),
        SwarmErrorTokens.STALE_TURN to RoutedError(
            ErrorState.STALE_TURN, Remedy.REFRESH,
            // Gentle by design (Mirror M2.4): the conversation moving on is ordinary, not an
            // error, and the copy never claims the message went anywhere. The composer keeps
            // the draft (ComposerModel.noticeFor's retainsDraft), so the remedy is one act.
            "The conversation moved on before your message landed. Nothing was typed into the " +
                "session; your draft is kept -- read the latest turn and press send again.",
        ),
        SwarmErrorTokens.RATE_LIMITED to RoutedError(
            ErrorState.RATE_LIMITED, Remedy.WAIT_AND_RETRY,
            "Too many requests in a short window. Wait a moment before trying again.",
        ),
        SwarmErrorTokens.PAIRING_FAILED to RoutedError(
            ErrorState.PAIRING_FAILED, Remedy.RETRY_PAIRING,
            "The pairing call itself failed. Start the pairing again from your machine's code.",
        ),
        // THE THREE ENTRY ROWS. They share PAIRING_FAILED's remedy and none of its words, and
        // that is the split rather than a redundancy: RETRY_PAIRING is the act of trying again
        // WHERE THE READER IS STANDING -- it is not Remedy.PAIR, so no screen offers to open a
        // flow they are already inside -- while the sentence has to name which of the four
        // things went wrong, because the four are fixed in four different places.
        SwarmErrorTokens.PAIRING_CODE_INVALID to RoutedError(
            ErrorState.PAIRING_CODE_INVALID, Remedy.RETRY_PAIRING,
            "That code does not look right. It is ten characters from your machine's screen -- " +
                "check for a typo and try again.",
        ),
        SwarmErrorTokens.RELAY_UNKNOWN to RoutedError(
            ErrorState.RELAY_UNKNOWN, Remedy.RETRY_PAIRING,
            "This phone does not know your relay address yet. Scan the QR once, or paste the " +
                "full code your machine printed.",
        ),
        SwarmErrorTokens.RELAY_ADDRESS_INVALID to RoutedError(
            ErrorState.RELAY_ADDRESS_INVALID, Remedy.RETRY_PAIRING,
            "That is not a relay address. It looks like wss://host:port -- your machine printed " +
                "the whole thing.",
        ),
    )

    /**
     * Route a message thrown out of any bound facade verb.
     *
     * A message carrying no token at all answers [ErrorState.UNKNOWN], which is the reserved
     * row and NOT a plausible-looking neighbour. An `else -> OFFLINE` branch reads as tidy and
     * turns every future error class into a network problem the user is told to wait out.
     */
    fun route(message: String): RoutedError {
        var outermost: RoutedError? = null
        var outermostAt = Int.MAX_VALUE
        for ((token, routed) in byToken) {
            val at = message.indexOf(token)
            if (at in 0 until outermostAt) {
                outermostAt = at
                outermost = routed
            }
        }
        return outermost ?: checkNotNull(byToken[SwarmErrorTokens.UNKNOWN])
    }

    /**
     * The DAEMON's refusal codes, which are a second vocabulary (Wave R6 review round 2).
     *
     * [route] matches the FACADE's `swarm/...` tokens inside a thrown message. A machine
     * refusal does not arrive that way: it arrives as `OperationOutcome.code`, in the daemon's
     * own `schema.ErrorCode` spelling. Nothing translated between them, so `ErrorState.
     * STALE_TURN` -- the one state Mirror M2.4 wrote gentle copy for -- had no producer at all:
     * the composer could only reach it from a facade-local error that never carries that class.
     *
     * THE TABLE IS SMALL AND ITS DEFAULT IS [ErrorState.UNKNOWN] ON PURPOSE, which is [route]'s
     * own reserved-row rule rather than a second decision: a code this build has never seen is
     * not a network problem, and a plausible-looking neighbour would tell the user to wait out
     * a fault waiting cannot fix. What keeps an unrecognised refusal legible is not this table
     * but the machine's own words, which every caller renders verbatim in the detail cell
     * beside the sentence (agents-tracker-ksvb.10's split).
     */
    fun routeMachineCode(code: String): RoutedError {
        val unknown = checkNotNull(byToken[SwarmErrorTokens.UNKNOWN])
        val token = MachineRefusalCodes.toToken[code] ?: return unknown
        return byToken[token] ?: unknown
    }
}

/**
 * The daemon-side refusal codes this app translates, and nothing else it may.
 *
 * They are spelled here as literals for [SwarmErrorTokens]' reason exactly: the unit-test JVM
 * does not load the AAR, so nothing can be read out of it. The authority is
 * `internal/protocol/schema`; a code missing from this map is not a bug in the map, it is a
 * code with no phone-side remedy, and [ErrorRouter.routeMachineCode] answers UNKNOWN for it.
 */
object MachineRefusalCodes {

    /** `schema.CodeStaleTurn`: the conversation moved on between the render and the tap. */
    const val STALE_TURN: String = "stale_turn"

    /** `schema.CodeRateLimit`: the one refusal waiting actually fixes. */
    const val RATE_LIMIT: String = "rate_limit"

    /**
     * `schema.CodeUnavailable`: the machine no longer holds what was asked for. On the M3 reads
     * that means IS-CAP-3's answer for an evicted body -- and it is TERMINAL for that card.
     *
     * NAMED HERE, DELIBERATELY NOT IN [toToken] (Wave R6 review round 3, finding F4). It is a fact
     * about ONE verb, so it is the screen's own sentence plus the machine's words -- see
     * `SessionDetailScreen.detailReadNoticeFor`. It is spelled here rather than at the screen so
     * the two places that must agree on the literal (the sentence and the withdrawal of the offer)
     * read the same constant.
     */
    const val UNAVAILABLE: String = "unavailable"

    /**
     * `schema.CodeInvalidField`: the machine cannot look this id up at all. Also terminal for a
     * card, also deliberately absent from [toToken] -- see [UNAVAILABLE].
     */
    const val INVALID_FIELD: String = "invalid_field"

    /**
     * The map, and it is TWO ROWS rather than a translation of the whole daemon vocabulary.
     *
     * `policy`, `structured_unsupported`, `interrupt_unsupported`, `unavailable`,
     * `invalid_field`, `already_applied` and the rest are deliberately absent: each of them is a
     * fact about ONE verb, and the honest rendering of one is the screen's own sentence for that
     * verb plus the machine's words verbatim -- which is what `SessionDetailScreen` and
     * `ApprovalSheetScreen` already do. Routing them through this table would replace a
     * verb-specific sentence with a generic remedy, which reads as advice and is not.
     *
     * THE RULE HAS A COST WHEN A CALLER FORGETS IT (round 3, finding F4). The two M3 reads DID
     * hand their code to [ErrorRouter.routeMachineCode], so `unavailable` and `invalid_field`
     * -- absent from this map on purpose -- fell to `ErrorState.UNKNOWN` and a user tapping a
     * clipped card whose body the daemon had evicted read "Something failed in a way the app does
     * not recognise. Try again", advice for a retry that can never work. Absence from this map is
     * an instruction to the SCREEN, not a licence to route anyway.
     */
    internal val toToken: Map<String, String> = mapOf(
        STALE_TURN to SwarmErrorTokens.STALE_TURN,
        RATE_LIMIT to SwarmErrorTokens.RATE_LIMITED,
    )
}
