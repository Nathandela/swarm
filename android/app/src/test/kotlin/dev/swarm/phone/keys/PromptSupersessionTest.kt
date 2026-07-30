package dev.swarm.phone.keys

import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test
import javax.crypto.Cipher
import javax.crypto.KeyGenerator

/**
 * PB-SEC-2's remaining prompt-identity half: SAME-OPERATION SUPERSESSION.
 *
 * WHAT IS ALREADY CLOSED, so this file is not read as re-fencing it. ADR-007 B63 landed the
 * refusal in [AuthorizationLedger.endPrompt]: a callback that does not belong to the prompt on
 * screen authorizes nothing, and `StalePromptCallbackTest` holds it. That fence discriminates on
 * the OPERATION -- `inFlight != operation` -- so it catches a stale callback for KILL while REVOKE
 * is up, and it catches a callback arriving into a ledger an invalidation has emptied.
 *
 * WHAT IT CANNOT CATCH, and what this file is pointed at. Two prompts for the SAME operation are
 * indistinguishable at that check, because `inFlight == operation` is true of BOTH of them.
 * ADR-007 B63 records the sequence in as many words: prompt #1 for KILL outstanding, an
 * invalidation, prompt #2 for KILL begun, then #1's late callback -- which still resolves against
 * #2. It is reachable rather than theoretical: `ContentLockTriggers` invalidates on
 * `ACTION_SCREEN_OFF`, nothing in the app calls `BiometricPrompt.cancelAuthentication`, and
 * `BiometricPrompts.show` answers on the main executor, so the first prompt survives the keyguard
 * and lands afterwards while the user is looking at a second one.
 *
 * THE PROPERTY, AND WHY IT IS PINNED HERE RATHER THAN AT THE LEDGER. What must hold is that A
 * CALLBACK RESOLVES ONLY AGAINST THE PROMPT THAT PRODUCED IT. That property cannot be stated at
 * `endPrompt`'s own signature without inventing the identity it is missing -- a token, a
 * generation, a handle -- and a test that named one would be a specification of the fix rather
 * than of the defect. [PerUseGate] is the seam where it IS expressible without naming anything:
 * the gate is the production caller, it builds one callback per prompt, and the observable is
 * whether the ACTION that callback belongs to runs. Whatever carries prompt identity, and
 * wherever the implementer chooses to keep it, these assertions read the same.
 *
 * A CANCELLATION-BASED FIX DOES NOT SATISFY THIS FILE, deliberately, for the reason
 * `StalePromptCallbackTest` already gives: the tests DELIVER the superseded callback themselves.
 * The platform delivering a callback the app did not expect is the case being fenced, and an app
 * that relies on the callback not arriving has not fenced it.
 *
 * WHAT IS MODELLED AND WHAT IS NOT. These are JVM policy tests over the ledger's state machine
 * and the gate's ordering, like every file beside them. Nothing here claims a real
 * `BiometricPrompt` appeared, a real finger was accepted, or a real Keystore withheld a real key:
 * that is PB-E2E-5, DEFERRED (ADR-007 B31).
 */
class PromptSupersessionTest {

