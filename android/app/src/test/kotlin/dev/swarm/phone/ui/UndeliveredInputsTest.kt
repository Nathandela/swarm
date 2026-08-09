package dev.swarm.phone.ui

import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * FAILING-FIRST (TDD RED, GG-5) for PB-INPUT-1's ledger as this side models it
 * (agents-tracker-hxv, agents-tracker-nx44.6).
 *
 * WHY THE MODEL IS TESTED APART FROM THE SCREEN. `App.UndeliveredInputs` crosses as a gomobile
 * handle over .so files no unit-test JVM can load, so what CAN be asserted here is the shape this
 * side folds it into and the one decision that shape makes: narrowing the ledger to one session.
 * Where the calls are placed stays `FacadeBridge`'s and review's, which is that adapter's own
 * recorded split.
 */
class UndeliveredInputsTest {

    private fun entry(
        session: String,
        bytes: Int = 4,
        reason: String = "the link dropped before the keystroke was written",
    ) = UndeliveredInput(
        sessionId = session,
        bytes = bytes,
        reason = reason,
        atMillis = 1_700_000_000_000L,
    )

    @Test
    fun `the ledger narrows to one session and keeps the bound's own overflow count`() {
        val ledger = UndeliveredLedger(
            entries = listOf(entry("mbp/api"), entry("mbp/web"), entry("mbp/api")),
            dropped = 12,
        )

        val one = ledger.forSession("mbp/api")

        assertEquals(2, one.entries.size)
        assertTrue(
            "the narrowed ledger carries another session's loss, which is the proximity error " +
                "PB-SYNC-2 forbids applied to input: a screen open on one session would report a " +
                "keystroke another session lost",
            one.entries.all { it.sessionId == "mbp/api" },
        )
        assertEquals(
            "narrowing zeroed the ledger's own overflow count, and no session can own it: the " +
                "discarded records took their session ids with them when the bound dropped them. " +
                "A per-session view that reported 0 would tell a user nothing was lost beyond " +
                "what is listed, which is the silent discard PB-INPUT-1 exists to prevent",
            12,
            one.dropped,
        )
    }

    @Test
    fun `an empty ledger is the shape a session that lost nothing reads`() {
        assertEquals(emptyList<UndeliveredInput>(), UndeliveredLedger.EMPTY.entries)
        assertEquals(0, UndeliveredLedger.EMPTY.dropped)
        assertEquals(UndeliveredLedger.EMPTY, UndeliveredLedger.EMPTY.forSession("mbp/api"))
    }

    /**
     * PB-INPUT-1's own words on the wire type: `UndeliveredInput` carries a BYTE COUNT and not the
     * bytes, "so nothing on screen can echo what was typed". This side must not widen that.
     */
    @Test
    fun `an entry carries how much was lost and never what was lost`() {
        val one = entry("mbp/api", bytes = 11)

        assertEquals(11, one.bytes)
        assertEquals("the link dropped before the keystroke was written", one.reason)
    }
}
