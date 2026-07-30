package dev.swarm.phone.keys

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test
import javax.crypto.Cipher
import javax.crypto.KeyGenerator

/**
 * PB-SEC-2's invalidation clause in the one window nothing covered: while a prompt is still
 * OUTSTANDING.
 *
 * WHY THIS FILE EXISTS, AND WHY THE EXISTING TESTS DO NOT COVER IT. Every invalidation assertion
 * in `BiometricGateTest` and `ContentLockTest` follows the same shape --
 * `beginPrompt` / `endPrompt` / `invalidate` -- so the authorization being destroyed is one that
 * was already COMPLETE when the event arrived. `AuthorizationLedger.endPrompt` had nothing left to
 * do by then. Not one of them starts a prompt, invalidates while it is still on screen, and only
 * then delivers the callback. That ordering is the whole defect: `endPrompt` records an outcome
 * against whatever operation the caller names, without asking whether a prompt for it is actually
 * active, so a callback that arrives after an invalidation writes a fresh grant into a ledger the
 * lock had just emptied.
 *
 * IT IS NOT A RACE THAT HAS TO BE WON. `ContentLockTriggers` invalidates on `ACTION_SCREEN_OFF`
 * and on the started-Activity count reaching zero, and neither path cancels an outstanding
 * `BiometricPrompt` -- nothing in the app calls `cancelAuthentication`. So a user who taps kill,
 * locks the handset with the prompt still up, unlocks it and then puts their finger on the sensor
 * walks the sequence deterministically: screen-off invalidates, the prompt survives, and its
 * callback resurrects the authorization ADR-007 B44 says a lock returns to LOCKED.
 *
 * WHAT IS MODELLED AND WHAT IS NOT. These are JVM policy tests over the ledger's state machine and
 * over [PerUseGate]'s ordering, exactly like the files beside them. Nothing here claims a real
 * prompt was shown, a real finger accepted, or a real Keystore key withheld -- that is PB-E2E-5,
 * DEFERRED (ADR-007 B31).
 */
class StalePromptCallbackTest {

    // ---------------------------------------------------------------------
    // The ledger, at the seam where the stale callback lands.
    // ---------------------------------------------------------------------

    /**
     * The reported defect, minimally. The prompt is on screen when the handset locks, and the
     * callback lands afterwards.
     *
     * `invalidate` cleared both the grants and the in-flight marker, so by the time this callback
     * arrives there is no prompt for KILL and no authorization for it. Recording a grant here is
     * creating an authorization out of an event the ledger has already disowned.
     */
    @Test
    fun a_callback_delivered_after_a_lock_does_not_resurrect_authorization() {
        val ledger = AuthorizationLedger()
        assertEquals(
            "precondition: the prompt is on screen",
            GateResolution.PROMPT_STARTED,
            ledger.beginPrompt(GatedOperation.KILL),
        )

        ledger.invalidate(InvalidationEvent.DEVICE_LOCKED)

        ledger.endPrompt(GatedOperation.KILL, PromptOutcome.SUCCEEDED, atMillis = 1_000, ticket = null)

        assertFalse(
            "the lock returned the tier to LOCKED (ADR-007 B44) and the outstanding prompt's " +
                "callback put the authorization back",
            ledger.authorized(GatedOperation.KILL, atMillis = 1_000),
        )
    }

    /**
     * The same, for every event and both tiers -- the loop `BiometricGateTest` already runs, moved
     * into the window where the prompt has not resolved yet.
     *
     * PB-SEC-2 names invalidation as a clause of its own and does not qualify it by what happens
     * to be on screen at the time. An event that drops a finished authorization but not an
     * unfinished one has not invalidated anything: the user need only leave the prompt up.
     */
    @Test
    fun no_invalidation_event_is_survived_by_an_outstanding_prompts_callback() {
        for (event in InvalidationEvent.entries) {
            for (operation in GatedOperation.entries) {
                val ledger = AuthorizationLedger()
                ledger.beginPrompt(operation)

                ledger.invalidate(event)
                ledger.endPrompt(operation, PromptOutcome.SUCCEEDED, atMillis = 0, ticket = null)

                assertFalse(
                    "$event was survived by an outstanding $operation prompt",
                    ledger.authorized(operation, atMillis = 0),
                )
            }
        }
    }

