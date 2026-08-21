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
 * FAILING-FIRST (TDD RED, GG-5) for the committee round-3 onPause finding -- **the release
 * path blocked the Android main thread for the relay's whole graceful close.**
 *
 * THE DEFECT. `PhoneActivity.onPause` calls `PhoneSurface.release()` synchronously, and
 * `release()` called `live.enterBackground()`, `live.unsubscribeJournal()` and `live.stop()`
 * INLINE on the main thread. `App.Stop` is not a quick flag write: it cancels the relay
 * session and then WAITS on `<-s.done` (mobile/app.go:480) for the drain goroutine, whose
 * teardown performs a documented five-second graceful close
 * (internal/remote/relay/client.go:411). Five seconds on the main looper inside a lifecycle
 * callback is ANR territory, and it is SILENT: `NetworkOnMainThreadException` never fires for
 * a socket Go opened, and Robolectric cannot observe a blocked looper.
 *
 * WHAT THIS FILE TESTS, and what it cannot. [LifecycleLane] is the seam that puts the
 * background/foreground verbs on [VerbDispatch]'s command lane, the same idiom
 * [TerminalWatchLane] uses for the watch verbs. The production handle wraps the bound facade
 * and cannot be constructed on the unit-test JVM (libgojni), so the verbs are driven through
 * a fake [LifecycleHandle]. That `PhoneSurface.release()` and `converge()` actually route
 * through this lane is asserted as source text by android/gate/s25r3_releasepath_test.go;
 * the live-App join is owed to the hardware run, exactly as VerbDispatchTest's header records.
 *
 * THE SEMANTICS THAT MUST SURVIVE THE MOVE, each with its own test below:
 *
 *  - The lease is STILL released on backgrounding (ADR-017 T8-b, the owner-locked rule): the
 *    severance is enqueued unkeyed and [VerbDispatch.enqueue] gates only the settle on
 *    attachment, never the work -- a posted severance that runs promptly in onPause's
 *    background window is acceptable, a dropped one is not.
 *  - Re-foregrounding sequences AFTER a still-draining stop: both verbs ride the one serial
 *    command lane in program order, so a start asked for while the stop is draining runs
 *    behind it -- never against the session the queued stop is about to kill, which would
 *    leave a foregrounded phone disconnected (App.Start no-ops while a.sess != nil).
 *
 * THE TWO KINDS OF TEST BELOW ARE DELIBERATELY DIFFERENT, [VerbDispatchTest]'s own split:
 * facts about THREADS are asserted against a real executor with latches, facts about ORDER
 * and LIFECYCLE against hand-driven executors, because a test that sleeps to observe an
 * absence is a flake waiting for a loaded machine.
 */
