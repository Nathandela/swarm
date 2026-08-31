package dev.swarm.phone

import androidx.test.core.app.ActivityScenario
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import java.util.concurrent.Executor

@RunWith(RobolectricTestRunner::class)
class ComposerRetryLifecycleTest {

    @Test
    fun `surface release cancels a scheduled retry before its clock advances`() {
        ActivityScenario.launch(PhoneActivity::class.java).use { scenario ->
            scenario.onActivity { activity ->
                val clock = HeldDelay()
                val scheduler = ComposerRetryScheduler(clock)
                val ledger = dev.swarm.phone.ui.screens.ComposerSendLedger()
                val surface = PhoneSurface(
                    activity,
                    PhoneRuntime(activity),
                    VerbDispatch.direct(),
                    scheduler,
                    ledger,
                )
                var facadeCalls = 0
                var redraws = 0
                ledger.sealed("op-1", "m/one", "turn-a", "first")
                val firstAttempt = ledger.beginRetry("op-1")!!
                scheduler.schedule(
                    attempt = firstAttempt.retryAttempt,
                    submit = { facadeCalls += 1; false },
                    rejected = { redraws += 1 },
                )

                surface.release()
                surface.render()
                assertEquals(
                    "the resumed surface cannot re-read the durable input_busy outcome",
                    listOf("op-1"),
                    ledger.unansweredOperations(),
                )
                val resumedAttempt = ledger.beginRetry("op-1")!!
                scheduler.schedule(
                    attempt = resumedAttempt.retryAttempt,
                    submit = { facadeCalls += 1; true },
                    rejected = { redraws += 1 },
                )

                // The cancelled generation was posted first and advances first. It must do
                // nothing; the resumed generation owns the only facade call.
                clock.runNext()
                assertEquals(0, facadeCalls)
                clock.runNext()

                assertEquals("resume duplicated or lost the logical retry", 1, facadeCalls)
                assertEquals("release let an old retry redraw or reattach its dispatch", 0, redraws)
                assertEquals(
                    "release/resume duplicated the operation-owned bubble",
                    listOf("first"),
                    ledger.pendingFor("m/one").map { it.text },
                )
            }
        }
    }

    @Test
    fun `an admitted retry finishing after release reconciles once without detached render`() {
        ActivityScenario.launch(PhoneActivity::class.java).use { scenario ->
            scenario.onActivity { activity ->
                val command = HeldExecutor()
                val main = HeldExecutor()
                val dispatch = VerbDispatch(command, command, main)
                val ledger = dev.swarm.phone.ui.screens.ComposerSendLedger()
                val surface = PhoneSurface(
                    activity,
                    PhoneRuntime(activity),
                    dispatch,
                    ComposerRetryScheduler(HeldDelay()),
                    ledger,
                )
                ledger.sealed("op-1", "m/one", "turn-a", "first")
                ledger.beginRetry("op-1")
                var facadeCalls = 0
                var detachedRenders = 0
                val accepted = dispatch.enqueueCompleting(
                    SendPlane.COMMAND,
                    work = { facadeCalls += 1; "op-2" },
                    complete = { answer ->
                        answer.onSuccess { ledger.retrySealed("op-1", it) }
                    },
                    settle = { detachedRenders += 1 },
                )
                assertTrue(accepted)
                ledger.retryDispatched("op-1")

                surface.release()
                command.runAll()
                main.runAll()

                assertEquals(1, facadeCalls)
                assertEquals("the detached completion touched UI/render", 0, detachedRenders)
                assertEquals(listOf("op-2"), ledger.unansweredOperations())
                assertEquals(1, ledger.pendingFor("m/one").size)
                assertEquals("op-2", ledger.pendingFor("m/one").single().operationId)

                surface.render()
                main.runAll()
                assertEquals("resume duplicated the admitted facade call", 1, facadeCalls)
                assertEquals("resume duplicated the logical bubble", 1, ledger.pendingFor("m/one").size)
            }
        }
    }

    private class HeldDelay : ComposerRetryDelay {
        private val work = ArrayDeque<() -> Unit>()

        override fun post(delayMillis: Long, action: () -> Unit) {
            work += action
        }

        fun runNext() = work.removeFirst().invoke()
    }

    private class HeldExecutor : Executor {
        private val work = ArrayDeque<Runnable>()

        override fun execute(command: Runnable) {
            work += command
        }

        fun runAll() {
            while (work.isNotEmpty()) work.removeFirst().run()
        }
    }
}
