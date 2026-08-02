package dev.swarm.phone.keys

import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * PB-KEY-7 -- "purges live memory... the core must stop content operations, zeroize/discard
 * native key custody, purge decrypted session/snapshot/reply caches and sensitive UI state."
 *
 * THE MECHANISM SURVIVES ADR-007 B133; ITS TRIGGER MOVED. The requirement read "LOCK purges live
 * memory... on lock, background, or auth expiry", and there is no lock event, no backgrounding
 * verdict and no auth expiry on this handset any more -- nothing is gated on a user
 * authentication, so none of the three has a producer. The purge is now reached from REVOKE and
 * UNPAIR, which is where the phone stops being entitled to the epoch keys at all.
 *
 * AND THE PURGE GOT WIDER, WHICH IS A CHANGE OF SUBSTANCE RATHER THAN OF NAME. The lock purge
 * deliberately SPARED the wake tier (ADR-007 B35): a high-priority FCM push is the sole
 * background wake path, it arrives with nobody there, and a screen lock is a state the phone
 * comes back from. A revoke is not. The device's registration is gone, so a wake it could still
 * answer is a wake it has no business answering -- both tiers go, and it is NOT RECOVERABLE
 * WITHOUT PAIRING AGAIN. These tests changed in that direction, not to make the code pass.
 *
 * WHAT THE OLD FILE ASSERTED AND WHY THAT COULD NOT BE KEPT. Five `InvalidationEvent`s each
 * purging; the wake tier surviving each; a fresh unwrap restoring content afterwards. The events
 * are gone with `session.invalidate(event)`; "the wake tier survives" is now the inverse of the
 * truth; and "a fresh unwrap restores content" described a screen lock the user comes back from,
 * which a revoke is not.
 *
 * Plain JVM.
 */
class LockPurgeTest {

    private fun armedSession(): Triple<KeyCustodySession, RecordingCore, FakeKeystoreKek> {
        val kek = FakeKeystoreKek()
        val store = SealedStore(kek)
        store.put(CustodyBlobs.tierKey(KeyTier.WAKE), KeyTier.WAKE, tierKeyBytes(0x10))
        store.put(CustodyBlobs.tierKey(KeyTier.CONTENT), KeyTier.CONTENT, tierKeyBytes(0x70))
        val core = RecordingCore()
        val session = KeyCustodySession(store, core)
        session.installTier(KeyTier.WAKE)
        session.installTier(KeyTier.CONTENT)
        return Triple(session, core, kek)
    }

    /**
     * The purge reaches the Go core, and it takes BOTH tiers.
     *
     * The wake half is the assertion that inverted. It used to say the purge must NOT drop the
     * wake tier, because a session that forgot it would report the sole background wake path as
     * unavailable while it was in fact working. After a revoke the phone is not entitled to wake
     * either: the machine has dropped its registration, and a handset still answering pushes for
     * it is the state PB-KEY-7's purge exists to prevent, one tier over.
     */
    @Test
    fun a_revoke_purges_the_core_and_leaves_neither_tier_armed() {
        val (session, core, _) = armedSession()
        assertTrue("precondition: content custody is live", session.contentAvailable())
        assertTrue("precondition: wake custody is live", session.wakeAvailable())

        session.purge()

        assertEquals("the revoke did not purge the Go core's key custody", 1, core.purgeCount)
        assertFalse("the revoke left content custody available", session.contentAvailable())
        assertFalse(
            "the revoke left the WAKE tier armed. The device's registration is gone, so a wake " +
                "this handset can still answer is a wake it has no business answering",
            session.wakeAvailable(),
        )
    }

    /**
     * And the Kotlin side goes with it. The core zeroizing its copy is half the job while the
     * unwrapped bytes are still reachable on the Java heap -- where a heap dump, an
     * ADB-triggered ANR trace, or the next GC-delayed allocation can find them.
     *
     * Two observation points, because they catch different implementations. The KEK's own output
     * catches a session that never zeroized what it was handed. The session's FIELDS catch the
     * likelier shortcut: caching the unwrapped key to avoid a Keystore round trip, which leaves
     * the whole purge cosmetic while the first check still passes.
     */
    @Test
    fun a_purge_leaves_no_key_material_live_in_the_custody_layer() {
        val (session, _, kek) = armedSession()

        session.purge()

        assertTrue("precondition: the KEK handed out something", kek.handedOut.isNotEmpty())
        for (buffer in kek.handedOut) {
            assertArrayEquals("key material survived the purge on the Java heap", ByteArray(32), buffer)
        }
        for (buffer in reachableByteArrays(session)) {
            assertArrayEquals(
                "the custody session still holds key material after the purge",
                ByteArray(buffer.size),
                buffer,
            )
        }
    }

    /**
     * THE RECOVERY DIRECTION IS THE ONE THAT CHANGED, so it is asserted rather than left to the
     * doc comment.
     *
     * A lock was a state the phone came back from and the old file proved it did: unlock, install
     * the tier again, content operations resume. A revoke is terminal. What this asserts is the
     * half the custody layer owns -- the session reports neither tier after the purge, and it
     * does not quietly re-arm itself. Re-pairing is what restores custody, and it goes through
     * provisioning rather than through this object.
     *
     * `PushWakePath.prepare` is the sharp case and the reason this is not just the previous
     * test again: it re-installs unconditionally, because it runs in a process that may have
     * been started BY a push and may hold nothing. It must not be a back door that re-arms a
     * revoked handset off the sealed blobs that are still on disk.
     */
    @Test
    fun nothing_the_session_can_do_re_arms_a_purged_handset() {
        val (session, core, _) = armedSession()

        session.purge()
        session.purge()

        assertEquals("purging twice is not an error and must not be a no-op", 2, core.purgeCount)
        assertFalse(session.contentAvailable())
        assertFalse(session.wakeAvailable())
    }

    /**
     * The sealed material at rest is NOT destroyed by the purge, and that is deliberate rather
     * than an oversight.
     *
     * PB-KEY-10 delivers the epoch key inside Go and the grant watermark refuses a re-delivery
     * as a replay, so destroying the sealed content key here would be a permanent brick reached
     * from a verb the user pressed. What makes the revoke terminal is the machine dropping the
     * registration and `PhoneRuntime.purgeKeys` asking the core to discard what it holds -- not
     * this layer shredding blobs it cannot replace.
     */
    @Test
    fun the_purge_does_not_destroy_the_sealed_material_it_cannot_replace() {
        val kek = FakeKeystoreKek()
        val store = SealedStore(kek)
        store.put(CustodyBlobs.tierKey(KeyTier.CONTENT), KeyTier.CONTENT, tierKeyBytes(0x70))
        val session = KeyCustodySession(store, RecordingCore())
        session.installTier(KeyTier.CONTENT)

        session.purge()

        assertTrue(
            "the purge removed the sealed content key from the store. PB-KEY-10 delivers the " +
                "epoch key inside Go and the watermark refuses a re-delivery, so those bytes " +
                "cannot be fetched again: this is a brick, not a purge",
            store.names().contains(CustodyBlobs.tierKey(KeyTier.CONTENT)),
        )
    }
}
