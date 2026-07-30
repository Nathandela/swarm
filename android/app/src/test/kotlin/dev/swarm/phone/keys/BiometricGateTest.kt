package dev.swarm.phone.keys

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotEquals
import org.junit.Assert.assertThrows
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * PB-SEC-2 -- "The biometric gate is cryptographically enforced, not cosmetic: a stated
 * freshness window per operation; invalidation on background, screen lock, process death, and
 * biometric-enrollment change; defined cancel/failure/lockout and concurrent-prompt behavior;
 * Keystore-enforced unwrap/sign authorization rather than a UI boolean; no reuse of one
 * authentication for a different action unless explicitly allowed."
 *
 * Criterion: "Tests per clause. A test must fail if the implementation is an in-memory
 * `authenticated = true` flag."
 *
 * Plain JVM. The freshness numbers come from requirements 6.0 and are asserted as numbers.
 */
class BiometricGateTest {

    private val timedWindowSeconds = 60
    private val timedOps = setOf(GatedOperation.INPUT, GatedOperation.TAKE_CONTROL)
    private val perUseOps = setOf(
        GatedOperation.REVOKE,
        GatedOperation.KILL_SWITCH,
        GatedOperation.LAUNCH,
        GatedOperation.KILL,
    )

    // --- clause 1: a stated freshness window per operation ------------------

    /** 6.0's row, entry by entry. The operation set is closed, so a seventh cannot default in. */
    @Test
    fun the_freshness_table_is_exactly_the_one_in_6_0() {
        assertEquals(timedOps + perUseOps, GatedOperation.entries.toSet())

        for (op in timedOps) {
            assertEquals(
                "6.0 binds $op to a 60 s freshness window",
                Freshness.Timed(timedWindowSeconds),
                BiometricPolicy.specFor(op).freshness,
            )
        }
        for (op in perUseOps) {
            assertEquals(
                "6.0 binds $op to per-use (CryptoObject) authorization",
                Freshness.PerUse,
                BiometricPolicy.specFor(op).freshness,
            )
        }
    }

    /**
     * THE silent downgrade this requirement exists to catch. Per-use and a zero-second timeout
     * are the same integer in KeyGenParameterSpec terms, so the integer cannot tell them
     * apart: only a CryptoObject bound to the one operation can. An implementation that
     * spelled per-use as `Timed(0)` -- or, worse, as `Timed(60)` because "it prompts anyway" --
     * fails here.
     */
    @Test
    fun per_use_operations_are_bound_to_a_crypto_object_not_to_a_short_timeout() {
        for (op in perUseOps) {
            val spec = BiometricPolicy.specFor(op)
            assertTrue(
                "$op is per-use, which is carried by a CryptoObject bound to the operation. " +
                    "A timeout -- any timeout -- authorizes whatever else uses that key inside " +
                    "the window",
                spec.requiresCryptoObject,
            )
            assertEquals("per-use is timeout 0 in KeyGenParameterSpec terms", 0, spec.timeoutSeconds)
        }
        for (op in timedOps) {
            val spec = BiometricPolicy.specFor(op)
            assertFalse("$op is a timed tier and must not demand a CryptoObject per keystroke", spec.requiresCryptoObject)
            assertEquals(timedWindowSeconds, spec.timeoutSeconds)
        }
    }

    // --- clause 2: no reuse for a different action --------------------------

    /**
     * A per-use authorization authorizes ONE use of ONE operation. Not the same operation
     * twice, and certainly not a different one: "revoke this device" and "kill the session"
     * are both one prompt away, and sharing would make the second free.
     */
    @Test
    fun a_per_use_authorization_is_shared_with_nothing_including_itself() {
        for (a in perUseOps) {
            for (b in GatedOperation.entries) {
                assertFalse(
                    "an authorization for $a must not authorize $b",
                    BiometricPolicy.sharesAuthorizationWith(a, b),
                )
            }
        }
    }

    /**
     * Sharing among the timed operations is allowed only if it is DECLARED. The Android
     * platform makes it true by default -- one timed key, one window, every user of that key
     * authorized -- so the declaration is what turns an accident into a decision.
     */
    @Test
    fun sharing_among_timed_operations_is_declared_explicitly() {
        assertTrue(
            "typing and taking control share the 60 s window by construction; PB-SEC-2 " +
                "requires that be explicit rather than incidental",
            BiometricPolicy.sharesAuthorizationWith(GatedOperation.INPUT, GatedOperation.TAKE_CONTROL),
        )
        for (timed in timedOps) {
            for (perUse in perUseOps) {
                assertFalse(
                    "a 60 s typing authorization must not reach $perUse",
                    BiometricPolicy.sharesAuthorizationWith(timed, perUse),
                )
            }
        }
    }

