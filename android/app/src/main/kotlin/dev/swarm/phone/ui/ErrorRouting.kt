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
 * THE TWO CUSTODY VERDICTS ARE NOT RE-SPELLED HERE. They are already owned by
 * [GoCustodyFailure], and mobile/s14_custody_test.go checks those literals against the Go
 * constants in the direction that matters -- Go is authoritative, Kotlin is checked against
 * it. A third copy of a discriminator string is a third thing to get wrong, and the failure is
 * silent: an unrecognised token falls through to [ErrorState.UNKNOWN], which for the PERMANENT
 * invalidation means the user is offered a prompt they can never satisfy (PB-KEY-6's recorded
 * defect).
 *
 * The other fourteen are literals for the reason the two custody ones are: the unit-test JVM
 * does not load the AAR, so the constants cannot be read from it.
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
    const val REAUTH_REQUIRED: String = GoCustodyFailure.AUTH_REQUIRED_TOKEN
    const val REPAIR_REQUIRED: String = GoCustodyFailure.KEY_INVALIDATED_TOKEN
    const val REVOKED: String = "swarm/revoked"
    const val NEEDS_LEASE: String = "swarm/no-lease"
    const val RATE_LIMITED: String = "swarm/rate-limited"
    const val PAIRING_FAILED: String = "swarm/pairing-failed"
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
    REAUTH_REQUIRED,
    REPAIR_REQUIRED,
    REVOKED,
    NEEDS_LEASE,
    RATE_LIMITED,
    PAIRING_FAILED,
}

/**
 * What the user -- or the MACHINE -- has to do about it.
 *
 * PB-APP-10's three may never collapse into each other, and the reason is that two of them are
 * dead ends when misrouted. [AUTHENTICATE] is recoverable; [RE_PAIR] is permanent but the user
 * can carry it out; [MACHINE_REGRANT] is the one the user cannot perform from the handset at
 * all, and sending a grant-loss user to the pairing flow is a BRICK rather than a wording
 * mistake -- BeginPairing fail-fasts while this device is still registered (PB-STATE-10), so
 * the advice cannot be followed and the only exit is physical access to the machine.
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
    AUTHENTICATE,
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
    val offersPairing: Boolean get() = remedy == Remedy.PAIR || remedy == Remedy.RE_PAIR
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
        SwarmErrorTokens.REAUTH_REQUIRED to RoutedError(
            ErrorState.REAUTH_REQUIRED, Remedy.AUTHENTICATE,
            "Authenticate to carry on -- the key this needs sits behind your device unlock.",
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
        SwarmErrorTokens.RATE_LIMITED to RoutedError(
            ErrorState.RATE_LIMITED, Remedy.WAIT_AND_RETRY,
            "Too many requests in a short window. Wait a moment before trying again.",
        ),
        SwarmErrorTokens.PAIRING_FAILED to RoutedError(
            ErrorState.PAIRING_FAILED, Remedy.RETRY_PAIRING,
            "The pairing call itself failed. Start the pairing again from your machine's code.",
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
}
