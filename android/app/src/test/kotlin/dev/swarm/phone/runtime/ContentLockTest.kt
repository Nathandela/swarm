package dev.swarm.phone.runtime

import android.app.Application
import android.content.Intent
import android.os.Looper
import androidx.test.core.app.ApplicationProvider
import dev.swarm.phone.keys.AuthorizationLedger
import dev.swarm.phone.keys.GatedOperation
import dev.swarm.phone.keys.InvalidationEvent
import dev.swarm.phone.keys.PromptOutcome
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.Shadows.shadowOf

/**
 * PB-KEY-7 ("lock purges live memory") and PB-SEC-2's invalidation clause, at the layer that
 * decides WHEN. Both requirements were NOT MET for want of exactly this: ADR-007 B35 found no
 * trigger anywhere in the tree -- no `ProcessLifecycleOwner`, no `ACTION_SCREEN_OFF`, no
 * `onStop`/`onTrimMemory` -- and B36 found [AuthorizationLedger.invalidate] with zero production
 * callers beside it.
 *
 * WHAT IS AND IS NOT PROVABLE HERE, stated plainly because PB-E2E-5 is DEFERRED (ADR-007 B31)
 * and Robolectric models POLICY only:
 *
 *  - a real screen lock, a real biometric, a real Keystore refusal and real Doze are hardware
 *    and are claimed by nothing in this file. What is asserted is that the app is LISTENING for
 *    the signals the platform sends, and that each one reaches the purge exactly once;
 *  - the cryptographic half is not here either. The content KEK is provisioned with
 *    `setUserAuthenticationParameters(60, AUTH_BIOMETRIC_STRONG)`, so the platform itself
 *    refuses the unwrap on a locked handset and past the window; this layer's job is only to
 *    stop the Go core answering from memory instead of asking.
 */
@RunWith(RobolectricTestRunner::class)
class ContentLockTest {

    private val application get() = ApplicationProvider.getApplicationContext<Application>()

    /** A core that records what the lifecycle layer asked of it, and nothing else. */
    private class RecordingSink : ContentLockSink {
        var locks = 0
            private set

        override fun lockContent() {
            locks++
        }
    }

    private fun lock(ledger: AuthorizationLedger = AuthorizationLedger()): Pair<ContentLock, RecordingSink> {
        val sink = RecordingSink()
        return ContentLock(sink, ledger) to sink
    }

    /**
     * PB-SEC-2 names invalidation as a clause of its own and lists four events; PB-KEY-7 adds
     * auth expiry. Every one must end content custody -- a table with a hole in it is a state
     * where the process keeps the key with the user believing it does not.
     */
    @Test
    fun every_invalidation_event_ends_content_custody() {
        for (event in InvalidationEvent.entries) {
            val (contentLock, sink) = lock()

            contentLock.invalidate(event)

            assertEquals("$event did not reach the Go core's lock purge", 1, sink.locks)
            assertEquals(event, contentLock.lastEvent)
        }
    }

    /**
     * The ledger half, which is what ADR-007 B36 found unwired. It is not the gate -- the gate is
     * the Keystore refusing an unwrap -- but it decides whether prompting is worth doing, and one
     * still holding a live authorization across a screen lock answers "already authorized" for an
     * operation the platform is about to refuse.
     */
    @Test
    fun an_invalidation_drops_every_live_authorization() {
        val ledger = AuthorizationLedger()
        val (contentLock, _) = lock(ledger)
        ledger.beginPrompt(GatedOperation.INPUT)
        ledger.endPrompt(GatedOperation.INPUT, PromptOutcome.SUCCEEDED, 1_000, ticket = null)
        assertTrue(
            "fixture: the authorization was never granted, so the assertion below would pass vacuously",
            contentLock.authorized(GatedOperation.INPUT, 1_000),
        )

        contentLock.invalidate(InvalidationEvent.DEVICE_LOCKED)

        assertFalse(
            "PB-SEC-2: a screen lock left a live INPUT authorization in the ledger",
            contentLock.authorized(GatedOperation.INPUT, 1_000),
        )
    }

