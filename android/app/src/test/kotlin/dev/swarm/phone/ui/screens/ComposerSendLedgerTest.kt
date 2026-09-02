package dev.swarm.phone.ui.screens

import dev.swarm.phone.ui.kit.SendState
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class ComposerSendLedgerTest {

    @Test
    fun `durable hydration is idempotent across surface recreation`() {
        val ledger = ComposerSendLedger()
        val durable = listOf(
            DurableComposerPublication(
                logicalId = "logical-1",
                operationId = "op-1",
                sessionId = "m/one",
                expectedTurn = "turn-a",
                text = "hello",
                phase = "admitted",
                terminalCode = "",
            ),
        )

        ledger.hydrate(durable)
        ledger.hydrate(durable)

        assertEquals(listOf("op-1"), ledger.unansweredOperations())
        assertEquals(listOf("hello"), ledger.pendingFor("m/one").map { it.text })
    }

    @Test
    fun `recreated retry replaces its prior operation by logical id`() {
        val ledger = ComposerSendLedger()
        ledger.hydrate(
            listOf(
                DurableComposerPublication(
                    logicalId = "logical-1",
                    operationId = "op-old",
                    sessionId = "m/one",
                    expectedTurn = "turn-a",
                    text = "same bubble",
                    phase = "terminal",
                    terminalCode = "input_busy",
                ),
            ),
        )
        ledger.hydrate(
            listOf(
                DurableComposerPublication(
                    logicalId = "logical-1",
                    operationId = "op-new",
                    sessionId = "m/one",
                    expectedTurn = "turn-a",
                    text = "same bubble",
                    phase = "prepared",
                    terminalCode = "",
                ),
            ),
        )

        assertEquals(listOf("op-new"), ledger.unansweredOperations())
        assertEquals(listOf("op-new"), ledger.pendingFor("m/one").map { it.operationId })
    }

    @Test
    fun `identical text with distinct logical ids remains two messages`() {
        val ledger = ComposerSendLedger()
        ledger.hydrate(
            listOf(
                DurableComposerPublication("logical-a", "op-a", "m/one", "turn-a", "repeat", "admitted", ""),
                DurableComposerPublication("logical-b", "op-b", "m/one", "turn-a", "repeat", "admitted", ""),
            ),
        )

        assertEquals(listOf("op-a", "op-b"), ledger.unansweredOperations())
        assertEquals(listOf("repeat", "repeat"), ledger.pendingFor("m/one").map { it.text })
    }

    @Test
    fun `hydration does not reset a claimed terminal result or authorize an unknown retry`() {
        val ledger = ComposerSendLedger()
        val durable = DurableComposerPublication(
            "logical-unknown", "op-unknown", "m/one", "turn-a", "check first",
            "terminal", "outcome_unknown",
        )
        ledger.hydrate(listOf(durable))
        ledger.settle(
            "op-unknown",
            ComposerVerdict(
                answered = true,
                state = SendState.REFUSED,
                refusal = "UNKNOWN",
                clearsDraft = false,
                notice = "Not sure it went through. Check before retrying.",
                detail = "outcome_unknown",
            ),
        )

        ledger.hydrate(listOf(durable))

        assertTrue(ledger.unansweredOperations().isEmpty())
        assertEquals("UNKNOWN", ledger.latestFor("m/one")?.refusal)
        assertEquals(null, ledger.beginRetry("op-unknown"))
    }

    @Test
    fun `input busy retry keeps one logical bubble and replaces only its operation id`() {
        val ledger = ComposerSendLedger()
        ledger.sealed("op-1", "m/one", "turn-a", "first")

        val retry = ledger.beginRetry("op-1")

        assertEquals("m/one", retry?.sessionId)
        assertEquals("turn-a", retry?.expectedTurn)
        assertEquals("first", retry?.text)
        assertEquals(
            "the transient answer removed the logical message while its retry was queued",
            listOf("first"),
            ledger.pendingFor("m/one").map { it.text },
        )
        assertTrue(
            "the answered operation stayed claimable and could enqueue the same retry on every redraw",
            ledger.unansweredOperations().isEmpty(),
        )

        ledger.retrySealed("op-1", "op-2")

        val pending = ledger.pendingFor("m/one")
        assertEquals("the retry duplicated the logical bubble", 1, pending.size)
        assertEquals("op-2", pending.single().operationId)
        assertEquals("first", pending.single().text)
        assertEquals(listOf("op-2"), ledger.unansweredOperations())
    }

    @Test
    fun `multiple transient sends retain their original fifo order across fresh operation ids`() {
        val ledger = ComposerSendLedger()
        ledger.sealed("op-a1", "m/one", "turn-a", "first")
        ledger.sealed("op-b1", "m/one", "turn-a", "second")

        assertEquals("first", ledger.beginRetry("op-a1")?.text)
        assertEquals(
            "second",
            ledger.beginRetry("op-b1")?.text,
        )
        assertFalse(
            "the later logical message could dispatch past an earlier unresolved retry",
            ledger.retryReady("op-b1"),
        )
        ledger.retryDispatched("op-a1")
        assertTrue(
            "the later retry stayed blocked after the earlier retry entered the serial command lane",
            ledger.retryReady("op-b1"),
        )
        ledger.retrySealed("op-a1", "op-a2")
        ledger.settle(
            "op-a2",
            ComposerVerdict(
                answered = true,
                state = SendState.SENT,
                refusal = "",
                clearsDraft = true,
                notice = "",
                detail = "",
            ),
        )
        assertTrue("the later retry never became ready after the earlier message settled", ledger.retryReady("op-b1"))
        ledger.retrySealed("op-b1", "op-b2")

        assertEquals(listOf("first", "second"), ledger.pendingFor("m/one").map { it.text })
        assertEquals(listOf("op-b2"), ledger.unansweredOperations())
    }

    @Test
    fun `an older send with a lost outcome does not freeze a later proven retry`() {
        val ledger = ComposerSendLedger()
        ledger.sealed("op-a", "m/one", "turn-a", "outcome was lost during recovery")
        ledger.sealed("op-b", "m/one", "turn-a", "machine answered input busy")

        assertEquals("machine answered input busy", ledger.beginRetry("op-b")?.text)
        assertTrue(
            "an older operation with no retryable verdict permanently blocked the later safe retry",
            ledger.retryReady("op-b"),
        )
    }

    @Test
    fun `a retry in one conversation never blocks a different conversation`() {
        val ledger = ComposerSendLedger()
        ledger.sealed("op-a", "m/one", "turn-a", "first session")
        ledger.sealed("op-b", "m/two", "turn-b", "second session")

        assertEquals("first session", ledger.beginRetry("op-a")?.text)
        assertEquals(
            "a busy draft in one conversation blocked an independent conversation",
            "second session",
            ledger.beginRetry("op-b")?.text,
        )
        assertTrue(ledger.retryReady("op-b"))
    }

    @Test
    fun `release rearms only a scheduled retry and keeps its one logical bubble`() {
        val ledger = ComposerSendLedger()
        ledger.sealed("op-1", "m/one", "turn-a", "first")
        assertEquals(1, ledger.beginRetry("op-1")?.retryAttempt)

        ledger.rearmScheduledRetries()

        assertEquals(listOf("op-1"), ledger.unansweredOperations())
        assertEquals(listOf("first"), ledger.pendingFor("m/one").map { it.text })
        assertEquals(2, ledger.beginRetry("op-1")?.retryAttempt)
        ledger.retryDispatched("op-1")
        ledger.rearmScheduledRetries()
        assertTrue(
            "release rearmed an attempt already admitted to the facade and could duplicate it",
            ledger.unansweredOperations().isEmpty(),
        )
    }

    @Test
    fun `two sends in one conversation remain independently claimable and ordered`() {
        val ledger = ComposerSendLedger()
        ledger.sealed("op-1", "m/one", "first")
        ledger.sealed("op-2", "m/one", "second")

        assertEquals(listOf("op-1", "op-2"), ledger.unansweredOperations())
        assertEquals(listOf("first", "second"), ledger.pendingFor("m/one").map { it.text })

        ledger.settle(
            "op-2",
            ComposerVerdict(
                answered = true,
                state = SendState.REFUSED,
                refusal = "OFFLINE",
                clearsDraft = false,
                notice = "Not sent.",
                detail = "link dropped",
            ),
        )

        assertEquals(listOf("op-1"), ledger.unansweredOperations())
        assertFalse(ledger.pendingFor("m/one").first().refused)
        assertTrue(ledger.pendingFor("m/one").last().refused)
        assertEquals("OFFLINE", ledger.latestFor("m/one")?.refusal)
    }

    @Test
    fun `sends in different conversations never replace each other`() {
        val ledger = ComposerSendLedger()
        ledger.sealed("op-a", "m/one", "one")
        ledger.sealed("op-b", "m/two", "two")

        assertEquals(listOf("one"), ledger.pendingFor("m/one").map { it.text })
        assertEquals(listOf("two"), ledger.pendingFor("m/two").map { it.text })
        assertEquals(listOf("op-a", "op-b"), ledger.unansweredOperations())
    }

    @Test
    fun `an echo hides only its bubble while its machine outcome remains claimable`() {
        val ledger = ComposerSendLedger()
        ledger.sealed("op-1", "m/one", "first")
        ledger.sealed("op-2", "m/one", "second")

        ledger.observeEchoes("m/one", setOf("op-1"))

        assertEquals(listOf("second"), ledger.pendingFor("m/one").map { it.text })
        assertEquals(
            "the transcript echo orphaned the still-unclaimed operation outcome",
            listOf("op-1", "op-2"),
            ledger.unansweredOperations(),
        )
    }

    @Test
    fun `a newer accepted echo does not resurrect an older refusal as the current notice`() {
        val ledger = ComposerSendLedger()
        ledger.sealed("op-1", "m/one", "first")
        ledger.settle(
            "op-1",
            ComposerVerdict(
                answered = true,
                state = SendState.REFUSED,
                refusal = "OFFLINE",
                clearsDraft = false,
                notice = "Not sent.",
                detail = "link dropped",
            ),
        )
        ledger.sealed("op-2", "m/one", "second")
        ledger.observeEchoes("m/one", setOf("op-2"))
        ledger.settle(
            "op-2",
            ComposerVerdict(
                answered = true,
                state = SendState.SENT,
                refusal = "",
                clearsDraft = true,
                notice = "",
                detail = "",
            ),
        )

        assertEquals(SendState.SENT, ledger.latestFor("m/one")?.state)
        assertEquals("", ledger.latestFor("m/one")?.refusal)
    }

    @Test
    fun `an older refusal keeps its exact copy and machine detail on its own send`() {
        val ledger = ComposerSendLedger()
        ledger.sealed("op-1", "m/one", "first")
        ledger.sealed("op-2", "m/one", "second")

        ledger.settle(
            "op-1",
            ComposerVerdict(
                answered = true,
                state = SendState.REFUSED,
                refusal = "INPUT_BUSY",
                clearsDraft = false,
                notice = "Not sent. Finish typing on your computer first.",
                detail = "input region is not empty",
            ),
        )

        val pending = ledger.pendingFor("m/one")
        assertEquals(listOf("op-1", "op-2"), pending.map { it.operationId })
        assertEquals(
            "Not sent. Finish typing on your computer first.",
            pending.first().notice,
        )
        assertEquals("input region is not empty", pending.first().detail)
        assertEquals("", pending.last().notice)
        assertEquals("", pending.last().detail)
        assertEquals(
            "the older refusal replaced the newer send's current state",
            "op-2",
            ledger.latestFor("m/one")?.operationId,
        )
    }

    @Test
    fun `a matching user message echo overrides an uncertain machine refusal`() {
        val ledger = ComposerSendLedger()
        ledger.sealed("op-1", "m/one", "please continue")
        ledger.settle(
            "op-1",
            ComposerVerdict(
                answered = true,
                state = SendState.REFUSED,
                refusal = "UNKNOWN",
                clearsDraft = false,
                notice = "Not sent. Try again.",
                detail = "outcome_unknown: provider write may have committed",
            ),
        )

        ledger.observeEchoes("m/one", setOf("op-1"))

        assertTrue("the authoritative echo did not clear the provisional bubble", ledger.pendingFor("m/one").isEmpty())
        val latest = ledger.latestFor("m/one")
        assertEquals(SendState.SENT, latest?.state)
        assertEquals("", latest?.refusal)
        assertEquals("", latest?.notice)
        assertEquals("", latest?.detail)
    }
}
