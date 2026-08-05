package dev.swarm.phone.runtime

import android.app.NotificationManager
import org.junit.Assert.assertEquals
import org.junit.Test

/**
 * FAILING-FIRST (TDD RED, GG-5) for agents-tracker-2yfn -- "a blocked notification channel is
 * invisible".
 *
 * THE HOLE THIS FILLS IS THE ONE THE PERMISSION CHECK CANNOT SEE. On API 33+ a GLOBAL disable
 * revokes POST_NOTIFICATIONS, so `checkSelfPermission` reports it and PB-RUN-2's resolver already
 * says so. A PER-CHANNEL block does not: the user long-presses a wake, blocks `Agent updates`, the
 * permission stays GRANTED, the settings screen shows two live switches and every subsequent wake
 * is dropped by the framework before this app is even started. Push is the sole path to a
 * backgrounded phone (ADR-007 B16), so the product simply stops working, silently -- PB-RUN-2's
 * named failure arriving by the one route the permission is blind to.
 *
 * IT IS A PURE FUNCTION FOR [PermissionStateResolver]'S OWN REASON, restated because it is the same
 * argument: routing the decision through NotificationManager would put the interesting logic behind
 * shadow behaviour and make the table below unreadable, and §10's tier order prefers a plain JVM
 * test to a Robolectric one wherever the Android runtime is not the subject. The Android-runtime
 * halves -- that the channel exists at process start, and that the redirect reaches the channel's
 * own screen -- are `SwarmApplicationTest` and `SettingsSurfaceNotificationsTest`.
 *
 * THE IMPORTANCE CONSTANTS ARE THE PLATFORM'S and are not re-spelled as integers here. They are
 * compile-time constants, so this table needs no device to read them, and a copy would be a second
 * opinion about a number Android owns.
 */
class NotificationDeliveryTest {

    /**
     * The defect itself: a channel the user set to `None`, on an app whose permission is intact.
     *
     * This is the state the whole issue is about. Nothing in the app looked at it, so the screen
     * reported two working switches over a phone that had not shown a notification in weeks.
     */
    @Test
    fun `a channel set to none is a block the permission check cannot see`() {
        assertEquals(
            "agents-tracker-2yfn: a channel at IMPORTANCE_NONE resolves as deliverable. The " +
                "permission is GRANTED in this state and always will be -- blocking a channel " +
                "does not revoke it -- so if this fact is not read here nothing in the app reads " +
                "it at all, and every wake is dropped with two live switches on screen",
            NotificationDelivery.CHANNEL_BLOCKED,
            NotificationDeliveryResolver.resolve(
                notificationsEnabled = true,
                channelImportance = NotificationManager.IMPORTANCE_NONE,
            ),
        )
    }

    /**
     * An app-level disable is answered before the channel is consulted, whatever the channel says.
     *
     * The channel's own importance is not the question once the app may show nothing: a channel
     * page reached in that state offers a switch that changes nothing, so the two are DIFFERENT
     * states with different remedies rather than degrees of one.
     */
    @Test
    fun `an app whose notifications are off is blocked before the channel is reached`() {
        for (importance in listOf(
            null,
            NotificationManager.IMPORTANCE_NONE,
            NotificationManager.IMPORTANCE_HIGH,
        )) {
            assertEquals(
                "agents-tracker-2yfn: areNotificationsEnabled() is false and the resolution " +
                    "consulted the channel anyway (importance $importance). Nothing this app " +
                    "posts is shown in that state, and the channel's own page cannot undo it",
                NotificationDelivery.APP_BLOCKED,
                NotificationDeliveryResolver.resolve(
                    notificationsEnabled = false,
                    channelImportance = importance,
                ),
            )
        }
    }

    /**
     * A CHANNEL THAT DOES NOT EXIST IS NOT A BLOCKED ONE, and this is the row that is easy to get
     * wrong in the direction that hurts.
     *
     * `getNotificationChannel` answers null until something creates it. Reading null as "blocked"
     * would put a permanent "Android has your alerts switched off" notice on a fresh install where
     * nothing is wrong, and send its owner to a system screen for a channel that has no page yet --
     * which is agents-tracker-0dij's defect (a hardcoded bit resolving a first run to
     * PERMANENTLY_DENIED) recurring one field over.
     */
    @Test
    fun `a channel that does not exist yet is not a blocked one`() {
        assertEquals(
            "agents-tracker-2yfn: a channel nothing has created yet reads as blocked, so a phone " +
                "with nothing wrong is told its notifications are off",
            NotificationDelivery.DELIVERABLE,
            NotificationDeliveryResolver.resolve(
                notificationsEnabled = true,
                channelImportance = null,
            ),
        )
    }

    /**
     * A LOWERED IMPORTANCE IS NOT A BLOCK EITHER, and the distinction is the whole scope of this
     * check.
     *
     * `WakeNotifications.ensureChannel` argues IMPORTANCE_HIGH from android/fcm-priority.tsv and
     * `createNotificationChannel` cannot re-raise a level the user lowered -- so a user CAN overrule
     * that decision. What they cannot do quietly is make the wake vanish: at MIN, LOW or DEFAULT the
     * notification is still shown, just not as a heads-up. Only NONE drops it. A screen that
     * reported "blocked" over a channel the user deliberately made quiet would be arguing with a
     * choice the platform offers, on a screen whose subject is what is actually true.
     */
    @Test
    fun `a channel the user merely quietened still delivers`() {
        for (importance in listOf(
            NotificationManager.IMPORTANCE_MIN,
            NotificationManager.IMPORTANCE_LOW,
            NotificationManager.IMPORTANCE_DEFAULT,
            NotificationManager.IMPORTANCE_HIGH,
        )) {
            assertEquals(
                "agents-tracker-2yfn: importance $importance reads as blocked. The wake is still " +
                    "shown at every level above NONE, so this notice would contradict the phone " +
                    "in the user's hand",
                NotificationDelivery.DELIVERABLE,
                NotificationDeliveryResolver.resolve(
                    notificationsEnabled = true,
                    channelImportance = importance,
                ),
            )
        }
    }
}
