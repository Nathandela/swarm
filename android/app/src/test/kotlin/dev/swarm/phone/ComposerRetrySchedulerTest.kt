package dev.swarm.phone

import dev.swarm.phone.ui.screens.ComposerSendLedger
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class ComposerRetrySchedulerTest {

    @Test
    fun `input busy retry waits for bounded backoff instead of spinning immediately`() {
        val clock = HeldDelay()
        val scheduler = ComposerRetryScheduler(clock)
        var submits = 0

        scheduler.schedule(
            attempt = 1,
            submit = { submits += 1; true },
            rejected = {},
        )

        assertEquals("input_busy retried synchronously in the render loop", 0, submits)
        assertEquals(listOf(1_000L), clock.delays)

        clock.runNext()
        assertEquals(1, submits)

        val delays = (2..8).map { attempt -> scheduler.delayFor(attempt) }
        assertEquals(listOf(2_000L, 4_000L, 8_000L, 16_000L, 30_000L, 30_000L, 30_000L), delays)
    }

    @Test
    fun `a rejected queue admission rearms the same logical operation after the delay`() {
        val clock = HeldDelay()
        val scheduler = ComposerRetryScheduler(clock)
        val ledger = ComposerSendLedger()
        ledger.sealed("op-1", "m/one", "turn-a", "first")
        val retry = ledger.beginRetry("op-1")!!

        scheduler.schedule(
            attempt = retry.retryAttempt,
            submit = { false },
            rejected = { ledger.retryRejected("op-1") },
        )

        assertTrue("the delayed retry was claimable again before admission was refused", ledger.unansweredOperations().isEmpty())
        clock.runNext()
        assertEquals(
            "queue rejection left the logical bubble permanently retrying",
            listOf("op-1"),
            ledger.unansweredOperations(),
        )
        assertFalse(ledger.pendingFor("m/one").single().refused)
    }

    @Test
    fun `cancel makes every callback from the released generation inert`() {
        val clock = HeldDelay()
        val scheduler = ComposerRetryScheduler(clock)
        var submits = 0
        var rejected = 0
        scheduler.schedule(
            attempt = 1,
            submit = { submits += 1; false },
            rejected = { rejected += 1 },
        )

        scheduler.cancel()
        clock.runNext()

        assertEquals("a released generation still entered the facade queue", 0, submits)
        assertEquals("a released generation still ran its render/re-arm callback", 0, rejected)
    }

    @Test
    fun `a new generation runs while an older delayed callback stays cancelled`() {
        val clock = HeldDelay()
        val scheduler = ComposerRetryScheduler(clock)
        val calls = mutableListOf<String>()
        scheduler.schedule(attempt = 1, submit = { calls += "old"; true }, rejected = {})
        scheduler.cancel()
        scheduler.schedule(attempt = 1, submit = { calls += "new"; true }, rejected = {})

        clock.runNext()
        assertTrue("the old surface generation ran after replacement", calls.isEmpty())
        clock.runNext()
        assertEquals(listOf("new"), calls)
    }

    private class HeldDelay : ComposerRetryDelay {
        val delays = mutableListOf<Long>()
        private val work = ArrayDeque<() -> Unit>()

        override fun post(delayMillis: Long, action: () -> Unit) {
            delays += delayMillis
            work += action
        }

        fun runNext() = work.removeFirst().invoke()
    }
}
