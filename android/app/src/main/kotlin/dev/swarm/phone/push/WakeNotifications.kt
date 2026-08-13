package dev.swarm.phone.push

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.content.Context
import android.content.Intent
import dev.swarm.phone.PhoneActivity
import dev.swarm.phone.R

/**
 * Phase B slice S17 -- PB-PUSH-4's platform half: what a wake puts on the lock screen.
 *
 * IT DECIDES NOTHING ABOUT THE WAKE. Whether the payload was genuine, whether it was new, and
 * whether the content key is held are all answered in the Go core -- only it holds the epoch
 * wake key and the persisted replay coordinate (PB-PUSH-3). This object is handed the two
 * primitives that come back from `App.handlePushWake` and renders them; it never reads the
 * payload, never reaches for a session and has nothing it could fetch.
 *
 * THE TEXT IS SUPPLIED, NOT AUTHORED HERE, and that is the same argument PB-SAS-1 makes about
 * the emoji table one layer down: `swarmmobile.WakeNotificationText` is the single copy, a
 * second one in Kotlin is a copy that drifts, and this one would drift towards saying more.
 *
 * SECRET, NOT PRIVATE, ON BOTH THE CHANNEL AND THE NOTIFICATION. The requirement names the two
 * separately because they fail separately: the channel's value is what the user's system
 * settings show and what applies when a notification sets none, and the notification's is what
 * applies to that notification. VISIBILITY_PRIVATE would still put a redacted Swarm line on a
 * lock screen anyone walking past can read; VISIBILITY_SECRET shows nothing until the device is
 * unlocked. It is KEPT under ADR-007 B133: the lock screen is a surface the phone's holder has
 * not opened, so it is not the endpoint that entry declares trusted.
 */
object WakeNotifications {

    /**
     * The channel id. On API 26+ a notification posted to a channel that was never created is
     * DROPPED by the framework, so [ensureChannel] runs before every post rather than once at
     * startup -- the process handling a wake is routinely a fresh one Android built for this
     * message alone.
     */
    const val CHANNEL_ID = "swarm.wake"

    /**
     * One id for every wake, so a second wake REPLACES the first rather than stacking.
     *
     * The alert carries nothing that distinguishes one wake from another -- it must not
     * (PB-PUSH-4) -- so N of them on the lock screen would be N copies of one sentence, which
     * tells the user only how chatty their agents are while the phone was locked. That count is
     * itself a channel from the machine to a locked screen, and it is the same argument
     * ADR-007 B20 makes about the envelope's SIZE: a disclosure is benign while it is CONSTANT.
     */
    const val NOTIFICATION_ID = 1

    /**
     * Create the wake channel, idempotently.
     *
     * IMPORTANCE_HIGH is not a preference. android/fcm-priority.tsv resolves the wake class to
     * HIGH priority because ADR-007 B16 makes push the sole path to a backgrounded phone, and a
     * high-priority FCM message delivered into a low-importance channel is a wake that arrives
     * and is not shown -- the two halves of one decision, which is why they are stated together.
     */
    fun ensureChannel(context: Context) {
        val channel = NotificationChannel(
            CHANNEL_ID,
            context.getString(R.string.wake_channel_name),
            NotificationManager.IMPORTANCE_HIGH,
        )
        channel.setLockscreenVisibility(Notification.VISIBILITY_SECRET)
        context.getSystemService(NotificationManager::class.java).createNotificationChannel(channel)
    }

