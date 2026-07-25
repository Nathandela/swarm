package dev.swarm.phone.keys

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertThrows
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * PB-KEY-6 -- "`crypto.KeyStore` must become failable... Only the Android implementation
 * stays at S14."
 *
 * SCOPE, stated because it is smaller than the requirement reads. PB-KEY-6's criterion is
 * "Interface returns errors; a test drives an auth-required failure and a key-invalidated
 * failure through every signing path". The signing paths are `SignCommand` and
 * `SignRelayAuth` in `internal/remote/crypto`, whose signatures are STILL errorless in this
 * tree (`keystore.go:52,54`) -- the S7 hoist has not landed. Nothing in android/ can drive a
 * failure through a Go function that cannot return one, and internal/remote/crypto is FROZEN
 * and out of this slice's scope besides. See the S14 RED report.
 *
 * What IS in scope and is asserted here: every Android custody entry point that can fail does
 * fail -- typed, and never as a default value. That is the property the Go-side change exists
 * to make expressible, and it is testable today.
 *
 * Plain JVM. The mapping from the platform's own exception classes onto these types is
 * KeystoreSpecTest, which needs the Android runtime to construct them.
 */
class FailableCustodyTest {

    private fun storeWith(failure: (FakeKeystoreKek) -> Unit): Triple<SealedStore, RecordingCore, FakeKeystoreKek> {
        val kek = FakeKeystoreKek(lockedTiers = emptySet())
        val store = SealedStore(kek)
        for (tier in KeyTier.entries) {
            store.put(CustodyBlobs.tierKey(tier), tier, tierKeyBytes(0x10))
        }
        for (role in KeyRole.entries) {
            store.put(CustodyBlobs.deviceRole(role), KeyTierPolicy.tierOf(role), tierKeyBytes(0x50))
        }
        failure(kek)
        return Triple(store, RecordingCore(), kek)
    }

    /**
     * Both failure modes, through every tier, at the store. A `runCatching { ... }.getOrNull()`
     * anywhere on this path turns a refusal into a null the caller reads as "no key yet".
     */
    @Test
    fun every_tier_surfaces_auth_required_and_permanent_invalidation_from_the_store() {
        for (tier in KeyTier.entries) {
            val (locked, _, _) = storeWith { it.lockedTiers = setOf(tier) }
            assertThrows(
                "$tier must surface an auth-required refusal",
                KeyCustodyException.UserAuthenticationRequired::class.java,
            ) { locked.open(CustodyBlobs.tierKey(tier)) }

            val (invalidated, _, _) = storeWith { it.invalidatedTiers = setOf(tier) }
            assertThrows(
                "$tier must surface a permanent invalidation distinctly from an auth refusal",
                KeyCustodyException.KeyPermanentlyInvalidated::class.java,
            ) { invalidated.open(CustodyBlobs.tierKey(tier)) }
        }
    }

    /** And through every ROLE blob, which is where PB-KEY-5's per-role tiers actually bite. */
    @Test
    fun every_role_blob_surfaces_both_failure_modes_according_to_its_tier() {
        for (role in KeyRole.entries) {
            val tier = KeyTierPolicy.tierOf(role)

            val (locked, _, _) = storeWith { it.lockedTiers = setOf(tier) }
            assertThrows(
                "$role is $tier-tier, so it must refuse when that tier is gated",
                KeyCustodyException.UserAuthenticationRequired::class.java,
            ) { locked.open(CustodyBlobs.deviceRole(role)) }

            val (invalidated, _, _) = storeWith { it.invalidatedTiers = setOf(tier) }
            assertThrows(
                KeyCustodyException.KeyPermanentlyInvalidated::class.java,
            ) { invalidated.open(CustodyBlobs.deviceRole(role)) }
        }
    }

    /**
     * The install path is the one that matters most: it is the only thing that hands material
     * across the JNI boundary, so a swallowed failure there means the Go core carries on with
     * a stale key -- or with zeros -- and the failure surfaces much later as a decrypt error
     * with no cause attached.
     */
    @Test
    fun a_failed_unwrap_installs_nothing() {
        for (tier in KeyTier.entries) {
            for (fail in listOf<(FakeKeystoreKek) -> Unit>(
                { it.lockedTiers = setOf(tier) },
                { it.invalidatedTiers = setOf(tier) },
                { it.missingTiers = setOf(tier) },
            )) {
                val (store, core, _) = storeWith(fail)
                val session = KeyCustodySession(store, core)

                assertThrows(KeyCustodyException::class.java) { session.installTier(tier) }

                assertEquals("a failed unwrap still crossed the boundary", 0, core.installedWake.size)
                assertEquals("a failed unwrap still crossed the boundary", 0, core.installedContent.size)
            }
        }
    }

    /**
     * A permanent invalidation of a CONTENT-tier key is not a prompt away. Those keys carry
     * the device's identity -- COMMAND_SIGN is what the daemon registry pins the device id to
     * (`crypto/keystore.go`, R-DEV.1) -- so losing them to a biometric enrollment change is a
     * pairing-level event, not an authentication-level one. Recovering by re-prompting
     * produces a prompt the user can satisfy and that changes nothing.
     */
    @Test
    fun a_permanently_invalidated_content_key_is_not_recovered_by_reauthenticating() {
        val recovery = GateInvalidation.recoveryFor(InvalidationEvent.BIOMETRIC_ENROLLMENT_CHANGED)
        assertTrue(
            "recovery from a destroyed content-tier key must re-provision or re-pair, not " +
                "re-prompt; it was $recovery",
            recovery == Recovery.REPROVISION_KEK || recovery == Recovery.REPAIR_DEVICE,
        )
    }

    /**
     * The wake tier must NOT be invalidated by an enrollment change, or a re-enrolled
     * fingerprint silently kills the background wake path -- the one thing that is supposed to
     * work without any biometric at all.
     */
    @Test
    fun an_enrollment_change_does_not_take_the_wake_tier_with_it() {
        val kek = FakeKeystoreKek(lockedTiers = emptySet(), invalidatedTiers = setOf(KeyTier.CONTENT))
        val store = SealedStore(kek)
        store.put(CustodyBlobs.tierKey(KeyTier.WAKE), KeyTier.WAKE, tierKeyBytes(0x10))
        store.put(CustodyBlobs.tierKey(KeyTier.CONTENT), KeyTier.CONTENT, tierKeyBytes(0x70))
        val core = RecordingCore()
        val session = KeyCustodySession(store, core)

        PushWakePath.prepare(session)

        assertTrue(session.wakeAvailable())
        assertFalse(session.contentAvailable())
    }
}