    // --- the two platform signals -------------------------------------------

    /**
     * Backgrounding. It is the STARTED COUNT reaching zero, not any single Activity stopping:
     * one Activity stopping while another is up is not the app leaving the foreground.
     */
    @Test
    fun the_app_leaving_the_foreground_ends_content_custody() {
        val (contentLock, sink) = lock()
        val triggers = ContentLockTriggers(contentLock)

        triggers.activityStarted()
        assertEquals("nothing has happened yet", 0, sink.locks)

        triggers.activityStopped(changingConfigurations = false)

        assertEquals(
            "PB-KEY-7: backgrounding did not end content custody. The process keeps the epoch " +
                "content key and every decrypted cache while the app is out of sight",
            1,
            sink.locks,
        )
    }

    /**
     * And the false positive that would make the app unusable. A rotation stops and restarts the
     * Activity without the app leaving the foreground; ending content custody there is a
     * re-authentication the user cannot connect to anything they did.
     */
    @Test
    fun a_configuration_change_does_not_end_content_custody() {
        val (contentLock, sink) = lock()
        val triggers = ContentLockTriggers(contentLock)

        triggers.activityStarted()
        triggers.activityStopped(changingConfigurations = true)

        assertEquals("PB-KEY-7: an orientation change was treated as a backgrounding", 0, sink.locks)
    }

    /**
     * The screen going off, through the receiver as REGISTERED rather than by calling the
     * handler. That is the half a hand-rolled call cannot check and the half that was missing:
     * a receiver nothing registers is a purge that never happens.
     */
    @Test
    fun the_screen_going_off_ends_content_custody() {
        val (contentLock, sink) = lock()
        ContentLockTriggers(contentLock).install(application)

        application.sendBroadcast(Intent(Intent.ACTION_SCREEN_OFF))
        shadowOf(Looper.getMainLooper()).idle()

        assertEquals(
            "PB-KEY-7: ACTION_SCREEN_OFF reached no receiver. The screen locking is the event the " +
                "requirement names first, and the app is not listening for it",
            1,
            sink.locks,
        )
        assertEquals(InvalidationEvent.DEVICE_LOCKED, contentLock.lastEvent)
    }

    /**
     * The receiver is registered NOT_EXPORTED and `ACTION_SCREEN_OFF` is a protected broadcast,
     * so neither of the two ways a third-party app could reach it is open. This asserts the third
     * defence, which is the only one visible in this module's own code: the handler re-checks the
     * action and does nothing with an intent it did not expect.
     *
     * It matters for PB-SEC-11 rather than for tidiness. The purge is the one facade verb this
     * layer can reach, so if anything could drive it from outside the process, the resolution of
     * PB-SEC-11 against PB-KEY-7 would be a relocation rather than a fix.
     */
    @Test
    fun a_broadcast_carrying_another_action_changes_nothing() {
        val (contentLock, sink) = lock()
        ContentLockTriggers(contentLock).install(application)

        application.sendBroadcast(Intent(Intent.ACTION_USER_PRESENT))
        application.sendBroadcast(Intent("dev.swarm.phone.NOT_A_REAL_ACTION"))
        shadowOf(Looper.getMainLooper()).idle()

        assertEquals("an unrelated broadcast reached the lock purge", 0, sink.locks)
    }

    /**
     * Lock and background arrive together all the time -- the screen goes off and the Activity
     * stops -- so the purge has to be idempotent. A guard that skipped the second because it
     * believed nothing was left is a guard that cannot fail: the Go core is the only thing that
     * knows what it is holding, and it is the thing being asked.
     */
    @Test
    fun locking_twice_is_not_an_error() {
        val (contentLock, sink) = lock()
        val triggers = ContentLockTriggers(contentLock)
        triggers.activityStarted()

        contentLock.invalidate(InvalidationEvent.DEVICE_LOCKED)
        triggers.activityStopped(changingConfigurations = false)

        assertEquals(2, sink.locks)
    }
}
