package dev.swarm.phone.ui

import java.util.UUID

/**
 * Phase B slice S16 -- PB-APP-5 (machine pane) and PB-APP-6 (launch).
 *
 * WHAT IS MODELLED IS THE POLICY, NOT A BIOMETRIC. PB-APP-5's criterion is "revoke + kill
 * switch gated per PB-SEC-2", and PB-SEC-2's own criterion is that a test must FAIL if the
 * implementation is an in-memory `authenticated = true` flag. So this file states which
 * freshness each action demands and scopes a grant to the action it was obtained for; whether
 * the platform enforces it is PB-E2E-5, which is deferred. Nothing here imports
 * androidx.biometric, and android/gate/s16_ui_test.go fences that.
 */

/** The actions PB-SEC-2 puts behind the gate. */
enum class GatedAction {
    /** The phone's own panic action, and the only destructive one it owns outright. */
    REVOKE_DEVICE,

    /** Ends a session on the machine. */
    KILL_SESSION,

    /** Starts an agent on the machine, with whatever the spec says. */
    LAUNCH,

    /** Acquires the input lease. */
    TAKE_CONTROL,

    /** Every keystroke and paste while the lease is held. */
    SEND_INPUT,
}

/** PB-SEC-2's freshness tiers. [NONE] is what a relaxed timed gate leaves. */
enum class BiometricFreshness { NONE, WINDOW_60S, PER_USE }

/**
 * PB-SEC-2's freshness table, transcribed from section 6.0: 60 s for input and take_control,
 * PER-USE for revoke, launch and kill.
 *
 * A per-use requirement is what a CryptoObject-bound Keystore key enforces, and the reason it
 * cannot be a boolean is PB-SEC-2's last clause -- "no reuse of one authentication for a
 * different action". A flag set by one prompt authorises everything after it.
 *
 * The kill switch has no entry because the phone cannot set it: protocol/server.go
 * handleRemoteSetControl refuses the remote tier before consulting its backend, so an action
 * here would be a gate on a call that does not exist (PB-SEC-6, and see [MachinePane]).
 */
object GateFreshness {
    fun of(action: GatedAction): BiometricFreshness = when (action) {
        GatedAction.REVOKE_DEVICE, GatedAction.KILL_SESSION, GatedAction.LAUNCH ->
            BiometricFreshness.PER_USE

        GatedAction.TAKE_CONTROL, GatedAction.SEND_INPUT ->
            BiometricFreshness.WINDOW_60S
    }
}

/**
 * The three events PB-SEC-2 says invalidate an outstanding authentication. Process death is the
 * fourth and needs no entry: it takes the grant with it, and PB-KEY-7's purge is what makes it
 * real on the Go side.
 */
enum class GateEvent { BACKGROUNDED, SCREEN_LOCKED, BIOMETRIC_ENROLLMENT_CHANGED }

/**
 * One authentication, SCOPED TO THE ACTION IT WAS OBTAINED FOR.
 *
 * The scoping is the whole content of the type. An implementation holding a single
 * `authenticated = true` passes every positive case and fails the negative one PB-SEC-2 names:
 * an authentication satisfied for take_control must not authorise a device revocation.
 */
data class AuthGrant(
    val action: GatedAction,
    val atMillis: Long,
) {
    /**
     * A per-use action is NEVER covered by an earlier grant, however fresh -- that is what
     * per-use means, and it is why the check consults [GateFreshness] rather than only the
     * clock.
     */
    fun authorises(action: GatedAction, nowMillis: Long): Boolean {
        if (action != this.action) return false
        if (GateFreshness.of(action) != BiometricFreshness.WINDOW_60S) return false
        val elapsed = nowMillis - atMillis
        return elapsed >= 0 && elapsed < WINDOW_MILLIS
    }

    /**
     * The grant that survives the event, which is none of them.
     *
     * The `when` is exhaustive rather than a blanket null so that an event added later has to
     * state its own verdict here instead of inheriting an invalidation nobody decided.
     */
    fun afterEvent(event: GateEvent): AuthGrant? = when (event) {
        GateEvent.BACKGROUNDED,
        GateEvent.SCREEN_LOCKED,
        GateEvent.BIOMETRIC_ENROLLMENT_CHANGED,
        -> null
    }

    companion object {
        /** Section 6.0's window. Strictly within: a grant at the boundary has expired. */
        const val WINDOW_MILLIS: Long = 60_000
    }
}

