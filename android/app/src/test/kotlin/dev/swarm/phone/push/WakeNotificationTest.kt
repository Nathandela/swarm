package dev.swarm.phone.push

import android.app.Notification
import android.app.NotificationManager
import android.content.Context
import androidx.test.core.app.ApplicationProvider
import dev.swarm.phone.R
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
 * "The app receives a push and renders a content-free notification ~~unless the user has
 *  authenticated~~; it never decrypts session content with a locked device (PB-KEY-2).
 *  Lock-screen redaction and notification-channel privacy are set.
 *  Robolectric test: ~~locked~~ -> generic alert only; ~~authenticated~~ -> content rendered."
 *
 * PB-PUSH-4 NARROWS, AND THE PART THAT SURVIVES IS THE PART THAT MATTERS MOST (ADR-007 B133).
 * The struck clauses lost their producer: nothing on this handset is gated on a user
 * authentication, so there is no authenticated side to be "unless" of. CONTENT-FREE RENDERING
 * ITSELF IS KEPT WHOLE -- FCM reads push payloads, Google is on the declared wire, and the lock
 * screen still exists on every handset this app runs on. VISIBILITY_SECRET, the no-leak sweep
 * and the no-interpolation fence are unchanged below.
 *
 * `contentReady` IS RE-PREMISED, NOT DELETED, and it is not a fixture's invention: it comes off
 * the Go core (`alert.getContentReady()` in `SwarmMessagingService`) and it now tracks KEY
 * AVAILABILITY rather than authentication. It is false on a phone awaiting its first epoch grant
 * and on one whose keys a revoke purged -- two states the product reaches for reasons that have
 * nothing to do with who is holding the phone. So the two-sided tests keep both sides.
 *
 * WHAT ROBOLECTRIC CAN AND CANNOT SAY, before the assertions rather than after. It models
 * POLICY: which channel is created with which visibility, which text a builder produces for a
 * given input. It models NO handset. There is no real FCM delivery here, no Doze, no lock
 * screen and no real Keystore. PB-E2E-5 is the physical-handset gate, it is DEFERRED under
 * section 13, and nothing in this file may be read as covering any part of it. A UI test that
 * appeared to prove a real lock screen redacted something would be worse than no test at all.
 *
 * WHAT IS DELIBERATELY NOT HERE. "It never decrypts session content" is NOT asserted in Kotlin,
 * and must not be: a Kotlin test can only observe that this builder was handed no content, which
 * is true of an app that fetched content, was refused, and rendered the generic string anyway.
 * That property is measured at the CUSTODY SEAM, in mobile/conformance/s17_pushwake_test.go, by
 * counting content-tier KEK unwraps -- zero -- and fenced at the source in
 * android/gate/s17_pushclient_test.go, which requires that no content verb is reachable from
 * onMessageReceived at all.
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
     * No content key: the constant, and nothing else. The text is supplied by the Go core
     * (swarmmobile.WakeNotificationText) so there is one copy of it and no Kotlin string to
     * drift towards saying more.
     *
     * The leak sweep below is the one assertion in this file that would matter even if
     * `contentReady` disappeared entirely: FCM reads the payload and the notification lands on a
     * lock screen, so a session name reaching either is the exposure PB-PUSH-4 is about.
     */
    @Test
    fun `a phone holding no content key renders the constant it was given`() {
        val notification = WakeNotifications.build(context, text = GENERIC, contentReady = false)

        val rendered = notification.extras.getString(Notification.EXTRA_TEXT).orEmpty() + " " +
            notification.extras.getString(Notification.EXTRA_TITLE).orEmpty()
        assertTrue(
            "PB-PUSH-4: the rendered notification does not contain the constant it was handed",
            rendered.contains(GENERIC),
        )
        for (leak in listOf(LOUD_SESSION, "refactor-the-auth-middleware", "build-box-17")) {
            assertFalse(
                "PB-PUSH-4: the content-free notification contains $leak",
                rendered.contains(leak),
            )
        }
    }

    /**
     * Holding no content key means no action that would need content, either. A content intent
     * that opens a session screen is a tap that drives a decrypt, and there is no key to do it
     * with -- the user gets a refusal they did not ask for, and the app has expressed an
     * intention to read content from a push.
     */
    @Test
    fun `a notification with no content key offers no action that needs content`() {
        val notification = WakeNotifications.build(context, text = GENERIC, contentReady = false)

        assertEquals(
            "PB-PUSH-4/PB-KEY-2: the content-free notification carries ${notification.actions?.size} " +
                "action(s). Every action on this notification would run against a content " +
                "tier this phone holds no key for",
            0,
            notification.actions?.size ?: 0,
        )
    }

    /**
     * The second half of the requirement, and the non-vacuity control for all of the above: an
     * implementation that rendered the generic notification unconditionally would pass every
     * test before this one and would never show the user anything else.
     *
     * ITS PREMISE IS KEY AVAILABILITY (ADR-007 B133) and the control is worth MORE under it, not
     * less: with the authenticated side gone, hardwiring `contentReady` to true is the obvious
     * simplification, and this is what fails when somebody makes it.
     *
     * What "content rendered" means here is deliberately narrow -- the notification is
     * DISTINGUISHABLE from the other one -- because what content to show is the session screen's
     * business and PB-APP-9's taxonomy already owns how it is worded.
     */
    @Test
    fun `a phone holding the content key renders something other than the generic alert`() {
        val withoutKey = WakeNotifications.build(context, text = GENERIC, contentReady = false)
        val withKey = WakeNotifications.build(context, text = GENERIC, contentReady = true)

        val withoutKeyText = withoutKey.extras.getString(Notification.EXTRA_TEXT).orEmpty()
        val withKeyText = withKey.extras.getString(Notification.EXTRA_TEXT).orEmpty()
        assertTrue(
            "PB-PUSH-4: a phone holding the content key is shown exactly what a phone without " +
                "one is shown, so `contentReady` decides nothing. That is what hardwiring it " +
                "to a constant looks like, and it is the likeliest edit here now that the " +
                "authenticated side of the requirement is gone",
            withoutKeyText != withKeyText || withoutKey.actions?.size != withKey.actions?.size,
        )
    }

    /**
     * The clause PB-PUSH-4's 2026-07-26 amendment ADDED, and which nothing asserted.
     *
     * The amendment weakened "content rendered" to "a *distinguishable* notification that still
     * reads NO session content", explicitly because distinguishability alone is satisfiable by a
     * defect. Only the first half had a test: the content-leak loop above runs against
     * `contentReady = false` only, so an implementation that appended a session name on the
     * ready path was distinguishable, leaked, and green. The behaviour was right by
     * construction, so this closes a missing CHECK rather than a live defect.
     *
     * HOW IT IS ASSERTED, because a leak-marker list would not do it. The test supplies no
     * session content, so "the rendered text does not contain build-box-17" is satisfied by any
     * implementation that leaked something ELSE. What can be asserted exactly is that the
     * ready notification is the content-free one PLUS A KNOWN CONSTANT: the difference is
     * compared against the string resource itself, so ANY added text that is not that resource
     * fails, whatever it is and wherever it came from.
     *
     * It doubles as the non-vacuity control for the distinguishability test above: an
     * implementation that ignored `contentReady` produces an empty difference, which is not the
     * resource either.
     */
    @Test
    fun `a phone holding the content key is told more without being told about a session`() {
        val withoutKey = WakeNotifications.build(context, text = GENERIC, contentReady = false)
        val withKey = WakeNotifications.build(context, text = GENERIC, contentReady = true)

        val withoutKeyText = withoutKey.extras.getString(Notification.EXTRA_TEXT).orEmpty()
        val withKeyText = withKey.extras.getString(Notification.EXTRA_TEXT).orEmpty()

        assertTrue(
            "PB-PUSH-4: the ready notification is not an EXTENSION of the content-free one " +
                "(without=$withoutKeyText, with=$withKeyText). Holding the key may add a " +
                "constant invitation; it may not rewrite the line the Go core supplied",
            withKeyText.startsWith(withoutKeyText),
        )
        assertEquals(
            "PB-PUSH-4: holding the content key added text that is not the constant invitation. " +
                "The amendment permits a DISTINGUISHABLE notification that still reads no " +
                "session content, so the only thing this path may add is that resource",
            context.getString(R.string.wake_notification_open),
            withKeyText.removePrefix(withoutKeyText).trim(),
        )
        assertEquals(
            "PB-PUSH-4/PB-KEY-2: the ready notification carries an action. An action opens a " +
                "screen, which on this path is a tap that drives a content read",
            0,
            withKey.actions?.size ?: 0,
        )
    }

    /**
     * The claim WakeNotifications makes about itself in prose -- "both are string resources with
     * no arguments, so there is no interpolation site here for a session id to arrive at" -- as
     * an assertion.
     *
     * It guards a different edit from the test above: not a call site that appends content, but
     * a resource that GROWS a format specifier, after which a session id has somewhere to be
     * passed and the next reader sees a `getString(id, ...)` overload that looks intended.
     */
    @Test
    fun `the notification strings offer nowhere to interpolate a session`() {
        for (id in listOf(R.string.wake_notification_open, R.string.app_name, R.string.wake_channel_name)) {
            val value = context.getString(id)
            assertFalse(
                "PB-PUSH-4: the notification string \"$value\" contains a format specifier. " +
                    "Every string on the wake path is argument-less by design; one that takes an " +
                    "argument is an interpolation site for the session content this path must never read",
                value.contains('%'),
            )
        }
    }

    private companion object {
        /** The constant the Go core supplies; see swarmmobile.WakeNotificationText. */
        const val GENERIC = "Swarm has an update for you."

        /** A session name with something recognisable to leak, as S12's payload tests use. */
        const val LOUD_SESSION = "build-box-17.local/refactor-the-auth-middleware"
    }
}
