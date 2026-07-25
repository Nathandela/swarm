package dev.swarm.phone.keys

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * PB-KEY-6's cross-language half, which no RED test could cover because the property did not
 * exist yet: `crypto.ErrKeyAuthRequired` and `crypto.ErrKeyInvalidated` survive every Go hop
 * distinctly and then hit gomobile, which flattens a Go error into a Java exception carrying
 * only a MESSAGE. It was a recorded S14a residual -- "the two sentinels are not machine-readable
 * across the gomobile boundary" -- and S14 owns it.
 *
 * The requirement is that the UI act DIFFERENTLY on each: one is a re-prompt, the other means
 * the key is gone and the device must pair again. So the discriminator is a stable token the
 * facade stamps onto every custody refusal (centrally, in the panic barrier every entry point
 * installs) and this maps it back onto a type.
 *
 * Plain JVM. Nothing here touches Keystore, a prompt, or hardware of any kind; the Go side of
 * the same pin is mobile/s14_custody_test.go, which holds these literals and the bound
 * constants to each other in the direction that matters -- Go is authoritative.
 */
class GoCustodyFailureTest {

    private fun goErrorMessage(token: String) =
        "$token: swarmmobile: the content Keystore key needs a fresh authentication: " +
            "crypto: key requires user authentication"

    @Test
    fun a_recoverable_refusal_from_the_go_core_becomes_the_re_prompt_type() {
        val mapped = GoCustodyFailure.classify(
            KeyTier.CONTENT,
            goErrorMessage(GoCustodyFailure.AUTH_REQUIRED_TOKEN),
        )
        assertTrue(
            "a recoverable refusal must arrive as UserAuthenticationRequired; it was $mapped",
            mapped is KeyCustodyException.UserAuthenticationRequired,
        )
        assertEquals(Recovery.REAUTHENTICATE, GoCustodyFailure.recoveryFor(mapped!!))
    }

    /**
     * The consequential direction. A permanent invalidation reported as an authentication
     * problem produces a prompt the user CAN satisfy and that changes nothing -- forever,
     * because the key it would authorize no longer exists.
     */
    @Test
    fun a_permanent_refusal_from_the_go_core_is_never_reported_as_a_prompt() {
        val mapped = GoCustodyFailure.classify(
            KeyTier.CONTENT,
            goErrorMessage(GoCustodyFailure.KEY_INVALIDATED_TOKEN),
        )
        assertTrue(
            "a permanent invalidation must arrive as KeyPermanentlyInvalidated; it was $mapped",
            mapped is KeyCustodyException.KeyPermanentlyInvalidated,
        )
        assertNotEquals(
            "the key is gone; prompting again cannot bring it back",
            Recovery.REAUTHENTICATE,
            GoCustodyFailure.recoveryFor(mapped!!),
        )
    }

    /**
     * And it does NOT classify everything. A caller that turned every facade failure into a
     * custody failure would report "authenticate again" for a relay timeout, which is the same
     * unsatisfiable-prompt defect reached from the other side.
     */
    @Test
    fun an_ordinary_facade_failure_is_not_a_custody_failure() {
        assertNull(GoCustodyFailure.classify(KeyTier.CONTENT, "swarmmobile: App is not running; call Start first"))
        assertNull(GoCustodyFailure.classify(KeyTier.CONTENT, null))
    }

    /**
     * The round trip. The exceptions this layer throws OUT to the Go core must be classifiable
     * BACK by the same tokens: a Kotlin KeyCustody implementation throws these, gomobile hands
     * Go the message, and mobile/keycustody.go's classifyCustodyVerdict reads the token to
     * decide whether a locked content tier is a per-operation refusal or a corrupt blob. If the
     * message stopped carrying the token, `phonecore.openSealedDeviceKeys` would refuse the
     * Resume outright and a locked handset would be an app that cannot start.
     */
    @Test
    fun the_exceptions_this_layer_throws_carry_the_token_the_go_core_reads() {
        val refusal = KeyCustodyException.UserAuthenticationRequired(KeyTier.CONTENT)
        val invalidated = KeyCustodyException.KeyPermanentlyInvalidated(KeyTier.CONTENT)

        assertTrue(
            "the refusal a KeyCustody throws does not carry the token the Go core matches on: " +
                "${refusal.message}",
            refusal.message!!.contains(GoCustodyFailure.AUTH_REQUIRED_TOKEN),
        )
        assertTrue(invalidated.message!!.contains(GoCustodyFailure.KEY_INVALIDATED_TOKEN))

        assertTrue(
            GoCustodyFailure.classify(KeyTier.CONTENT, refusal.message)
                is KeyCustodyException.UserAuthenticationRequired,
        )
        assertTrue(
            GoCustodyFailure.classify(KeyTier.CONTENT, invalidated.message)
                is KeyCustodyException.KeyPermanentlyInvalidated,
        )
    }

    /**
     * The two tokens must not be prefixes of one another, or `contains` matches both and the
     * classifier's order of checks silently becomes the whole decision.
     */
    @Test
    fun the_two_tokens_cannot_be_mistaken_for_each_other() {
        val auth = GoCustodyFailure.AUTH_REQUIRED_TOKEN
        val invalid = GoCustodyFailure.KEY_INVALIDATED_TOKEN
        assertNotEquals(auth, invalid)
        assertTrue("$auth contains $invalid", !auth.contains(invalid))
        assertTrue("$invalid contains $auth", !invalid.contains(auth))
    }

    // --- the two transport states PB-KEY-6 needs ----------------------------

