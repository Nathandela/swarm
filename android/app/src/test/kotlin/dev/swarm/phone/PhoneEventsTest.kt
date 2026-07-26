package dev.swarm.phone

import android.os.Looper
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.Shadows.shadowOf
import java.util.concurrent.atomic.AtomicInteger

/**
 * The listener seam PB-APP-3/4/5 had no instance of.
 *
 * `App.SetEventListener` appeared ZERO times in all Kotlin -- main, test and androidTest alike
 * -- so no listener was ever installed and no journal event could reach the app (residuals
 * §2.9). This is the object that gets installed, and the two facts about it that a unit test
 * on this machine can actually establish.
 *
 * WHY IT IS A SINGLETON AND NOT A LAMBDA HANDED TO THE FACADE. `PhoneRuntime` caches the built
 * `App` across Activity instances, and `SetEventListener` has no un-set that does not involve
 * crossing a null through JNI. A listener holding the Activity would therefore outlive it: the
 * screen goes away, the phone core keeps the reference, and every rotation adds another. So the
 * listener is a process-lived object holding a REPLACEABLE sink, and pausing clears the sink
 * rather than the listener.
 *
 * WHAT IT DELIBERATELY DOES NOT ASSERT. Not that any event ever arrives -- that needs a relay,
 * a machine and a paired handset (PB-E2E-2's smoke, and PB-E2E-5 for a real device). The
 * `swarmmobile.Event` argument is never touched here either: its accessors are native, so
 * reading one on a JVM with no libgojni is an UnsatisfiedLinkError rather than a test. Null is
 * passed for exactly that reason, and the listener never dereferences it.
 */
@RunWith(RobolectricTestRunner::class)
class PhoneEventsTest {

    private val redraws = AtomicInteger()

    @After
    fun detach() = PhoneEvents.stopObserving()

    /**
     * The Go core delivers on its OWN goroutine, which on Android is not a Looper thread at
     * all. A sink invoked there would touch Views off the main thread -- which Android
     * sometimes tolerates and sometimes crashes on, so it is the class of bug that survives
     * every emulator run and appears on a user's handset.
     */
    @Test
    fun an_event_from_a_go_goroutine_reaches_the_sink_on_the_main_thread() {
        var sawMainLooper = false
        PhoneEvents.observe {
            sawMainLooper = Looper.myLooper() == Looper.getMainLooper()
            redraws.incrementAndGet()
        }

        val goroutine = Thread { PhoneEvents.onEvent(null) }
        goroutine.start()
        goroutine.join()

        assertEquals(
            "the sink ran on the delivering thread; nothing marshalled it onto the looper",
            0,
            redraws.get(),
        )
        shadowOf(Looper.getMainLooper()).idle()
        assertEquals("the event never reached the sink at all", 1, redraws.get())
        assertEquals("the sink ran off the main looper", true, sawMainLooper)
    }

    /**
     * A paused screen must stop being redrawn. The surface tears its observation down in
     * `release`, and the listener the facade holds stays installed -- so this is the only thing
     * standing between a backgrounded Activity and a redraw against views it no longer owns.
     */
    @Test
    fun an_event_after_the_screen_paused_reaches_nothing() {
        PhoneEvents.observe { redraws.incrementAndGet() }
        PhoneEvents.stopObserving()

        PhoneEvents.onEvent(null)
        shadowOf(Looper.getMainLooper()).idle()

        assertEquals("a detached sink was still invoked", 0, redraws.get())
    }

    /**
     * An event before any screen installed a sink is normal, not exceptional: the facade holds
     * the listener for the life of the process and the relay drain does not stop when the app
     * backgrounds. A throw here crosses back into Go, where `deliver` recovers it -- so the
     * cost of getting this wrong is silence, which is the failure mode hardest to notice.
     */
    @Test
    fun an_event_with_no_sink_installed_is_dropped_quietly() {
        PhoneEvents.onEvent(null)
        shadowOf(Looper.getMainLooper()).idle()

        assertEquals(0, redraws.get())
    }

    /** The second screen replaces the first; two sinks would redraw a screen that is gone. */
    @Test
    fun installing_a_sink_replaces_the_previous_one() {
        PhoneEvents.observe { redraws.incrementAndGet() }
        PhoneEvents.observe { redraws.addAndGet(TENS) }

        PhoneEvents.onEvent(null)
        shadowOf(Looper.getMainLooper()).idle()

        assertEquals("the replaced sink still ran", TENS, redraws.get())
    }

    private companion object {
        const val TENS = 10
    }
}
