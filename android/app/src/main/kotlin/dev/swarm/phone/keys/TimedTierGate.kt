package dev.swarm.phone.keys

/**
 * PB-SEC-2's TIMED tier, with the prompt lifecycle its per-use sibling already had.
 *
 * WHAT WAS ACTUALLY MISSING (ADR-007 B96). ADR-007 B63 closed the stale-callback class by giving
 * every prompt an identity -- [PromptTicket] -- minted BEFORE the platform prompt is shown and
 * presented back by the callback that prompt produces. THAT FIX WAS APPLIED TO [PerUseGate] AND
 * TO NOTHING ELSE. `PhoneSurface.reauthorizeTimedTier` called
 * [BiometricPrompts.confirmForContent] with no ticket registered: the ledger entry was created
 * INSIDE the callback, by a `beginPrompt`/`endPrompt` pair that ran back to back on arrival.
 * `promptForContent` had the same shape.
 *
 * So for the entire time the timed tier's prompt was on screen the ledger held NOTHING, and two
 * things followed from that:
 *
 *  - AN INVALIDATION HAD NOTHING TO CLEAR. `ContentLockTriggers` invalidates on
 *    ACTION_SCREEN_OFF and on the app going to background. [AuthorizationLedger.invalidate]
 *    empties the in-flight marker and every grant -- but for a prompt registered nowhere that is
 *    a no-op, and the prompt survives behind the keyguard: nothing calls
 *    `BiometricPrompt.cancelAuthentication` and the callback lands on the main executor
 *    afterwards. That late success then minted a FRESH sixty-second authorization for both timed
 *    operations and ran the action it was holding, after the lock ADR-007 B44 says destroyed that
 *    authority.
 *  - `ConcurrentPromptPolicy.REFUSE_SECOND` DID NOT APPLY TO THIS PATH AT ALL, because
 *    [AuthorizationLedger.beginPrompt] was never asked. A per-use prompt on screen did not stop a
 *    second, timed `BiometricPrompt` being raised over it -- the state `BiometricPolicy` says
 *    either replaces the first, leaving its caller waiting on a result that never arrives, or
 *    throws.
 *
 * THE DECISION LIVES HERE AND NOT ON THE SCREEN, and that is what makes it assertable. The whole
 * of requirements 6.0's renewal clause -- proceed inside the window, pause and re-authorize
 * outside it -- was previously a private method of an Activity-hosted view class, which no unit
 * test on this tier can construct (ADR-007 B56 puts `androidTest` out of reach, and
 * `android/gate/s20_pbsec2_peruse_test.go` check (4) forbids a Robolectric shadow standing in for
 * a `BiometricPrompt`). ADR-007 B96 records the cost: replacing that decision with `if (true)`
 * left every check that existed green. `TimedTierGateTest` drives this class on a plain JVM and
 * that mutation fails it.
 *
 * IT IS ONE PROMPT FOR THE WHOLE TIER, which is a declaration and not an economy.
 * [BiometricPolicy.sharesAuthorizationWith] says the timed operations share an authorization,
 * because they share one Keystore entry and one window by construction -- so what re-opens the
 * window re-opens it for both, and [AuthorizationLedger.endPrompt] propagates the grant along
 * that declaration. A tier that recorded a grant against only the operation in hand would ask for
 * a second fingerprint to type into the session the first one just took control of.
 *
 * WHAT IS NOT ESTABLISHED HERE, and may not be claimed from here: that a real `BiometricPrompt`
 * was shown, accepted or refused, or that a real Keystore refuses a timed key past its window.
 * That is PB-E2E-5, DEFERRED (ADR-007 B31). What this file establishes is the ORDER and the
 * refusals.
 */

/**
 * The platform prompt that re-opens the timed tier, as the gate sees it.
 *
 * NO `CryptoObject`, and that is not a weaker prompt: the content KEK is a TIMED key, so what it
 * needs is a recent authentication of the right class. [BiometricPrompts] states the argument at
 * length. The seam exists so that the ORDER below can be asserted without a handset.
 *
 * Two members rather than two seams, for the reason [PerUsePrompt] gives: they are one question
 * asked of one subsystem, and an availability answer that could disagree with the prompt it gates
 * would be two sources for one fact.
 */