    @Test
    fun a_consumed_per_use_authorization_does_not_authorize_a_second_use() {
        val ledger = AuthorizationLedger()
        ledger.beginPrompt(GatedOperation.REVOKE)
        ledger.endPrompt(GatedOperation.REVOKE, PromptOutcome.SUCCEEDED, atMillis = 1_000, ticket = null)

        assertTrue(ledger.authorized(GatedOperation.REVOKE, atMillis = 1_000))
        ledger.consume(GatedOperation.REVOKE)
        assertFalse(
            "the authorization was spent by the revoke it authorized",
            ledger.authorized(GatedOperation.REVOKE, atMillis = 1_001),
        )
    }

    @Test
    fun a_timed_authorization_expires_exactly_at_the_stated_window() {
        val ledger = AuthorizationLedger()
        ledger.beginPrompt(GatedOperation.INPUT)
        ledger.endPrompt(GatedOperation.INPUT, PromptOutcome.SUCCEEDED, atMillis = 0, ticket = null)

        assertTrue(ledger.authorized(GatedOperation.INPUT, atMillis = 59_999))
        assertFalse(
            "60 s is the window, so 60 s is where it ends",
            ledger.authorized(GatedOperation.INPUT, atMillis = 60_000),
        )
    }

    // --- clause 3: cancel / failure / lockout / concurrent prompts -----------

    /**
     * Four non-success outcomes, four distinct resolutions. Collapsing them -- lockout treated
     * as a retryable failure, say -- produces a prompt loop against a platform that is
     * refusing on purpose.
     */
    @Test
    fun every_prompt_outcome_has_a_distinct_defined_resolution() {
        assertEquals(GateResolution.AUTHORIZED, BiometricPolicy.resolve(PromptOutcome.SUCCEEDED))

        val failures = PromptOutcome.entries.filter { it != PromptOutcome.SUCCEEDED }
        val resolutions = failures.map { BiometricPolicy.resolve(it) }
        for ((outcome, resolution) in failures.zip(resolutions)) {
            assertNotEquals("$outcome must not authorize anything", GateResolution.AUTHORIZED, resolution)
        }
        assertEquals(
            "cancel, failure, transient lockout and permanent lockout need different answers; " +
                "resolutions were $resolutions",
            failures.size,
            resolutions.toSet().size,
        )
    }

    @Test
    fun a_failed_prompt_leaves_nothing_authorized() {
        for (outcome in PromptOutcome.entries.filter { it != PromptOutcome.SUCCEEDED }) {
            val ledger = AuthorizationLedger()
            ledger.beginPrompt(GatedOperation.KILL)
            ledger.endPrompt(GatedOperation.KILL, outcome, atMillis = 1_000, ticket = null)
            assertFalse(
                "$outcome left KILL authorized",
                ledger.authorized(GatedOperation.KILL, atMillis = 1_000),
            )
        }
    }

    /**
     * Two prompts at once is a real state: a push taps through to a gated action while a
     * typing session is re-authorizing. BiometricPrompt does not queue for you.
     */
    @Test
    fun a_second_prompt_while_one_is_in_flight_is_refused() {
        assertEquals(ConcurrentPromptPolicy.REFUSE_SECOND, BiometricPolicy.concurrentPrompt)

        val ledger = AuthorizationLedger()
        assertNotEquals(
            GateResolution.REFUSED_PROMPT_IN_FLIGHT,
            ledger.beginPrompt(GatedOperation.INPUT),
        )
        assertEquals(
            GateResolution.REFUSED_PROMPT_IN_FLIGHT,
            ledger.beginPrompt(GatedOperation.REVOKE),
        )
    }

    /** And the first prompt still resolves; refusing the second must not wedge the gate. */
    @Test
    fun the_first_prompt_still_resolves_after_a_second_is_refused() {
        val ledger = AuthorizationLedger()
        ledger.beginPrompt(GatedOperation.INPUT)
        ledger.beginPrompt(GatedOperation.REVOKE)

        ledger.endPrompt(GatedOperation.INPUT, PromptOutcome.SUCCEEDED, atMillis = 0, ticket = null)
        assertTrue(ledger.authorized(GatedOperation.INPUT, atMillis = 0))
        assertNotEquals(
            "the gate is wedged: no prompt can be started again",
            GateResolution.REFUSED_PROMPT_IN_FLIGHT,
            ledger.beginPrompt(GatedOperation.REVOKE),
        )
    }

    // --- clause 4: invalidation --------------------------------------------

    @Test
    fun every_invalidation_event_drops_every_authorization() {
        for (event in InvalidationEvent.entries) {
            val ledger = AuthorizationLedger()
            ledger.beginPrompt(GatedOperation.INPUT)
            ledger.endPrompt(GatedOperation.INPUT, PromptOutcome.SUCCEEDED, atMillis = 0, ticket = null)

            ledger.invalidate(event)

            assertFalse(
                "$event left an authorization alive",
                ledger.authorized(GatedOperation.INPUT, atMillis = 1),
            )
        }
    }

