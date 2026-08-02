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
     * Every failure mode, through every tier, at the store. A `runCatching { ... }.getOrNull()`
     * anywhere on this path turns a refusal into a null the caller reads as "no key yet".
     *
     * THE THREE MODES ARE NOT INTERCHANGEABLE and the reason is not the UI (ADR-007 B133 sends
     * two of them to the same screen). It is `phonecore.openSealedDeviceKeys`, which refuses a
     * Resume outright for any content-tier error that is neither sentinel -- so a refusal that
     * arrived as the wrong type, or as the untyped third, is a handset whose app will not start.
     *
     * `lockedTiers` IS ASKED FOR EXPLICITLY HERE, and the fixture no longer supplies it by
     * default. Nothing this build provisions can be in that state; the population that can is an
     * install made before B133, still holding an `AUTH_BIOMETRIC_STRONG` content KEK.
     */
    @Test
    fun every_tier_surfaces_each_failure_mode_as_its_own_type_from_the_store() {
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

            val (missing, _, _) = storeWith { it.missingTiers = setOf(tier) }
            assertThrows(
                "$tier's Keystore entry is gone, which is neither of the two sentinels and " +
                    "must not be reported as either",
                KeyCustodyException.KeystoreKeyMissing::class.java,
            ) { missing.open(CustodyBlobs.tierKey(tier)) }
        }
    }

    /** And through every ROLE blob, which is where PB-KEY-5's per-role tiers actually bite. */
    @Test
    fun every_role_blob_surfaces_both_failure_modes_according_to_its_tier() {
        for (role in KeyRole.entries) {
            val tier = KeyTierPolicy.tierOf(role)

            val (locked, _, _) = storeWith { it.lockedTiers = setOf(tier) }
            assertThrows(
                "$role is $tier-tier, so it must refuse when that tier refuses",
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
     * DELETED HERE AND NOT REPAIRED: `a_permanently_invalidated_content_key_is_not_recovered_by_
     * reauthenticating`. It read `GateInvalidation.recoveryFor(BIOMETRIC_ENROLLMENT_CHANGED)` and
     * required the answer to be re-provision or re-pair rather than re-prompt. `GateInvalidation`
     * and its `Recovery` are gone with ADR-007 B133 -- their middle answer was REAUTHENTICATE and
     * there is nothing left to name -- so the assertion has no subject at this layer.
     *
     * ITS CLAIM DID NOT EVAPORATE, it moved: what a destroyed key is worth to the USER is
     * `PhoneStartupRoutingTest.a_destroyed_or_missing_key_still_routes_to_the_re_pair_it_needs`,
     * over the table PhoneRuntime actually consults. This file's own doc explains why that split
     * matters -- accepting the two as an interchangeable pair is correct at THIS layer and is
     * exactly what hid them being merged one layer up.
     */

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
