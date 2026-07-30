package dev.swarm.phone.keys

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertThrows
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * PB-SEC-2's TIMED tier, at the seam where a prompt is either registered or is not.
 *
 * WHY THIS FILE EXISTS (ADR-007 B96). ADR-007 B63 closed the stale-callback class by giving every
 * prompt an identity minted BEFORE the platform prompt is shown -- [PromptTicket] -- so that an
 * invalidation has something to find and a queued callback can be refused for not belonging to
 * the prompt on screen. That fix reached [PerUseGate] and stopped there.
 * `PhoneSurface.reauthorizeTimedTier` called `BiometricPrompts.confirmForContent` with NO TICKET
 * REGISTERED: the ledger entry was created INSIDE the callback, by a `beginPrompt`/`endPrompt`
 * pair that ran back to back on arrival. `promptForContent` had the same shape.
 *
 * So for the whole time the timed tier's prompt was actually on screen -- which is exactly when
 * the screen locks -- the ledger held nothing. `AuthorizationLedger.invalidate` emptied a ledger
 * the prompt was never in, the prompt survived behind the keyguard (nothing calls
 * `BiometricPrompt.cancelAuthentication`, and the callback lands on the main executor), and the
 * late success MINTED A FRESH SIXTY-SECOND AUTHORIZATION FOR BOTH TIMED OPERATIONS and ran the
 * action it was holding -- after the lock ADR-007 B44 says destroyed that authority.
 *
 * EVERY ASSERTION BELOW IS ABOUT ORDER, and that is the point. The defect was invisible to a
 * policy test because the policy was already right: `BiometricPolicy.resolve` returned AUTHORIZED
 * for a success, `AuthorizationLedger.invalidate` really did clear everything, and
 * `InputFreshness.decide` returned the correct verdict at 59_999 ms and at 60_000 ms. What was
 * wrong was WHEN the ledger was told, and nothing asked.
 *
 * WHAT IS MODELLED AND WHAT IS NOT, said here because this is a file that could be misread as
 * biometric coverage.
 *
 *  - MODELLED: the lifecycle. The gate consults a [TimedTierPrompt]; it is a seam, and the tests
 *    drive it -- including holding a prompt unanswered, which is the whole state under test.
 *  - NOT MODELLED, AND NOT CLAIMED ANYWHERE: that a real `BiometricPrompt` was shown, accepted or
 *    refused; that a real Keystore refuses a timed key past its window. That is PB-E2E-5,
 *    DEFERRED (ADR-007 B31), and ADR-007 B56 makes the whole `androidTest` tier unexecutable
 *    besides.
 */
class TimedTierGateTest {

    // ---------------------------------------------------------------------
    // Fixtures.
    // ---------------------------------------------------------------------

    /**
     * The platform, as the gate sees it.
     *
     * IT HOLDS EVERY CALLBACK RATHER THAN ANSWERING, because an answered prompt is not the state
     * this file is about. A real `BiometricPrompt` is on screen for as long as the user takes,
     * and every defect here lives in that window: a lock arrives, a second request arrives, and
     * the callback lands afterwards. [answer] is what ends it, one prompt at a time, so a test
     * can resolve them out of order the way a superseded prompt really does.
     */
    private class FakePrompt(
        var availability: PromptAvailability = PromptAvailability.READY,
    ) : TimedTierPrompt {

        val pending = mutableListOf<(PromptOutcome) -> Unit>()
        var shown = 0

        override fun availability(): PromptAvailability = availability

        override fun confirmForContent(onResult: (PromptOutcome) -> Unit) {
            shown++
            pending += onResult
        }

        fun answer(index: Int, outcome: PromptOutcome) = pending[index](outcome)
    }

    private class Run {
        var ran = 0
        val notices = mutableListOf<TimedTierNotice>()
    }

    private var clock = 0L

    private fun gate(prompt: TimedTierPrompt, ledger: AuthorizationLedger) =
        TimedTierGate(prompt = prompt, ledger = ledger, now = { clock })

