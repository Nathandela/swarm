package dev.swarm.phone.push

import android.app.Notification
import android.app.NotificationManager
import android.content.Context
import androidx.test.core.app.ApplicationProvider
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner

/**
 * FAILING-FIRST (TDD RED, GG-5) for PB-PUSH-4: what the phone puts on the lock screen.
 *
 * "The app receives a push and renders a content-free notification unless the user has
 *  authenticated; it never decrypts session content with a locked device (PB-KEY-2).
 *  Lock-screen redaction and notification-channel privacy are set.
 *  Robolectric test: locked -> generic alert only; authenticated -> content rendered."
 *
 * WHAT ROBOLECTRIC CAN AND CANNOT SAY, before the assertions rather than after. It models
 * POLICY: which channel is created with which visibility, which text a builder produces for a
 * given input. It models NO handset. There is no real FCM delivery here, no Doze, no lock
 * screen, no biometric prompt and no real Keystore. PB-E2E-5 is the physical-handset gate, it
 * is DEFERRED under section 13, and nothing in this file may be read as covering any part of
 * it. A UI test that appeared to prove a real lock screen redacted something would be worse
 * than no test at all.
 *
 * WHAT IS DELIBERATELY NOT HERE. "It never decrypts session content with a locked device" is
 * NOT asserted in Kotlin, and must not be: a Kotlin test can only observe that this builder was
 * handed no content, which is true of an app that fetched content, was refused, and rendered
 * the generic string anyway. That property is measured at the CUSTODY SEAM, in
 * mobile/conformance/s17_pushwake_test.go, by counting content-tier KEK unwraps -- zero -- and
 * fenced at the source in android/gate/s17_pushclient_test.go, which requires that no content
 * verb is reachable from onMessageReceived at all.
 *
 * THE INPUT IS TWO PRIMITIVES, not the bound WakeAlert, on purpose. The service adapts the Go
 * type; this file tests the rendering policy. Binding the AAR type here would make a
 * notification-policy test fail whenever the AAR was stale, which teaches everyone to ignore it.
 */
@RunWith(RobolectricTestRunner::class)
class WakeNotificationTest {

    private val context: Context get() = ApplicationProvider.getApplicationContext()

    /**
     * The channel must exist before anything is posted. On API 26+ a notification posted to a
     * channel that was never created is DROPPED by the framework -- so this is not a policy
     * nicety, it is the difference between a notification and silence.
     */
    @Test
    fun `the wake channel is created`() {
        WakeNotifications.ensureChannel(context)

        val manager = context.getSystemService(NotificationManager::class.java)
        val channel = manager.getNotificationChannel(WakeNotifications.CHANNEL_ID)
        assertNotNull(
            "PB-PUSH-4: no notification channel with id ${WakeNotifications.CHANNEL_ID}. " +
                "Every wake posted to a channel that does not exist is dropped by the framework",
            channel,
        )
    }

    /**
     * "Notification-channel privacy is set" -- the requirement's own wording.
     *
     * SECRET, not PRIVATE. VISIBILITY_PRIVATE shows a redacted line on the lock screen, which is
     * still a line saying this app wants the owner's attention, on a device that may be in
     * someone else's hands. VISIBILITY_SECRET shows nothing until the device is unlocked.
     */
    @Test
    fun `the wake channel hides its notifications from the lock screen`() {
        WakeNotifications.ensureChannel(context)

        val manager = context.getSystemService(NotificationManager::class.java)
        val channel = manager.getNotificationChannel(WakeNotifications.CHANNEL_ID)
        assertEquals(
            "PB-PUSH-4: the wake channel's lockscreenVisibility is not VISIBILITY_SECRET",
            Notification.VISIBILITY_SECRET,
            channel.lockscreenVisibility,
        )
    }