    /**
     * `App.ConnectionState` reports strings, so the mapping is total in the direction the UI
     * reads it. The two custody states differ from the four transport ones in the two ways that
     * matter: one asks for a biometric, the other stops retrying.
     */
    @Test
    fun the_custody_connection_states_are_distinguishable_from_the_transport_ones() {
        assertEquals(ConnectionState.REAUTH_REQUIRED, ConnectionState.of("reauth_required"))
        assertEquals(ConnectionState.REPAIR_REQUIRED, ConnectionState.of("repair_required"))

        assertTrue(ConnectionState.REAUTH_REQUIRED.needsBiometricPrompt)
        assertTrue(!ConnectionState.REAUTH_REQUIRED.isTerminal)

        assertTrue(ConnectionState.REPAIR_REQUIRED.isTerminal)
        assertTrue(
            "a terminal state must not ask for a biometric: the key it would authorize is gone",
            !ConnectionState.REPAIR_REQUIRED.needsBiometricPrompt,
        )

        for (transport in listOf(
            ConnectionState.OFFLINE,
            ConnectionState.CONNECTING,
            ConnectionState.ONLINE,
            ConnectionState.RECONNECTING,
        )) {
            assertTrue("$transport is a transport state and must not prompt", !transport.needsBiometricPrompt)
            assertTrue("$transport is a transport state and must not be terminal", !transport.isTerminal)
        }
    }

    // --- the seam itself, on the BOUND interface ----------------------------

    private fun custodyOver(kek: FakeKeystoreKek): KeystoreKeyCustody {
        val store = SealedStore(kek)
        val unlocked = FakeKeystoreKek(lockedTiers = emptySet())
        val seeding = SealedStore(unlocked)
        // Seeded through an unlocked provider and re-sealed through the one under test, so the
        // fixture does not depend on the tier being open at setup time.
        for (tier in KeyTier.entries) {
            seeding.put(CustodyBlobs.stateKek(tier), tier, tierKeyBytes(if (tier == KeyTier.WAKE) 0x10 else 0x70))
            store.put(CustodyBlobs.stateKek(tier), tier, seeding.open(CustodyBlobs.stateKek(tier)))
        }
        return KeystoreKeyCustody(store)
    }

    @Test
    fun the_go_core_gets_each_tiers_own_state_key_and_not_the_other_one() {
        val custody = custodyOver(FakeKeystoreKek(lockedTiers = emptySet()))

        assertTrue(custody.wakeKEK().contentEquals(tierKeyBytes(0x10)))
        assertTrue(custody.contentKEK().contentEquals(tierKeyBytes(0x70)))
        assertTrue(
            "the two tiers handed the Go core the SAME key, which is one tier with two names: " +
                "whatever authorizes one unwrap would authorize both",
            !custody.wakeKEK().contentEquals(custody.contentKEK()),
        )
    }

    /**
     * The gate, at the seam. A locked handset must make the CONTENT key unobtainable by the Go
     * core -- and the refusal must arrive carrying the token, because that is the only thing
     * distinguishing "the user is not authenticated" from "this blob is not ours" once gomobile
     * has flattened it. `phonecore.openSealedDeviceKeys` refuses a Resume outright for the
     * second, so a mis-stamped refusal turns a locked handset into an app that cannot start.
     */
    @Test
    fun a_locked_content_tier_refuses_the_go_core_with_the_token_it_reads() {
        val custody = custodyOver(FakeKeystoreKek(lockedTiers = setOf(KeyTier.CONTENT)))

        val refusal = try {
            custody.contentKEK()
            null
        } catch (e: KeyCustodyException) {
            e
        }
        assertTrue("a locked content tier handed the Go core a key", refusal != null)
        assertTrue(
            "the refusal reaching the Go core does not carry a verdict token, so it is " +
                "indistinguishable from a corrupt container: ${refusal!!.message}",
            refusal.message!!.contains(GoCustodyFailure.AUTH_REQUIRED_TOKEN),
        )

        // And the WAKE tier still answers with nobody there. Without this the assertion above
        // is satisfied by a handset that can never wake, which is ADR-007 B16's sole background
        // path dead in exactly the state it exists for.
        assertTrue(custody.wakeKEK().contentEquals(tierKeyBytes(0x10)))
    }

    /**
     * ADR-007 B8 on the BOUND artifact rather than on the Go source. `mobile/s14_custody_test.go`
     * pins the Go declaration; this pins what gomobile actually emitted into the AAR, which is
     * what the app compiles against.
     *
     * The direction is mirrored on a reverse-bound interface: Go is the caller, so a RESULT is
     * inbound and a PARAMETER is outbound. So these methods may return bytes and must accept
     * none -- the shape that would violate it is a `seal(plaintext)`, which hands Java the
     * device's private scalars.
     */
    @Test
    fun the_bound_custody_interface_takes_no_key_material_outbound() {
        val methods = swarmmobile.KeyCustody::class.java.methods
        assertEquals("the bound seam is exactly one verb per tier", 2, methods.size)
        for (method in methods) {
            assertEquals(
                "swarmmobile.KeyCustody.${method.name} takes ${method.parameterTypes.size} " +
                    "argument(s). On a reverse-bound interface a parameter travels Go -> Java, " +
                    "so any argument here is an OUTBOUND key crossing -- the direction ADR-007 " +
                    "B8 forbids. The KEK comes IN; key material never goes OUT",
                0,
                method.parameterTypes.size,
            )
            assertEquals(ByteArray::class.java, method.returnType)
        }
    }
}
