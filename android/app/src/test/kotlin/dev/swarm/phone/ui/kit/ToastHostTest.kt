package dev.swarm.phone.ui.kit

import android.content.Context
import android.os.Looper
import android.view.View
import android.widget.TextView
import androidx.test.core.app.ApplicationProvider
import dev.swarm.phone.theme.SwarmTheme
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertSame
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.Shadows.shadowOf
import org.robolectric.annotation.GraphicsMode
import java.time.Duration

/**
 * FAILING-FIRST (TDD RED, GG-5) for the BEHAVIOUR half of derivation row 1: "3200 ms then hidden,
 * no transition", and §8.7's announcement with a lifetime of its own.
 *
 * WHY THE HOST IS A COMPONENT AND NOT THREE LINES IN A SURFACE. A toast is the only thing in this
 * app that appears without anyone navigating to it, and the three facts that make it one -- where
 * it sits, how long it stays, and that it says so to a screen reader -- are all row 1's. A surface
 * that owned them would own a timer, a placement and an accessibility decision each, in as many
 * copies as there are surfaces, which is exactly what `PhoneSurface` and `SettingsSurface` already
 * do with their two unstyled outcome lines.
 *
 * **THERE IS NO ENTRANCE ANIMATION AND THAT IS A DECISION, NOT AN OMISSION.** ADR-007 B134 keeps
 * three motions -- the sheet, the banner and the streaming caret -- and row 1 says "3200 ms then
 * hidden, NO TRANSITION" in as many words. The timer is a `Handler.postDelayed`, which
 * `android/gate/s23_motion_test.go` records as the one frame-driving call it deliberately permits
 * (the pairing screen's state poll is the same program), and it is CANCELLED on every show so two
 * toasts in a row do not leave the first one's expiry to hide the second.
 */
