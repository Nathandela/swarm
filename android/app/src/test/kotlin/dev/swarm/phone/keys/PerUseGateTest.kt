package dev.swarm.phone.keys

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotEquals
import org.junit.Assert.assertThrows
import org.junit.Assert.assertTrue
import org.junit.Test
import javax.crypto.Cipher
import javax.crypto.KeyGenerator

/**
 * PB-SEC-2's PER-USE tier, at the seam where it either runs an operation or does not.
 *
 * WHY THIS FILE EXISTS. `BiometricGateTest` asserts the POLICY -- which operation is per-use,
 * what each prompt outcome resolves to, that an authorization is consumed. Every one of those
 * assertions passed while `KeystoreSpecs.forOperation` was referenced from `src/test/` alone,
 * `AuthorizationLedger.beginPrompt/endPrompt/consume` had no production caller, and no
 * `BiometricPrompt` existed anywhere (ADR-007 B51). A policy nothing consults is a policy the
 * app does not have, and a test over it passes because its subject is unreachable. This file
 * asserts the thing that was missing: that the ACTION does not run unless the platform released
 * a key for it, once, for that operation.
 *
 * WHAT IS MODELLED AND WHAT IS NOT -- said here rather than left to be assumed, because this is
 * the file most likely to be misread as biometric coverage.
 *
 *  - MODELLED: the order of operations, and what happens on every refusal. The gate consults a
 *    [PerUsePrompt] and a [PerUseCipherSource]; both are seams, and the tests drive them.
 *  - NOT MODELLED, AND NOT CLAIMED ANYWHERE: that a real biometric prompt was shown, accepted
 *    or refused; that a real Keystore withheld a real key from an unauthenticated user; that
 *    `setUserAuthenticationParameters(0, AUTH_BIOMETRIC_STRONG)` behaves as documented on any
 *    handset. That is PB-E2E-5, DEFERRED (ADR-007 B31), and ADR-007 B56 makes the whole
 *    `androidTest` tier unexecutable besides -- the emulator's keymint reports
 *    SECURITY_LEVEL_SOFTWARE and PB-KEY-8 fails the app closed before any screen renders.
 *
 * THE CIPHERS BELOW ARE PLAIN JVM AES CIPHERS. They stand in for the Keystore-backed ones at
 * the seam, and they are the right stand-in for exactly one reason: the property under test is
 * that the gate USES what the platform hands back and refuses when using it fails. A cipher
 * that works and a cipher that throws are the two platform answers, and neither assertion says
 * anything about why a real Keystore would choose one.
 */
class PerUseGateTest {

    // ---------------------------------------------------------------------
    // Fixtures.
    // ---------------------------------------------------------------------

    /** A cipher that works: the platform released the key. */
    private fun liveCipher(): Cipher {
        val key = KeyGenerator.getInstance("AES").apply { init(256) }.generateKey()
        return Cipher.getInstance("AES/GCM/NoPadding").apply { init(Cipher.ENCRYPT_MODE, key) }
    }

    /**
     * A cipher that refuses on use: the platform handed back a CryptoObject whose key it will
     * not actually operate. An uninitialised Cipher throws on `doFinal`, which is the same
     * shape as a Keystore key the platform declines to run.
     */
    private fun deadCipher(): Cipher = Cipher.getInstance("AES/GCM/NoPadding")

    /**
     * The platform, as the gate sees it. Every answer is stated by the test rather than
     * defaulted, so no assertion below rests on a value nobody chose.
     */
    private class FakePrompt(
        var availability: PromptAvailability = PromptAvailability.READY,
        var outcome: PromptOutcome = PromptOutcome.SUCCEEDED,
        /** What the platform RELEASES. Null models a success that carried no usable key. */
        var release: (Cipher) -> Cipher? = { it },
    ) : PerUsePrompt {

        val shown = mutableListOf<GatedOperation>()
        val ciphersOffered = mutableListOf<Cipher>()

        /** Set to hold the callback instead of answering, so a prompt can be left in flight. */
        var deferred: (() -> Unit)? = null

        override fun availability(): PromptAvailability = availability

        override fun show(
            operation: GatedOperation,
            cipher: Cipher,
            onResult: (PromptOutcome, Cipher?) -> Unit,
        ) {
            shown += operation
            ciphersOffered += cipher
            val answer = { onResult(outcome, release(cipher)) }
            if (deferred == null) answer() else deferred = answer
        }
    }