    /**
     * The floor under the two above: a callback with NO prompt behind it at all.
     *
     * `endPrompt` is reached from exactly one asynchronous place -- `PerUseGate`'s
     * `prompt.show { ... }` closure, which captures the operation it was built for -- so a
     * callback naming an operation the ledger is not prompting for is by definition one whose
     * prompt the ledger has already forgotten. It must authorize nothing, and it must not disturb
     * whatever IS on screen.
     */
    @Test
    fun a_callback_with_no_prompt_behind_it_authorizes_nothing() {
        val virgin = AuthorizationLedger()
        virgin.endPrompt(GatedOperation.REVOKE, PromptOutcome.SUCCEEDED, atMillis = 1_000, ticket = null)
        assertFalse(
            "a ledger that never prompted for REVOKE authorized it",
            virgin.authorized(GatedOperation.REVOKE, atMillis = 1_000),
        )

        // And with a different prompt genuinely in flight, which is the state a second gated
        // action reaches: the callback must neither authorize itself nor clear the live marker.
        val busy = AuthorizationLedger()
        busy.beginPrompt(GatedOperation.KILL)

        busy.endPrompt(GatedOperation.REVOKE, PromptOutcome.SUCCEEDED, atMillis = 1_000, ticket = null)

        assertFalse(
            "a callback for REVOKE authorized REVOKE while the prompt on screen was KILL's",
            busy.authorized(GatedOperation.REVOKE, atMillis = 1_000),
        )
        assertEquals(
            "KILL's prompt is still on screen; a third must still be refused",
            GateResolution.REFUSED_PROMPT_IN_FLIGHT,
            busy.beginPrompt(GatedOperation.LAUNCH),
        )
    }

    /**
     * A late duplicate of a prompt that has ALREADY resolved, delivered while a newer prompt is up.
     *
     * The newer prompt is what makes this expressible rather than ambiguous: KILL is demonstrably
     * not the operation on screen, so this callback can only be the resolved prompt's. It must not
     * reach past the cancel that already answered it, and it must not clear or satisfy the marker
     * belonging to the prompt that IS outstanding.
     */
    @Test
    fun a_late_duplicate_of_a_resolved_prompt_neither_authorizes_it_nor_disturbs_the_newer_one() {
        val ledger = AuthorizationLedger()
        ledger.beginPrompt(GatedOperation.KILL)
        ledger.endPrompt(GatedOperation.KILL, PromptOutcome.CANCELLED, atMillis = 1_000, ticket = null)
        assertFalse(
            "precondition: the cancel answered KILL",
            ledger.authorized(GatedOperation.KILL, atMillis = 1_000),
        )

        ledger.beginPrompt(GatedOperation.REVOKE)

        ledger.endPrompt(GatedOperation.KILL, PromptOutcome.SUCCEEDED, atMillis = 2_000, ticket = null)

        assertFalse(
            "a duplicate callback overturned the cancel the user already gave",
            ledger.authorized(GatedOperation.KILL, atMillis = 2_000),
        )
        assertFalse(
            "and it must not have answered the prompt that is actually on screen",
            ledger.authorized(GatedOperation.REVOKE, atMillis = 2_000),
        )
        assertEquals(
            "REVOKE's prompt is still on screen; the stale callback cleared its marker",
            GateResolution.REFUSED_PROMPT_IN_FLIGHT,
            ledger.beginPrompt(GatedOperation.LAUNCH),
        )
    }

    /**
     * THE CONTROL, and it is not decoration. Every assertion above is that some callback does NOT
     * authorize; a ledger that authorized nothing at all would pass all of them. This is the
     * ordinary path -- a prompt started, its own callback delivered -- for every operation, plus
     * the one synchronous `endPrompt` `PerUseGate` performs when Keystore refuses a cipher, which
     * must go on clearing the marker or the gate wedges shut for the life of the process.
     */
    @Test
    fun the_current_prompts_own_callback_still_authorizes_and_still_clears_the_marker() {
        for (operation in GatedOperation.entries) {
            val ledger = AuthorizationLedger()
            assertEquals(GateResolution.PROMPT_STARTED, ledger.beginPrompt(operation))
            assertEquals(
                "$operation's own callback must still be honoured",
                GateResolution.AUTHORIZED,
                ledger.endPrompt(operation, PromptOutcome.SUCCEEDED, atMillis = 1_000, ticket = null),
            )
            assertTrue(ledger.authorized(operation, atMillis = 1_000))
            assertEquals(
                "$operation's marker was not cleared by its own callback; the gate is wedged",
                GateResolution.PROMPT_STARTED,
                ledger.beginPrompt(operation),
            )
        }

        // `PerUseGate`'s cipher-failure path: beginPrompt succeeded, Keystore then refused, and
        // the gate reports FAILED through the ledger rather than reaching into it.
        val refused = AuthorizationLedger()
        refused.beginPrompt(GatedOperation.REVOKE)
        refused.endPrompt(GatedOperation.REVOKE, PromptOutcome.FAILED, atMillis = 1_000, ticket = null)
        assertEquals(
            "the gate is wedged: a Keystore refusal left the marker behind",
            GateResolution.PROMPT_STARTED,
            refused.beginPrompt(GatedOperation.REVOKE),
        )
    }

