package dev.swarm.phone.keys

import javax.crypto.Cipher

/**
 * PB-SEC-2's PER-USE tier, wired -- the half `BiometricPolicy.kt` describes and nothing
 * performed.
 *
 * WHAT WAS ACTUALLY MISSING (ADR-007 B51). `KeystoreSpecs.forOperation` -- the per-use
 * `KeyGenParameterSpec` for revoke, kill switch, launch and kill -- was referenced from
 * `src/test/` alone. [AuthorizationLedger.beginPrompt], [AuthorizationLedger.endPrompt] and
 * [AuthorizationLedger.consume] had no production caller. There was no `BiometricPrompt` in the
 * app at all. So those operations were gated by exactly what ordinary input is gated by: the
 * content KEK's 60-second TIMED window -- the per-use-implemented-as-timed downgrade
 * `BiometricPolicy.kt`'s own header says that file exists to make impossible.
 *
 * THE DISCRIMINATOR IS NOT THE LEDGER, AND THAT IS THE WHOLE DESIGN. PB-SEC-2's criterion is
 * that a test must fail if the implementation is an in-memory `authenticated = true` flag, and
 * the ledger IS an in-memory map. So the ledger is never asked whether the action may run. What
 * is asked is the platform, twice and in this order:
 *
 *  1. Keystore is asked for a `Cipher` under the operation's OWN per-use entry
 *     ([KeystoreAliases.forOperation]). That entry is provisioned with
 *     `setUserAuthenticationParameters(0, AUTH_BIOMETRIC_STRONG)`, so obtaining one at all
 *     requires the platform to be willing.
 *  2. After the prompt reports success, the cipher the PLATFORM RELEASED is USED. A
 *     `BiometricPrompt` success is a statement about the user, not about the key; an
 *     implementation that stopped at the outcome enum would be trusting a UI event, which is the
 *     same defect wearing the platform's clothes.
 *
 * The ledger's job is narrower and is still worth doing: it refuses a second concurrent prompt
 * (`BiometricPrompt` does not queue), it records what is in flight, and it is what every
 * [InvalidationEvent] clears. It decides whether prompting is worth doing. It never decides
 * whether the action runs.
 *
 * THE AUTHORIZATION IS SPENT BEFORE THE ACTION RUNS. Per-use means one use; consuming
 * afterwards would leave a live authorization on the stack for the duration of the action, which
 * a redraw or a second delivered tap could reach.
 *
 * WHAT IS NOT ESTABLISHED HERE, and may not be claimed from here. That a real biometric prompt
 * was shown, accepted or refused; that a real Keystore withholds a real key from an
 * unauthenticated user; that `setUserAuthenticationParameters(0, ...)` behaves as documented on
 * any handset. Those are PB-E2E-5, DEFERRED (ADR-007 B31), and ADR-007 B56 puts the entire
 * `androidTest` tier out of reach besides: the emulator's keymint reports
 * SECURITY_LEVEL_SOFTWARE and PB-KEY-8 fails the app closed before a screen renders. What this
 * file establishes is the ORDER and the refusals -- which is the part that was absent.
 */

/**
 * What the platform says about its ability to prompt, as a decision rather than an int.
 *
 * It is an enum in this file and not a `BiometricManager` constant on purpose: the mapping from
 * the platform's integers lives in the one thin androidx wrapper ([BiometricPrompts]), and the
 * POLICY -- what each state means for the user -- lives here where it can be asserted on a plain
 * JVM. The same split `KeystoreSecurityLevel` already uses.
 */
enum class PromptAvailability {
    /** A Class-3 biometric is enrolled and usable now. */
    READY,

    /** The hardware is there and nothing is enrolled. The user can fix this. */
    NONE_ENROLLED,

    /** No Class-3 biometric sensor. Nothing the user does to this handset changes it. */
    NO_HARDWARE,

    /** Present, enrolled, busy or disabled right now. Transient by definition. */
    TEMPORARILY_UNAVAILABLE,

    /** The platform wants a security update before it will vouch for the sensor. */
    SECURITY_UPDATE_REQUIRED,
}

/**
 * Why a per-use action did not run, at the granularity the REMEDIES differ at.
 *
 * The granularity is the point. Collapsing any pair produces either a prompt loop against a
 * platform that is refusing on purpose, or a user told to do something that cannot help.
 */
enum class PerUseRefusalReason {
    /** One prompt is already on screen. `BiometricPrompt` does not queue. */
    PROMPT_IN_FLIGHT,
    NO_BIOMETRIC_ENROLLED,
    NO_BIOMETRIC_HARDWARE,
    BIOMETRIC_UNAVAILABLE,
    SECURITY_UPDATE_REQUIRED,

