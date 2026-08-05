package dev.swarm.phone

import android.app.NotificationManager
import androidx.test.core.app.ApplicationProvider
import dev.swarm.phone.push.WakeNotifications
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotNull
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner

/**
 * FAILING-FIRST (TDD RED, GG-5) for agents-tracker-2yfn -- the half of the defect that is about
 * WHEN the channel exists.
 *
 * THE CHANNEL WAS CREATED ONLY AT THE FIRST WAKE. `WakeNotifications.ensureChannel`'s sole caller
 * was `SwarmMessagingService`, which runs when a push arrives, so before the first one there was no
 * channel at all: `getNotificationChannel` answered null, and anything that wanted to ask whether
 * delivery was blocked had nothing to ask about. The settings screen is precisely a place a user
 * opens BEFORE any wake has arrived -- it is where they turn the two switches on -- so the one
 * screen that reports on notifications could never see the object it reports on.
 *
 * THE PER-WAKE CALL IS KEPT AND THIS IS IN ADDITION TO IT. `WakeNotifications`'s own KDoc gives the
 * reason and it still holds: the process handling a wake is routinely a fresh one Android built for
 * that message alone, and a notification posted to a channel that was never created is dropped by
 * the framework. Two calls to an idempotent creator is not duplication -- it is the same guarantee
 * made at both moments it has to be true.
 *
 * WHY THIS IS THE APPLICATION AND NOT THE SETTINGS SURFACE. A settings screen that created the
 * channel would make the channel's existence a side effect of somebody opening a tab, and the
 * inspection would then be reporting on an object it had just built. `onCreate` runs once per
 * process, before any screen, which is what makes the channel a fact the screen can read rather
 * than one it manufactures.
 */
@RunWith(RobolectricTestRunner::class)
class SwarmApplicationTest {

    /**
     * The wake channel is there as soon as the process is, with the importance the wake needs.
     *
     * The importance is asserted here as well as in `WakeNotificationTest` because the two claims
     * are different: that suite says `ensureChannel` builds a HIGH channel, and this one says the
     * channel this process actually holds is that channel -- an `onCreate` that created some other
     * one, or that created it and then had it re-created differently, would pass the first and fail
     * this.
     */
    @Test
    fun `the wake channel exists before any wake has arrived`() {
        val app = ApplicationProvider.getApplicationContext<SwarmApplication>()

        val channel = app.getSystemService(NotificationManager::class.java)
            .getNotificationChannel(WakeNotifications.CHANNEL_ID)

        assertNotNull(
            "agents-tracker-2yfn: no `${WakeNotifications.CHANNEL_ID}` channel exists after " +
                "Application.onCreate. Until the first wake arrives there is then nothing for the " +
                "settings screen to inspect, so a user whose channel is blocked is shown two live " +
                "switches -- and on the very first wake the framework has a channel to drop the " +
                "notification into only because the service creates one on its way past",
            channel,
        )
        assertEquals(
            "the channel this process holds is not the HIGH one android/fcm-priority.tsv resolves " +
                "the wake class to. A high-priority FCM message delivered into a low-importance " +
                "channel is a wake that arrives and is not shown",
            NotificationManager.IMPORTANCE_HIGH,
            channel.importance,
        )
    }
}