    // ---------------------------------------------------------------------
    // The gate, where the resurrected authorization actually runs something.
    // ---------------------------------------------------------------------

    /**
     * What the resurrection is WORTH, at the seam that performs the operation.
     *
     * The ledger assertions above are about a map. This one is about a session being killed: the
     * prompt is on screen when the handset locks, `ContentLock` invalidates, and the callback
     * lands afterwards carrying a cipher the platform is perfectly willing to operate. `PerUseGate`
     * asks the ledger exactly once -- `endPrompt(...) != AUTHORIZED` -- so a ledger that answers
     * AUTHORIZED here runs the action.
     *
     * THE FENCE BELONGS IN THE LEDGER AND NOT IN A PROMPT CANCELLATION. This test delivers the
     * callback itself, so an implementation that instead cancelled the outstanding prompt on
     * invalidation would still fail here -- which is the point. The platform delivering a callback
     * the app did not expect is precisely the case being fenced, and an app that relies on the
     * callback not arriving has not fenced it.
     *
     * The refusal REASON is deliberately not asserted: which one a stale callback earns is the
     * implementer's decision, and pinning it here would make this test a specification of the fix
     * rather than of the defect. That the caller is told SOMETHING is asserted, because a gate that
     * silently drops the action is PB-APP-9's failure by another road.
     */
    @Test
    fun a_lock_while_the_prompt_is_on_screen_does_not_let_the_late_callback_run_the_action() {
        val ledger = AuthorizationLedger()
        val prompt = HeldPrompt()
        val run = Run()

        gate(prompt, ledger).authorize(
            GatedOperation.KILL,
            onRefused = { run.refusals += it },
        ) { run.ran++ }

        assertEquals("precondition: the prompt is still on screen", 0, run.ran)
        assertTrue("precondition: nothing has been refused yet", run.refusals.isEmpty())

        // The handset locks with the prompt still up. Nothing cancels it.
        ledger.invalidate(InvalidationEvent.DEVICE_LOCKED)

        // The finger lands afterwards, and the platform releases a working key.
        prompt.deliver()

        assertEquals(
            "the session was killed on an authorization the screen lock had already destroyed",
            0,
            run.ran,
        )
        assertEquals(
            "the caller was told nothing at all about the action it asked for",
            1,
            run.refusals.size,
        )
    }

    /**
     * The control for the gate assertion: the same gate, the same held callback, no invalidation
     * in between. Without this, a gate that refused every late callback -- or every callback --
     * would pass the test above.
     */
    @Test
    fun the_ordinary_deferred_callback_still_runs_the_action() {
        val ledger = AuthorizationLedger()
        val prompt = HeldPrompt()
        val run = Run()

        gate(prompt, ledger).authorize(
            GatedOperation.KILL,
            onRefused = { run.refusals += it },
        ) { run.ran++ }

        prompt.deliver()

        assertEquals("the undisturbed per-use path must still run the action", 1, run.ran)
        assertTrue("and must refuse nothing: $run.refusals", run.refusals.isEmpty())
    }

    // ---------------------------------------------------------------------
    // Fixtures.
    // ---------------------------------------------------------------------

    private class Run {
        var ran = 0
        val refusals = mutableListOf<PerUseRefusal>()
    }

    /**
     * A prompt that HOLDS its callback instead of answering inline, so a test can put an event
     * between `show` and the result. `BiometricPrompts` answers on `activity.mainExecutor`, so
     * holding it is the ordinary case rather than a contrived one.
     *
     * It releases the cipher it was given, which is a working one: the released-key check in
     * `PerUseGate` must not be what refuses, or the assertion would be about the cipher rather
     * than about the stale callback.
     */
    private class HeldPrompt : PerUsePrompt {

        private var held: (() -> Unit)? = null

        override fun availability(): PromptAvailability = PromptAvailability.READY

        override fun show(
            operation: GatedOperation,
            cipher: Cipher,
            onResult: (PromptOutcome, Cipher?) -> Unit,
        ) {
            held = { onResult(PromptOutcome.SUCCEEDED, cipher) }
        }

        fun deliver() = checkNotNull(held) { "no prompt was ever shown" }.invoke()
    }

    private fun gate(prompt: PerUsePrompt, ledger: AuthorizationLedger) = PerUseGate(
        prompt = prompt,
        ciphers = { liveCipher() },
        ledger = ledger,
        now = { 1_000L },
    )

    /** A plain JVM AES cipher: the platform released a key and it works. */
    private fun liveCipher(): Cipher {
        val key = KeyGenerator.getInstance("AES").apply { init(256) }.generateKey()
        return Cipher.getInstance("AES/GCM/NoPadding").apply { init(Cipher.ENCRYPT_MODE, key) }
    }
}
