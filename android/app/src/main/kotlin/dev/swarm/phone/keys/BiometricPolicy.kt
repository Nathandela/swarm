package dev.swarm.phone.keys

/**
 * PB-SEC-2 -- the biometric gate, cryptographically enforced rather than cosmetic.
 *
 * SCAFFOLDING ONLY; every decision is TODO().
 *
 * The freshness numbers are not policy sketches. Requirements 6.0 binds them:
 *
 *     | Biometric freshness | 60 s for input/take_control; per-use (CryptoObject) for
 *     | revoke, kill switch, launch, kill | PB-SEC-2 |
 *
 * A per-use tier implemented as a 60 s tier is the silent downgrade this file exists to make
 * impossible. The integer alone cannot distinguish them -- per-use IS timeout 0 in
 * KeyGenParameterSpec terms -- so the discriminator is whether the authorization is carried
 * by a CryptoObject bound to the one operation.
 */

/** The operations 6.0 assigns a freshness tier to. */
enum class GatedOperation { INPUT, TAKE_CONTROL, REVOKE, KILL_SWITCH, LAUNCH, KILL }

sealed class Freshness {
    /** Authorization is reusable for [seconds] after the prompt succeeds. */
    data class Timed(val seconds: Int) : Freshness()

    /** Every use needs its own CryptoObject-bound authorization. */
    data object PerUse : Freshness()
}

data class AuthorizationSpec(
    val freshness: Freshness,
    /**
     * True only for per-use. This is the discriminator: `Timed(0)` and `PerUse` encode the
     * same KeyGenParameterSpec timeout, and only the CryptoObject binding tells them apart.
     */
    val requiresCryptoObject: Boolean,
    /** KeyGenParameterSpec.setUserAuthenticationParameters timeout. 0 means per-use. */
    val timeoutSeconds: Int,
)

/** What the prompt did. */
enum class PromptOutcome { SUCCEEDED, CANCELLED, FAILED, LOCKED_OUT, LOCKED_OUT_PERMANENT }

/** What the app does about it. Distinct per outcome, or the outcome was not handled. */
enum class GateResolution {
    /** The prompt is now on screen. Not an outcome -- only [AuthorizationLedger.beginPrompt] returns it. */
    PROMPT_STARTED,
    AUTHORIZED,
    ABANDONED,
    RETRYABLE,
    BLOCKED_UNTIL_TIMEOUT,
    BLOCKED_UNTIL_DEVICE_CREDENTIAL,
    REFUSED_PROMPT_IN_FLIGHT,
}

enum class ConcurrentPromptPolicy { REFUSE_SECOND, COALESCE_ONTO_FIRST }

object BiometricPolicy {

    /** 6.0's window for the typed tier, in seconds. Named once. */
    const val TIMED_WINDOW_SECONDS: Int = 60

    private val timed = AuthorizationSpec(
        freshness = Freshness.Timed(TIMED_WINDOW_SECONDS),
        requiresCryptoObject = false,
        timeoutSeconds = TIMED_WINDOW_SECONDS,
    )

    private val perUse = AuthorizationSpec(
        freshness = Freshness.PerUse,
        requiresCryptoObject = true,
        // Per-use IS timeout 0 in KeyGenParameterSpec terms. The integer alone cannot
        // distinguish the tiers, which is exactly why requiresCryptoObject exists beside it.
        timeoutSeconds = 0,
    )

    fun specFor(operation: GatedOperation): AuthorizationSpec = when (operation) {
        GatedOperation.INPUT, GatedOperation.TAKE_CONTROL -> timed
        GatedOperation.REVOKE, GatedOperation.KILL_SWITCH,
        GatedOperation.LAUNCH, GatedOperation.KILL,
        -> perUse
    }

    /**
     * PB-SEC-2: "no reuse of one authentication for a different action unless explicitly
     * allowed". Explicitly allowed means declared here, not implied by two operations
     * happening to share a key.
     *
     * A per-use authorization is shared with NOTHING, including itself: it is carried by a
     * CryptoObject bound to one operation, and it is spent by that operation. The timed
     * operations DO share, because they share one Keystore entry and one window by
     * construction (`KeystoreAliases.forOperation`) -- the declaration is what turns that
     * accident of the platform into a decision.
     */
    fun sharesAuthorizationWith(a: GatedOperation, b: GatedOperation): Boolean =
        !specFor(a).requiresCryptoObject && !specFor(b).requiresCryptoObject

