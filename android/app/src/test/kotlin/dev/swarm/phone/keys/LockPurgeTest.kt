package dev.swarm.phone.keys

import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertThrows
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * PB-KEY-7 -- "Lock purges live memory. Invalidating the biometric gate is not enough...
 * On lock, background, or auth expiry the core must stop content operations, zeroize/discard
 * native key custody, purge decrypted session/snapshot/reply caches and sensitive UI state,
 * and require a fresh unwrap before restoring content."
 *
 * The Go half is App.PurgeKeys. This is the ANDROID half: the events that must reach it, the
 * Kotlin-side material that must be destroyed alongside it, and the recovery -- because a purge
 * with no way back bricks the app on the first screen lock.
 *
 * WHAT THE PURGE MEANS CHANGED WITH ADR-007 B35/B36, in one direction, and these tests changed
 * with it. It ends CONTENT custody: the live epoch content key, the router binding and the
 * decrypted caches. It leaves the sealed content key at rest -- destroying it is a permanent
 * brick, because PB-KEY-10 delivers the epoch key inside Go and the grant watermark refuses a
 * re-delivery as a replay -- and it leaves the WAKE tier entirely alone, because a push arrives
 * with nobody there to authorize anything.
 *
 * Plain JVM.
 */
class LockPurgeTest {

    private fun unlockedSession(): Triple<KeyCustodySession, RecordingCore, FakeKeystoreKek> {
        val kek = FakeKeystoreKek(lockedTiers = emptySet())
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
     * The three events PB-KEY-7 names outright, plus the two that reach the same state by
     * another route. Every one must purge; missing one leaves the content key live in a
     * process the user believes is locked.
     */
    @Test
    fun every_invalidation_event_purges_the_core() {
        for (event in InvalidationEvent.entries) {
            val (session, core, _) = unlockedSession()
            assertTrue("precondition: content custody is live", session.contentAvailable())

            session.invalidate(event)

            assertEquals("$event did not purge the Go core's key custody", 1, core.purgeCount)
            assertFalse("$event left content custody available", session.contentAvailable())
            assertTrue(
                "$event dropped the WAKE tier as well. App.PurgeKeys does not touch it " +
                    "(ADR-007 B35), so a session that forgets it reports the sole background " +
                    "wake path as unavailable while it is in fact working",
                session.wakeAvailable(),
            )
        }
    }

    /**
     * And the Kotlin side goes with it. The core zeroizing its copy is half the job while the
     * unwrapped bytes are still reachable on the Java heap -- where a heap dump, an
     * ADB-triggered ANR trace, or the next GC-delayed allocation can find them.
     *
     * Two observation points, because they catch different implementations. The KEK's own
     * output catches a session that never zeroized what it was handed. The session's FIELDS
     * catch the likelier shortcut: caching the unwrapped key so the user is not re-prompted,
     * which leaves the whole purge cosmetic while the first check still passes.
     */
    @Test
    fun a_purge_leaves_no_key_material_live_in_the_custody_layer() {
        val (session, _, kek) = unlockedSession()

        session.invalidate(InvalidationEvent.DEVICE_LOCKED)

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

    @Test
    fun content_operations_are_refused_until_a_fresh_unwrap() {
        val (session, core, kek) = unlockedSession()
        session.invalidate(InvalidationEvent.DEVICE_LOCKED)
        kek.lockedTiers = setOf(KeyTier.CONTENT)

        assertThrows(KeyCustodyException.UserAuthenticationRequired::class.java) {
            session.installTier(KeyTier.CONTENT)
        }
        assertEquals("nothing new crossed while locked", 1, core.installedContent.size)
        assertFalse(session.contentAvailable())
    }

    /** "Recoverable by re-installing the tier key" -- a screen lock must not brick the app. */
    @Test
    fun a_fresh_unwrap_restores_content_operations() {
        val (session, core, kek) = unlockedSession()
        session.invalidate(InvalidationEvent.DEVICE_LOCKED)

        kek.unlockAll()
        session.installTier(KeyTier.CONTENT)

        assertTrue(session.contentAvailable())
        assertEquals(2, core.installedContent.size)
        assertArrayEquals(tierKeyBytes(0x70), core.installedContent.last())
    }

    /**
     * THIS TEST IS THE INVERSE OF THE ONE IT REPLACES, and the replacement is the point rather
     * than a tidy-up. It was `the_wake_tier_is_reinstallable_after_a_purge_without_authentication`
     * -- ADR-007 B17(b)'s named fence -- and B35 established that it pinned a property the
     * product cannot have, twice over:
     *
     *  - the CLAIM was that App.PurgeKeys clears `st.Keys` wholesale, so a push arriving after a
     *    lock finds no wake key and Kotlin must re-install it. The purge no longer touches the
     *    wake tier at all, precisely so that this situation cannot arise;
     *  - and the REMEDY was unbuildable regardless. PB-KEY-10 moved epoch-key delivery entirely
     *    into Go, so the Android side has no source for those bytes: every `CustodyBlobs.tierKey`
     *    reference outside this test tree is under `src/test/`, and a wired production re-install
     *    would have thrown on its first call rather than restoring anything.
     *
     * So the fence stays at the same seam -- where the wrong model would be reintroduced -- and
     * asserts what has to be true instead: a screen lock leaves the sole background wake path
     * working, with nothing to re-install and nobody present to authorize it if there were.
     */
    @Test
    fun the_wake_tier_survives_a_lock_and_needs_no_reinstall() {
        val (session, core, kek) = unlockedSession()
        val wakeInstallsBefore = core.installedWake.size

        session.invalidate(InvalidationEvent.DEVICE_LOCKED)
        kek.lockedTiers = setOf(KeyTier.CONTENT)

        assertTrue(
            "the lock dropped the wake tier. A high-priority FCM push is the SOLE background " +
                "wake path (ADR-007 B9/B16) and the handset holds no other source for the key, " +
                "so a lock that takes it stops the phone being wakeable for good",
            session.wakeAvailable(),
        )

        // And the push path still runs on a locked handset, which is the property that matters.
        PushWakePath.prepare(session)
        assertTrue(session.wakeAvailable())
        assertTrue(
            "the push path could not arm itself while the CONTENT tier was locked",
            core.installedWake.size > wakeInstallsBefore,
        )
        assertFalse("re-arming the wake path must not restore content custody", session.contentAvailable())
    }

    /** Purging twice is not an error; lock and background arrive together all the time. */
    @Test
    fun purging_is_idempotent() {
        val (session, core, _) = unlockedSession()

        session.invalidate(InvalidationEvent.DEVICE_LOCKED)
        session.invalidate(InvalidationEvent.APP_BACKGROUNDED)

        assertFalse(session.contentAvailable())
        assertEquals(2, core.purgeCount)
    }
}
