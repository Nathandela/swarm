package dev.swarm.phone.keys

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotEquals
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * PB-KEY-2, NARROWED BY ADR-007 B133 -- "ADR-007 A15's two-tier split is honored on Android: the
 * wake key is after-first-unlock and readable by the push path; ~~the content key is
 * user-authentication-gated and~~ not readable by the push path or derivable from the wake key."
 *
 * The struck clause has no producer: both KEKs carry `setUserAuthenticationRequired(false)`. What
 * survives is the half that was always the wire concern -- FCM READS PUSH PAYLOADS, so the key
 * the push path may touch has to be one that opens nothing but content-free wakes.
 *
 * EVERY TEST BELOW NOW RUNS OVER AN UNLOCKED HANDSET, AND THAT IS THE POINT. They used to run
 * over a fixture whose content tier refused, so "the push path did not obtain the content key"
 * was true because THE FIXTURE'S LOCK refused it -- an implementation that reached for the
 * content tier would have been stopped by the KEK rather than by the discipline under test.
 * After B133 the discipline is the ONLY phone-side enforcement left (`PushWakePath.requiredTiers`
 * declares one tier, and FirebaseMessagingService shares the app process), so the fence has to
 * hold with nothing behind it. On an unlocked fixture a push path that asked for the content tier
 * SUCCEEDS -- and these tests fail, which is what makes them worth running.
 *
 * WHICH PROPERTY THESE TESTS PROVE: the CODE PATH. They do NOT prove a hardware guarantee; the
 * KEK here is a fake AES-GCM key, and real Keystore behaviour is PB-E2E-5, the deferred handset
 * gate. ADR-007 B9 says the same thing about the emulator's software Keystore.
 *
 * Plain JVM. Nothing below is Android runtime behaviour; the platform-request half is
 * KeystoreSpecTest.
 */
class KeyTierSplitTest {

    /**
     * A handset in the state production actually reaches: both tier KEKs answer.
     *
     * It was `lockedHandset()`, with `lockedTiers = setOf(CONTENT)`. Keeping that would have kept
     * four assertions green over a state no install this build provisions can enter.
     */
    private fun handset(): Triple<SealedStore, RecordingCore, FakeKeystoreKek> {
        val kek = FakeKeystoreKek()
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
    fun the_enforcement_mechanism_is_code_discipline_not_process_isolation() {
        assertEquals(
            "ADR-007 B9: FirebaseMessagingService runs in the app process, so the split is " +
                "enforced by code discipline and not by OS isolation. It was KEYSTORE_AUTH_GATING " +
                "plus code discipline; B133 removed the auth gate, so the declaration " +
                "PushWakePath makes about which tiers it may touch is the whole of what is left " +
                "on this side. B133 records the reduction and accepts it: the property the split " +
                "buys -- that the CARRIER of the push cannot read session content -- is enforced " +
                "at the sender (PB-PUSH-0)",
            EnforcementMechanism.CODE_DISCIPLINE,
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
    fun the_push_path_wakes_a_handset_with_nobody_there() {
        val (store, core, _) = handset()
        val session = KeyCustodySession(store, core)

        PushWakePath.prepare(session)

        assertTrue("a push must be able to wake a handset with nobody there", session.wakeAvailable())
        assertEquals("the wake key never crossed", 1, core.installedWake.size)
    }

    /** And it touches exactly one tier, declared rather than inferred from what it happened to call. */
    @Test
    fun the_push_path_declares_only_the_wake_tier() {
        assertEquals(setOf(KeyTier.WAKE), PushWakePath.requiredTiers)
    }

    /**
     * The property PB-KEY-2 still names, on a handset where NOTHING ELSE WOULD STOP IT.
     *
     * The content tier answers here, so a `prepare` that reached for it would install a content
     * key and this test would fail. That is the whole difference from the version this replaces,
     * which ran against a refusing KEK: there, the discipline could have been absent and the
     * assertion would still have held.
     */
    @Test
    fun the_push_path_cannot_obtain_the_content_key() {
        val (store, core, kek) = handset()
        val session = KeyCustodySession(store, core)

        PushWakePath.prepare(session)

        assertEquals(
            "the push path installed a content key; on Android nothing but this discipline " +
                "stops it, because the messaging service shares the app process and the content " +
                "KEK asks for no authentication (ADR-007 B133)",
            0,
            core.installedContent.size,
        )
        assertFalse(session.contentAvailable())

        // ANTI-VACUITY: the content tier really was reachable throughout. Without this, an
        // assertion of "zero content installs" is satisfied by a fixture that could never have
        // produced one.
        session.installTier(KeyTier.CONTENT)
        assertEquals(
            "the content tier refused, so the assertion above was about the KEK and not about " +
                "the push path's declared tier set",
            1,
            core.installedContent.size,
        )
    }

    // --- what used to sit here ---------------------------------------------
    //
    // `a_locked_handset_cannot_install_the_content_key` and `unlocking_restores_the_content_tier`
    // are DELETED. They drove a lock/unlock cycle -- refuse the install, then `unlockAll()` and
    // watch it succeed -- and no install this build provisions can enter either half: the content
    // KEK asks for no authenticator, and there is no prompt anywhere in the app to end a refusal
    // with. The typed-refusal property they leaned on survives on its own terms, over the
    // population that CAN still raise it (a pre-B133 install), in
    // `FailableCustodyTest.every_tier_surfaces_each_failure_mode_as_its_own_type_from_the_store`.

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
        val (store, core, _) = handset()
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
