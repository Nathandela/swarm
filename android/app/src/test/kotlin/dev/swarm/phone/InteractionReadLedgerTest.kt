package dev.swarm.phone

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

class InteractionReadLedgerTest {
    @Test
    fun `overlapping detail and history reads retire only their exact answered operation`() {
        val reads = InteractionReadLedger()
        val earlier = PendingInteractionRead.History("machine/a", speaks = false)
        val firstDetail = PendingInteractionRead.Detail("item/a")
        val secondDetail = PendingInteractionRead.Detail("item/b")

        assertTrue(reads.record("history-1", earlier))
        assertTrue(reads.record("detail-1", firstDetail))
        assertTrue(reads.record("detail-2", secondDetail))
        assertFalse("a duplicate id must not replace its original context", reads.record("detail-1", secondDetail))

        assertNull("a different outcome id cannot claim this read", reads.answered("detail-1", "detail-2", "ok"))
        assertNull("an unanswered outcome stays pending", reads.answered("detail-1", "detail-1", ""))
        assertNull("a blank requested id cannot claim a pending entry", reads.answered("", "", "ok"))
        assertEquals(3, reads.pending().size)

        assertEquals(firstDetail, reads.answered("detail-1", "detail-1", "unavailable"))
        assertEquals(
            listOf("history-1", "detail-2"),
            reads.pending().map { it.operationId },
        )
        assertEquals(secondDetail, reads.answered("detail-2", "detail-2", "ok"))
        assertEquals(earlier, reads.answered("history-1", "history-1", "ok"))
        assertTrue(reads.pending().isEmpty())
    }
}