    /**
     * REFUSE_SECOND. BiometricPrompt does not queue: a second prompt while one is in flight
     * either replaces the first -- leaving the first caller waiting on a result that will
     * never arrive -- or throws. Coalescing onto the first is worse still, because the two
     * callers may want different operations and a coalesced success would authorize the
     * wrong one.
     */
    val concurrentPrompt: ConcurrentPromptPolicy = ConcurrentPromptPolicy.REFUSE_SECOND

    /**
     * Four non-success outcomes, four distinct resolutions. Collapsing any pair produces a
     * prompt loop against a platform that is refusing on purpose.
     */
    fun resolve(outcome: PromptOutcome): GateResolution = when (outcome) {
        PromptOutcome.SUCCEEDED -> GateResolution.AUTHORIZED
        // The user said no. Prompting again is the thing they just declined.
        PromptOutcome.CANCELLED -> GateResolution.ABANDONED
        // A finger that did not match. The platform allows another attempt.
        PromptOutcome.FAILED -> GateResolution.RETRYABLE
        // ERROR_LOCKOUT: 30 s, and only time clears it.
        PromptOutcome.LOCKED_OUT -> GateResolution.BLOCKED_UNTIL_TIMEOUT
        // ERROR_LOCKOUT_PERMANENT: only a device credential clears it.
        PromptOutcome.LOCKED_OUT_PERMANENT -> GateResolution.BLOCKED_UNTIL_DEVICE_CREDENTIAL
    }
}

/**
 * PB-SEC-2's invalidation clause plus PB-KEY-7's. Every one of these must end content
 * custody; they differ in how it is recovered.
 */
enum class InvalidationEvent {
    APP_BACKGROUNDED,
    DEVICE_LOCKED,
    PROCESS_DEATH,
    BIOMETRIC_ENROLLMENT_CHANGED,
    AUTH_TIMEOUT_EXPIRED,
}

enum class Recovery {
    /** Prompt again; the Keystore key survives. */
    REAUTHENTICATE,

    /** The KEK is gone: generate a new one and re-seal what it protected. */
    REPROVISION_KEK,

    /** The device identity key is gone: nothing on-device can recover it. */
    REPAIR_DEVICE,
}

object GateInvalidation {
    /**
     * PB-SEC-2. Four of the five destroy no key, so a fresh prompt is the whole recovery.
     *
     * BIOMETRIC_ENROLLMENT_CHANGED is the exception and it is REPAIR_DEVICE, not
     * REPROVISION_KEK. `setInvalidatedByBiometricEnrollment(true)` destroys the content KEK,
     * and the material that KEK protected -- the three content-tier device scalars, including
     * the COMMAND_SIGN seed the daemon registry pins this device's id to (R-DEV.1) -- exists
     * nowhere else. Reprovisioning a KEK re-seals plaintext you still hold; here there is
     * none to re-seal. Nothing on-device recovers it, so the honest answer is that the device
     * must pair again.
     */
    fun recoveryFor(event: InvalidationEvent): Recovery = when (event) {
        InvalidationEvent.BIOMETRIC_ENROLLMENT_CHANGED -> Recovery.REPAIR_DEVICE
        InvalidationEvent.APP_BACKGROUNDED,
        InvalidationEvent.DEVICE_LOCKED,
        InvalidationEvent.PROCESS_DEATH,
        InvalidationEvent.AUTH_TIMEOUT_EXPIRED,
        -> Recovery.REAUTHENTICATE
    }
}

/**
 * The runtime ledger. It is deliberately NOT a boolean: an authorization is per operation,
 * consumable, time-bounded and killed by an invalidation event.
 *
 * It is also NOT the gate. The gate is the Keystore refusing to unwrap; this only decides
 * whether to prompt. A test drives the two out of agreement to prove the ledger is not what
 * is being trusted.
 */
class AuthorizationLedger {

    /** The one prompt BiometricPrompt may have on screen, or null. */
    private var inFlight: GatedOperation? = null

    /** Grant time per authorized operation. Absent means not authorized, at any time. */
    private val grantedAtMillis = mutableMapOf<GatedOperation, Long>()

    fun beginPrompt(operation: GatedOperation): GateResolution {
        if (inFlight != null) return GateResolution.REFUSED_PROMPT_IN_FLIGHT
        inFlight = operation
        return GateResolution.PROMPT_STARTED
    }