    /** The user dismissed it. Prompting again is the thing they just declined. */
    CANCELLED,

    /** A finger that did not match. The platform allows another attempt. */
    FAILED,

    /** ERROR_LOCKOUT: 30 s, and only time clears it. */
    LOCKED_OUT,

    /** ERROR_LOCKOUT_PERMANENT: only a device credential clears it. */
    LOCKED_OUT_PERMANENT,

    /**
     * The prompt succeeded and the ledger refused its callback: it belongs to a prompt that is no
     * longer the one on screen. Either an invalidation emptied the ledger under it (ADR-007 B63)
     * or a second prompt superseded it ([PromptTicket]).
     */
    PROMPT_SUPERSEDED,

    /** Keystore would not produce a usable cipher, even after regenerating the entry. */
    KEY_UNAVAILABLE,

    /** The prompt said yes and the key it handed back does not work. */
    KEY_NOT_RELEASED,
}

/** What the user can do about it. [NONE] is reserved for the one case where nothing helps. */
enum class PerUseRemedy {
    TRY_AGAIN,
    WAIT_AND_TRY_AGAIN,
    ENROL_BIOMETRIC,
    UNLOCK_WITH_DEVICE_CREDENTIAL,
    UPDATE_SYSTEM,
    REPORT_BUG,
    NONE,
}

/**
 * @param detail the platform's own words, for a log and a bug report. Never what the user is
 *  shown: a Keystore alias is not a remedy (PB-APP-9).
 */
data class PerUseRefusal(
    val operation: GatedOperation,
    val reason: PerUseRefusalReason,
    val detail: String = "",
) {
    val message: String get() = PerUseRefusalText.messageFor(reason)
    val remedy: PerUseRemedy get() = PerUseRefusalText.remedyFor(reason)
}

/**
 * One table, one place to get a row wrong.
 *
 * EVERY ROW NAMES SOMETHING THE USER CAN DO except [PerUseRefusalReason.NO_BIOMETRIC_HARDWARE],
 * where nothing they do to this handset helps. That asymmetry is asserted by test, because a
 * gate that can refuse with no way forward is the failure this whole slice exists to remove --
 * it is why `ContentLock` declined a foreground timer, and it is what an unbuilt prompt made of
 * every per-use operation.
 */
object PerUseRefusalText {

    fun remedyFor(reason: PerUseRefusalReason): PerUseRemedy = when (reason) {
        PerUseRefusalReason.PROMPT_IN_FLIGHT -> PerUseRemedy.TRY_AGAIN
        PerUseRefusalReason.NO_BIOMETRIC_ENROLLED -> PerUseRemedy.ENROL_BIOMETRIC
        PerUseRefusalReason.NO_BIOMETRIC_HARDWARE -> PerUseRemedy.NONE
        PerUseRefusalReason.BIOMETRIC_UNAVAILABLE -> PerUseRemedy.WAIT_AND_TRY_AGAIN
        PerUseRefusalReason.SECURITY_UPDATE_REQUIRED -> PerUseRemedy.UPDATE_SYSTEM
        PerUseRefusalReason.CANCELLED -> PerUseRemedy.TRY_AGAIN
        PerUseRefusalReason.FAILED -> PerUseRemedy.TRY_AGAIN
        PerUseRefusalReason.LOCKED_OUT -> PerUseRemedy.WAIT_AND_TRY_AGAIN
        // A device-credential confirmation is what clears ERROR_LOCKOUT_PERMANENT, and the lock
        // screen is one button away on every handset. The app does not offer a credential prompt
        // of its own: the content KEK requires AUTH_BIOMETRIC_STRONG, so a credential confirm
        // would not authorize anything here -- it would only clear the lockout, which locking and
        // unlocking the phone does too, without the app asking for the device secret.
        PerUseRefusalReason.LOCKED_OUT_PERMANENT -> PerUseRemedy.UNLOCK_WITH_DEVICE_CREDENTIAL
        PerUseRefusalReason.PROMPT_SUPERSEDED -> PerUseRemedy.TRY_AGAIN
        PerUseRefusalReason.KEY_UNAVAILABLE -> PerUseRemedy.REPORT_BUG
        PerUseRefusalReason.KEY_NOT_RELEASED -> PerUseRemedy.TRY_AGAIN
    }

