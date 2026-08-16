package dev.swarm.phone.push

import android.app.NotificationManager
import android.content.Context
import android.util.Log

/**
 * The Go core's verdict on one WakeV1 payload, as primitives and a sealed type -- never the
 * bound AAR class, for the reason WakeNotificationTest records: binding a gomobile type here
 * would make a policy test fail whenever the AAR is stale, which teaches everyone to ignore
 * it.
 */
sealed class WakeVerdict {
    /** The core refused the wake: forged, replayed, expired, unknown address, or no key yet. */
    object Dropped : WakeVerdict()

    /** The core accepted the wake; [text] is the core-supplied constant, never composed here. */
    data class Accepted(val text: String, val contentReady: Boolean) : WakeVerdict()
}

/**
 * Wave R3 scope 3's rendering half: what one WakeV1 verdict entitles the app to DO
 * (push-gateway-api.md PG-WAKE-13/17 -- "an unverifiable wake is dropped and counted, never
 * acted on").
 *
 * The Go core is the only party holding the per-pairing wake key and the durable per-address
 * high-water; it answers with a verdict, and this object is the whole of what Kotlin may do
 * with it:
 *
 *  - [WakeVerdict.Accepted] renders exactly the one generic notification [WakeNotifications]
 *    already builds, on the existing secret-visibility channel. No new rendering surface.
 *  - [WakeVerdict.Dropped] renders NOTHING -- no notification, no activity start, no throw --
 *    and reports the drop to the [DropSink] exactly once. The relay and the provider both
 *    handle every wake, and a policy that rendered on a refused verdict hands whoever can
 *    replay a captured envelope a button that puts notifications on the owner's lock screen.
 *
 * Handling is TOTAL AND QUIET: this runs in a FirebaseMessagingService callback on a phone
 * with no user present, where an uncaught exception is a crashed process.
 */
object WakeReceiptPolicy {

    private const val TAG = "SwarmPush"

    /**
     * Where a dropped wake is REPORTED -- not counted. Authoritative counting lives in the
     * Go core: AcceptWakeV1 advances the WakeDrops counter on every refusal, so a verdict
     * of Dropped arrives here ALREADY COUNTED and Core.WakeDrops() is a read-only getter
     * with no incrementer to wire to. Production therefore hands a no-op reporter (or a
     * local metrics/log observer); wiring this to anything that increments the core's
     * counter again would double-count every drop. Tests hand a recorder to pin the
     * exactly-once call per Dropped verdict.
     */
    interface DropSink {
        fun dropped()
    }

    /** Act on one verdict. Never throws (see the class doc for why quiet is load-bearing). */
    fun handle(context: Context, verdict: WakeVerdict, sink: DropSink) {
        try {
            when (verdict) {
                is WakeVerdict.Dropped -> sink.dropped()
                is WakeVerdict.Accepted -> {
                    WakeNotifications.ensureChannel(context)
                    context.getSystemService(NotificationManager::class.java).notify(
                        WakeNotifications.NOTIFICATION_ID,
                        WakeNotifications.build(context, verdict.text, verdict.contentReady),
                    )
                }
            }
        } catch (t: Throwable) {
            // Quiet, not silent: the failure is logged, and a wake whose rendering failed is
            // still only a missed line on the lock screen -- the state it announces is
            // reconciled on the next foreground either way.
            Log.w(TAG, "wake verdict handling failed; nothing rendered", t)
        }
    }
}
