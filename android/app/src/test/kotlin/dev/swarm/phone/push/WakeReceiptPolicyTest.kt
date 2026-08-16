package dev.swarm.phone.push

import android.app.NotificationManager
import android.content.Context
import androidx.test.core.app.ApplicationProvider
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.Shadows.shadowOf

/**
 * FAILING-FIRST (TDD RED, GG-5) for Wave R3's Android slice, scope item 3's rendering
 * half: what one WakeV1 verdict entitles the app to DO (push-gateway-api.md PG-WAKE-13/17,
 * the scope's "an unverifiable wake is dropped and counted, never acted on").
 *
 * THE SEAM AND WHY IT IS NEW. `SwarmMessagingService` hands the raw payload to the Go core,
 * which is the only party holding the per-pairing wake key and the durable per-address
 * high-water; the core answers with a VERDICT. What Kotlin does with that verdict is policy
 * this module owns, and today it exists only as inline service code that renders the
 * accepted case. R3 makes the refused case load-bearing -- a dropped wake must leave a
 * countable trace for the degraded-push surface, and must provably do NOTHING else -- so
 * the policy becomes a named object, [WakeReceiptPolicy], testable without the AAR:
 *
 *   - WakeVerdict.Accepted(text, contentReady) -> exactly the one generic notification
 *     WakeNotifications already builds. No new rendering surface.
 *   - WakeVerdict.Dropped -> NO notification, NO activity start, NO throw; the drop is
 *     reported to the [WakeReceiptPolicy.DropSink] exactly once (a REPORT, not a count:
 *     the Go core's AcceptWakeV1 already counted the refusal, so production hands a no-op
 *     reporter or local observer -- see the DropSink doc -- and tests hand a recorder).
 *
 * THE INPUT IS PRIMITIVES AND A SEALED VERDICT, NOT THE BOUND AAR TYPE, for the reason
 * WakeNotificationTest records: binding a gomobile class here would make a policy test
 * fail whenever the AAR is stale, which teaches everyone to ignore it.
 *
 * WHAT ROBOLECTRIC CAN AND CANNOT SAY. It models policy: which notification a verdict
 * produces, which it must not. It models no handset, no FCM, no Doze, no lock screen.
 * PB-E2E-5 and R3's physical exit are untouched by this file.
 */
@RunWith(RobolectricTestRunner::class)
class WakeReceiptPolicyTest {

    private val context: Context = ApplicationProvider.getApplicationContext()

    private fun notificationManager(): NotificationManager =
        context.getSystemService(Context.NOTIFICATION_SERVICE) as NotificationManager

    /** A recording sink: the policy's only permitted side effect for a dropped wake. */
    private class RecordingSink : WakeReceiptPolicy.DropSink {
        var drops = 0
        override fun dropped() {
            drops++
        }
    }

    /**
     * The refused case is the one this slice exists for: an unverifiable wake -- failed
     * tag, replay, expired, unknown address, no key yet -- renders NOTHING. The relay and
     * the provider both handle every wake, and a policy that rendered on a refused verdict
     * hands whoever can replay a captured envelope a button that puts notifications on the
     * owner's lock screen at arbitrary times.
     */
    @Test
    fun aDroppedWakeRendersNothing() {
        val sink = RecordingSink()
        WakeReceiptPolicy.handle(context, WakeVerdict.Dropped, sink)

        val posted = shadowOf(notificationManager()).allNotifications
        assertTrue(
            "a dropped wake posted ${posted.size} notification(s); it must post none",
            posted.isEmpty(),
        )
    }

    /** The drop is COUNTED, exactly once -- the trace the degraded-push surface reads. */
    @Test
    fun aDroppedWakeIsCountedExactlyOnce() {
        val sink = RecordingSink()
        WakeReceiptPolicy.handle(context, WakeVerdict.Dropped, sink)
        assertEquals("one dropped wake must report one drop", 1, sink.drops)

        WakeReceiptPolicy.handle(context, WakeVerdict.Dropped, sink)
        assertEquals("two dropped wakes must report two drops", 2, sink.drops)
    }

    /**
     * An accepted wake renders exactly the one generic notification, on the existing
     * secret-visibility channel, with the core-supplied constant -- acceptance changes
     * nothing about PB-PUSH-4's rendering contract, and it reports no drop.
     */
    @Test
    fun anAcceptedWakeRendersTheOneGenericNotification() {
        val sink = RecordingSink()
        val text = "Swarm has an update for you."
        WakeReceiptPolicy.handle(context, WakeVerdict.Accepted(text, contentReady = false), sink)

        val posted = shadowOf(notificationManager()).allNotifications
        assertEquals("an accepted wake must post exactly one notification", 1, posted.size)
        assertEquals("an accepted wake reports no drop", 0, sink.drops)

        val shadow = shadowOf(posted[0])
        assertEquals(
            "the rendered line is the core's constant, nothing interpolated",
            text,
            shadow.contentText.toString(),
        )
    }

    /**
     * Verdict handling is total and quiet: a dropped wake in a background process must
     * never throw (an uncaught exception in a FirebaseMessagingService callback is a
     * crashed process on a phone with no user present). Calling handle twice with each
     * verdict shape is the cheap totality probe.
     */
    @Test
    fun handlingIsTotalAndQuiet() {
        val sink = RecordingSink()
        WakeReceiptPolicy.handle(context, WakeVerdict.Dropped, sink)
        WakeReceiptPolicy.handle(context, WakeVerdict.Accepted("Swarm has an update for you.", true), sink)
        WakeReceiptPolicy.handle(context, WakeVerdict.Dropped, sink)
        assertEquals(2, sink.drops)
    }
}