/**
 * PB-APP-5's machine pane: presence, the paired device, the activity log and the kill switch.
 *
 * THE KILL SWITCH IS READ-ONLY, and that is a security property rather than a simplification.
 * protocol/server.go handleRemoteSetControl refuses the remote tier BEFORE consulting its
 * backend -- a remote device must never re-enable a switch its owner turned off -- so a control
 * here would be advertising a bypass of a daemon gate (PB-SEC-6). The phone SHOWS the state so
 * a kill_switch refusal is legible, and offers the only panic action a phone legitimately has:
 * revoking itself.
 */
data class MachinePane(
    val machineId: String,
    /**
     * `App.Presence`, verbatim -- and it is the RELAY'S OPINION, never evidence about the
     * machine (PB-APP-11). It must be rendered with [freshness] beside it, which is why that
     * is a required parameter of this pane rather than a screen's option.
     */
    val presence: String,
    /**
     * `App.MachineFreshness` -- the phone's OWN evidence: how long since the machine's newest
     * authenticated word. A relay that withholds every frame while answering every poll leaves
     * presence reading "online" and this reading silent, which is the whole of ADR-007 B121.
     */
    val freshness: MachineFreshness,
    val pairedDeviceName: String,
    /** True when the owner has turned remote control OFF at the machine. */
    val killSwitchEngaged: Boolean,
    val activity: List<JournalRow>,
) {
    val canSetKillSwitch: Boolean = false

    val canRevokeThisDevice: Boolean = true

    /**
     * What the pane says about reachability, and it refuses to let the relay's word stand
     * alone: while the machine is silent past section 6.0's budget, the presence line is
     * qualified by what this phone can actually vouch for.
     */
    fun presenceExplanation(formatTime: (Long) -> String): String =
        freshness.notice(formatTime)?.let { notice ->
            "$notice The relay reports \"$presence\", which is the relay's word and not your " +
                "machine's."
        } ?: "Your machine is $presence."

    val killSwitchExplanation: String
        get() = if (killSwitchEngaged) {
            "Remote control is switched off at your machine, so it will refuse anything this " +
                "phone asks it to change. Only the machine's owner can switch it back on."
        } else {
            "Remote control is switched on at your machine. Only the machine itself can switch " +
                "it off."
        }
}

/** PB-APP-6's v1 builder: the three fields a launch spec carries from the handset. */
data class LaunchDraft(
    val agent: String,
    val cwd: String,
    val prompt: String,
)

/** One issued operation, keyed the way PB-SYNC-2 keys outcomes. */
data class LaunchOperation(
    val action: String,
    val operationId: String,
)

/** `swarmmobile.Outcome`, as the screen takes it. An empty [code] is an unresolved operation. */
data class OperationOutcome(
    val operationId: String,
    val code: String,
    val message: String,
)

/** What the launch screen shows for an operation. */
enum class LaunchResult {
    /** No answer yet, and saying anything else would be a guess. */
    PENDING,

    LAUNCHED,

    /** schema.CodePolicy. The machine's considered answer; retrying changes nothing. */
    REJECTED_BY_POLICY,

    /** schema.CodeRateLimit. Retryable, but only after waiting. */
    REFUSED_TRANSIENTLY,

    /** Every other refusal the machine can seal: kill switch, authorization, a bad field. */
    REFUSED,
}

