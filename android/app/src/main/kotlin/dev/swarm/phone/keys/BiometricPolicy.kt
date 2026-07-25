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
    AUTHORIZED,
    ABANDONED,
    RETRYABLE,
    BLOCKED_UNTIL_TIMEOUT,
    BLOCKED_UNTIL_DEVICE_CREDENTIAL,
    REFUSED_PROMPT_IN_FLIGHT,
}

enum class ConcurrentPromptPolicy { REFUSE_SECOND, COALESCE_ONTO_FIRST }

object BiometricPolicy {

    fun specFor(operation: GatedOperation): AuthorizationSpec =
        TODO("PB-SEC-2 / 6.0: freshness tier for $operation")

    /**
     * PB-SEC-2: "no reuse of one authentication for a different action unless explicitly
     * allowed". Explicitly allowed means declared here, not implied by two operations
     * happening to share a key.
     */
    fun sharesAuthorizationWith(a: GatedOperation, b: GatedOperation): Boolean =
        TODO("PB-SEC-2: may an authorization for $a authorize $b?")

    val concurrentPrompt: ConcurrentPromptPolicy
        get() = TODO("PB-SEC-2: define concurrent-prompt behaviour")

    fun resolve(outcome: PromptOutcome): GateResolution =
        TODO("PB-SEC-2: defined cancel/failure/lockout behaviour for $outcome")
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
    fun recoveryFor(event: InvalidationEvent): Recovery =
        TODO("PB-SEC-2: how is custody recovered after $event?")
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

    fun beginPrompt(operation: GatedOperation): GateResolution =
        TODO("PB-SEC-2: concurrent-prompt behaviour when starting a prompt for $operation")

    fun endPrompt(operation: GatedOperation, outcome: PromptOutcome, atMillis: Long): GateResolution =
        TODO("PB-SEC-2: record the result of the prompt for $operation")

    fun authorized(operation: GatedOperation, atMillis: Long): Boolean =
        TODO("PB-SEC-2: is $operation authorized at $atMillis?")

    /** Per-use authorizations are spent by the operation they authorized. */
    fun consume(operation: GatedOperation)  {
        TODO("PB-SEC-2: spend the authorization for $operation")
    }

    fun invalidate(event: InvalidationEvent) {
        TODO("PB-SEC-2: drop authorizations on $event")
    }
}

/**
 * 6.0's renewal row: "a typing session crossing the 60 s freshness window must pause input
 * and re-authorize, not silently continue or silently drop; the lease itself is not ended by
 * freshness expiry".
 */
enum class InputGateDecision { PROCEED, PAUSE_AND_REAUTHORIZE }

object InputFreshness {
    fun decide(lastAuthMillis: Long, nowMillis: Long): InputGateDecision =
        TODO("PB-SEC-2 / PB-INPUT-3: freshness decision")

    val freshnessExpiryEndsLease: Boolean
        get() = TODO("PB-SEC-2 / PB-INPUT-3: does freshness expiry end the lease?")
}
