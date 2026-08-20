package dev.swarm.phone.ui

import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * FAILING-FIRST (TDD RED, GG-5) for Wave R6 review ROUND 2: the DAEMON's refusal codes are a
 * second vocabulary, and nothing translated it.
 *
 * `ErrorRouter.route` matches the FACADE's `swarm/...` tokens inside a thrown message. A
 * machine refusal does not arrive that way: it arrives as `Outcome.code`, in the daemon's own
 * `schema.ErrorCode` spelling (`stale_turn`, `rate_limit`, ...). So `ErrorState.STALE_TURN`,
 * the one state Mirror M2.4 wrote gentle copy for, had NO producer -- the composer could only
 * ever reach it from a facade-local error that never carries that class.
 *
 * The table is deliberately SMALL and its default is deliberately UNKNOWN rather than a
 * plausible-looking neighbour: a code this build has never seen is not a network problem, and
 * an `else -> OFFLINE` arm reads as tidy and tells the user to wait out a fault waiting cannot
 * fix. What keeps an unrecognised refusal legible is not this table but the machine's own
 * words, which every caller renders verbatim in the detail cell beside the sentence.
 */
class MachineCodeRoutingTest {

    @Test
    fun `the daemons stale_turn is the phones gentle stale turn`() {
        val routed = ErrorRouter.routeMachineCode("stale_turn")
        assertEquals(ErrorState.STALE_TURN, routed.state)
        assertEquals(Remedy.REFRESH, routed.remedy)
        assertTrue(
            "the gentle copy must never claim the message was delivered",
            routed.message.contains("Nothing was typed"),
        )
    }

    @Test
    fun `a rate limited refusal is the one worth waiting out`() {
        assertEquals(ErrorState.RATE_LIMITED, ErrorRouter.routeMachineCode("rate_limit").state)
    }

    @Test
    fun `a code this build has never seen is UNKNOWN and not a plausible neighbour`() {
        assertEquals(ErrorState.UNKNOWN, ErrorRouter.routeMachineCode("some_future_code").state)
        assertEquals(ErrorState.UNKNOWN, ErrorRouter.routeMachineCode("").state)
    }

    @Test
    fun `a facade token is not a machine code and does not cross the table`() {
        assertEquals(
            "the two vocabularies must stay apart: one is thrown by the phone core, the other " +
                "is sent by the daemon, and a table that answered both would route a message " +
                "containing a code word by accident",
            ErrorState.UNKNOWN,
            ErrorRouter.routeMachineCode(SwarmErrorTokens.OFFLINE).state,
        )
    }
}