    private inner class FakeCiphers(
        var next: () -> Cipher = { liveCipher() },
    ) : PerUseCipherSource {
        val asked = mutableListOf<GatedOperation>()
        override fun cipherFor(operation: GatedOperation): Cipher {
            asked += operation
            return next()
        }
    }

    private class Run {
        var ran = 0
        val refusals = mutableListOf<PerUseRefusal>()
    }

    private fun gate(
        prompt: PerUsePrompt,
        ciphers: PerUseCipherSource,
        ledger: AuthorizationLedger = AuthorizationLedger(),
    ) = PerUseGate(prompt = prompt, ciphers = ciphers, ledger = ledger, now = { 1_000L })

    // ---------------------------------------------------------------------
    // The criterion: "a test must fail if the implementation is an in-memory
    // `authenticated = true` flag".
    // ---------------------------------------------------------------------

    /**
     * THE assertion this whole file is for, and the one that kills the flag.
     *
     * The ledger is driven to "authorized" first -- the app believes the user authenticated a
     * moment ago -- and the platform then refuses to produce a usable key. An implementation
     * that consults its own record and runs the action passes every policy test in
     * `BiometricGateTest` and fails here.
     *
     * The POSITIVE half is in the same test on purpose: without it, a gate that never ran any
     * action would pass, which is a fence proving nothing.
     */
    @Test
    fun a_believed_authorization_does_not_run_the_action_when_the_platform_releases_no_key() {
        val ledger = AuthorizationLedger()
        ledger.beginPrompt(GatedOperation.REVOKE)
        ledger.endPrompt(GatedOperation.REVOKE, PromptOutcome.SUCCEEDED, atMillis = 1_000, ticket = null)
        assertTrue(
            "precondition: the app's own record says REVOKE is authorized",
            ledger.authorized(GatedOperation.REVOKE, atMillis = 1_000),
        )

        val prompt = FakePrompt(release = { null })
        val run = Run()
        gate(prompt, FakeCiphers(), ledger)
            .authorize(GatedOperation.REVOKE, onRefused = { run.refusals += it }) { run.ran++ }

        assertEquals("the action ran on the app's belief rather than on a released key", 0, run.ran)
        assertEquals(1, run.refusals.size)
        assertEquals(PerUseRefusalReason.KEY_NOT_RELEASED, run.refusals.single().reason)

        // The control: the same gate, the same believed authorization, a platform that DOES
        // release the key. If this did not run, the assertion above would be vacuous.
        val ok = Run()
        gate(FakePrompt(), FakeCiphers(), ledger)
            .authorize(GatedOperation.REVOKE, onRefused = { ok.refusals += it }) { ok.ran++ }
        assertEquals("the released-key path must still run the action", 1, ok.ran)
        assertTrue(ok.refusals.isEmpty())
    }

    /**
     * The subtler form: the platform hands back a CryptoObject and the key inside it does not
     * work. `BiometricPrompt` returning SUCCEEDED is a statement about the USER, not about the
     * key -- an implementation that stops at the outcome enum has trusted a UI event, which is
     * the same defect wearing the platform's clothes.
     */
    @Test
    fun a_crypto_object_whose_key_refuses_does_not_authorize_the_action() {
        val prompt = FakePrompt(release = { deadCipher() })
        val run = Run()
        gate(prompt, FakeCiphers()).authorize(GatedOperation.KILL, onRefused = { run.refusals += it }) {
            run.ran++
        }

        assertEquals("SUCCEEDED is about the finger, not about the key", 0, run.ran)
        assertEquals(PerUseRefusalReason.KEY_NOT_RELEASED, run.refusals.single().reason)
    }

