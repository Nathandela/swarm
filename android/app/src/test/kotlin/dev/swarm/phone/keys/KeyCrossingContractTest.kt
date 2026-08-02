package dev.swarm.phone.keys

import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * PB-KEY-1 -- "The JNI key-custody contract: exactly which artifact crosses the boundary, in
 * which direction, and why that is acceptable... a test pins the crossing to that one
 * artifact."
 *
 * The Go side of that pin already exists (mobile/contract_test.go, which holds the exported
 * surface to a golden and forbids a bound method returning []byte). This is the ANDROID side:
 * the Kotlin layer that produces the artifact must not grow a second crossing or an outbound
 * one, and it must destroy its copy.
 *
 * Plain JVM: reflection over an interface and a byte array are not Android runtime behaviour,
 * and §10's tier order prefers the cheapest tier that can actually make the assertion.
 */
class KeyCrossingContractTest {

    // --- the crossing is INBOUND ONLY ---------------------------------------

    /**
     * ADR-007 B8: "No long-term private key crosses in either direction: Go returns only
     * sealed blobs, public keys and signatures". On this side the same rule is what stops a
     * convenience getter -- `fun contentKey(): ByteArray` for a test, say -- from becoming a
     * second crossing that nothing pins.
     */
    @Test
    fun no_method_on_the_crossing_returns_key_material() {
        for (method in CoreKeyCustody::class.java.methods) {
            assertTrue(
                "CoreKeyCustody.${method.name} returns ${method.returnType.simpleName}. " +
                    "ADR-007 B8 makes this crossing INBOUND ONLY; a method that returns bytes " +
                    "is a second crossing in the other direction",
                !method.returnType.isArray,
            )
        }
    }

    /**
     * And the crossing is exactly two verbs wide. A third method taking key material -- an
     * `installRelayAuthKey`, a `rewrapContentKey` -- widens the boundary that ADR-007 B8 says
     * may only ever NARROW.
     */
    @Test
    fun exactly_two_methods_carry_key_material_inbound() {
        val carriers = CoreKeyCustody::class.java.methods
            .filter { m -> m.parameterTypes.any { it == ByteArray::class.java } }
            .map { it.name }
            .toSortedSet()

        assertEquals(
            "ADR-007 B8 pins the crossing to InstallWakeKey and InstallContentKey. " +
                "The matrix may only narrow it, never widen it",
            sortedSetOf("installContentKey", "installWakeKey"),
            carriers,
        )
    }

    /**
     * The custody layer itself must not hand key material back to its own callers either. The
     * UI, the push handler and the pairing flow all hold this object; a getter here is how
     * the key ends up in a log line or a saved instance state.
     */
    @Test
    fun the_custody_session_never_returns_key_material() {
        for (method in KeyCustodySession::class.java.methods) {
            if (method.declaringClass != KeyCustodySession::class.java) continue
            assertTrue(
                "KeyCustodySession.${method.name} returns ${method.returnType.simpleName}; " +
                    "key material must never leave the custody layer",
                !method.returnType.isArray,
            )
        }
    }

    // --- and the copy is destroyed -----------------------------------------

    /**
     * The facade package doc: "the caller must zeroize its copy of the byte array as soon as
     * the call returns". Here the custody layer IS the caller.
     *
     * The observation point is deliberately the array the KEK handed out, not one the session
     * chose to expose. A session that copies the unwrapped bytes and zeroizes only its own
     * copy has left the original live on the Java heap, where it survives until GC and lands
     * in any heap dump taken in between -- which is the same defect with a tidier diff.
     */
    @Test
    fun the_unwrapped_key_is_zeroized_once_it_has_crossed() {
        val kek = FakeKeystoreKek(lockedTiers = emptySet())
        val store = SealedStore(kek)
        val core = RecordingCore()
        val content = tierKeyBytes(0x41)
        store.put(CustodyBlobs.tierKey(KeyTier.CONTENT), KeyTier.CONTENT, content)

        KeyCustodySession(store, core).installTier(KeyTier.CONTENT)

        assertEquals("the KEK was never asked to unwrap anything", 1, kek.handedOut.size)
        assertArrayEquals(
            "the unwrapped key must be zeroized once it has crossed",
            ByteArray(32),
            kek.handedOut.single(),
        )
    }

    /**
     * The other half of the same property, and the mutation that breaks a naive fix: zeroizing
     * BEFORE the install makes the assertion above pass while the core receives 32 zero bytes.
     */
    @Test
    fun the_key_that_crossed_is_the_key_that_was_unwrapped() {
        val kek = FakeKeystoreKek(lockedTiers = emptySet())
        val store = SealedStore(kek)
        val core = RecordingCore()
        val content = tierKeyBytes(0x41)
        store.put(CustodyBlobs.tierKey(KeyTier.CONTENT), KeyTier.CONTENT, content)

        KeyCustodySession(store, core).installTier(KeyTier.CONTENT)

        assertEquals(1, core.installedContent.size)
        assertArrayEquals(content, core.installedContent.single())
        assertEquals("installing the content tier must not install the wake tier", 0, core.installedWake.size)
    }
}