    /** The timed operations, derived from the tier assignment rather than restated. */
    private val timedOps = GatedOperation.entries.filter {
        !BiometricPolicy.specFor(it).requiresCryptoObject
    }

    // ---------------------------------------------------------------------
    // ADR-007 B96: the prompt is registered BEFORE it is shown.
    // ---------------------------------------------------------------------

    /**
     * THE assertion this whole file is for.
     *
     * While the platform prompt is unanswered the ledger must already hold it in flight. That is
     * observable without reaching inside: `AuthorizationLedger.beginPrompt` refuses a second
     * prompt while one is marked, and `ConcurrentPromptPolicy.REFUSE_SECOND` is the policy it
     * enforces. If the registration happens inside the callback -- the shape ADR-007 B96 found --
     * the ledger is empty here and this refusal does not happen.
     *
     * The precondition that the platform WAS asked is asserted too: a gate that showed no prompt
     * at all would satisfy the refusal by doing nothing, which is a fence proving nothing.
     */
    @Test
    fun the_prompt_is_in_flight_with_the_ledger_before_the_platform_answers() {
        val ledger = AuthorizationLedger()
        val prompt = FakePrompt()
        val run = Run()

        gate(prompt, ledger).reauthorize(onNotice = { run.notices += it }) { run.ran++ }

        assertEquals("precondition: the platform prompt is on screen", 1, prompt.shown)
        assertEquals("nothing runs until the prompt answers", 0, run.ran)
        assertEquals(
            "the prompt on screen is registered nowhere: an invalidation arriving now has " +
                "nothing to find, which is ADR-007 B96",
            GateResolution.REFUSED_PROMPT_IN_FLIGHT,
            ledger.beginPrompt(GatedOperation.REVOKE),
        )
    }

    /**
     * The reachable sequence, in order: prompt on screen, screen locks, finger accepted.
     *
     * `ContentLockTriggers` invalidates on ACTION_SCREEN_OFF; nothing cancels the platform prompt;
     * the callback lands on the main executor afterwards. Before the fix that callback minted a
     * fresh sixty-second authorization for the whole tier and ran the held action.
     */
    @Test
    fun an_invalidation_while_the_prompt_is_on_screen_refuses_its_late_success() {
        val ledger = AuthorizationLedger()
        val prompt = FakePrompt()
        val run = Run()

        gate(prompt, ledger).reauthorize(onNotice = { run.notices += it }) { run.ran++ }
        ledger.invalidate(InvalidationEvent.DEVICE_LOCKED)
        clock = 5_000
        prompt.answer(0, PromptOutcome.SUCCEEDED)

        assertEquals("the action ran on authority the lock destroyed", 0, run.ran)
        assertEquals(
            listOf(TimedTierNotice.Refused(PerUseRefusalReason.PROMPT_SUPERSEDED)),
            run.notices,
        )
        for (operation in timedOps) {
            assertFalse(
                "$operation was authorized by a callback belonging to a prompt the lock ended",
                ledger.authorized(operation, atMillis = clock),
            )
        }
    }

    /** Every event, not just the lock: PB-SEC-2 names invalidation as a clause of its own. */
    @Test
    fun every_invalidation_event_refuses_a_prompt_that_was_on_screen_when_it_arrived() {
        for (event in InvalidationEvent.entries) {
            val ledger = AuthorizationLedger()
            val prompt = FakePrompt()
            val run = Run()

            gate(prompt, ledger).reauthorize(onNotice = { run.notices += it }) { run.ran++ }
            ledger.invalidate(event)
            prompt.answer(0, PromptOutcome.SUCCEEDED)

            assertEquals("$event left the held action runnable", 0, run.ran)
            for (operation in timedOps) {
                assertFalse(
                    "$event left $operation authorized by a late callback",
                    ledger.authorized(operation, atMillis = clock),
                )
            }
        }
    }