    /**
     * The channel importance must be HIGH, and this is the one place the two halves of the
     * design meet: android/fcm-priority.tsv resolves the wake message class to HIGH priority
     * because ADR-007 B16 makes push the sole background wake path. A high-priority FCM message
     * delivered into a low-importance channel is a wake that arrives and is not shown.
     */
    @Test
    fun `the wake channel is high importance to match the FCM priority decision`() {
        WakeNotifications.ensureChannel(context)

        val manager = context.getSystemService(NotificationManager::class.java)
        val channel = manager.getNotificationChannel(WakeNotifications.CHANNEL_ID)
        assertEquals(
            "PB-PUSH-4/PB-RUN-4: the wake channel importance contradicts android/fcm-priority.tsv, " +
                "which resolves the wake class to HIGH because push is the only path to a " +
                "backgrounded phone",
            NotificationManager.IMPORTANCE_HIGH,
            channel.importance,
        )
    }

    /**
     * The notification itself carries VISIBILITY_SECRET too. The channel value is the default
     * and the user can change it; the per-notification value is what this app asserts. Both are
     * named in the requirement ("lock-screen redaction AND notification-channel privacy").
     */
    @Test
    fun `the notification is itself lock-screen secret`() {
        val notification = WakeNotifications.build(context, text = GENERIC, contentReady = false)

        assertEquals(
            "PB-PUSH-4: the wake notification does not set VISIBILITY_SECRET",
            Notification.VISIBILITY_SECRET,
            notification.visibility,
        )
    }

    /**
     * Locked: the constant, and nothing else. The text is supplied by the Go core
     * (swarmmobile.WakeNotificationText) so there is one copy of it and no Kotlin string to
     * drift towards saying more.
     */
    @Test
    fun `a locked device renders the constant it was given`() {
        val notification = WakeNotifications.build(context, text = GENERIC, contentReady = false)

        val rendered = notification.extras.getString(Notification.EXTRA_TEXT).orEmpty() + " " +
            notification.extras.getString(Notification.EXTRA_TITLE).orEmpty()
        assertTrue(
            "PB-PUSH-4: the rendered notification does not contain the constant it was handed",
            rendered.contains(GENERIC),
        )
        for (leak in listOf(LOUD_SESSION, "refactor-the-auth-middleware", "build-box-17")) {
            assertFalse(
                "PB-PUSH-4: the locked notification contains $leak",
                rendered.contains(leak),
            )
        }
    }

    /**
     * Locked and UNAUTHENTICATED means no action that would need content, either. A content
     * intent that opens a session screen is a tap that drives a decrypt, and the phone is locked
     * -- the user gets a refusal they did not ask for, and the app has expressed an intention to
     * read content from a push.
     */
    @Test
    fun `a locked notification offers no action that needs content`() {
        val notification = WakeNotifications.build(context, text = GENERIC, contentReady = false)

        assertEquals(
            "PB-PUSH-4/PB-KEY-2: the locked notification carries ${notification.actions?.size} " +
                "action(s). Every action on this notification would run against a locked content " +
                "tier",
            0,
            notification.actions?.size ?: 0,
        )
    }

    /**
     * The second half of the requirement, and the non-vacuity control for all of the above: an
     * implementation that rendered the generic notification unconditionally would pass every
     * test before this one and would never show the user anything else.
     *
     * What "content rendered" means here is deliberately narrow -- the notification is
     * DISTINGUISHABLE from the locked one -- because what content to show is the session
     * screen's business and PB-APP-9's taxonomy already owns how it is worded.
     */
    @Test
    fun `an authenticated device renders something other than the generic alert`() {
        val locked = WakeNotifications.build(context, text = GENERIC, contentReady = false)
        val unlocked = WakeNotifications.build(context, text = GENERIC, contentReady = true)

        val lockedText = locked.extras.getString(Notification.EXTRA_TEXT).orEmpty()
        val unlockedText = unlocked.extras.getString(Notification.EXTRA_TEXT).orEmpty()
        assertTrue(
            "PB-PUSH-4: an authenticated device is shown exactly what a locked one is shown. " +
                "'unless the user has authenticated' has to have an authenticated side, or the " +
                "requirement is met by rendering the generic alert forever",
            lockedText != unlockedText || locked.actions?.size != unlocked.actions?.size,
        )
    }

    private companion object {
        /** The constant the Go core supplies; see swarmmobile.WakeNotificationText. */
        const val GENERIC = "Swarm has an update for you."

        /** A session name with something recognisable to leak, as S12's payload tests use. */
        const val LOUD_SESSION = "build-box-17.local/refactor-the-auth-middleware"
    }
}
