package dev.swarm.phone

import android.content.Context
import android.view.View
import androidx.test.core.app.ApplicationProvider
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertSame
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import java.util.concurrent.CountDownLatch
import java.util.concurrent.Executor
import java.util.concurrent.TimeUnit
import java.util.concurrent.atomic.AtomicInteger
import java.util.concurrent.atomic.AtomicReference

/**
 * The seam that keeps a facade verb off the Android main thread.
 *
 * WHAT WAS WRONG. `PhoneSurface.invoke` ran the verb inside the click listener. A command verb
 * resolves its destination through `sendContext` -> `awaitConn`, which by its own doc "polls for
 * up to five seconds" (mobile/commands.go:513-524, mobile/relay.go:149), and then appends to the
 * relay -- so every command tap did synchronous network I/O across JNI on the thread that draws
 * the screen, and a tap issued while the link was reconnecting froze the app for about five
 * seconds. That is an ANR dialog on the first Launch tap of the first hardware run.
 *
 * WHY NOTHING SAW IT, and why this file tests the DISPATCHER and not the button.
 * `NetworkOnMainThreadException` never fires for a socket opened by Go, so the platform's own
 * detector is blind to this entire facade. Robolectric is single-threaded. And
 * `PhoneRuntime.phone()` answers `Unavailable` on every JVM run, so `invoke`'s `Ready` branch --
 * the only branch on which a verb runs at all -- is structurally unreachable in this suite. No
 * test in this repository can press one of these controls and have a verb execute. So what is
 * asserted here is this object's behaviour, driven directly; that `PhoneSurface` hands its verbs
 * to it is asserted as source text by android/gate/s25_mainthread_test.go; and the join between
 * the two is owed to the hardware run.
 *
 * THE TWO KINDS OF TEST BELOW ARE DELIBERATELY DIFFERENT. Facts about THREADS are asserted
 * against the real production wiring ([VerbDispatch.background]) with latches, because a fact
 * about which thread ran something cannot be established by a fake that never leaves this one.
 * Facts about ORDER and LIFECYCLE are asserted against hand-driven executors, because a test
 * that sleeps to observe an absence is a flake waiting for a loaded machine.
 */
@RunWith(RobolectricTestRunner::class)
class VerbDispatchTest {

    private val context = ApplicationProvider.getApplicationContext<Context>()

    private fun control() = View(context)

    // ---------------------------------------------------------------------
    // Threads: the real lanes, because this is what the defect is about.
    // ---------------------------------------------------------------------

    /**
     * The whole point. A command verb reaches `awaitConn`, so the thread it runs on is the one
     * that can be parked for five seconds -- and it must not be the one drawing the screen.
     */
    @Test
    fun a_command_verb_does_not_run_on_the_thread_that_pressed_the_control() {
        val dispatch = VerbDispatch.background()
        val ran = CountDownLatch(1)
        val where = AtomicReference<Thread>()
        val pressedOn = Thread.currentThread()

        dispatch.press(
            control(),
            SendPlane.COMMAND,
            work = { where.set(Thread.currentThread()); ran.countDown() },
            settle = {},
        )

        assertTrue("the command verb never ran at all", ran.await(TIMEOUT_S, TimeUnit.SECONDS))
        assertFalse(
            "the command verb ran on the thread that pressed the control, which on a handset is " +
                "the main thread. awaitConn polls for up to five seconds and then appends to the " +
                "relay, so that thread is the ANR",
            where.get() === pressedOn,
        )
    }