class LifecycleLaneTest {

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
        private val severRefused: Boolean = false,
        private val startRefusals: Int = 0,
    ) : LifecycleHandle {
        private var startsRefusedSoFar = 0
        override fun enterBackground() {
            log.add("enterBackground")
            if (severRefused) throw IllegalStateException("no_receiver")
        }
        override fun unsubscribeJournal() {
            log.add("unsubscribeJournal")
        }
        override fun stop() {
            log.add("stop")
        }
        override fun start() {
            log.add("start")
            if (startsRefusedSoFar < startRefusals) {
                startsRefusedSoFar++
                throw IllegalStateException("relay_refused")
            }
        }
    }

    private fun laneOn(command: Executor, main: Executor): Pair<VerbDispatch, LifecycleLane<FakeHandle>> {
        val dispatch = VerbDispatch(command, Held(), main)
        return dispatch to LifecycleLane(dispatch)
    }

    // ---------------------------------------------------------------------
    // Threads: a real executor, because this is what the defect is about.
    // ---------------------------------------------------------------------

    /**
     * The whole point. `App.Stop` waits on the drain goroutine, whose teardown is the relay's
     * five-second graceful close -- so the thread it runs on is the one that can be parked for
     * five seconds, and it must not be the one that released the screen, which on a handset is
     * the main thread inside onPause.
     */
    @Test
    fun the_stop_verb_does_not_run_on_the_thread_that_released_the_screen() {
        val command = Executors.newSingleThreadExecutor()
        try {
            val ran = CountDownLatch(1)
            val where = AtomicReference<Thread>()
            val releasedOn = Thread.currentThread()
            val dispatch = VerbDispatch(command, Held(), Held())
            val lane = LifecycleLane<LifecycleHandle>(dispatch)

            lane.foreground(
                object : LifecycleHandle {
                    override fun enterBackground() {}
                    override fun unsubscribeJournal() {}
                    override fun stop() {
                        where.set(Thread.currentThread())
                        ran.countDown()
                    }
                    override fun start() {}
                },
            ) {}
            lane.background(disconnect = true)

            assertTrue("the stop verb never ran at all", ran.await(10, TimeUnit.SECONDS))
            assertFalse(
                "the stop verb ran on the thread that released the screen, which on a handset " +
                    "is the main thread inside onPause. App.Stop waits on <-s.done for the " +
                    "relay drain, whose teardown is a five-second graceful close, so that " +
                    "thread is the ANR",
                where.get() === releasedOn,
            )
        } finally {
            command.shutdownNow()
        }
    }

    /**
     * The task's named scenario: the user re-foregrounds while the stop is STILL DRAINING the
     * five-second close. The lane is one serial thread, so the start cannot possibly run until
     * the stop returns -- asserted by parking the lane INSIDE the stop and looking, which is
     * deterministic rather than a sleep: while the only lane thread is parked in stop(), no
     * other frame on that lane can have run.
     */
    @Test
    fun a_start_asked_while_the_stop_is_still_draining_runs_after_it() {
        val command = Executors.newSingleThreadExecutor()
        try {
            val log = java.util.Collections.synchronizedList(mutableListOf<String>())
            val stopEntered = CountDownLatch(1)
            val drainReleased = CountDownLatch(1)
            val startRan = CountDownLatch(1)
            val dispatch = VerbDispatch(command, Held(), Held())
            val lane = LifecycleLane<LifecycleHandle>(dispatch)
            val handle = object : LifecycleHandle {
                override fun enterBackground() {
                    log.add("enterBackground")
                }
                override fun unsubscribeJournal() {
                    log.add("unsubscribeJournal")
                }
                override fun stop() {
                    log.add("stop entered")
                    stopEntered.countDown()
                    // The five-second graceful close, held open until the test lets go.
                    drainReleased.await(10, TimeUnit.SECONDS)
                    log.add("stop returned")
                }
                override fun start() {
                    log.add("start")
                    startRan.countDown()
                }
            }

            lane.foreground(handle) {}
            // Consume the first start so the drain below is against a started phone.
            assertTrue("the first start never ran", startRan.await(10, TimeUnit.SECONDS))

            lane.background(disconnect = true)
            assertTrue("the stop never began draining", stopEntered.await(10, TimeUnit.SECONDS))
            // Re-foreground while the stop is parked mid-drain.
            lane.foreground(handle) {}
            assertEquals(
                "the restart ran against a session the queued stop had not yet killed: with " +
                    "the one lane thread parked inside stop(), nothing else on the lane can " +
                    "have run, so anything beyond the entered mark is an interleaving",
                "stop entered",
                synchronized(log) { log.last() },
            )

            drainReleased.countDown()
            val drained = CountDownLatch(1)
            command.execute { drained.countDown() }
            assertTrue("the lane never drained", drained.await(10, TimeUnit.SECONDS))
            assertEquals(
                "the re-foreground start did not run behind the draining stop",
                listOf("stop entered", "stop returned", "start"),
                synchronized(log) { log.takeLast(3) },
            )
        } finally {
            command.shutdownNow()
        }
    }

    // ---------------------------------------------------------------------
    // Order and lifecycle: hand-driven executors.
    // ---------------------------------------------------------------------

    /** Nothing runs where the caller stands: the verbs are queued, not executed. */
    @Test
    fun backgrounding_runs_no_verb_on_the_calling_thread() {
        val log = mutableListOf<String>()
        val command = Held()
        val (_, lane) = laneOn(command, Held())
        val handle = FakeHandle(log)

        lane.foreground(handle) {}
        command.runAll()
        log.clear()

        lane.background(disconnect = true)
        assertTrue(
            "backgrounding ran ${log} on the calling thread, which on a handset is the main " +
                "thread inside onPause",
            log.isEmpty(),
        )
        command.runAll()
        assertEquals(listOf("enterBackground", "unsubscribeJournal", "stop"), log)
    }

    /**
     * T8-b first, then the withdrawal, then the disconnect -- the order `release()` always
     * had: the severance must not depend on the socket decision, and journal delivery is
     * withdrawn while there is still a socket to withdraw it over.
     */
    @Test
    fun backgrounding_severs_then_unsubscribes_then_stops_in_that_order() {
        val log = mutableListOf<String>()
        val command = Held()
        val (_, lane) = laneOn(command, Held())

        lane.foreground(FakeHandle(log)) {}
        command.runAll()
        log.clear()

        lane.background(disconnect = true)
        command.runAll()
        assertEquals(listOf("enterBackground", "unsubscribeJournal", "stop"), log)
    }

    /**
     * The connectivity policy said the socket stays: only the T8-b severance crosses, and the
     * hold stands so a later foreground does not restart a link that never closed.
     */
    @Test
    fun a_sever_only_background_neither_unsubscribes_nor_stops_and_keeps_the_hold() {
        val log = mutableListOf<String>()
        val command = Held()
        val (_, lane) = laneOn(command, Held())
        val handle = FakeHandle(log)

        lane.foreground(handle) {}
        command.runAll()
        log.clear()

        lane.background(disconnect = false)
        command.runAll()
        assertEquals(listOf("enterBackground"), log)
        assertSame("a sever-only background dropped the hold", handle, lane.started)

        lane.foreground(handle) {}
        command.runAll()
        assertEquals(
            "re-foregrounding over a socket that never closed restarted anyway",
            listOf("enterBackground"),
            log,
        )
    }

    /**
     * Each verb swallows its own refusal, exactly as the inline try/catch blocks did: a
     * severance that throws (the process may be going away regardless) must not take the
     * journal withdrawal and the stop down with it.
     */
    @Test
    fun a_refused_severance_still_unsubscribes_and_stops() {
        val log = mutableListOf<String>()
        val command = Held()
        val (_, lane) = laneOn(command, Held())

        lane.foreground(FakeHandle(log, severRefused = true)) {}
        command.runAll()
        log.clear()

        lane.background(disconnect = true)
        command.runAll()
        assertEquals(listOf("enterBackground", "unsubscribeJournal", "stop"), log)
    }

    /**
     * THE OWNER-LOCKED RULE. `release()` detaches the dispatch on every pause, and the
     * severance is enqueued around the same moment; [VerbDispatch.enqueue] gates only the
     * SETTLE on attachment, never the work, so the lease release still reaches the core. A
     * posted severance that runs promptly in onPause's background window is acceptable; a
     * dropped one is a raw-input generation outliving the screen that owns it.
     */
    @Test
    fun backgrounding_still_severs_and_stops_after_the_dispatch_detaches() {
        val log = mutableListOf<String>()
        val command = Held()
        val (dispatch, lane) = laneOn(command, Held())

        lane.foreground(FakeHandle(log)) {}
        command.runAll()
        log.clear()

        dispatch.detach()
        lane.background(disconnect = true)
        command.runAll()
        assertEquals(
            "the severance was dropped because the surface had detached; the lease and the " +
                "terminal control generation would outlive the screen that owns them",
            listOf("enterBackground", "unsubscribeJournal", "stop"),
            log,
        )
    }

    /** The no-restart-before-stopped property, in one lane order. */
    @Test
    fun foregrounding_after_backgrounding_runs_the_stop_before_the_start() {
        val log = mutableListOf<String>()
        val command = Held()
        val (_, lane) = laneOn(command, Held())
        val handle = FakeHandle(log)

        lane.foreground(handle) {}
        command.runAll()
        log.clear()

        lane.background(disconnect = true)
        lane.foreground(handle) {}
        command.runAll()
        assertEquals(
            listOf("enterBackground", "unsubscribeJournal", "stop", "start"),
            log,
        )
    }

    /**
     * THE HOLD IS EAGER, [TerminalWatchLane]'s recorded idiom: state is written at enqueue
     * time, so the redraw that follows a foreground does not enqueue a second start --
     * `converge` runs on every render, and a lane full of idempotent no-op starts is still a
     * lane a real command has to queue behind.
     */
    @Test
    fun foregrounding_twice_starts_once() {
        val log = mutableListOf<String>()
        val command = Held()
        val (_, lane) = laneOn(command, Held())
        val handle = FakeHandle(log)

        lane.foreground(handle) {}
        lane.foreground(handle) {}
        command.runAll()
        assertEquals(listOf("start"), log)
        assertSame(handle, lane.started)
    }

    /** A phone that was never started has nothing to sever and nothing to stop. */
    @Test
    fun backgrounding_with_nothing_started_sends_nothing() {
        val log = mutableListOf<String>()
        val command = Held()
        val (_, lane) = laneOn(command, Held())

        lane.background(disconnect = true)
        command.runAll()
        assertTrue("a pause before anything was built reached the facade: $log", log.isEmpty())
        assertTrue(command.isEmpty())
    }

    /**
     * THE REFUSAL CLAWS THE HOLD BACK, and reports itself: a start the facade refused must
     * clear the eager hold so the next render retries, and the refusal must reach the caller
     * so the screen can say why the phone is not connecting.
     */
    @Test
    fun a_refused_start_clears_the_hold_and_reports_the_refusal() {
        val log = mutableListOf<String>()
        val command = Held()
        val main = Held()
        val (_, lane) = laneOn(command, main)
        val handle = FakeHandle(log, startRefusals = 1)
        val refusals = mutableListOf<Throwable>()

        lane.foreground(handle) { refusals.add(it) }
        command.runAll()
        main.runAll()
        assertNull("a refused start left the hold standing, so no render will ever retry", lane.started)
        assertEquals(1, refusals.size)

        lane.foreground(handle) {}
        command.runAll()
        assertEquals(
            "the render after a refused start did not retry",
            listOf("start", "start"),
            log,
        )
    }

    /**
     * A STALE REFUSAL SPARES THE REPLACEMENT. The handle is cached per App, so identity alone
     * cannot tell a superseded start attempt from its successor -- which is why the lane keys
     * the clawback on a per-attempt token rather than on [TerminalWatchLane]'s handle
     * identity. The refusal of a start that a background-then-foreground has already
     * superseded must not clear the successor's hold: that hold nulled is a release() that
     * skips the severance, a lease outliving the screen.
     */
    @Test
    fun a_stale_refusal_does_not_clear_the_replacement_start() {
        val log = mutableListOf<String>()
        val command = Held()
        val main = Held()
        val (_, lane) = laneOn(command, main)
        val handle = FakeHandle(log, startRefusals = 1)
        val refusals = mutableListOf<Throwable>()

        lane.foreground(handle) { refusals.add(it) } // will be refused, but settles late
        lane.background(disconnect = true)
        lane.foreground(handle) { refusals.add(it) } // the replacement; this one lands
        command.runAll()
        main.runAll()

        assertSame(
            "the first start's refusal, settling after a background-then-foreground, cleared " +
                "the REPLACEMENT's hold: the next release() would then skip the severance",
            handle,
            lane.started,
        )
        assertTrue("a stale refusal was reported as if it were the live attempt's", refusals.isEmpty())
    }

    /** A start that landed keeps the hold; nothing settles it away. */
    @Test
    fun a_landed_start_keeps_the_hold() {
        val log = mutableListOf<String>()
        val command = Held()
        val main = Held()
        val (_, lane) = laneOn(command, main)
        val handle = FakeHandle(log)

        lane.foreground(handle) {}
        command.runAll()
        main.runAll()
        assertSame(handle, lane.started)
    }
}