    /**
     * SAME-OPERATION SUPERSESSION, which is the half no field on the ledger could tell apart:
     * both prompts are the timed tier's, so `inFlight.operation == operation` is true of both.
     * Only the ticket separates them.
     *
     * The second half of this test is what stops the fix being "refuse everything": prompt #2 is
     * the one genuinely on screen, and it must still authorize.
     */
    @Test
    fun a_late_callback_from_a_superseded_prompt_does_not_resolve_the_one_on_screen() {
        val ledger = AuthorizationLedger()
        val prompt = FakePrompt()
        val first = Run()
        val second = Run()
        val gate = gate(prompt, ledger)

        gate.reauthorize(onNotice = { first.notices += it }) { first.ran++ }
        ledger.invalidate(InvalidationEvent.DEVICE_LOCKED)
        gate.reauthorize(onNotice = { second.notices += it }) { second.ran++ }
        assertEquals("precondition: two prompts have been shown", 2, prompt.shown)

        prompt.answer(0, PromptOutcome.SUCCEEDED)
        assertEquals("the superseded prompt ran its action", 0, first.ran)
        assertEquals(
            listOf(TimedTierNotice.Refused(PerUseRefusalReason.PROMPT_SUPERSEDED)),
            first.notices,
        )
        assertEquals("the superseded callback resolved the prompt on screen", 0, second.ran)

        clock = 7_000
        prompt.answer(1, PromptOutcome.SUCCEEDED)
        assertEquals("the prompt actually on screen must still authorize", 1, second.ran)
        assertTrue(ledger.authorized(GatedOperation.INPUT, atMillis = clock))
    }

    /**
     * `BiometricPrompt` does not queue. Before the fix this path never asked the ledger, so a
     * per-use prompt on screen did not stop a second, timed prompt being raised over it.
     */
    @Test
    fun a_second_prompt_while_one_is_in_flight_is_refused_and_the_platform_is_not_asked_again() {
        val ledger = AuthorizationLedger()
        val prompt = FakePrompt()
        val run = Run()
        val gate = gate(prompt, ledger)

        gate.reauthorize(onNotice = { run.notices += it }) { run.ran++ }
        gate.reauthorize(onNotice = { run.notices += it }) { run.ran++ }

        assertEquals("a second BiometricPrompt was raised over the first", 1, prompt.shown)
        assertEquals(
            listOf(TimedTierNotice.Refused(PerUseRefusalReason.PROMPT_IN_FLIGHT)),
            run.notices,
        )
    }

    /** And a per-use prompt in flight refuses the timed one, which is the same policy from the other side. */
    @Test
    fun a_per_use_prompt_in_flight_refuses_the_timed_prompt() {
        val ledger = AuthorizationLedger()
        val prompt = FakePrompt()
        val run = Run()
        ledger.beginPrompt(GatedOperation.REVOKE, PromptTicket(GatedOperation.REVOKE))

        gate(prompt, ledger).reauthorize(onNotice = { run.notices += it }) { run.ran++ }

        assertEquals(0, prompt.shown)
        assertEquals(
            listOf(TimedTierNotice.Refused(PerUseRefusalReason.PROMPT_IN_FLIGHT)),
            run.notices,
        )
    }

    // ---------------------------------------------------------------------
    // Requirements 6.0's renewal clause, at the gate that performs it.
    // ---------------------------------------------------------------------