    /**
     * THE DEFECT. The superseded prompt's callback runs the operation it was issued for, on an
     * authorization the invalidation destroyed.
     *
     * The two prompts are for the SAME operation, which is the whole point: nothing in the
     * callback distinguishes them, so #1's success is spent as though it were #2's. What runs is a
     * KILL the user asked for before their handset locked, authorized by a finger they put on the
     * sensor for a DIFFERENT request afterwards -- or, with the handset in someone else's hands
     * between the two, by nobody's request at all.
     */
    @Test
    fun a_superseded_prompts_callback_does_not_run_the_action_it_belongs_to() {
        val ledger = AuthorizationLedger()
        val prompts = QueuedPrompt()
        val first = Run()
        val second = Run()
        val gate = gate(prompts, ledger)

        gate.authorize(GatedOperation.KILL, onRefused = { first.refusals += it }) { first.ran++ }
        assertEquals("precondition: the first prompt is on screen", 1, prompts.outstanding())

        // The handset locks with prompt #1 still up. Nothing cancels it: the app calls
        // `cancelAuthentication` nowhere, so it survives behind the keyguard.
        ledger.invalidate(InvalidationEvent.DEVICE_LOCKED)

        // The user unlocks and asks for the same thing again. This one must START -- the
        // invalidation cleared the in-flight marker -- or the sequence under test never arises.
        gate.authorize(GatedOperation.KILL, onRefused = { second.refusals += it }) { second.ran++ }
        assertEquals(
            "precondition: the second prompt was refused, so nothing is being superseded here. " +
                "Refusals: ${second.refusals.map { it.reason }}",
            2,
            prompts.outstanding(),
        )

        // Prompt #1's finger lands, and the platform releases a working key for it.
        prompts.deliver(0)

        assertEquals(
            "PB-SEC-2: the SUPERSEDED prompt's callback ran its action. Prompt #1 was destroyed " +
                "by the screen lock, and its late callback resolved against the prompt that " +
                "replaced it -- the two are for the same operation, so nothing in the callback " +
                "tells them apart. A callback must resolve only against the prompt that produced it",
            0,
            first.ran,
        )
        assertEquals(
            "PB-SEC-2: the superseded callback ran the NEWER request's action. The user's finger " +
                "has not reached the sensor yet; prompt #2 is still on screen",
            0,
            second.ran,
        )
        assertTrue(
            "the caller of the superseded prompt was told nothing at all about the action it " +
                "asked for, which is PB-APP-9's silent failure",
            first.refusals.isNotEmpty(),
        )
    }

    /**
     * THE OTHER HALF, and it is a live defect rather than the mirror of the one above: the
     * superseded callback SPENDS the surviving prompt.
     *
     * `endPrompt` clears the in-flight marker on the callback it accepts, so #1's late callback
     * takes #2's marker with it. The user is then looking at a `BiometricPrompt` that the ledger
     * has already forgotten, and their finger -- the one they are about to give -- resolves against
     * nothing. The kill they actually asked for is the one that does NOT happen.
     */
    @Test
    fun a_superseded_callback_does_not_spend_the_prompt_that_is_still_on_screen() {
        val ledger = AuthorizationLedger()
        val prompts = QueuedPrompt()
        val first = Run()
        val second = Run()
        val gate = gate(prompts, ledger)

        gate.authorize(GatedOperation.KILL, onRefused = { first.refusals += it }) { first.ran++ }
        ledger.invalidate(InvalidationEvent.DEVICE_LOCKED)
        gate.authorize(GatedOperation.KILL, onRefused = { second.refusals += it }) { second.ran++ }
        assertEquals("precondition: two prompts are outstanding", 2, prompts.outstanding())

        // The superseded callback lands first, as it must: it belongs to the prompt that has been
        // up the longer.
        prompts.deliver(0)

        assertEquals(
            "PB-SEC-2: the superseded callback cleared the marker belonging to the prompt that is " +
                "actually on screen. `AuthorizationLedger.beginPrompt` refuses while one is in " +
                "flight, and prompt #2 is in flight",
            GateResolution.REFUSED_PROMPT_IN_FLIGHT,
            ledger.beginPrompt(GatedOperation.LAUNCH),
        )

        // And now the user's finger, for the request they actually made.
        prompts.deliver(1)

        assertEquals(
            "PB-SEC-2: the prompt the user answered authorized nothing, because a superseded " +
                "callback had already spent it. The action the user asked for is the one that did " +
                "not run. Refusals: ${second.refusals.map { it.reason }}",
            1,
            second.ran,
        )
    }

