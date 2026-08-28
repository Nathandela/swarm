package dev.swarm.phone.ui

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotEquals
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * FAILING-FIRST (TDD RED, GG-5) for agents-tracker-ksvb.5 -- the three ways a pairing ENTRY can
 * be wrong, told apart on the screen where they happen.
 *
 * **THE DEFECT.** `mobile/pairing.go` has authored a specific sentence for each of these three
 * since agents-tracker-3fkm, and all three were stamped `ErrClassPairingFailed`. That class has
 * exactly one row here, and its copy is "The pairing call itself failed. Start the pairing again
 * from your machine's code." Every word of that is wrong for all three: none of them ever reached
 * a call, and "start again from your machine's code" is advice for a ceremony that never began.
 * So a person who mistyped one character of a ten-character code was told to walk back to their
 * machine, and a person whose phone had never seen a relay address was told the same thing.
 *
 * **WHY THREE CLASSES AND NOT ONE "BAD ENTRY".** The remedies differ by exactly the fact the
 * reader already has. The typist has the code in front of them and needs to read it again. The
 * phone with no relay is MISSING a fact, and there are two acts that supply it. The phone with a
 * malformed address has one and it is the wrong shape -- and telling that person to scan a QR
 * ignores what is on their own screen. Collapsed, at least two of the three read advice they
 * cannot act on, which is what PB-APP-9 exists to stop one layer up.
 *
 * The Go side proves the three classes are produced (`mobile/ksvb5_refusalcopy_test.go`) and that
 * they have taxonomy rows (`mobile/s16_taxonomy_test.go`). This side proves the screen says
 * something DIFFERENT for each.
 */
class PairingEntryRoutingTest {

    @Test
    fun `a mistyped code sends the reader back to the code and not to the machine`() {
        val routed = ErrorRouter.route("${SwarmErrorTokens.PAIRING_CODE_INVALID}: pairing: short code is not ten Crockford base32 characters")

        assertEquals(ErrorState.PAIRING_CODE_INVALID, routed.state)
        assertEquals(
            "That code does not look right. It is ten characters from your computer's screen -- " +
                "check for a typo and try again.",
            routed.message,
        )
        assertEquals(Remedy.RETRY_PAIRING, routed.remedy)
    }

    @Test
    fun `a phone with no relay is told the two acts that give it one`() {
        val routed = ErrorRouter.route("${SwarmErrorTokens.RELAY_UNKNOWN}: this phone has no relay yet")

        assertEquals(ErrorState.RELAY_UNKNOWN, routed.state)
        assertEquals(
            "This phone does not know your computer's address yet. Scan the QR once, or paste " +
                "the full code your computer printed.",
            routed.message,
        )
        assertEquals(Remedy.RETRY_PAIRING, routed.remedy)
    }

    @Test
    fun `a malformed relay address is answered with the shape it should have`() {
        val routed = ErrorRouter.route("${SwarmErrorTokens.RELAY_ADDRESS_INVALID}: that is not a relay address")

        assertEquals(ErrorState.RELAY_ADDRESS_INVALID, routed.state)
        assertEquals(
            "That is not an address. It looks like wss://host:port -- your computer printed " +
                "the whole thing.",
            routed.message,
        )
        assertEquals(Remedy.RETRY_PAIRING, routed.remedy)
    }

    /**
     * THE POINT OF THE SPLIT, asserted as the property rather than as three equalities: four
     * different failures on one screen must not read as one sentence. A router that answered
     * PAIRING_FAILED's row for all of them would satisfy every "is not blank" check ever written
     * about this screen.
     */
    @Test
    fun `the four pairing failures are four sentences and four states`() {
        val tokens = listOf(
            SwarmErrorTokens.PAIRING_FAILED,
            SwarmErrorTokens.PAIRING_CODE_INVALID,
            SwarmErrorTokens.RELAY_UNKNOWN,
            SwarmErrorTokens.RELAY_ADDRESS_INVALID,
        )
        val routed = tokens.map { ErrorRouter.route(it) }

        assertEquals(
            "two of the pairing entry failures render as one state, so the screen cannot say " +
                "which of them happened",
            tokens.size,
            routed.map { it.state }.toSet().size,
        )
        assertEquals(
            "two of the pairing entry failures share a sentence: ${routed.map { it.message }}",
            tokens.size,
            routed.map { it.message }.toSet().size,
        )
        assertEquals(tokens.size, tokens.toSet().size)
    }

    /**
     * NONE OF THE THREE OFFERS THE PAIRING FLOW, and that is [Remedy.offersPairing]'s own
     * distinction rather than a wording choice: the reader is ALREADY in the pairing flow -- these
     * three are refused by `payloadFromShortCode` before anything is dialled -- so a control that
     * opened it again is a button that lands them where they are standing.
     */
    @Test
    fun `the entry failures do not offer to open a flow the reader is already in`() {
        for (token in listOf(
            SwarmErrorTokens.PAIRING_CODE_INVALID,
            SwarmErrorTokens.RELAY_UNKNOWN,
            SwarmErrorTokens.RELAY_ADDRESS_INVALID,
        )) {
            val routed = ErrorRouter.route(token)
            assertTrue("$token routes to a state with nothing to say", routed.message.isNotBlank())
            assertNotEquals("$token routes to Remedy.NONE", Remedy.NONE, routed.remedy)
            assertEquals(
                "$token offers to open the pairing flow the reader is standing in",
                false,
                routed.offersPairing,
            )
        }
    }
}