interface TimedTierPrompt {

    fun availability(): PromptAvailability

    fun confirmForContent(onResult: (PromptOutcome) -> Unit)
}

/**
 * What the caller must put in front of the user, as a decision rather than a string.
 *
 * THE WORDING IS NOT CHOSEN HERE. The two tables that hold it -- `ContentUnlockPolicy.adviceFor`
 * and [PerUseRefusalText] -- live in `dev.swarm.phone.runtime` and beside [PerUseGate], and this
 * package must not reach the first of them: `runtime` already depends on `keys`, and a gate that
 * imported its own caller's package back would make the two impossible to read apart. So the gate
 * reports the FACT and the screen picks the sentence, which is the same split every other
 * decision in this module uses.
 */
sealed class TimedTierNotice {

    /**
     * Requirements 6.0's pause: the window lapsed, a prompt is now on screen, and the action is
     * held until it succeeds. It is neither of the two things the requirement forbids -- not
     * silently continued, because the verb is not reached, and not silently dropped, because the
     * action still runs on success.
     */
    data object PausedForFreshness : TimedTierNotice()

    /** The platform will not prompt at all. Nothing was shown and nothing is in flight. */
    data class CannotPrompt(val availability: PromptAvailability) : TimedTierNotice()

    /** A prompt happened and did not authorize. */
    data class Refused(val reason: PerUseRefusalReason) : TimedTierNotice()
}

/**
 * The gate. It holds no state of its own, for the reason [PerUseGate] gives: the only state worth
 * keeping across a `BiometricPrompt` callback is what is in flight, and that belongs to the
 * [AuthorizationLedger] every [InvalidationEvent] already clears. A second copy here would be a
 * second thing for a screen lock to have to reach.
 */
