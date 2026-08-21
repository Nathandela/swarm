package dev.swarm.phone

import java.util.concurrent.CountDownLatch
import java.util.concurrent.Executor
import java.util.concurrent.Executors
import java.util.concurrent.TimeUnit
import java.util.concurrent.atomic.AtomicReference
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertSame
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * FAILING-FIRST (TDD RED, GG-5) for agents-tracker-jx1x -- **the terminal watch verbs ran on
 * the Android main thread.**
 *
 * THE DEFECT. `PhoneSurface.reconcileTerminalWatch` called `TerminalFallbackBinding.watch()`,
 * `unwatch()` and `renew()` INLINE -- on every redraw of the fallback screen, and from a
 * 20-second main-looper tick. Each of those verbs crosses JNI into a Go relay append behind
 * `awaitConn`'s five-second poll (mobile/relay.go:178) plus a ten-second append bound: worst
 * case about fifteen seconds of main-thread blocking, and a SILENT ANR, because
 * `NetworkOnMainThreadException` never fires for a socket Go opened.
 *
 * WHAT THIS FILE TESTS, and what it cannot. [TerminalWatchLane] is the seam that puts those
 * verbs on [VerbDispatch]'s command lane; the production handle is `TerminalFallbackBinding`,
 * which wraps the bound facade and cannot be constructed on the unit-test JVM (libgojni), so
 * the verbs are driven through a fake [TerminalWatchHandle]. That `PhoneSurface` routes its
 * watch through this lane is asserted as source text by android/gate/s25_mainthread_test.go
 * (assertion 4, the wrapper derivation); the join is owed to the hardware run.
 *
 * THE TWO KINDS OF TEST BELOW ARE DELIBERATELY DIFFERENT, [VerbDispatchTest]'s own split:
 * facts about THREADS are asserted against a real executor with latches, facts about ORDER
 * and LIFECYCLE against hand-driven executors, because a test that sleeps to observe an
 * absence is a flake waiting for a loaded machine.
 */
class TerminalWatchLaneTest {

    /** A lane driven by hand, so order is observable and nothing races the assertions. */
    private class Held : Executor {
        private val queue = ArrayDeque<Runnable>()
        override fun execute(command: Runnable) {
            queue.addLast(command)
        }
        fun runAll() {
            while (queue.isNotEmpty()) queue.removeFirst().run()
        }
        fun isEmpty(): Boolean = queue.isEmpty()
    }

    private class FakeHandle(
        private val log: MutableList<String>,
        private val name: String,
        private val watchRefused: Boolean = false,
    ) : TerminalWatchHandle {
        override fun watch() {
            log.add("watch $name")
            if (watchRefused) throw IllegalStateException("capability_refused")
        }
        override fun unwatch() {
            log.add("unwatch $name")
        }
    }

    private fun laneOn(command: Executor, main: Executor): Pair<VerbDispatch, TerminalWatchLane<FakeHandle>> {
        val dispatch = VerbDispatch(command, Held(), main)
        return dispatch to TerminalWatchLane(dispatch)
    }

    // ---------------------------------------------------------------------
    // Threads: a real executor, because this is what the defect is about.
    // ---------------------------------------------------------------------

    /**
     * The whole point. The watch verb reaches awaitConn, so the thread it runs on is the one
     * that can be parked for five seconds -- and it must not be the one that reconciled the
     * screen, which on a handset is the main thread.
     */
    @Test
    fun the_watch_verb_does_not_run_on_the_thread_that_reconciled_the_screen() {
        val command = Executors.newSingleThreadExecutor()
        try {
            val ran = CountDownLatch(1)
            val where = AtomicReference<Thread>()
            val reconciledOn = Thread.currentThread()
            val dispatch = VerbDispatch(command, Held(), Held())
            val lane = TerminalWatchLane<TerminalWatchHandle>(dispatch)

            lane.hold(
                "s-1",
                object : TerminalWatchHandle {
                    override fun watch() {
                        where.set(Thread.currentThread())
                        ran.countDown()
                    }
                    override fun unwatch() {}
                },
            )

            assertTrue("the watch verb never ran at all", ran.await(10, TimeUnit.SECONDS))
            assertFalse(
                "the watch verb ran on the thread that reconciled the screen, which on a " +
                    "handset is the main thread. awaitConn polls for up to five seconds and " +
                    "then appends to the relay, so that thread is the ANR",
                where.get() === reconciledOn,
            )
        } finally {
            command.shutdownNow()
        }
    }