    /**
     * The window is real, at both of its edges.
     *
     * THE MUTATION THIS EXISTS FOR: replacing the freshness decision with `if (true)`, which is
     * the mutation ADR-007 B96 records as surviving every check that existed. It runs the action
     * with no prompt at 60_000 ms, and the second half of this test fails.
     */
    @Test
    fun the_action_runs_without_a_prompt_inside_the_window_and_not_outside_it() {
        val ledger = AuthorizationLedger()
        ledger.beginPrompt(GatedOperation.INPUT, PromptTicket(GatedOperation.INPUT))
        ledger.endPrompt(GatedOperation.INPUT, PromptOutcome.SUCCEEDED, atMillis = 0, ticket = null)

        val prompt = FakePrompt()
        val inside = Run()
        clock = 59_999
        gate(prompt, ledger).withFreshAuthorization(
            GatedOperation.INPUT,
            onNotice = { inside.notices += it },
        ) { inside.ran++ }

        assertEquals("a fresh authorization must not ask for a second finger", 0, prompt.shown)
        assertEquals(1, inside.ran)
        assertEquals(emptyList<TimedTierNotice>(), inside.notices)

        val outside = Run()
        clock = 60_000
        gate(prompt, ledger).withFreshAuthorization(
            GatedOperation.INPUT,
            onNotice = { outside.notices += it },
        ) { outside.ran++ }

        assertEquals("60 s is the window, so 60 s is where it ends", 1, prompt.shown)
        assertEquals(
            "6.0: pause and re-authorize, which is neither continuing nor dropping",
            listOf<TimedTierNotice>(TimedTierNotice.PausedForFreshness),
            outside.notices,
        )
        assertEquals("the held action must not run until the prompt succeeds", 0, outside.ran)

        prompt.answer(0, PromptOutcome.SUCCEEDED)
        assertEquals("the paused action must run once re-authorized", 1, outside.ran)
    }

    /**
     * A missing grant is not an old one. `InputFreshness.decide` cannot say so from two longs, and
     * feeding it a zero would make the verdict a property of the epoch.
     */
    @Test
    fun an_operation_that_was_never_authorized_pauses_rather_than_proceeding() {
        val ledger = AuthorizationLedger()
        val prompt = FakePrompt()
        val run = Run()
        clock = 1_000

        gate(prompt, ledger).withFreshAuthorization(
            GatedOperation.INPUT,
            onNotice = { run.notices += it },
        ) { run.ran++ }

        assertEquals(1, prompt.shown)
        assertEquals(0, run.ran)
    }

    /**
     * ONE prompt for the tier, because the tier shares ONE authorization by declaration
     * (`BiometricPolicy.sharesAuthorizationWith`): one Keystore entry, one window. A grant
     * recorded against only the operation in hand asks for a second fingerprint to type into the
     * session the first one just took control of.
     */
    @Test
    fun one_successful_prompt_authorizes_every_operation_that_shares_the_window() {
        val ledger = AuthorizationLedger()
        val prompt = FakePrompt()
        val run = Run()
        clock = 2_000

        gate(prompt, ledger).reauthorize(onNotice = { run.notices += it }) { run.ran++ }
        prompt.answer(0, PromptOutcome.SUCCEEDED)

        assertEquals(1, run.ran)
        for (operation in timedOps) {
            assertTrue(
                "$operation shares the window that was just re-opened and is not authorized",
                ledger.authorized(operation, atMillis = clock),
            )
        }
        for (operation in GatedOperation.entries.filter { it !in timedOps }) {
            assertFalse(
                "a 60 s typing authorization must not reach $operation",
                ledger.authorized(operation, atMillis = clock),
            )
        }
    }

    /** And the shared grant is what the freshness check then reads, without a second prompt. */
    @Test
    fun the_shared_grant_satisfies_the_other_timed_operation_without_prompting_again() {
        val ledger = AuthorizationLedger()
        val prompt = FakePrompt()
        val gate = gate(prompt, ledger)
        val run = Run()

        gate.reauthorize(onNotice = { run.notices += it }) { run.ran++ }
        prompt.answer(0, PromptOutcome.SUCCEEDED)

        clock = 30_000
        val typed = Run()
        gate.withFreshAuthorization(
            GatedOperation.TAKE_CONTROL,
            onNotice = { typed.notices += it },
        ) { typed.ran++ }

        assertEquals("the tier shares one window; this asked for a second finger", 1, prompt.shown)
        assertEquals(1, typed.ran)
    }