class TimedTierGate(
    private val prompt: TimedTierPrompt,
    private val ledger: AuthorizationLedger,
    private val now: () -> Long = System::currentTimeMillis,
) {

    /**
     * Requirements 6.0's renewal clause: "a typing session crossing the 60 s freshness window
     * must pause input and re-authorize, not silently continue or silently drop; the lease itself
     * is not ended by freshness expiry".
     *
     * THE LEASE IS NOT ENDED, and it is not ended by omission: nothing on this path releases it,
     * which is `InputFreshness.freshnessExpiryEndsLease` being false at the one place that could
     * contradict it.
     *
     * NEVER AUTHORIZED IS ITS OWN BRANCH, because [InputFreshness.decide] cannot say it from two
     * longs: a missing grant is not an old one, and feeding it a zero would make the verdict a
     * property of the epoch. It answers the same way -- an operation the user has not
     * authenticated for is one they are asked to authenticate for -- but it answers deliberately.
     *
     * @throws IllegalArgumentException when [operation] is a PER-USE tier. The tiers are not
     *  interchangeable in either direction: a timed gate over revoke would run it on a
     *  sixty-second-old fingerprint, which is the downgrade ADR-007 B51 found shipped, and
     *  [PerUseGate] refuses the mirror case for the mirror reason.
     */
    fun withFreshAuthorization(
        operation: GatedOperation,
        onNotice: (TimedTierNotice) -> Unit,
        action: () -> Unit,
    ) {
        require(!BiometricPolicy.specFor(operation).requiresCryptoObject) {
            "$operation is a per-use tier (requirements 6.0); a timed gate would run it on an " +
                "authorization up to a minute old, carried by no CryptoObject"
        }

        val granted = ledger.grantedAt(operation)
        if (granted != null && InputFreshness.decide(granted, now()) == InputGateDecision.PROCEED) {
            action()
            return
        }

        onNotice(TimedTierNotice.PausedForFreshness)
        reauthorize(onNotice, action)
    }

    /**
     * The tier's prompt, and the action it is holding. Also the content tier's way back in --
     * ADR-007 B44's missing exit -- which is the same prompt and not a second one: the timed tier
     * IS the content KEK's window ([BiometricPolicy.specFor]).
     */
    fun reauthorize(onNotice: (TimedTierNotice) -> Unit, action: () -> Unit) {
        // ASKED FIRST, AND BEFORE THE LEDGER IS TOUCHED, for the reason PerUseGate states: a
        // handset that cannot prompt must not leave an in-flight marker behind. One that did
        // would refuse every later prompt as concurrent -- the wedge `endPrompt` documents -- on
        // a handset where the user has just been told to go and enrol a fingerprint.
        val availability = prompt.availability()
        if (availability != PromptAvailability.READY) {
            onNotice(TimedTierNotice.CannotPrompt(availability))
            return
        }

        // THE IDENTITY OF THIS PROMPT, and it lives in this closure and nowhere else. It is
        // registered BEFORE the platform is asked to show anything, because the window in which
        // the prompt is on screen is precisely the window in which the screen locks -- and a
        // prompt the ledger does not hold is one `invalidate` cannot clear and one a late
        // callback cannot be refused for. That was ADR-007 B96.
        val ticket = PromptTicket(TIER_PROMPT)
        if (ledger.beginPrompt(TIER_PROMPT, ticket) != GateResolution.PROMPT_STARTED) {
            onNotice(TimedTierNotice.Refused(PerUseRefusalReason.PROMPT_IN_FLIGHT))
            return
        }

        prompt.confirmForContent { outcome ->
            // THE TICKET IS PRESENTED BACK. Without it a callback is discriminated by its
            // operation alone, and both prompts for this tier name the same operation -- so #1's
            // late success resolved against #2, or against an emptied ledger, and authorized the
            // whole tier either way.
            if (ledger.endPrompt(TIER_PROMPT, outcome, now(), ticket) != GateResolution.AUTHORIZED) {
                onNotice(TimedTierNotice.Refused(reasonFor(outcome)))
                return@confirmForContent
            }
            action()
        }
    }

    private fun reasonFor(outcome: PromptOutcome): PerUseRefusalReason = when (outcome) {
        PromptOutcome.CANCELLED -> PerUseRefusalReason.CANCELLED
        PromptOutcome.FAILED -> PerUseRefusalReason.FAILED
        PromptOutcome.LOCKED_OUT -> PerUseRefusalReason.LOCKED_OUT
        PromptOutcome.LOCKED_OUT_PERMANENT -> PerUseRefusalReason.LOCKED_OUT_PERMANENT
        // REACHABLE, and the one row where the outcome does not name the refusal. A SUCCEEDED
        // callback resolves to AUTHORIZED unless the LEDGER refused it, and it refuses exactly
        // one thing: a callback that does not belong to the prompt on screen.
        PromptOutcome.SUCCEEDED -> PerUseRefusalReason.PROMPT_SUPERSEDED
    }

    private companion object {

        /**
         * THE IDENTITY OF THE TIER'S ONE PROMPT, and it is the tier's rather than any member's.
         *
         * A timed prompt is not raised for an operation; it re-opens the ONE window the timed
         * operations share by declaration ([BiometricPolicy.sharesAuthorizationWith]), and
         * [AuthorizationLedger.endPrompt] hands the resulting grant to every operation that
         * shares it. So which member names the in-flight marker changes nothing about who ends up
         * authorized, and registering under the requesting operation would only invite two
         * timed prompts to think they were different prompts -- they are not, and
         * `ConcurrentPromptPolicy.REFUSE_SECOND` refuses the second whatever it is called.
         *
         * DERIVED from the tier assignment rather than named, so an operation moved between tiers
         * cannot leave this pointing at a per-use entry.
         */
        val TIER_PROMPT: GatedOperation =
            GatedOperation.entries.first { !BiometricPolicy.specFor(it).requiresCryptoObject }
    }
}