    /**
     * And the Keystore refusing to produce a cipher AT ALL -- the state a locked handset, a
     * lapsed window or a destroyed key leaves. The action must not run, and the gate must not
     * be left wedged with a prompt marked in flight that will never resolve.
     */
    @Test
    fun a_keystore_that_will_not_produce_a_cipher_refuses_and_does_not_wedge_the_gate() {
        val ciphers = FakeCiphers(next = { throw KeyCustodyException.UserAuthenticationRequired(KeyTier.CONTENT) })
        val prompt = FakePrompt()
        val run = Run()
        val gate = gate(prompt, ciphers)

        gate.authorize(GatedOperation.REVOKE, onRefused = { run.refusals += it }) { run.ran++ }

        assertEquals(0, run.ran)
        assertEquals(PerUseRefusalReason.KEY_UNAVAILABLE, run.refusals.single().reason)
        assertTrue("no prompt may be shown over a cipher that does not exist", prompt.shown.isEmpty())

        // Not wedged: the next attempt starts a prompt rather than being refused as concurrent.
        ciphers.next = { liveCipher() }
        gate.authorize(GatedOperation.REVOKE, onRefused = { run.refusals += it }) { run.ran++ }
        assertEquals("the gate refused once and then could not prompt again", 1, run.ran)
    }

    // ---------------------------------------------------------------------
    // Per-use is per USE. Not per window, not per session.
    // ---------------------------------------------------------------------

    /**
     * The downgrade `BiometricPolicy.kt`'s header says the file exists to make impossible,
     * asserted where it can actually happen. Two uses, two prompts, two ciphers. An
     * implementation that cached the first authorization -- for any window at all, including
     * "while the screen is up" -- shows one prompt and fails here.
     */
    @Test
    fun every_use_is_prompted_for_again() {
        val prompt = FakePrompt()
        val ciphers = FakeCiphers()
        val gate = gate(prompt, ciphers)
        val run = Run()

        repeat(3) {
            gate.authorize(GatedOperation.KILL, onRefused = { run.refusals += it }) { run.ran++ }
        }

        assertEquals(3, run.ran)
        assertEquals("per-use means one prompt per use", 3, prompt.shown.size)
        assertEquals("and one CryptoObject per use", 3, ciphers.asked.size)
        assertEquals(
            "each prompt must carry its own cipher; a reused one is a reused authorization",
            3,
            prompt.ciphersOffered.toSet().size,
        )
    }

    /**
     * The authorization is spent BEFORE the action runs, not after. An action that re-entered
     * the gate -- a redraw, a second tap delivered while the first is still on the stack --
     * must not find a live authorization waiting for it.
     */
    @Test
    fun the_authorization_is_consumed_before_the_action_runs() {
        val ledger = AuthorizationLedger()
        var authorizedInsideTheAction: Boolean? = null

        gate(FakePrompt(), FakeCiphers(), ledger)
            .authorize(GatedOperation.REVOKE, onRefused = { }) {
                authorizedInsideTheAction = ledger.authorized(GatedOperation.REVOKE, atMillis = 1_000)
            }

        assertEquals(
            "the action ran holding a live per-use authorization; a second use would be free",
            false,
            authorizedInsideTheAction,
        )
        assertFalse(
            "and nothing is left behind afterwards",
            ledger.authorized(GatedOperation.REVOKE, atMillis = 1_000),
        )
    }

    /**
     * A per-use gate pointed at a TIMED operation is a category error in the direction that
     * looks harmless: it would prompt per keystroke, which is not what 6.0 says and which users
     * turn off. It is refused loudly rather than silently accepted.
     */
    @Test
    fun the_per_use_gate_refuses_to_gate_a_timed_operation() {
        for (timed in GatedOperation.entries.filterNot { BiometricPolicy.specFor(it).requiresCryptoObject }) {
            assertThrows(
                "$timed is a 60 s tier and must not be routed through the per-use gate",
                IllegalArgumentException::class.java,
            ) {
                gate(FakePrompt(), FakeCiphers()).authorize(timed, onRefused = { }) { }
            }
        }
        // The control: the per-use operations are accepted, so the assertion above is about the
        // tier and not about the gate refusing everything.
        for (perUse in GatedOperation.entries.filter { BiometricPolicy.specFor(it).requiresCryptoObject }) {
            val run = Run()
            gate(FakePrompt(), FakeCiphers()).authorize(perUse, onRefused = { run.refusals += it }) { run.ran++ }
            assertEquals("$perUse is per-use and must be gateable", 1, run.ran)
        }
    }