    /**
     * Records the result and clears the in-flight marker WHATEVER the outcome. A resolution
     * path that cleared it only on success wedges the gate on the first cancel: every later
     * prompt is refused as concurrent and no prompt can ever start again.
     *
     * ADR-007 B60(3). A CALLBACK THAT DOES NOT BELONG TO THE PROMPT ON SCREEN AUTHORIZES
     * NOTHING. The `inFlight` check used to decide only whether to CLEAR the marker while the
     * grant below ran unconditionally, so a ledger that had never prompted for an operation
     * would authorize it on any callback naming it.
     *
     * The reachable flow is not a race to be won: `ContentLockTriggers` invalidates on
     * ACTION_SCREEN_OFF, nothing in the app calls `BiometricPrompt.cancelAuthentication`, and
     * `BiometricPrompts.show` answers on the main executor -- so the prompt survives behind the
     * keyguard and its callback lands after the lock emptied the ledger. Without this refusal
     * the session is killed on an authority ADR-007 B44 says the lock destroyed.
     *
     * ABANDONED rather than a refusal that clears: the marker belongs to whatever prompt is
     * genuinely on screen, and a stale callback must not disturb it.
     *
     * WHAT THIS DOES NOT CLOSE, stated so it is chosen rather than assumed: two prompts for the
     * SAME operation are indistinguishable here, because the signature carries nothing that
     * separates them. Superseding prompt #1 with prompt #2 for the same operation and then
     * delivering #1's callback still resolves against #2. Closing that needs a per-prompt token
     * issued by `beginPrompt` and presented back here.
     */
    fun endPrompt(operation: GatedOperation, outcome: PromptOutcome, atMillis: Long): GateResolution {
        if (inFlight != operation) return GateResolution.ABANDONED
        inFlight = null
        val resolution = BiometricPolicy.resolve(outcome)
        if (resolution == GateResolution.AUTHORIZED) {
            grantedAtMillis[operation] = atMillis
        } else {
            grantedAtMillis.remove(operation)
        }
        return resolution
    }

    /**
     * Never consulted as the gate. The gate is the Keystore refusing to unwrap; this only
     * decides whether prompting is worth doing, and BiometricGateTest drives the two out of
     * agreement to prove which one is trusted.
     */
    fun authorized(operation: GatedOperation, atMillis: Long): Boolean {
        val granted = grantedAtMillis[operation] ?: return false
        val spec = BiometricPolicy.specFor(operation)
        // A per-use authorization has no window: it is carried by a CryptoObject bound to one
        // operation and it ends when that operation consumes it. Giving it a timeout would be
        // the Timed(0) confusion this file exists to prevent, from the other direction.
        if (spec.requiresCryptoObject) return true
        return atMillis - granted < spec.timeoutSeconds * 1_000L
    }

    /** Per-use authorizations are spent by the operation they authorized. */
    fun consume(operation: GatedOperation) {
        grantedAtMillis.remove(operation)
    }

    /**
     * Every event drops EVERY authorization, not just the ones whose tier looks affected.
     * PB-SEC-2 names invalidation as a clause of its own, and an event-specific purge is a
     * table someone has to keep correct as operations are added.
     */
    fun invalidate(event: InvalidationEvent) {
        inFlight = null
        grantedAtMillis.clear()
    }
}

/**
 * 6.0's renewal row: "a typing session crossing the 60 s freshness window must pause input
 * and re-authorize, not silently continue or silently drop; the lease itself is not ended by
 * freshness expiry".
 */
enum class InputGateDecision { PROCEED, PAUSE_AND_REAUTHORIZE }

object InputFreshness {
    fun decide(lastAuthMillis: Long, nowMillis: Long): InputGateDecision {
        val window = BiometricPolicy.specFor(GatedOperation.INPUT).timeoutSeconds * 1_000L
        return if (nowMillis - lastAuthMillis < window) {
            InputGateDecision.PROCEED
        } else {
            InputGateDecision.PAUSE_AND_REAUTHORIZE
        }
    }

    /**
     * FALSE. take_control's ExpiresAt is now + 15 min precisely so the lease is not the
     * binding constraint on a typing session (6.0); ending it on a 60 s freshness lapse would
     * reintroduce that constraint through the back door, and the user would lose the session
     * rather than be asked for a fingerprint.
     */
    const val freshnessExpiryEndsLease: Boolean = false
}
