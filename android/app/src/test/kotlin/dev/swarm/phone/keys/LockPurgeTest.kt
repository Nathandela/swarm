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
 * The Go half is App.PurgeKeys (mobile/app.go:305-325), already shipped by S8. This is the
 * ANDROID half: the events that must reach it, the Kotlin-side material that must be
 * destroyed alongside it, and the recovery -- because a purge with no way back bricks the app
 * on the first screen lock.
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
            assertFalse(
                "$event left the session believing the wake key is still installed. " +
                    "App.PurgeKeys clears st.Keys wholesale, so it is not",
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
     * The interaction with ADR-007 B16 that is easy to miss. App.PurgeKeys clears
     * `st.Keys` WHOLESALE -- `st.Keys = crypto.EpochKeys{}` (mobile/app.go:317) -- so the wake
     * key goes with the content key. Since a high-priority FCM push is the SOLE background
     * wake path, a locked handset that received a push after a lock purge has no wake key in
     * the core, and the Android side must put it back without any authentication.
     *
     * An implementation that assumes the wake tier survived the purge produces a wake path
     * that works until the first screen lock and then silently stops.
     */
    @Test
    fun the_wake_tier_is_reinstallable_after_a_purge_without_authentication() {
        val (session, core, kek) = unlockedSession()
        session.invalidate(InvalidationEvent.DEVICE_LOCKED)
        kek.lockedTiers = setOf(KeyTier.CONTENT)
        val wakeInstallsBefore = core.installedWake.size

        PushWakePath.prepare(session)

        assertTrue(
            "after a purge the core holds no wake key either (App.PurgeKeys clears both " +
                "tiers), so the push path must re-install it -- while locked",
            core.installedWake.size > wakeInstallsBefore,
        )
        assertTrue(session.wakeAvailable())
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