    // ---------------------------------------------------------------------
    // Order and lifecycle: hand-driven executors.
    // ---------------------------------------------------------------------

    /** Nothing runs where the caller stands: the verb is queued, not executed. */
    @Test
    fun holding_a_watch_runs_no_verb_on_the_calling_thread() {
        val log = mutableListOf<String>()
        val command = Held()
        val (_, lane) = laneOn(command, Held())

        lane.hold("s-1", FakeHandle(log, "a"))

        assertEquals(
            "the verb ran before the command lane was driven, so it ran on the calling thread",
            emptyList<String>(),
            log,
        )
        command.runAll()
        assertEquals(listOf("watch a"), log)
    }

    /**
     * The ordering requirement, stated over the one serial lane: a replaced watch is closed
     * BEFORE its successor is opened, and the two never interleave.
     */
    @Test
    fun replacing_the_held_watch_unwatches_the_old_one_first_on_one_lane() {
        val log = mutableListOf<String>()
        val command = Held()
        val (_, lane) = laneOn(command, Held())
        val a = FakeHandle(log, "a")
        val b = FakeHandle(log, "b")

        lane.hold("s-a", a)
        lane.hold("s-b", b)
        command.runAll()

        assertEquals(listOf("watch a", "unwatch a", "watch b"), log)
        assertSame(b, lane.held)
        assertEquals("s-b", lane.heldSession)
    }

    /**
     * The release path. `PhoneSurface.release()` drops the watch and THEN detaches the
     * dispatch; a posted unwatch that survives the teardown is fine, a dropped one is a
     * machine rendering, sealing and appending for a screen nobody is looking at. Only the
     * settle is gated on attachment -- the verb itself must run.
     */
    @Test
    fun dropping_the_watch_still_unwatches_after_the_dispatch_detaches() {
        val log = mutableListOf<String>()
        val command = Held()
        val main = Held()
        val (dispatch, lane) = laneOn(command, main)
        lane.hold("s-1", FakeHandle(log, "a"))
        command.runAll()
        main.runAll()

        lane.drop()
        dispatch.detach()
        assertNull("the hold outlived drop()", lane.held)
        assertNull(lane.heldSession)

        command.runAll()
        assertEquals(listOf("watch a", "unwatch a"), log)
        main.runAll() // the dropped settle must not throw either
    }

    /** Dropping nothing enqueues nothing: no verb, no settle, no append spent. */
    @Test
    fun dropping_with_nothing_held_does_nothing() {
        val command = Held()
        val (_, lane) = laneOn(command, Held())

        lane.drop()

        assertTrue("an unwatch was enqueued with nothing held", command.isEmpty())
    }

    /**
     * A refused watch is the capability gate answering, and it answers the same way every
     * time: nothing is held, so the next reconcile re-watches instead of renewing into
     * nothing forever.
     */
    @Test
    fun a_refused_watch_clears_the_hold() {
        val log = mutableListOf<String>()
        val command = Held()
        val main = Held()
        val (_, lane) = laneOn(command, main)

        lane.hold("s-1", FakeHandle(log, "a", watchRefused = true))
        command.runAll()
        main.runAll()

        assertNull("a refused watch left the lane believing it holds one", lane.held)
        assertNull(lane.heldSession)
    }

    /**
     * The refusal that arrives LATE, after the screen has already moved on: it belongs to the
     * superseded watch and must not clear its replacement.
     */
    @Test
    fun a_refusal_for_a_superseded_watch_does_not_clear_its_replacement() {
        val log = mutableListOf<String>()
        val command = Held()
        val main = Held()
        val (_, lane) = laneOn(command, main)
        val b = FakeHandle(log, "b")

        lane.hold("s-a", FakeHandle(log, "a", watchRefused = true))
        lane.hold("s-b", b)
        command.runAll()
        main.runAll()

        assertSame("a stale refusal cleared the watch that replaced it", b, lane.held)
        assertEquals("s-b", lane.heldSession)
    }

    /** A watch that lands keeps the hold, which is what the renewal tick reads. */
    @Test
    fun a_landed_watch_keeps_the_hold() {
        val log = mutableListOf<String>()
        val command = Held()
        val main = Held()
        val (_, lane) = laneOn(command, main)
        val a = FakeHandle(log, "a")

        lane.hold("s-1", a)
        command.runAll()
        main.runAll()

        assertSame(a, lane.held)
        assertEquals("s-1", lane.heldSession)
    }
}
