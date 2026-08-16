package dev.swarm.phone.push

import android.util.Log
import com.google.firebase.messaging.FirebaseMessagingService
import com.google.firebase.messaging.RemoteMessage
import dev.swarm.phone.PhoneStartup
import dev.swarm.phone.SwarmApplication
import swarmmobile.App

/**
 * Phase B slice S17 -- the OS entry point for push, and the only way an FCM message reaches
 * this app.
 *
 * The relay's sender emits a DATA-ONLY message (internal/remote/push/fcm.go). That is not an
 * implementation detail: a `notification` block would be rendered by the SYSTEM, on the lock
 * screen, from text the PROVIDER composed, which is precisely the rendering PB-PUSH-4 puts
 * under this app's control. Android does not render a data-only message itself -- it hands it
 * to this service, and without the service the phone receives every wake and acts on none.
 *
 * IT DECIDES NOTHING. Kotlin does no base64, no parsing and no crypto: the payload crosses to
 * the Go core unchanged, because only the core holds the epoch wake key that says the wake is
 * genuine and the persisted replay coordinate (PB-PUSH-3) that says it is new. A notification
 * raised without that call is raised on the say-so of the push provider and the relay, neither
 * of which holds a wake key and neither of which is therefore distinguishable from an attacker
 * -- and the relay handles every wake and can re-deliver one, which would be a button that puts
 * a notification on the owner's lock screen whenever it likes.
 *
 * AND IT FETCHES NOTHING. No content verb is reachable from [onMessageReceived], and that is
 * PB-PUSH-4's real requirement rather than a property of the string it renders. It is also now
 * the whole of the tier boundary on this side (ADR-007 B133): the content KEK's unwrap used to
 * refuse while the user was not authenticated, and this file's discipline sat behind that
 * refusal. It no longer does, so an app that reached for a session here would GET one.
 */
class SwarmMessagingService : FirebaseMessagingService() {

    /**
     * FCM rotated this install's token. It does not ask, and it fires on reinstall, on app data
     * restore and on a token TTL expiry -- any of them while the app is backgrounded and
     * therefore, under ADR-007 B16, disconnected. A phone that does not notice is silently
     * unreachable by push forever, with nothing on either side reporting it.
     */
    override fun onNewToken(token: String) {
        PushTokens.register(this, token)
    }

    /**
     * One wake, as the provider delivered it.
     *
     * "e" is the ONE key internal/remote/push/fcm.go marshalMessage puts in the data block, and
     * it is spelled here rather than hidden behind a constant because a rename on either side is
     * a phone that receives every wake and opens none -- a failure neither side's own tests can
     * see. Every additional key would be provider-visible metadata, and PB-PUSH-3 concedes the
     * provider the token, the timing and the size only.
     */
    override fun onMessageReceived(message: RemoteMessage) {
        val payload = message.data["e"] ?: return
        renderWake(payload)
    }

    /**
     * Verify in the core, then hand the VERDICT to [WakeReceiptPolicy] -- the one tested copy
     * of what a verdict entitles this app to do (R3: "an unverifiable wake is dropped and
     * counted, never acted on"; a refusal arrives here ALREADY counted by the core's durable
     * counter, which is why the sink below reports the core's numbers rather than keeping its
     * own).
     *
     * A REFUSED WAKE RENDERS NOTHING, and that decision is the policy's, not this file's: an
     * unparseable payload, a forgery, a replay and a wake past its TTL are all cases where the
     * only party that could have told this app to notify the user did not. An inline second
     * copy of that rule is the copy that drifts toward rendering anyway.
     *
     * A phone that cannot be built renders nothing either. There is no user present and no
     * screen to report to, so the alternative is a lock-screen line about a wake this app was
     * never able to verify.
     */
    private fun renderWake(payload: String) {
        val startup = (application as SwarmApplication).phoneRuntime.phone()
        if (startup !is PhoneStartup.Ready) return
        val verdict = try {
            val alert = startup.app.handlePushWake(payload)
            WakeVerdict.Accepted(alert.getText(), alert.getContentReady())
        } catch (refused: Exception) {
            WakeVerdict.Dropped
        }
        WakeReceiptPolicy.handle(this, verdict, DiagnosedByTheCore(startup.app))
    }

    /**
     * The production [WakeReceiptPolicy.DropSink]. It COUNTS NOTHING -- authoritative counting
     * is the Go core's (AcceptWakeV1 advances the durable counter on every refusal before the
     * verdict ever reaches Kotlin), and anything counting again here would double-count every
     * drop. What it does is REPORT the core's own breakdown, which is the only surface an
     * operator can reach from a process that wakes, refuses and dies.
     *
     * WHY THE BREAKDOWN AND NOT THE TOTAL. A machine whose clock runs ahead of this phone's
     * has 100% of its wakes correctly refused, permanently -- the machine stamps issued_at
     * from its own clock and re-sends the SAME sealed bytes on every retry (PG-WAKE-12) -- and
     * against a single total that is indistinguishable from somebody forging wakes at this
     * address. The two have opposite remedies: fix a clock on a machine the owner controls, or
     * do not trust the sender. `peer_clock_ahead` versus `unauthenticated` is the whole of
     * that distinction, and without it a dead wake path looks like an attack (or an attack
     * looks like a clock).
     *
     * IT PRINTS COUNTERS AND NOTHING ELSE: no address, no key, no sequence number, no
     * timestamp. PB-SEC-3's inventory records the line.
     */
    private class DiagnosedByTheCore(private val app: App) : WakeReceiptPolicy.DropSink {
        override fun dropped() {
            val counts = app.wakeDropCounts()
            Log.w(TAG, "wake refused; durable drop counters total=${counts.getTotal()} " +
                "peer_clock_ahead=${counts.getPeerClockAhead()} " +
                "unauthenticated=${counts.getUnauthenticated()} replay=${counts.getReplay()} " +
                "expired=${counts.getExpired()} no_key=${counts.getNoKey()} " +
                "revoked=${counts.getRevoked()} malformed=${counts.getMalformed()}")
        }
    }

    private companion object {
        const val TAG = "SwarmPush"
    }
}