@RunWith(RobolectricTestRunner::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
class ToastHostTest {

    private val context: Context
        get() = SwarmTheme.applyTo(ApplicationProvider.getApplicationContext())

    private val message = "Interrupt sent"

    private val second = "Controller lease taken"

    private fun host() = ToastHost(context)

    private fun toastIn(host: ToastHost): TextView = host.kitRequire(KitTag.TOAST) as TextView

    /** Let [millis] of the main looper's clock pass, running whatever it owes. */
    private fun elapse(millis: Long) =
        shadowOf(Looper.getMainLooper()).idleFor(Duration.ofMillis(millis))

    // ---- showing --------------------------------------------------------------

    @Test
    fun `the host holds one hidden toast until something is said`() {
        val subject = host()
        assertEquals(
            "the host builds its toast on demand. It is built ONCE, so the view a screen reader " +
                "is announced from is the same view every time -- §8.7's live region cannot have " +
                "a lifetime of its own if the view carrying it is replaced on every message",
            1,
            subject.childCount,
        )
        assertEquals(View.GONE, toastIn(subject).visibility)
    }

    @Test
    fun `showing a message puts it on screen`() {
        val subject = host()
        subject.show(message)

        val toast = toastIn(subject)
        assertEquals(View.VISIBLE, toast.visibility)
        assertEquals(message, toast.text.toString())
    }

    @Test
    fun `a second message replaces the first in the same view`() {
        val subject = host()
        subject.show(message)
        val first = toastIn(subject)
        subject.show(second)

        assertSame(
            "the host built a second toast view. §8.7 wants the announcement's lifetime " +
                "independent of the visual one, which starts with the view outliving the message",
            first,
            toastIn(subject),
        )
        assertEquals(second, first.text.toString())
    }

    // ---- the 3200 ms lifetime -------------------------------------------------

    /**
     * The boundary rather than the number: the value itself is joined to row 1 by
     * `android/gate/s23_kit_test.go`, which reads the table. What is asserted here is that a timer
     * keyed on it exists at all -- a toast with no expiry is a modal nobody can dismiss.
     */
    @Test
    fun `the toast is still up one millisecond before its lifetime elapses`() {
        val subject = host()
        subject.show(message)
        elapse(KitMetrics.TOAST_LIFETIME_MS - 1)

        assertEquals(View.VISIBLE, toastIn(subject).visibility)
    }

    @Test
    fun `the toast is hidden once its lifetime elapses`() {
        val subject = host()
        subject.show(message)
        elapse(KitMetrics.TOAST_LIFETIME_MS)

        assertEquals(
            "the toast is still on screen after row 1's lifetime. Nothing dismisses it, so it " +
                "sits over the tab bar until the next redraw",
            View.GONE,
            toastIn(subject).visibility,
        )
    }

    /**
     * THE TIMER IS CANCELABLE, and this is the defect that shape has when it is not: a second
     * toast shown 3.0 s into the first one's life inherits 0.2 s of screen time from a timer
     * nobody cancelled.
     */
    @Test
    fun `a second message restarts the lifetime rather than inheriting it`() {
        val subject = host()
        subject.show(message)
        elapse(KitMetrics.TOAST_LIFETIME_MS - 200)
        subject.show(second)
        elapse(300)

        assertEquals(
            "the second toast was hidden by the FIRST one's expiry, so a message shown late in " +
                "another's life is on screen for whatever is left of it",
            View.VISIBLE,
            toastIn(subject).visibility,
        )
    }

    @Test
    fun `dismissing hides the toast and cancels the pending expiry`() {
        val subject = host()
        subject.show(message)
        subject.dismiss()
        assertEquals(View.GONE, toastIn(subject).visibility)

        subject.show(second)
        elapse(KitMetrics.TOAST_LIFETIME_MS - 1)
        assertEquals(
            "a dismissed toast left its expiry queued, and it landed on the next message",
            View.VISIBLE,
            toastIn(subject).visibility,
        )
    }

    // ---- §8.7 -----------------------------------------------------------------

    /**
     * §8.7's actual sentence: "the announcement must be a live region with a lifetime of its own,
     * not a side effect of the view being visible".
     *
     * WHAT THAT MEANS MECHANICALLY, since the utterance itself is TalkBack's and cannot be
     * observed here: expiry HIDES the view and destroys nothing the announcement came from. The
     * implementation this rules out is the obvious one -- remove the view, or clear its text, when
     * the timer fires -- which cancels an in-flight reading of copy longer than 3200 ms is worth.
     */
    @Test
    fun `expiry hides the toast without unmaking what was announced`() {
        val subject = host()
        subject.show(message)
        elapse(KitMetrics.TOAST_LIFETIME_MS)

        val toast = toastIn(subject)
        assertEquals(
            "expiry cleared the toast's text, which cancels a reading still in progress -- §8.7 " +
                "measures the longest copy in this app as taking longer than 3200 ms to speak",
            message,
            toast.text.toString(),
        )
        assertEquals(
            "expiry took the live region off the toast",
            View.ACCESSIBILITY_LIVE_REGION_POLITE,
            toast.accessibilityLiveRegion,
        )
        assertSame(
            "expiry detached the toast from its host, so the next message is announced from a " +
                "view a screen reader has never seen",
            subject,
            toast.parent,
        )
    }

    // ---- what an overlay must not do ------------------------------------------

    /**
     * PB-SEC-12 clause 1's subject, from the other side: a view laid over content the user is
     * reading is the tapjacking surface, and this one covers the bottom of every screen in the app
     * for 3.2 s at a time. It takes no taps, so there is nothing to steal.
     */
    @Test
    fun `the host takes no touches`() {
        val subject = host()
        subject.show(message)

        assertFalse("the toast host is clickable", subject.isClickable)
        assertFalse("the toast host is focusable", subject.isFocusable)
        assertFalse("the toast itself is clickable", toastIn(subject).isClickable)
        assertTrue(
            "the toast host is not shown, so nothing it says can be read",
            toastIn(subject).visibility == View.VISIBLE,
        )
    }
}