    /**
     * Input is not exempt from the thread rule, only from the WAITING rule. `liveSendContext`
     * never waits for a connection, which is ADR-007 D7's requirement and is why typing degrades
     * gracefully -- but the append it then performs is still a relay round trip, and a round trip
     * on the drawing thread is still a freeze.
     */
    @Test
    fun an_input_verb_does_not_run_on_the_thread_that_pressed_the_control() {
        val dispatch = VerbDispatch.background()
        val ran = CountDownLatch(1)
        val where = AtomicReference<Thread>()
        val pressedOn = Thread.currentThread()

        dispatch.press(
            control(),
            SendPlane.LIVE,
            work = { where.set(Thread.currentThread()); ran.countDown() },
            settle = {},
        )

        assertTrue("the input verb never ran at all", ran.await(TIMEOUT_S, TimeUnit.SECONDS))
        assertFalse(
            "the input verb ran on the thread that pressed the control",
            where.get() === pressedOn,
        )
    }

    /**
     * ADR-007 D7, defended at the layer that could undo it from above.
     *
     * The two planes take SEPARATE lanes precisely so a keystroke can never wait behind a
     * command. This holds the command lane the way a real awaitConn holds it -- occupied, going
     * nowhere -- and requires a keystroke pressed during that hold to reach the wire anyway.
     *
     * A one-executor implementation passes every other test in this file and fails this one.
     */
    @Test
    fun a_command_inside_awaitconns_poll_does_not_delay_a_keystroke() {
        val dispatch = VerbDispatch.background()
        val holdTheCommandLane = CountDownLatch(1)
        val commandStarted = CountDownLatch(1)
        val keystrokeSent = CountDownLatch(1)

        dispatch.press(
            control(),
            SendPlane.COMMAND,
            // BOUNDED, and it has to be. An implementation that runs the verb on the CALLING
            // thread -- which is the defect this whole file is about -- would block the test
            // thread here forever on an unbounded await, and a suite that hangs reports nothing
            // at all. Bounded, such an implementation fails the assertions below instead.
            work = { commandStarted.countDown(); holdTheCommandLane.await(TIMEOUT_S, TimeUnit.SECONDS) },
            settle = {},
        )
        assertTrue(
            "the command never reached a lane",
            commandStarted.await(TIMEOUT_S, TimeUnit.SECONDS),
        )

        dispatch.press(
            control(),
            SendPlane.LIVE,
            work = { keystrokeSent.countDown() },
            settle = {},
        )
        val sent = keystrokeSent.await(TIMEOUT_S, TimeUnit.SECONDS)
        holdTheCommandLane.countDown()

        assertTrue(
            "a keystroke pressed while a command sat in awaitConn's five-second poll waited for " +
                "that command to finish. Input and commands are sharing one lane, which is the " +
                "queue on live input ADR-007 D7 forbids",
            sent,
        )
    }

    // ---------------------------------------------------------------------
    // Order and lifecycle: hand-driven lanes, so nothing here can flake.
    // ---------------------------------------------------------------------

    /** PB-APP-9's outcome line is a View, so the answer has to come back to the looper. */
    @Test
    fun the_verbs_answer_settles_on_the_main_executor_and_not_on_the_lane() {
        val lane = Held()
        val main = Held()
        val dispatch = VerbDispatch(lane, lane, main)
        val answer = AtomicReference<String>()
        val settles = AtomicInteger()

        dispatch.press(
            control(),
            SendPlane.COMMAND,
            work = { "op-7" },
            settle = { result -> answer.set(result.getOrNull()); settles.incrementAndGet() },
        )
        assertEquals("the verb ran on the pressing thread", 0, settles.get())

        lane.runAll()
        assertEquals(
            "the answer settled on the lane. Every view the settle touches belongs to the looper",
            0,
            settles.get(),
        )

        main.runAll()
        assertEquals("the answer never settled at all", 1, settles.get())
        assertEquals("the verb's answer did not reach the settle", "op-7", answer.get())
    }