    // ---------------------------------------------------------------------
    // Refusals: every one of them, and every one with a way forward.
    // ---------------------------------------------------------------------

    /** Every non-success prompt outcome refuses the action, with its own reason. */
    @Test
    fun no_failed_prompt_runs_the_action_and_each_failure_keeps_its_own_reason() {
        val seen = mutableSetOf<PerUseRefusalReason>()
        for (outcome in PromptOutcome.entries.filter { it != PromptOutcome.SUCCEEDED }) {
            val run = Run()
            gate(FakePrompt(outcome = outcome), FakeCiphers())
                .authorize(GatedOperation.REVOKE, onRefused = { run.refusals += it }) { run.ran++ }

            assertEquals("$outcome ran the action", 0, run.ran)
            seen += run.refusals.single().reason
        }
        assertEquals(
            "cancel, failure, transient lockout and permanent lockout need different answers; " +
                "collapsing any pair produces a prompt loop against a platform refusing on purpose",
            PromptOutcome.entries.size - 1,
            seen.size,
        )
    }

    /** Every availability the platform can report that is not READY refuses, distinctly. */
    @Test
    fun no_unavailable_platform_runs_the_action_and_each_state_keeps_its_own_reason() {
        val seen = mutableSetOf<PerUseRefusalReason>()
        for (state in PromptAvailability.entries.filter { it != PromptAvailability.READY }) {
            val prompt = FakePrompt(availability = state)
            val ciphers = FakeCiphers()
            val run = Run()
            gate(prompt, ciphers)
                .authorize(GatedOperation.KILL, onRefused = { run.refusals += it }) { run.ran++ }

            assertEquals("$state ran the action", 0, run.ran)
            assertTrue("$state must not reach Keystore at all", ciphers.asked.isEmpty())
            assertTrue("$state must not put a prompt on screen", prompt.shown.isEmpty())
            seen += run.refusals.single().reason
        }
        assertEquals(
            "each unavailable state has a different remedy and must not be collapsed",
            PromptAvailability.entries.size - 1,
            seen.size,
        )
    }

    /**
     * THE STANDING TRAP, as an assertion: a gate whose exit is unbuilt. Every refusal this gate
     * can produce names something -- and the one that names nothing is the one where nothing
     * the user does helps, which is a fact about the handset rather than a missing exit.
     *
     * `PerUseRefusalReason.NO_BIOMETRIC_HARDWARE` is that one. If a second reason ever maps to
     * [PerUseRemedy.NONE], this fails and whoever added it has to say why the user is stuck.
     */
    @Test
    fun every_refusal_names_a_way_forward_except_the_one_where_there_is_none() {
        val stuck = PerUseRefusalReason.entries.filter { PerUseRefusalText.remedyFor(it) == PerUseRemedy.NONE }
        assertEquals(
            "a refusal with no remedy is a gate whose exit is unbuilt; got $stuck",
            listOf(PerUseRefusalReason.NO_BIOMETRIC_HARDWARE),
            stuck,
        )
        for (reason in PerUseRefusalReason.entries) {
            assertTrue(
                "$reason has no message; a refusal the screen cannot render is a screen that " +
                    "looks identical whether the action ran or not",
                PerUseRefusalText.messageFor(reason).isNotBlank(),
            )
        }
    }