/** A resolved (or still unresolved) launch, with the machine's own reason attached. */
data class LaunchRendering(
    val result: LaunchResult,
    /** The machine's message, verbatim. Empty only while [LaunchResult.PENDING]. */
    val reason: String,
    val retryable: Boolean,
)

/**
 * PB-APP-6's screen.
 *
 * IT REMEMBERS THE OPERATION ID IT ISSUED, because PB-SYNC-2 keys outcomes by operation id
 * precisely so that a screen never resolves one by proximity. An outcome for someone else's
 * operation leaves this one pending, which is the honest answer: an unresolved launch is
 * neither a success nor a failure, and claiming either is worse than saying nothing.
 */
class LaunchScreen {

    var inFlight: LaunchOperation? = null
        private set

    /**
     * The required field [draft] is missing, in the words a user reads, or null when it carries
     * both. The daemon has no default for either -- it refuses a launch without an agent and
     * without a working directory -- so a form that sent one anyway would be inventing a value
     * nobody chose, which is the defect `leaseHeld = false` already cost this project once.
     *
     * IT IS ONE STATEMENT OF THE RULE AND IT IS WHY THIS IS PUBLIC. [submit] refuses on it and
     * the screen asks on it, so a surface cannot enforce a different bar from the model's -- and
     * a screen that discovered the refusal by catching submit's own exception would report a
     * programming error to a user who simply left a field empty.
     */
    fun missingField(draft: LaunchDraft): String? = when {
        draft.agent.isBlank() -> "Say which agent to start."
        draft.cwd.isBlank() -> "Say which folder on your machine it starts in."
        else -> null
    }

    /**
     * @param operationId the id the MACHINE will key the outcome by --
     *  `swarmmobile.Op.getOperationID()`, which `App.Launch` returns synchronously even for an
     *  operation it queued. The default exists so the screen's policy can be exercised without
     *  a facade; production passes the core's id, and a locally minted one would never match
     *  the outcome that comes back.
     */
    fun submit(
        draft: LaunchDraft,
        operationId: String = UUID.randomUUID().toString(),
    ): LaunchOperation {
        val missing = missingField(draft)
        require(missing == null) { "PB-APP-6: $missing" }
        val op = LaunchOperation(action = ACTION_LAUNCH, operationId = operationId)
        inFlight = op
        return op
    }

    fun resolve(outcome: OperationOutcome): LaunchRendering {
        if (outcome.operationId != inFlight?.operationId || outcome.code.isBlank()) {
            return LaunchRendering(LaunchResult.PENDING, reason = "", retryable = false)
        }
        return when (outcome.code) {
            CODE_POLICY -> LaunchRendering(
                LaunchResult.REJECTED_BY_POLICY, outcome.message, retryable = false,
            )

            CODE_RATE_LIMIT -> LaunchRendering(
                LaunchResult.REFUSED_TRANSIENTLY, outcome.message, retryable = true,
            )

            // A success carries the reply op rather than an error code (mobile/app.go
            // outcomeOf falls back to Control.Op when ErrorCode is empty), and the reply op
            // for an accepted command is protocol.OpOK.
            CODE_OK -> LaunchRendering(
                LaunchResult.LAUNCHED, outcome.message, retryable = false,
            )

            // Every other refusal the machine can seal -- kill_switch, not_authorized,
            // stale_approval, invalid_field. NOT folded into REJECTED_BY_POLICY: a kill-switch
            // refusal ends when the owner flips a switch, and telling that user their launch
            // was against policy sends them to change a spec that was fine.
            else -> LaunchRendering(LaunchResult.REFUSED, outcome.message, retryable = false)
        }
    }

    private companion object {
        /** schema.ActionLaunch. */
        const val ACTION_LAUNCH = "launch"

        /** protocol.OpOK, schema.CodePolicy, schema.CodeRateLimit. */
        const val CODE_OK = "ok"
        const val CODE_POLICY = "policy"
        const val CODE_RATE_LIMIT = "rate_limit"
    }
}
