package dev.swarm.phone.push

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.content.Context
import dev.swarm.phone.R

/**
 * Phase B slice S17 -- PB-PUSH-4's platform half: what a wake puts on the lock screen.
 *
 * IT DECIDES NOTHING ABOUT THE WAKE. Whether the payload was genuine, whether it was new, and
 * whether the user has authenticated are all answered in the Go core -- only it holds the epoch
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
 * applies to that notification. VISIBILITY_PRIVATE would still put a redacted Swarm line on the
 * lock screen of a device its owner may not be holding; VISIBILITY_SECRET shows nothing until
 * the device is unlocked.
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
     * The notification for one authenticated wake.
     *
     * @param text the constant the Go core supplied (`swarmmobile.WakeNotificationText`).
     * @param contentReady whether the CONTENT tier is open. It is a fact about custody and not
     *  about any session: false on every wake that arrives with the screen locked, which is the
     *  case the wake exists for.
     *
     * THERE IS NO ACTION ON IT, in either state. An action opens a screen, and this app declares
     * no Activity yet (S16 shipped the screen MODELS), so one would be a button that does
     * nothing; on a locked handset it would additionally be a tap that drives a decrypt the
     * content tier is going to refuse. The authenticated variant differs by SAYING more, which is
     * the only thing that can be said honestly -- there is no session content anywhere on this
     * path to render, because the wake is a constant 78 bytes over an empty plaintext
     * (ADR-007 B20) and fetching some is the defect PB-PUSH-4 exists to stop.
     */
    fun build(context: Context, text: String, contentReady: Boolean): Notification =
        Notification.Builder(context, CHANNEL_ID)
            .setSmallIcon(R.drawable.ic_swarm_wake)
            .setContentTitle(context.getString(R.string.app_name))
            .setContentText(bodyFor(context, text, contentReady))
            .setVisibility(Notification.VISIBILITY_SECRET)
            .setAutoCancel(true)
            .build()

    /**
     * The rendered line: the supplied constant, plus a second constant when the user has
     * authenticated. Both are string resources with no arguments, so there is no interpolation
     * site here for a session id to arrive at.
     */
    private fun bodyFor(context: Context, text: String, contentReady: Boolean): String =
        if (contentReady) text + " " + context.getString(R.string.wake_notification_open) else text
}