    /**
     * A device with no enrolled Class-3 biometric cannot use the content tier at all, and the
     * whole content of this assertion is that it is TOLD SO with the one action that fixes it.
     * ADR-007 B59 records why the alternative -- adding AUTH_DEVICE_CREDENTIAL so a PIN would
     * do -- was rejected.
     */
    @Test
    fun a_handset_with_no_enrolled_biometric_is_told_to_enrol_one() {
        val run = Run()
        gate(FakePrompt(availability = PromptAvailability.NONE_ENROLLED), FakeCiphers())
            .authorize(GatedOperation.REVOKE, onRefused = { run.refusals += it }) { run.ran++ }

        val refusal = run.refusals.single()
        assertEquals(PerUseRefusalReason.NO_BIOMETRIC_ENROLLED, refusal.reason)
        assertEquals(PerUseRemedy.ENROL_BIOMETRIC, PerUseRefusalText.remedyFor(refusal.reason))
        assertNotEquals(
            "an unsupported handset and an unenrolled one have opposite remedies",
            PerUseRefusalText.remedyFor(PerUseRefusalReason.NO_BIOMETRIC_HARDWARE),
            PerUseRefusalText.remedyFor(refusal.reason),
        )
    }

    // ---------------------------------------------------------------------
    // Concurrency: BiometricPrompt does not queue.
    // ---------------------------------------------------------------------

    /**
     * `BiometricPolicy.concurrentPrompt` is REFUSE_SECOND, and this is where it becomes true of
     * the app rather than of a constant. The second action must not run, and the first must
     * still be able to.
     */
    @Test
    fun a_second_action_while_a_prompt_is_in_flight_is_refused_and_the_first_still_resolves() {
        val prompt = FakePrompt().apply { deferred = { } }
        val gate = gate(prompt, FakeCiphers())
        val first = Run()
        val second = Run()

        gate.authorize(GatedOperation.REVOKE, onRefused = { first.refusals += it }) { first.ran++ }
        gate.authorize(GatedOperation.KILL, onRefused = { second.refusals += it }) { second.ran++ }

        assertEquals("the first prompt is still on screen", 0, first.ran)
        assertEquals(0, second.ran)
        assertEquals(PerUseRefusalReason.PROMPT_IN_FLIGHT, second.refusals.single().reason)
        assertEquals("only one prompt may be on screen", 1, prompt.shown.size)

        // The first resolves.
        prompt.deferred?.invoke()
        assertEquals(1, first.ran)

        // And the gate is not wedged: a later action prompts again.
        prompt.deferred = null
        val third = Run()
        gate.authorize(GatedOperation.KILL, onRefused = { third.refusals += it }) { third.ran++ }
        assertEquals("refusing the second wedged the gate", 1, third.ran)
    }

    /**
     * The refusal is per OPERATION as well as per use: a prompt in flight for REVOKE must not
     * be answered into KILL. `BiometricPolicy.sharesAuthorizationWith` says a per-use
     * authorization is shared with nothing including itself; this is that, at the gate.
     */
    @Test
    fun an_authorization_obtained_for_one_operation_does_not_run_another() {
        val ledger = AuthorizationLedger()
        ledger.beginPrompt(GatedOperation.REVOKE)
        ledger.endPrompt(GatedOperation.REVOKE, PromptOutcome.SUCCEEDED, atMillis = 1_000, ticket = null)

        val prompt = FakePrompt()
        val run = Run()
        gate(prompt, FakeCiphers(), ledger)
            .authorize(GatedOperation.KILL, onRefused = { run.refusals += it }) { run.ran++ }

        assertEquals(
            "KILL must be prompted for on its own, whatever REVOKE is holding",
            listOf(GatedOperation.KILL),
            prompt.shown,
        )
        assertEquals(1, run.ran)
    }

    /**
     * The alias the prompt's CryptoObject is built over is the operation's OWN entry.
     * `KeystoreAliases.forOperation` gives per-use operations one entry each precisely so a
     * single authorization cannot be pointed at whichever operation the caller picks, and this
     * is the assertion that the gate asks for the right one.
     */
    @Test
    fun the_cipher_is_asked_for_by_operation() {
        val ciphers = FakeCiphers()
        val gate = gate(FakePrompt(), ciphers)
        val perUse = GatedOperation.entries.filter { BiometricPolicy.specFor(it).requiresCryptoObject }

        for (op in perUse) gate.authorize(op, onRefused = { }) { }

        assertEquals(perUse, ciphers.asked)
        assertEquals(
            "one Keystore entry per per-use operation, so no two share an authorization",
            perUse.size,
            perUse.map { KeystoreAliases.forOperation(it) }.toSet().size,
        )
    }
}