    /**
     * Everything the facade refuses arrives as an exception -- that is what
     * `FacadeBridge.routeFacadeError` reads. Thrown on a lane it would reach an uncaught handler
     * with the outcome line left saying nothing, which is the silence PB-APP-9 exists to prevent.
     */
    @Test
    fun a_verb_that_throws_settles_as_a_failure_rather_than_escaping_onto_the_lane() {
        val lane = Held()
        val main = Held()
        val dispatch = VerbDispatch(lane, lane, main)
        val refusal = IllegalStateException("swarmmobile: not reconciled")
        val carried = AtomicReference<Throwable>()

        dispatch.press<Unit>(
            control(),
            SendPlane.COMMAND,
            work = { throw refusal },
            settle = { result -> carried.set(result.exceptionOrNull()) },
        )
        lane.runAll()
        main.runAll()

        assertSame(
            "the refusal did not reach the settle, so nothing can route it to the outcome line",
            refusal,
            carried.get(),
        )
    }

    /**
     * A responsive button can be tapped twice, and two Launches is worse than a frozen UI: it
     * starts two sessions on the machine, both signed, both real.
     */
    @Test
    fun a_second_press_while_the_first_is_still_crossing_is_refused() {
        val lane = Held()
        val main = Held()
        val dispatch = VerbDispatch(lane, lane, main)
        val button = control()
        val verbs = AtomicInteger()

        dispatch.press(button, SendPlane.COMMAND, work = { verbs.incrementAndGet() }, settle = {})
        dispatch.press(button, SendPlane.COMMAND, work = { verbs.incrementAndGet() }, settle = {})
        lane.runAll()
        main.runAll()

        assertEquals(
            "the second tap issued a second verb. The control was responsive and looked " +
                "pressable, so the user pressed it again",
            1,
            verbs.get(),
        )
    }

    /** The refusal must not report a phantom answer either. */
    @Test
    fun a_refused_second_press_settles_nothing() {
        val lane = Held()
        val main = Held()
        val dispatch = VerbDispatch(lane, lane, main)
        val button = control()
        val settles = AtomicInteger()

        dispatch.press(button, SendPlane.COMMAND, work = {}, settle = { settles.incrementAndGet() })
        dispatch.press(button, SendPlane.COMMAND, work = {}, settle = { settles.incrementAndGet() })
        lane.runAll()
        main.runAll()

        assertEquals("the refused press produced an answer of its own", 1, settles.get())
    }

    /** A second control is a second press. Single-flight is per control, not per surface. */
    @Test
    fun a_press_on_one_control_does_not_refuse_a_press_on_another() {
        val lane = Held()
        val main = Held()
        val dispatch = VerbDispatch(lane, lane, main)
        val verbs = AtomicInteger()

        dispatch.press(control(), SendPlane.COMMAND, work = { verbs.incrementAndGet() }, settle = {})
        dispatch.press(control(), SendPlane.COMMAND, work = { verbs.incrementAndGet() }, settle = {})
        lane.runAll()
        main.runAll()

        assertEquals("a press on one control refused a press on a different one", 2, verbs.get())
    }

    /**
     * A control that has been tapped must not look untapped. There is no in-flight state anywhere
     * in docs/design/substrate-components.md, so the honest minimum is the disabled pair the
     * design already carries (derivation row 24: `--p-hair` fill, `--p-ink3` ink, no bloom, not
     * clickable, not focusable), which the kit already paints off the view's own drawable state.
     */
    @Test
    fun the_pressed_control_is_disabled_until_its_answer_lands() {
        val lane = Held()
        val main = Held()
        val dispatch = VerbDispatch(lane, lane, main)
        val button = control()

        dispatch.press(button, SendPlane.COMMAND, work = {}, settle = {})
        assertFalse("the control still looks pressable while its verb is in flight", button.isEnabled)
        assertTrue("the dispatch does not report the press in flight", dispatch.inFlight(button))

        lane.runAll()
        assertFalse(
            "the control was given back while its answer was still on its way to the looper",
            button.isEnabled,
        )

        main.runAll()
        assertTrue("the control was never given back after its answer landed", button.isEnabled)
        assertFalse("the press is still marked in flight after it settled", dispatch.inFlight(button))
    }

