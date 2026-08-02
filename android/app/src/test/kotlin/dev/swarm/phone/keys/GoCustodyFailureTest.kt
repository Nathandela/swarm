package dev.swarm.phone.keys

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertThrows
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * PB-KEY-6's cross-language half, which no RED test could cover because the property did not
 * exist yet: `crypto.ErrKeyAuthRequired` and `crypto.ErrKeyInvalidated` survive every Go hop
 * distinctly and then hit gomobile, which flattens a Go error into a Java exception carrying
 * only a MESSAGE. It was a recorded S14a residual -- "the two sentinels are not machine-readable
 * across the gomobile boundary" -- and S14 owns it.
 *
 * WHAT THE DISCRIMINATOR IS FOR NOW (ADR-007 B133), because its consumer moved and the
 * classification did not. It used to decide what the UI did: one was a re-prompt, the other was
 * "pair this device again". There is no prompt left in the product, so both verdicts route to
 * the same screen -- and BOTH TOKENS SURVIVE ANYWAY, because the reader that matters is not the
 * UI. `internal/remote/crypto` is FROZEN and still raises both sentinels, and
 * `phonecore.openSealedDeviceKeys` refuses a Resume OUTRIGHT for any content-tier error that is
 * neither of them. So a token dropped here does not merge two screens; it turns a legible
 * refusal into a handset whose app will not start. The classification is a WIRE property.
 *
 * Plain JVM. Nothing here touches Keystore or hardware of any kind; the Go side of the same pin
 * is mobile/s14_custody_test.go, which holds these literals and the bound constants to each
 * other in the direction that matters -- Go is authoritative.
 */
class GoCustodyFailureTest {

    private fun goErrorMessage(token: String) =
        "$token: swarmmobile: the content Keystore key needs a fresh authentication: " +
            "crypto: key requires user authentication"

    /**
     * The two sentinels arrive as two TYPES and never as one.
     *
     * The `recoveryFor` assertions that used to close each of these are gone with
     * `GoCustodyFailure.Recovery`: its middle answer was REAUTHENTICATE, and ADR-007 B133
     * removed the state that answer named. What is asserted instead is the property the tokens
     * still buy -- each verdict is distinguishable at the boundary, which is what
     * `phonecore.openSealedDeviceKeys` reads and what `PhoneStartupRoutingTest` routes.
     */
    @Test
    fun a_refusal_for_want_of_authentication_arrives_as_its_own_type() {
        val mapped = GoCustodyFailure.classify(
            KeyTier.CONTENT,
            goErrorMessage(GoCustodyFailure.AUTH_REQUIRED_TOKEN),
        )
        assertTrue(
            "an auth-required refusal must arrive as UserAuthenticationRequired; it was $mapped",
            mapped is KeyCustodyException.UserAuthenticationRequired,
        )
    }

    /**
     * The consequential direction, and it survives B133 unchanged. A permanent invalidation
     * that arrived as anything else would be read by `openSealedDeviceKeys` as a container it
     * cannot parse rather than a key that is gone, and a re-pairable handset becomes an app
     * that refuses to start.
     */
    @Test
    fun a_permanent_refusal_from_the_go_core_is_never_the_other_verdict() {
        val mapped = GoCustodyFailure.classify(
            KeyTier.CONTENT,
            goErrorMessage(GoCustodyFailure.KEY_INVALIDATED_TOKEN),
        )
        assertTrue(
            "a permanent invalidation must arrive as KeyPermanentlyInvalidated; it was $mapped",
            mapped is KeyCustodyException.KeyPermanentlyInvalidated,
        )
        assertFalse(
            "the two sentinels collapsed into one type at the boundary",
            mapped is KeyCustodyException.UserAuthenticationRequired,
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

    // --- the terminal state PB-KEY-6 needs ----------------------------------

    /**
     * `App.ConnectionState` reports strings, so the mapping is total in the direction the UI
     * reads it. The custody state differs from the four transport ones in the way that is left:
     * it stops retrying.
     *
     * THE OTHER HALF OF THIS TEST IS GONE, DELIBERATELY (ADR-007 B133). It asserted that
     * `reauth_required` was a state the app understood and that it asked for a biometric while
     * not being terminal. There is no prompt anywhere in this product, so the state had lost its
     * producer AND its remedy at once -- a screen nothing can reach, whose only instruction is
     * an act the user cannot perform. It was removed atomically with `mobile/relay.go`'s
     * `connReauthRequired`, the taxonomy row and `Remedy.AUTHENTICATE`; the next test is what
     * holds that removal to being atomic.
     */
    @Test
    fun the_custody_connection_state_is_distinguishable_from_the_transport_ones() {
        assertEquals(ConnectionState.REPAIR_REQUIRED, ConnectionState.of("repair_required"))
        assertTrue(ConnectionState.REPAIR_REQUIRED.isTerminal)

        for (transport in listOf(
            ConnectionState.OFFLINE,
            ConnectionState.CONNECTING,
            ConnectionState.ONLINE,
            ConnectionState.RECONNECTING,
        )) {
            assertFalse(
                "$transport is a transport state and must not stop the app retrying",
                transport.isTerminal,
            )
        }
    }

    /**
     * THE REMOVAL HAS TO BE ATOMIC, and this is the half a Kotlin test can hold.
     *
     * `of` errors on a wire string it does not know, which is the right behaviour -- a state the
     * facade reports and the app cannot name is not something to guess at. So if
     * `mobile/relay.go` ever emits `reauth_required` again while nothing here can render it, the
     * app CRASHES on a live connection rather than showing a wrong banner. Asserting the refusal
     * here is what makes re-introducing the producer a failure in this suite instead of on a
     * handset, and it is the reason the enum row could be deleted rather than kept "for safety":
     * a row kept with no producer is a screen nobody can ever reach.
     */
    @Test
    fun the_removed_reauth_state_is_not_quietly_still_understood() {
        assertThrows(
            "ConnectionState still maps the reauth_required wire string. It was removed with " +
                "its producer and its remedy (ADR-007 B133); a row kept on one side of that " +
                "join is a state the app can enter and offer nothing for",
            IllegalStateException::class.java,
        ) { ConnectionState.of("reauth_required") }
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
     * A refusing content tier at the seam, and the premise is now a NAMED POPULATION rather than
     * "the screen is locked" (ADR-007 B133).
     *
     * Nothing this build provisions can refuse for want of an authentication -- both KEKs carry
     * `setUserAuthenticationRequired(false)`. What still can is an install made BEFORE that
     * change, whose content KEK holds `AUTH_BIOMETRIC_STRONG` and which
     * `KeystoreCustodyBootstrap.ensure` does not re-spec because the alias exists. On that
     * handset the refusal is real and permanent, and it must still arrive carrying the token:
     * that is the only thing distinguishing it from "this blob is not ours" once gomobile has
     * flattened the error, and `phonecore.openSealedDeviceKeys` refuses a Resume outright for
     * the second. A mis-stamped refusal turns an upgradeable handset into an app that cannot
     * start, with nothing on screen saying why.
     */
    @Test
    fun a_refusing_content_tier_refuses_the_go_core_with_the_token_it_reads() {
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