    fun messageFor(reason: PerUseRefusalReason): String = when (reason) {
        PerUseRefusalReason.PROMPT_IN_FLIGHT ->
            "Finish the unlock already on screen, then try this again."

        PerUseRefusalReason.NO_BIOMETRIC_ENROLLED ->
            "This action needs a fingerprint or face unlock. Add one in system settings, then " +
                "try again."

        PerUseRefusalReason.NO_BIOMETRIC_HARDWARE ->
            "This handset has no fingerprint or face sensor the app can require, so it cannot " +
                "protect this action. Nothing here will change that."

        PerUseRefusalReason.BIOMETRIC_UNAVAILABLE ->
            "The fingerprint or face sensor is not available right now. Try again in a moment."

        PerUseRefusalReason.SECURITY_UPDATE_REQUIRED ->
            "The system wants a security update before it will use the sensor for this. Install " +
                "pending updates, then try again."

        PerUseRefusalReason.CANCELLED ->
            "Unlock cancelled, so nothing was done."

        PerUseRefusalReason.FAILED ->
            "That was not recognised. Try again."

        PerUseRefusalReason.LOCKED_OUT ->
            "Too many attempts. The sensor is locked for about thirty seconds; try again after " +
                "that."

        PerUseRefusalReason.LOCKED_OUT_PERMANENT ->
            "The sensor is locked until you unlock the phone with its PIN, pattern or password. " +
                "Do that, then try again."

        PerUseRefusalReason.PROMPT_SUPERSEDED ->
            "That unlock no longer applies -- the phone locked, or another request replaced it " +
                "-- so nothing was done. Try again."

        PerUseRefusalReason.KEY_UNAVAILABLE ->
            "The phone could not prepare the key this action is protected by. The app is " +
                "otherwise healthy; please report it."

        PerUseRefusalReason.KEY_NOT_RELEASED ->
            "The unlock was accepted but the key was not released, so nothing was done. Try again."
    }
}

/**
 * The Keystore side of the per-use gate: a `Cipher` under the operation's OWN entry.
 *
 * It is a seam because the far side is `AndroidKeyStore`, which no unit test may claim to speak
 * for. What crosses is a `javax.crypto.Cipher` and nothing else -- no key bytes in either
 * direction.
 */
fun interface PerUseCipherSource {
    /**
     * @throws KeyCustodyException when the platform refuses. A locked handset, a lapsed window
     *  and a destroyed key are all legitimate refusals here.
     */
    fun cipherFor(operation: GatedOperation): Cipher
}

/**
 * The prompt, as the gate sees it: can you prompt, and here is one to show.
 *
 * Two members rather than two seams, because they are one question asked of one subsystem, and
 * an availability answer that could disagree with the prompt it gates would be two sources for
 * one fact.
 */
interface PerUsePrompt {

    fun availability(): PromptAvailability

    /**
     * Show the platform prompt for [operation] over [cipher], and report what happened.
     *
     * @param onResult the outcome, and the cipher the PLATFORM released -- null when it released
     *  none. The gate uses what comes back here, never the [cipher] it passed in: handing the
     *  input cipher to the caller on success would authorize the action with an object the
     *  platform never unlocked.
     */
    fun show(operation: GatedOperation, cipher: Cipher, onResult: (PromptOutcome, Cipher?) -> Unit)
}

/**
 * The gate. One method, and the action is the trailing lambda so a call site reads as what it is.
 *
 * IT IS NOT A `suspend` FUNCTION AND HOLDS NO STATE OF ITS OWN. `BiometricPrompt` answers on the
 * main thread through a callback, and the only state worth keeping across that callback -- what
 * is in flight -- belongs to the [AuthorizationLedger] every [InvalidationEvent] already clears.
 * A second copy here would be a second thing for a screen lock to have to reach.
 */
