package dev.swarm.phone.ui.screens

import dev.swarm.phone.ui.kit.SendState
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class ComposerSendLedgerTest {

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