    /**
     * The notification for one verified wake.
     *
     * @param text the constant the Go core supplied (`swarmmobile.WakeNotificationText`).
     * @param contentReady whether the phone HOLDS an epoch content key. It is a fact about KEY
     *  AVAILABILITY and not about any session, and not about the user either (ADR-007 B133
     *  decision 2): false on a phone awaiting its first grant, or on one whose keys a revoke
     *  purged.
     *
     * IT OFFERS NO NOTIFICATION ACTION (`Notification.Action`, the button row) IN EITHER STATE.
     * A button opens a screen ARMED to do something -- approve, dismiss -- and a tap that drove a
     * content read from a process FCM woke is exactly the fetch PB-PUSH-4 exists to stop. The
     * ready variant differs by SAYING more, which is the only thing that can be said honestly --
     * there is no session content anywhere on this path to render, because the wake is a
     * constant 78 bytes over an empty plaintext (ADR-007 B20).
     *
     * IT DOES OFFER A TAP ACTION NOW (agents-tracker-dwwv.2.5, M1.5), and that is a narrower
     * thing than the button row above: [openAppPendingIntent] opens the app and nothing more --
     * no verb, no fetch, no decrypt driven by the push. Before this it offered NONE either, so a
     * tapped wake dismissed itself (`setAutoCancel`) and opened nothing at all. What it opens to
     * is [dev.swarm.phone.PhoneSurface]'s own compiled-in default screen -- `Destination.INBOX`
     * with no drill-down -- because the wake envelope carries no session id to open a DETAIL on
     * (`swarmmobile.WakeAlert`'s own KDoc: "it must not grow one"), and `TriageInbox.TRIAGE_ORDER`
     * already sorts `needs_input`, the group an approval's session sits in, first. That is the
     * nearest honest destination: one tap from the card, never zero, and never a guess at which
     * session to open.
     */
    fun build(context: Context, text: String, contentReady: Boolean): Notification =
        Notification.Builder(context, CHANNEL_ID)
            .setSmallIcon(R.drawable.ic_swarm_wake)
            .setContentTitle(context.getString(R.string.app_name))
            .setContentText(bodyFor(context, text, contentReady))
            .setVisibility(Notification.VISIBILITY_SECRET)
            .setAutoCancel(true)
            .setContentIntent(openAppPendingIntent(context))
            .build()

    /**
     * The tap action: open [PhoneActivity] on a FRESH task, reading nothing off the Intent to
     * get there.
     *
     * `FLAG_ACTIVITY_NEW_TASK or FLAG_ACTIVITY_CLEAR_TASK` rather than an extra naming a
     * destination, and that is PB-SEC-11 rather than a style choice: PhoneActivity's own KDoc
     * states the boundary in the imperative -- "IT READS NOTHING OFF THE INTENT ... What is
     * shown comes from persisted local state alone" -- and `PhoneActivityWindowTest
     * .a_crafted_launch_intent_selects_nothing` enforces it by comparing a hostile intent's
     * render against a plain launch's, byte for byte. An extra here would be exactly the shape
     * that test exists to catch, so the mechanism is the task flags instead: CLEAR_TASK finishes
     * whatever `PhoneActivity` instance is running and starts a new one, which is constructed
     * with its own default field values (`destination = Destination.INBOX`, `detail = null`) --
     * the same screen a plain launch renders, reached deterministically regardless of what the
     * app had open when the wake arrived.
     *
     * `FLAG_IMMUTABLE` is not a hardening option on this app's minSdk 33 floor -- `PendingIntent
     * .getActivity` without either mutability flag throws on API 31+, and `FLAG_UPDATE_CURRENT`
     * is what lets a second wake arriving before the first is tapped refresh this PendingIntent's
     * saved Intent (there is nothing to refresh here, since the Intent carries no per-wake data,
     * but a stale duplicate lingering under the same request code is one Intent object doing no
     * harm either way).
     */
    private fun openAppPendingIntent(context: Context): PendingIntent {
        val intent = Intent(context, PhoneActivity::class.java).apply {
            flags = Intent.FLAG_ACTIVITY_NEW_TASK or Intent.FLAG_ACTIVITY_CLEAR_TASK
        }
        return PendingIntent.getActivity(
            context,
            NOTIFICATION_ID,
            intent,
            PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE,
        )
    }

    /**
     * The rendered line: the supplied constant, plus a second constant when the content key is
     * held. Both are string resources with no arguments, so there is no interpolation site here
     * for a session id to arrive at.
     */
    private fun bodyFor(context: Context, text: String, contentReady: Boolean): String =
        if (contentReady) text + " " + context.getString(R.string.wake_notification_open) else text
}