    /** Nothing is in flight before anything was pressed. */
    @Test
    fun an_untouched_control_is_not_in_flight() {
        val lane = Held()
        assertFalse(VerbDispatch(lane, lane, lane).inFlight(control()))
    }

    /**
     * Process death and rebuild are constant on Android, and a command takes a relay round trip
     * -- so the screen an answer was meant for is routinely gone before it arrives. A settle into
     * a released surface sets text on views nobody holds and redraws a screen that is not on the
     * display, which is exactly what `PhoneEvents.stopObserving` stops for the event plane.
     */
    @Test
    fun an_answer_arriving_after_the_screen_released_settles_nothing() {
        val lane = Held()
        val main = Held()
        val dispatch = VerbDispatch(lane, lane, main)
        val settles = AtomicInteger()

        dispatch.press(control(), SendPlane.COMMAND, work = {}, settle = { settles.incrementAndGet() })
        lane.runAll()
        dispatch.detach()
        main.runAll()

        assertEquals(
            "the answer settled into a surface that had already released its views",
            0,
            settles.get(),
        )
    }

    /**
     * The other half, and it is not symmetry for its own sake: a dropped settle that left the
     * in-flight mark standing would leave that control refusing every tap for the life of the
     * surface, with nothing to distinguish it from a dead button.
     */
    @Test
    fun an_answer_dropped_by_release_still_frees_the_control() {
        val lane = Held()
        val main = Held()
        val dispatch = VerbDispatch(lane, lane, main)
        val button = control()

        dispatch.press(button, SendPlane.COMMAND, work = {}, settle = {})
        lane.runAll()
        dispatch.detach()
        main.runAll()

        assertFalse(
            "the control is still marked in flight, so it will refuse every tap from now on",
            dispatch.inFlight(button),
        )
        assertTrue("the control was never re-enabled, so it is a dead button", button.isEnabled)
    }

    /** A surface that resumed after a pause presses controls again. */
    @Test
    fun a_reattached_surface_settles_answers_again() {
        val lane = Held()
        val main = Held()
        val dispatch = VerbDispatch(lane, lane, main)
        val settles = AtomicInteger()

        dispatch.detach()
        dispatch.attach()
        dispatch.press(control(), SendPlane.COMMAND, work = {}, settle = { settles.incrementAndGet() })
        lane.runAll()
        main.runAll()

        assertEquals("a resumed surface stopped receiving answers", 1, settles.get())
    }

    /**
     * The two planes are two lanes, asserted here as a fact about the OBJECT rather than about
     * timing, so it holds even on a machine too loaded for
     * [a_command_inside_awaitconns_poll_does_not_delay_a_keystroke] to mean anything.
     */
    @Test
    fun the_command_plane_and_the_live_plane_are_not_the_same_lane() {
        val commandLane = Held()
        val liveLane = Held()
        val main = Held()
        val dispatch = VerbDispatch(commandLane, liveLane, main)

        dispatch.press(control(), SendPlane.COMMAND, work = {}, settle = {})
        dispatch.press(control(), SendPlane.LIVE, work = {}, settle = {})

        assertEquals("the command did not take the command lane", 1, commandLane.pending())
        assertEquals(
            "the keystroke was handed to the command lane, where a five-second awaitConn poll " +
                "can be sitting in front of it",
            1,
            liveLane.pending(),
        )
    }

    /**
     * An executor that runs nothing until it is told to. The lanes and the looper are both driven
     * by hand so every assertion above is about ordering rather than about how loaded the machine
     * running the suite happens to be.
     */
    private class Held : Executor {
        private val queued = ArrayDeque<Runnable>()

        override fun execute(command: Runnable) {
            queued.addLast(command)
        }

        fun pending() = queued.size

        fun runAll() {
            while (queued.isNotEmpty()) queued.removeFirst().run()
        }
    }

    private companion object {
        const val TIMEOUT_S = 5L
    }
}