class PerUseGate(
    private val prompt: PerUsePrompt,
    private val ciphers: PerUseCipherSource,
    private val ledger: AuthorizationLedger,
    private val now: () -> Long = System::currentTimeMillis,
) {

    /**
     * Run [action] if, and only if, the platform authorizes THIS use of [operation].
     *
     * @throws IllegalArgumentException when [operation] is a TIMED tier. A per-use gate over
     *  input would prompt per keystroke, which is not what requirements 6.0 says and is the kind
     *  of over-gating users turn off; the tiers are not interchangeable in either direction.
     */
    fun authorize(
        operation: GatedOperation,
        onRefused: (PerUseRefusal) -> Unit,
        action: () -> Unit,
    ) {
        require(BiometricPolicy.specFor(operation).requiresCryptoObject) {
            "$operation is a timed tier (requirements 6.0); the per-use gate would prompt for " +
                "every use of an operation the design windows"
        }

        // ASKED FIRST, AND BEFORE THE LEDGER IS TOUCHED. A handset that cannot prompt must not
        // leave an in-flight marker behind, and it must not reach Keystore for a cipher it can
        // never have released -- on a handset with nothing enrolled, generating the per-use entry
        // is itself refused by the platform, and the resulting exception would report as a bug
        // rather than as the one thing the user can act on.
        val availability = prompt.availability()
        if (availability != PromptAvailability.READY) {
            onRefused(PerUseRefusal(operation, reasonFor(availability)))
            return
        }

        // THE IDENTITY OF THIS PROMPT, and it lives in this closure and nowhere else. Two
        // authorize calls for the SAME operation are otherwise indistinguishable to the ledger
        // (ADR-007 B63), so #1's late callback resolved against #2 -- a kill authorized by a
        // finger the user gave for a different request, or for nobody's request at all.
        val ticket = PromptTicket(operation)
        if (ledger.beginPrompt(operation, ticket) != GateResolution.PROMPT_STARTED) {
            onRefused(PerUseRefusal(operation, PerUseRefusalReason.PROMPT_IN_FLIGHT))
            return
        }

        val cipher = try {
            ciphers.cipherFor(operation)
        } catch (refused: Throwable) {
            // The in-flight marker is cleared through the ledger's own path rather than by
            // reaching into it: a gate that returned here without clearing would refuse every
            // later prompt as concurrent, which is the wedge `endPrompt` documents.
            ledger.endPrompt(operation, PromptOutcome.FAILED, now(), ticket)
            onRefused(
                PerUseRefusal(
                    operation,
                    PerUseRefusalReason.KEY_UNAVAILABLE,
                    "${refused.javaClass.name}: ${refused.message ?: "no message"}",
                ),
            )
            return
        }

        prompt.show(operation, cipher) { outcome, released ->
            if (ledger.endPrompt(operation, outcome, now(), ticket) != GateResolution.AUTHORIZED) {
                onRefused(PerUseRefusal(operation, reasonFor(outcome)))
                return@show
            }

            // THE GATE. Not the ledger above it, which by now says AUTHORIZED and is ignored.
            val released2 = released
            val proof = if (released2 == null) {
                null
            } else {
                try {
                    released2.doFinal(PER_USE_CHALLENGE)
                } catch (refused: Throwable) {
                    null
                }
            }

            // Spent whatever happened, and BEFORE the action: per-use means one use, and an
            // action running with a live authorization behind it is one a redraw could repeat.
            ledger.consume(operation)

            if (proof == null || proof.isEmpty()) {
                onRefused(PerUseRefusal(operation, PerUseRefusalReason.KEY_NOT_RELEASED))
                return@show
            }
            action()
        }
    }

    private fun reasonFor(availability: PromptAvailability): PerUseRefusalReason = when (availability) {
        PromptAvailability.NONE_ENROLLED -> PerUseRefusalReason.NO_BIOMETRIC_ENROLLED
        PromptAvailability.NO_HARDWARE -> PerUseRefusalReason.NO_BIOMETRIC_HARDWARE
        PromptAvailability.TEMPORARILY_UNAVAILABLE -> PerUseRefusalReason.BIOMETRIC_UNAVAILABLE
        PromptAvailability.SECURITY_UPDATE_REQUIRED -> PerUseRefusalReason.SECURITY_UPDATE_REQUIRED
        // Unreachable: the caller returned above. Stated rather than defaulted so a value added
        // later fails to compile here instead of being read as ready.
        PromptAvailability.READY -> PerUseRefusalReason.BIOMETRIC_UNAVAILABLE
    }

    private fun reasonFor(outcome: PromptOutcome): PerUseRefusalReason = when (outcome) {
        PromptOutcome.CANCELLED -> PerUseRefusalReason.CANCELLED
        PromptOutcome.FAILED -> PerUseRefusalReason.FAILED
        PromptOutcome.LOCKED_OUT -> PerUseRefusalReason.LOCKED_OUT
        PromptOutcome.LOCKED_OUT_PERMANENT -> PerUseRefusalReason.LOCKED_OUT_PERMANENT
        // REACHABLE, and the one row where the outcome does not name the refusal. A SUCCEEDED
        // callback resolves to AUTHORIZED unless the LEDGER refused it, and it refuses exactly
        // one thing: a callback that does not belong to the prompt on screen. It used to read
        // KEY_NOT_RELEASED -- "the unlock was accepted but the key was not released" -- which
        // tells the user the wrong story about a prompt that was superseded or invalidated.
        PromptOutcome.SUCCEEDED -> PerUseRefusalReason.PROMPT_SUPERSEDED
    }

    private companion object {
        /**
         * The bytes the released key is made to operate on. The ciphertext is DISCARDED -- its
         * value is not the output but the fact that producing it required the platform to
         * authorize this key, for this use, now. Constant on purpose: nothing about the plaintext
         * is secret, and a per-call value would suggest it carried meaning.
         */
        val PER_USE_CHALLENGE: ByteArray = "swarm/per-use".toByteArray(Charsets.UTF_8)
    }
}
