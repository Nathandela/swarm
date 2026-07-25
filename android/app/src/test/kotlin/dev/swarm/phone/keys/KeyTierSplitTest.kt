package dev.swarm.phone.keys

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotEquals
import org.junit.Assert.assertThrows
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * PB-KEY-2 -- "ADR-007 A15's two-tier split is honored on Android: the wake key is
 * after-first-unlock and readable by the push path; the content key is user-authentication-
 * gated and not readable by the push path or derivable from the wake key."
 *
 * WHICH PROPERTY THESE TESTS PROVE, stated because PB-KEY-2's own criterion demands it:
 * they prove the CODE PATH -- that the push path asks for only the wake tier, that the
 * content tier is reached only through an unwrap that can refuse, and that a refusal
 * propagates as a failure instead of a fallback. They do NOT prove the hardware guarantee.
 * The gate here is a fake AES-GCM KEK that refuses on command; a real Keystore refusing
 * because KeyMint says the user is not authenticated is PB-E2E-5, the deferred handset gate.
 * ADR-007 B9 says the same thing about the emulator's software Keystore.
 *
 * Plain JVM. Nothing below is Android runtime behaviour; the platform-request half is
 * KeystoreSpecTest.
 */
class KeyTierSplitTest {

    private fun lockedHandset(): Triple<SealedStore, RecordingCore, FakeKeystoreKek> {
        val kek = FakeKeystoreKek(lockedTiers = setOf(KeyTier.CONTENT))
        val store = SealedStore(kek)
        store.put(CustodyBlobs.tierKey(KeyTier.WAKE), KeyTier.WAKE, tierKeyBytes(0x10))
        store.put(CustodyBlobs.tierKey(KeyTier.CONTENT), KeyTier.CONTENT, tierKeyBytes(0x70))
        return Triple(store, RecordingCore(), kek)
    }

    // --- the mechanism is STATED -------------------------------------------

    /**
     * PB-KEY-2: "The enforcement mechanism must be stated". On iOS A15 leans on the
     * Notification Service Extension being a separate process. FirebaseMessagingService runs
     * IN the app process, so that argument does not transfer and writing it down as if it did
     * would be the most consequential thing this slice could get wrong.
     */
    @Test
    fun the_enforcement_mechanism_is_keystore_auth_gating_not_process_isolation() {
        assertEquals(
            "ADR-007 B9: FirebaseMessagingService runs in the app process, so the split is " +
                "enforced by Keystore auth-gating plus code discipline, not by OS isolation",
            EnforcementMechanism.KEYSTORE_AUTH_GATING,
            KeyTierPolicy.enforcement,
        )
    }

    // --- the wake path must work while locked ------------------------------

    /**
     * ADR-007 B16 makes high-priority FCM the SOLE background wake path. If waking needed the
     * content tier, push would work only when the phone was already unlocked -- which is
     * exactly when it is not needed. This is the failure mode nobody notices in a demo,
     * because the demo phone is unlocked.
     */
    @Test
    fun the_push_path_wakes_a_locked_handset() {
        val (store, core, _) = lockedHandset()
        val session = KeyCustodySession(store, core)

        PushWakePath.prepare(session)

        assertTrue("a push must be able to wake a LOCKED handset", session.wakeAvailable())
        assertEquals("the wake key never crossed", 1, core.installedWake.size)
    }

    /** And it touches exactly one tier, declared rather than inferred from what it happened to call. */
    @Test
    fun the_push_path_declares_only_the_wake_tier() {
        assertEquals(setOf(KeyTier.WAKE), PushWakePath.requiredTiers)
    }

    /**
     * The property PB-KEY-2 names outright. Preparing the push path on a locked handset must
     * not install the content key -- not as a best-effort, not "if it happens to be
     * available", not by falling back to a cached copy.
     */
    @Test
    fun the_push_path_cannot_obtain_the_content_key() {
        val (store, core, _) = lockedHandset()
        val session = KeyCustodySession(store, core)

        PushWakePath.prepare(session)

        assertEquals(
            "the push path installed a content key; on Android nothing but this discipline " +
                "stops it, because the messaging service shares the app process",
            0,
            core.installedContent.size,
        )
        assertFalse(session.contentAvailable())
    }

    // --- and the content tier really is gated ------------------------------

    @Test
    fun a_locked_handset_cannot_install_the_content_key() {
        val (store, core, _) = lockedHandset()
        val session = KeyCustodySession(store, core)

        assertThrows(KeyCustodyException.UserAuthenticationRequired::class.java) {
            session.installTier(KeyTier.CONTENT)
        }
        assertEquals(0, core.installedContent.size)
    }

    /**
     * Unlocking must actually work, or the test above passes on an app that can never decrypt
     * anything -- the vacuous form of "locked device cannot decrypt".
     */
    @Test
    fun unlocking_restores_the_content_tier() {
        val (store, core, kek) = lockedHandset()
        val session = KeyCustodySession(store, core)

        kek.unlockAll()
        session.installTier(KeyTier.CONTENT)

        assertTrue(session.contentAvailable())
        assertEquals(1, core.installedContent.size)
    }

    // --- not derivable from the wake key -----------------------------------

    /**
     * Two tiers under one Keystore alias is one tier with two names: whatever authorizes the
     * unwrap authorizes both.
     */
    @Test
    fun the_two_tiers_have_distinct_keystore_aliases() {
        assertNotEquals(
            KeystoreAliases.forTier(KeyTier.WAKE),
            KeystoreAliases.forTier(KeyTier.CONTENT),
        )
    }

    /**
     * The wake key is content-free by construction, so a process holding only it must not be
     * able to reach the content key through anything it can read.
     */
    @Test
    fun nothing_the_wake_tier_opens_contains_the_content_key() {
        val (store, core, _) = lockedHandset()
        val session = KeyCustodySession(store, core)
        PushWakePath.prepare(session)

        val contentBytes = tierKeyBytes(0x70)
        for (name in store.names()) {
            if (store.tierOf(name) != KeyTier.WAKE) continue
            assertFalse(
                "$name is wake-tier and contains the content key",
                containsBytes(store.open(name), contentBytes),
            )
        }
        assertEquals(
            "the wake key that DID cross must be the wake key, not the content key",
            tierKeyBytes(0x10).toList(),
            core.installedWake.single().toList(),
        )
    }
}
