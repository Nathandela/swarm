package dev.swarm.phone

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test
import java.util.concurrent.Executor
import java.util.concurrent.atomic.AtomicInteger

/**
 * FAILING-FIRST (TDD RED, GG-5) for wave R4's D3 round-3 fix pack (bead agents-tracker-0ox9),
 * BLOCKING 3's second half: the double-tap fence for work that has no control to key on.
 *
 * COMPILE-RED ON PURPOSE: `enqueue` takes no `key` and returns Unit.
 *
 * WHY [VerbDispatch.press]'S FENCE DOES NOT COVER THIS. `press` keys single-flight on the VIEW,
 * and the machines screen's controls are built per draw from the row set -- an in-flight mark
 * keyed on a view the next redraw replaces fences nothing, which is why `machineVerb` uses
 * `enqueue` (its own KDoc says so). But `enqueue` was deliberately unfenced, for the push-token
 * reconciliation that must never be dropped (agents-tracker-b6iu), and Add computer runs
 * `App.Stop` -> AddMachine -> `App.Start` on that lane: a rapid double tap ran that sequence
 * TWICE, disconnecting the phone and abandoning buffered input a second time while the first was
 * still crossing.
 *
 * SO THE FENCE IS OPT-IN, BY KEY. Unkeyed work keeps b6iu's guarantee exactly -- nothing is ever
 * dropped -- and keyed work is single-flight per key, with the refusal REPORTED to the caller so
 * the surface can say it rather than swallow it.
 */
class VerbDispatchRound3Test {

    @Test
    fun a_second_enqueue_under_the_same_key_while_the_first_is_crossing_is_refused() {
        val lane = Held()
        val main = Held()
        val dispatch = VerbDispatch(lane, lane, main)
        val verbs = AtomicInteger()

        val first = dispatch.enqueue(
            SendPlane.COMMAND,
            key = "machines.add",
            work = { verbs.incrementAndGet() },
            settle = {},
        )
        val second = dispatch.enqueue(
            SendPlane.COMMAND,
            key = "machines.add",
            work = { verbs.incrementAndGet() },
            settle = {},
        )
        lane.runAll()
        main.runAll()

        assertTrue("the first keyed verb was refused although nothing was in flight", first)
        assertFalse(
            "the second tap was accepted. Add computer stops the drain -- every buffered " +
                "keystroke resolved undelivered, every input lease severed, the link dropped -- " +
                "so running it twice does that twice, and the caller was never told",
            second,
        )
        assertEquals(
            "the second tap issued a second stop/add/start sequence on the COMMAND lane",
            1,
            verbs.get(),
        )
    }

    @Test
    fun a_refused_keyed_enqueue_settles_nothing() {
        val lane = Held()
        val main = Held()
        val dispatch = VerbDispatch(lane, lane, main)
        val settles = AtomicInteger()

        dispatch.enqueue(SendPlane.COMMAND, key = "k", work = {}, settle = { settles.incrementAndGet() })
        dispatch.enqueue(SendPlane.COMMAND, key = "k", work = {}, settle = { settles.incrementAndGet() })
        lane.runAll()
        main.runAll()

        assertEquals("the refused enqueue produced an answer of its own", 1, settles.get())
    }

    @Test
    fun the_key_is_free_again_once_the_answer_settles() {
        val lane = Held()
        val main = Held()
        val dispatch = VerbDispatch(lane, lane, main)
        val verbs = AtomicInteger()

        dispatch.enqueue(SendPlane.COMMAND, key = "k", work = { verbs.incrementAndGet() }, settle = {})
        lane.runAll()
        main.runAll()
        val again = dispatch.enqueue(
            SendPlane.COMMAND,
            key = "k",
            work = { verbs.incrementAndGet() },
            settle = {},
        )
        lane.runAll()
        main.runAll()

        assertTrue("the key was never released, so the control is dead for the surface's life", again)
        assertEquals("the second, legitimate add never ran", 2, verbs.get())
    }

    @Test
    fun an_answer_dropped_by_release_still_frees_the_key() {
        // [VerbDispatch.press]'s recorded argument, spent again: a dropped settle that left the
        // mark standing would leave that verb refusing every tap for the life of the surface,
        // with nothing to distinguish it from a dead button.
        val lane = Held()
        val main = Held()
        val dispatch = VerbDispatch(lane, lane, main)

        dispatch.enqueue(SendPlane.COMMAND, key = "k", work = {}, settle = {})
        lane.runAll()
        dispatch.detach()
        main.runAll()
        dispatch.attach()

        assertTrue(
            "the key is still held after its answer was dropped by release",
            dispatch.enqueue(SendPlane.COMMAND, key = "k", work = {}, settle = {}),
        )
    }

    @Test
    fun unkeyed_work_is_never_refused() {
        // agents-tracker-b6iu, pinned: a push-token reconciliation discarded because an earlier
        // one is still crossing leaves the token disagreeing with the switches. The fence must
        // be opt-in, and this is the assertion that keeps it so.
        val lane = Held()
        val main = Held()
        val dispatch = VerbDispatch(lane, lane, main)
        val verbs = AtomicInteger()

        dispatch.enqueue(SendPlane.COMMAND, work = { verbs.incrementAndGet() }, settle = {})
        dispatch.enqueue(SendPlane.COMMAND, work = { verbs.incrementAndGet() }, settle = {})
        lane.runAll()
        main.runAll()

        assertEquals("an unkeyed enqueue was dropped by the new fence", 2, verbs.get())
    }

    @Test
    fun two_different_keys_do_not_refuse_each_other() {
        val lane = Held()
        val main = Held()
        val dispatch = VerbDispatch(lane, lane, main)
        val verbs = AtomicInteger()

        dispatch.enqueue(SendPlane.COMMAND, key = "add", work = { verbs.incrementAndGet() }, settle = {})
        dispatch.enqueue(SendPlane.COMMAND, key = "forget", work = { verbs.incrementAndGet() }, settle = {})
        lane.runAll()
        main.runAll()

        assertEquals("single-flight is per key, not per lane", 2, verbs.get())
    }

    /** [VerbDispatchTest.Held]'s twin: an executor that runs nothing until it is told to. */
    private class Held : Executor {
        private val queued = ArrayDeque<Runnable>()

        override fun execute(command: Runnable) {
            queued.addLast(command)
        }

        fun runAll() {
            while (queued.isNotEmpty()) queued.removeFirst().run()
        }
    }
}
