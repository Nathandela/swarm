package dev.swarm.phone.runtime

import android.app.NotificationManager

/**
 * Whether the framework will actually SHOW what this app posts (agents-tracker-2yfn).
 *
 * IT IS A DIFFERENT QUESTION FROM THE PERMISSION AND THAT IS THE WHOLE POINT. On API 33+ a GLOBAL
 * disable revokes POST_NOTIFICATIONS, so `checkSelfPermission` reports it and
 * [PermissionStateResolver] already answers. A PER-CHANNEL block does not: the user long-presses a
 * wake, blocks `Agent updates`, the permission stays GRANTED and always will, and every wake from
 * then on is dropped by the framework before this app is started at all. Push is the sole path to a
 * backgrounded phone (ADR-007 B16), so the product simply stops working -- PB-RUN-2's named silent
 * drop, arriving by the one route a permission check cannot see.
 *
 * ANDROID OFFERS NO REMEDY THIS APP CAN APPLY. `createNotificationChannel` is idempotent and cannot
 * re-raise an importance the user lowered, which is deliberate on the platform's part: a user's
 * choice about notifications is not an app's to overrule. So the only thing left is to SAY so, and
 * to lead them to the screen where the choice was made.
 */
enum class NotificationDelivery {
    /** The framework will show a wake posted to the channel. */
    DELIVERABLE,

    /**
     * `areNotificationsEnabled()` is false: nothing this app posts is shown, whatever any channel
     * says. Remedied on the APP's notification screen; the channel's own page offers a switch that
     * changes nothing while this holds.
     */
    APP_BLOCKED,

    /**
     * The wake channel exists and is set to `None`. The app may notify and does; this one category
     * is dropped. Remedied only on THAT CHANNEL's page.
     */
    CHANNEL_BLOCKED,
}

/**
 * The resolution, as a pure function of the two facts the platform will answer.
 *
 * IT IS PURE FOR [PermissionStateResolver]'S REASON, which is stated there and holds here: routing
 * the decision through NotificationManager would put it behind shadow behaviour and leave the
 * interesting rows untestable without a device. The Android half -- asking the platform -- is
 * `SettingsSurface.read`, which is where every other platform fact this screen renders is asked for.
 */
object NotificationDeliveryResolver {

    /**
     * @param notificationsEnabled `NotificationManagerCompat.areNotificationsEnabled()`.
     * @param channelImportance the wake channel's importance, or null where the channel does not
     *  exist. NULL IS NOT A BLOCK: `getNotificationChannel` answers null until something creates
     *  one, and reading that as "blocked" would put a permanent "your alerts are off" notice on a
     *  phone where nothing is wrong -- agents-tracker-0dij's defect, which resolved a first run to
     *  PERMANENTLY_DENIED, recurring one field over. `SwarmApplication.onCreate` now creates the
     *  channel at process start, so this row is reached only in the window before that runs.
     *
     * ONLY `IMPORTANCE_NONE` IS A BLOCK, and the distinction is the scope of this whole check.
     * `WakeNotifications.ensureChannel` argues IMPORTANCE_HIGH from android/fcm-priority.tsv and the
     * user can overrule it; what they cannot do quietly is make the wake vanish. At MIN, LOW or
     * DEFAULT the notification is still shown, just not as a heads-up. A screen that reported
     * "blocked" over a channel somebody deliberately made quiet would be arguing with a choice the
     * platform offers, on a screen whose whole subject is what is actually true.
     *
     * THE APP-LEVEL ANSWER COMES FIRST because the channel's importance is not the question once
     * nothing may be shown at all -- and because the two have different remedies, so collapsing
     * them would send half the users to a page that cannot fix their problem.
     */
    fun resolve(notificationsEnabled: Boolean, channelImportance: Int?): NotificationDelivery = when {
        !notificationsEnabled -> NotificationDelivery.APP_BLOCKED
        channelImportance == NotificationManager.IMPORTANCE_NONE -> NotificationDelivery.CHANNEL_BLOCKED
        else -> NotificationDelivery.DELIVERABLE
    }
}