    /**
     * The four PB-SEC-2 names, plus auth expiry, all present. An enum missing APP_BACKGROUNDED
     * makes the loop above pass while backgrounding invalidates nothing.
     */
    @Test
    fun the_invalidation_events_are_the_ones_PB_SEC_2_names() {
        assertEquals(
            setOf(
                InvalidationEvent.APP_BACKGROUNDED,
                InvalidationEvent.DEVICE_LOCKED,
                InvalidationEvent.PROCESS_DEATH,
                InvalidationEvent.BIOMETRIC_ENROLLMENT_CHANGED,
                InvalidationEvent.AUTH_TIMEOUT_EXPIRED,
            ),
            InvalidationEvent.entries.toSet(),
        )
    }

    /**
     * A biometric enrollment change is PERMANENT: setInvalidatedByBiometricEnrollment destroys
     * the key, and KeyPermanentlyInvalidatedException is not something another prompt fixes.
     * An implementation that recovers by re-authenticating produces a prompt the user can
     * never satisfy.
     */
    @Test
    fun a_biometric_enrollment_change_is_not_recovered_by_reauthenticating() {
        assertNotEquals(
            "the key is gone; prompting again cannot bring it back",
            Recovery.REAUTHENTICATE,
            GateInvalidation.recoveryFor(InvalidationEvent.BIOMETRIC_ENROLLMENT_CHANGED),
        )
        for (event in listOf(
            InvalidationEvent.APP_BACKGROUNDED,
            InvalidationEvent.DEVICE_LOCKED,
            InvalidationEvent.PROCESS_DEATH,
            InvalidationEvent.AUTH_TIMEOUT_EXPIRED,
        )) {
            assertEquals(
                "$event does not destroy any key; a fresh prompt is the whole recovery",
                Recovery.REAUTHENTICATE,
                GateInvalidation.recoveryFor(event),
            )
        }
    }

    // --- clause 5: NOT a boolean -------------------------------------------

    /**
     * The criterion's own words: "A test must fail if the implementation is an in-memory
     * `authenticated = true` flag."
     *
     * So drive the ledger and the platform out of agreement. The app believes it is
     * authenticated -- a prompt succeeded a moment ago -- and the Keystore refuses anyway,
     * which is what happens when the key's auth window lapsed between the prompt and the use,
     * or when the enrollment changed underneath. The unwrap MUST fail closed. An
     * implementation that consults its own boolean and skips the unwrap passes the ledger
     * check and installs a key it could not actually obtain.
     */
    @Test
    fun a_successful_prompt_does_not_authorize_an_unwrap_the_platform_refuses() {
        val ledger = AuthorizationLedger()
        ledger.beginPrompt(GatedOperation.INPUT)
        ledger.endPrompt(GatedOperation.INPUT, PromptOutcome.SUCCEEDED, atMillis = 0, ticket = null)
        assertTrue("precondition: the app believes it is authenticated", ledger.authorized(GatedOperation.INPUT, 0))

        val kek = FakeKeystoreKek(lockedTiers = setOf(KeyTier.CONTENT))
        val store = SealedStore(kek)
        val core = RecordingCore()
        store.put(CustodyBlobs.tierKey(KeyTier.CONTENT), KeyTier.CONTENT, tierKeyBytes(0x70))

        assertThrows(
            "the gate is the Keystore refusing, not the app's own belief about the user",
            KeyCustodyException.UserAuthenticationRequired::class.java,
        ) {
            KeyCustodySession(store, core).installTier(KeyTier.CONTENT)
        }
        assertEquals(0, core.installedContent.size)
    }

    // --- 6.0's renewal row --------------------------------------------------

    /**
     * 6.0: "a typing session crossing the 60 s freshness window must pause input and
     * re-authorize, not silently continue or silently drop; the lease itself is not ended by
     * freshness expiry".
     *
     * Both wrong answers are silent, which is why both are named.
     */
    @Test
    fun crossing_the_freshness_window_pauses_input_rather_than_continuing_or_dropping() {
        assertEquals(InputGateDecision.PROCEED, InputFreshness.decide(lastAuthMillis = 0, nowMillis = 59_999))
        assertEquals(
            "input past the window must pause and re-authorize",
            InputGateDecision.PAUSE_AND_REAUTHORIZE,
            InputFreshness.decide(lastAuthMillis = 0, nowMillis = 60_000),
        )
    }

    /**
     * And the lease survives. take_control's ExpiresAt is now + 15 min precisely so the lease
     * is not the binding constraint on a typing session (6.0); ending it on a 60 s freshness
     * lapse would reintroduce the constraint through the back door.
     */
    @Test
    fun freshness_expiry_does_not_end_the_lease() {
        assertFalse(InputFreshness.freshnessExpiryEndsLease)
    }
}
