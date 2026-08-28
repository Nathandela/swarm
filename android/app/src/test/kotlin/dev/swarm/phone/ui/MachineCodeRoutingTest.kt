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
 *
 * THE TABLE GAINED A THIRD ROW SINCE THIS FILE WAS WRITTEN -- `input_busy`, Slice 0's refusal --
 * and its coverage is [ErrorRoutingRefusalCopyTest] rather than another case here. That file owns
 * the drawing's tabled sentences and the admission argument for why a verb-specific code earns a
 * state at all; this one owns the vocabulary boundary and the reserved row. Adding an
 * `input_busy` case below would be a second assertion about one decision, which is how two tests
 * come to disagree.
 */
class MachineCodeRoutingTest {

    @Test
    fun `the daemons stale_turn is the phones gentle stale turn`() {
        val routed = ErrorRouter.routeMachineCode("stale_turn")
        assertEquals(ErrorState.STALE_TURN, routed.state)
        assertEquals(Remedy.REFRESH, routed.remedy)
        // MOVED, NOT DELETED. The line here was:
        //
        //     routed.message.contains("Nothing was typed"),
        //
        // and it went when the row's words became the conversation drawing's `bubble.stale`
        // sentence, which is captioned "shipped copy, kept" and matched none of the four wordings
        // of this fact the app was carrying. The ASSERTION'S INTENT SURVIVES INTACT -- the copy
        // must never claim the message was delivered -- and only its literal did not.
        //
        // AND THE REPLACEMENT IS THE STRONGER GUARD, which is why this is worth a comment rather
        // than a quiet edit. `startsWith("Not sent")` cannot be satisfied by copy that claims
        // delivery: the sentence has to OPEN by saying the message did not go. A `contains` on a
        // fragment could sit inside almost any sentence built around it -- including one that led
        // with the cause and read as an explanation of a delivery that happened.
        assertTrue(
            "the gentle copy must never claim the message was delivered",
            routed.message.startsWith("Not sent"),
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

    /**
     * W2.2 (phone-refit-playbook §3): every code a shipped daemon can answer has one plain
     * sentence, in a COPY-ONLY sibling of [MachineRefusalCodes.toToken]. The routing table keeps
     * its three rows (it decides state and remedy, and a per-verb fact may not borrow a generic
     * remedy); the sentence table is what a screen says. UNKNOWN is reached only by a code this
     * build has never seen.
     */
    @Test
    fun `every daemon code this build ships has a sentence`() {
        val shipped = setOf(
            "stale_turn", "interrupt_unsupported", "unavailable", "structured_unsupported",
            "input_busy", "policy", "kill_switch", "rate_limit", "stale_approval",
            "not_authorized", "invalid_field", "op_not_implemented", "unknown_preset",
            "stale_preset", "outcome_unknown", "capability_refused", "stale_generation",
            "stale_instance",
        )
        assertEquals(shipped, MachineRefusalCodes.sentence.keys)
        val unknown = ErrorRouter.routeMachineCode("some_future_code").message
        for (code in shipped) {
            val sentence = MachineRefusalCodes.sentenceFor(code)
            assertEquals(MachineRefusalCodes.sentence.getValue(code), sentence)
            assertTrue("`$code` has a blank sentence", sentence.isNotBlank())
            assertTrue("`$code` renders the UNKNOWN sentence", sentence != unknown)
        }
        assertEquals(unknown, MachineRefusalCodes.sentenceFor("some_future_code"))
        assertEquals("toToken decides state and remedy and stays three rows", 3, MachineRefusalCodes.toToken.size)
    }

    /**
     * W2.2's caller (phone-refit-playbook §3, "The caller"): a code with a sentence and no
     * routing row keeps UNKNOWN's state and remedy and says its own words. Words only; a code
     * with no sentence is the reserved row, unchanged.
     */
    @Test
    fun `an unmapped code with a sentence keeps UNKNOWN's routing and says its sentence`() {
        val unknown = ErrorRouter.routeMachineCode("some_future_code")
        val routed = ErrorRouter.routeMachineCode("structured_unsupported")
        assertEquals(ErrorState.UNKNOWN, routed.state)
        assertEquals(unknown.remedy, routed.remedy)
        assertEquals("Chat is off for this session.", routed.message)
        assertEquals(unknown, ErrorRouter.routeMachineCode("some_future_code"))
        assertTrue(
            "the generic sentence is for a code this build has never seen, not for one it has words for",
            routed.message != unknown.message,
        )
    }
}