    /**
     * THE CONTROL, and it is not decoration. Both assertions above are that some callback does
     * NOT run an action; an implementation that refused every callback after the first
     * invalidation would pass them, and so would one that allowed a per-use operation to be
     * authorized only once for the life of the ledger.
     *
     * This is the ordinary sequence a user walks when they cancel and try again: two prompts for
     * the same operation, one at a time, each answered by its own callback. Both must run.
     */
    @Test
    fun two_sequential_prompts_for_the_same_operation_each_run_their_own_action() {
        val ledger = AuthorizationLedger()
        val prompts = QueuedPrompt()
        val first = Run()
        val second = Run()
        val gate = gate(prompts, ledger)

        gate.authorize(GatedOperation.KILL, onRefused = { first.refusals += it }) { first.ran++ }
        prompts.deliver(0)
        assertEquals("the undisturbed per-use path must run the action", 1, first.ran)

        gate.authorize(GatedOperation.KILL, onRefused = { second.refusals += it }) { second.ran++ }
        prompts.deliver(1)

        assertEquals(
            "a second, entirely ordinary prompt for the same operation authorized nothing. " +
                "Per-use means one authorization per use, not one per process. " +
                "Refusals: ${second.refusals.map { it.reason }}",
            1,
            second.ran,
        )
        assertTrue("the ordinary path refused something: ${first.refusals}", first.refusals.isEmpty())
    }

    /**
     * The control for the invalidation itself: with prompt #1 destroyed by the lock and NO second
     * prompt started, its callback must still authorize nothing. This is ADR-007 B63's own
     * property, restated here only so a fix that keys on "is a newer prompt outstanding" rather
     * than on prompt identity cannot pass this file while re-opening that one.
     */
    @Test
    fun an_invalidated_prompts_callback_authorizes_nothing_even_with_no_successor() {
        val ledger = AuthorizationLedger()
        val prompts = QueuedPrompt()
        val only = Run()

        gate(prompts, ledger).authorize(
            GatedOperation.KILL,
            onRefused = { only.refusals += it },
        ) { only.ran++ }

        ledger.invalidate(InvalidationEvent.DEVICE_LOCKED)
        prompts.deliver(0)

        assertEquals(
            "ADR-007 B63: a callback delivered after an invalidation ran the action",
            0,
            only.ran,
        )
        assertTrue("and the caller was told nothing", only.refusals.isNotEmpty())
    }

    // ---------------------------------------------------------------------
    // Fixtures.
    // ---------------------------------------------------------------------

    private class Run {
        var ran = 0
        val refusals = mutableListOf<PerUseRefusal>()
    }

    /**
     * A prompt that QUEUES its callbacks instead of answering inline, so more than one can be
     * outstanding at a time and a test can choose which one lands.
     *
     * More than one outstanding is the real state and not a contrived one: `BiometricPrompts.show`
     * answers on `activity.mainExecutor`, the app cancels nothing, and an invalidation clears the
     * ledger's in-flight marker without touching the `BiometricPrompt` it was standing for.
     *
     * Every callback releases the cipher it was given, and that cipher works. The released-key
     * check inside [PerUseGate] must not be what refuses, or an assertion here would be about the
     * cipher rather than about which prompt the callback belongs to.
     */
    private class QueuedPrompt : PerUsePrompt {

        private val held = mutableListOf<(() -> Unit)?>()

        override fun availability(): PromptAvailability = PromptAvailability.READY

        override fun show(
            operation: GatedOperation,
            cipher: Cipher,
            onResult: (PromptOutcome, Cipher?) -> Unit,
        ) {
            held += { onResult(PromptOutcome.SUCCEEDED, cipher) }
        }

        fun outstanding(): Int = held.count { it != null }

        /** Deliver the callback of the [index]-th prompt shown, in the order they were shown. */
        fun deliver(index: Int) {
            val callback = checkNotNull(held.getOrNull(index)) {
                "prompt #${index + 1} was never shown; ${held.size} prompt(s) exist"
            }
            held[index] = null
            callback()
        }
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