    /**
     * The tiers are not interchangeable in either direction. [PerUseGate] refuses a timed
     * operation for the mirror reason: a per-use gate over input would prompt per keystroke.
     */
    @Test
    fun the_timed_gate_refuses_a_per_use_operation() {
        val ledger = AuthorizationLedger()
        val gate = gate(FakePrompt(), ledger)
        for (operation in GatedOperation.entries.filter { it !in timedOps }) {
            assertThrows(IllegalArgumentException::class.java) {
                gate.withFreshAuthorization(operation, onNotice = {}) {}
            }
        }
    }

    // ---------------------------------------------------------------------
    // The refusals, and not wedging the gate on any of them.
    // ---------------------------------------------------------------------

    /**
     * A handset that cannot prompt must not leave an in-flight marker behind. One that did would
     * refuse every later prompt as concurrent -- the wedge `AuthorizationLedger.endPrompt`
     * documents -- on a handset where the user has just been told to go and enrol a fingerprint.
     */
    @Test
    fun a_platform_that_cannot_prompt_shows_nothing_and_leaves_nothing_in_flight() {
        for (availability in PromptAvailability.entries.filter { it != PromptAvailability.READY }) {
            val ledger = AuthorizationLedger()
            val prompt = FakePrompt(availability = availability)
            val run = Run()

            gate(prompt, ledger).reauthorize(onNotice = { run.notices += it }) { run.ran++ }

            assertEquals("$availability: a prompt was raised anyway", 0, prompt.shown)
            assertEquals(0, run.ran)
            assertEquals(listOf<TimedTierNotice>(TimedTierNotice.CannotPrompt(availability)), run.notices)
            assertEquals(
                "$availability wedged the gate: no prompt can ever start again",
                GateResolution.PROMPT_STARTED,
                ledger.beginPrompt(GatedOperation.REVOKE),
            )
        }
    }

    /**
     * Every non-success outcome refuses the action, keeps the grant unmade, and leaves the gate
     * able to prompt again. A resolution path that cleared the in-flight marker only on success
     * wedges on the first cancel.
     */
    @Test
    fun no_failed_outcome_runs_the_action_or_wedges_the_gate() {
        for (outcome in PromptOutcome.entries.filter { it != PromptOutcome.SUCCEEDED }) {
            val ledger = AuthorizationLedger()
            val prompt = FakePrompt()
            val run = Run()
            val gate = gate(prompt, ledger)

            gate.reauthorize(onNotice = { run.notices += it }) { run.ran++ }
            prompt.answer(0, outcome)

            assertEquals("$outcome ran the action", 0, run.ran)
            for (operation in timedOps) {
                assertFalse(
                    "$outcome left $operation authorized",
                    ledger.authorized(operation, atMillis = clock),
                )
            }
            gate.reauthorize(onNotice = { run.notices += it }) { run.ran++ }
            assertEquals("$outcome wedged the gate: no prompt can start again", 2, prompt.shown)
        }
    }

    /**
     * The refusal reaches the user in words, and they are the per-use gate's words. The two
     * prompts fail for the same platform reasons and a second wording table would drift from the
     * first on the first edit; PB-APP-9 requires every refusal name something to do about it.
     */
    @Test
    fun every_refusal_the_timed_gate_reports_carries_a_remedy() {
        val ledger = AuthorizationLedger()
        val prompt = FakePrompt()
        val run = Run()
        val gate = gate(prompt, ledger)

        for (outcome in PromptOutcome.entries.filter { it != PromptOutcome.SUCCEEDED }) {
            gate.reauthorize(onNotice = { run.notices += it }) { run.ran++ }
            prompt.answer(prompt.shown - 1, outcome)
        }

        assertEquals(PromptOutcome.entries.size - 1, run.notices.size)
        for (notice in run.notices) {
            val refused = notice as TimedTierNotice.Refused
            assertTrue(
                "${refused.reason} has no message for the user",
                PerUseRefusalText.messageFor(refused.reason).isNotEmpty(),
            )
            assertTrue(
                "${refused.reason} tells the user nothing they can do, and this prompt is one " +
                    "the user can always retry",
                PerUseRefusalText.remedyFor(refused.reason) != PerUseRemedy.NONE,
            )
        }
    }
}
