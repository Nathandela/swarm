package dev.swarm.phone.ui.kit

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotEquals
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * FAILING-FIRST (TDD RED, GG-5) for Mirror M2.4/M2.5 -- the composer's MODEL: availability,
 * the visible per-send lifecycle, the gentle stale_turn refusal, the status-driven
 * placeholder, and paste discipline (bead agents-tracker-hggx.7; compile-RED on the frozen
 * symbols).
 *
 * THE LIFECYCLE IS VISIBLE OR IT IS A LIE. ADR-009 (6): "the send item carries a visible
 * pending -> sent -> refused state ... A send that cannot get [through] is shown refused, not
 * silently swallowed." PB-INPUT-2 keeps the confirmation and removes only the ceremony -- and
 * R3's ruling (M2.4) removes the lease from the UX entirely: the composer gates on ONLINE
 * only, and a fallback session has no composer AT ALL (ADR-017: no message sink, so absence
 * is structural, not a disabled state pretending otherwise).
 */
class ComposerSendStateTest {

    // ---- availability: online only, structural absence ----------------------

    @Test
    fun theComposerGatesOnOnlineOnly() {
        assertEquals(ComposerAvailability.AVAILABLE, ComposerModel.availabilityFor(online = true, structuredChat = true))
        assertEquals(ComposerAvailability.OFFLINE, ComposerModel.availabilityFor(online = false, structuredChat = true))
    }

    // MOVED, not deleted: the subject survives and its state gained a name. ABSENT covered
    // four situations with one sentence between them (see ComposerShutReasonTest); the arm
    // this test has always been about -- a machine that reports no chat surface -- is NO_CHAT.
    @Test
    fun aFallbackSessionHasNoComposerNotADisabledOne() {
        assertEquals(
            "structured_chat=false means NO message sink exists; a greyed composer would promise " +
                "a verb the session structurally lacks (ADR-017)",
            ComposerAvailability.NO_CHAT,
            ComposerModel.availabilityFor(online = true, structuredChat = false),
        )
    }

    // ---- the visible send lifecycle ----------------------------------------

    // AMENDED TWICE, and both amendments are owner rulings rather than a test bent to green.
    //
    // (1) R6, the conversation-surface drawing. It used to assert `stateLabel(SENT).isNotEmpty()`,
    //     pinning the word "Sent" -- under a message, in this same method, calling "delivery
    //     unknown rendered as sent" a lie. What produces SENT is the daemon's OK for
    //     `composer_send`: bytes written into a PTY, not delivery.
    // (2) The composer's-voice ruling, this wave. It then asserted
    //     `stateLabel(REFUSED).isNotEmpty()` under "a refusal keeps its words". The refusal DOES
    //     keep its words -- in `noticeFor`, which is the only place that can also say why. The
    //     panel draws the label and the notice together, so a state with words in both said the
    //     same thing twice in two wordings.
    //
    // What survives both is the property this method was written for: every state is tellable
    // apart. It is asserted below, and more strictly than before.
    @Test
    fun onlyPendingWearsALabelAndTheRestAreExplainedInstead() {
        assertEquals(
            "`sending` NAMES the state and explains nothing, which is why it is the one label " +
                "that survives -- and it is lower case because the copy sheet's `bubble.pending` " +
                "row is",
            "sending",
            ComposerModel.stateLabel(SendState.PENDING),
        )
        assertEquals(
            "the settled state has NO label. `SENT` is the daemon's OK for composer_send -- " +
                "bytes into a PTY, not delivery -- and on the keystroke path the CLI " +
                "acknowledges nothing at all, so any word here is a claim the wire cannot back",
            "",
            ComposerModel.stateLabel(SendState.SENT),
        )
        for (refusal in listOf(SendState.REFUSED, SendState.STALE_TURN)) {
            assertEquals(
                "$refusal is EXPLAINED by noticeFor and must not also be LABELLED here: the " +
                    "panel draws both, so `Not sent` above `Not sent - the terminal's input " +
                    "line was not empty.` is the one fact said twice in two wordings",
                "",
                ComposerModel.stateLabel(refusal),
            )
        }
        // And the words are not lost. noticeFor is keyed on the ErrorState name rather than on
        // the SendState, so this reads it the way the panel does -- and its `else` arm means
        // EVERY refusal reaches a sentence, which is what "never silently swallowed" requires
        // now that the label is gone.
        for (code in listOf("STALE_TURN", "INPUT_BUSY", "NOT_AUTHORIZED")) {
            assertTrue(
                "a send that cannot get through is still shown refused -- it says so once, in " +
                    "the one place that can also say why. $code reached no sentence",
                ComposerModel.noticeFor(code).copy.isNotEmpty(),
            )
        }
        assertNotEquals(
            "pending and settled must still be tellable apart -- and they are, by the label " +
                "going away and by the bubble's own surface (derivation row 26)",
            ComposerModel.stateLabel(SendState.PENDING),
            ComposerModel.stateLabel(SendState.SENT),
        )
    }

    // ---- stale_turn, gently -------------------------------------------------

    @Test
    fun staleTurnGetsItsOwnGentleNotice() {
        val stale = ComposerModel.noticeFor("STALE_TURN")
        val generic = ComposerModel.noticeFor("NOT_AUTHORIZED")
        assertTrue(stale.copy.isNotEmpty())
        assertNotEquals(
            "stale_turn has its own copy: the conversation moved on, which is ordinary, not an error",
            stale.copy, generic.copy,
        )
        // AMENDED with the copy itself. This used to read `assertFalse(stale.copy.lowercase()
        // .contains("sent"))` under the message below -- a substring guard that any NEGATION
        // defeats, and the drawing's tabled sentence is a negation: `Not sent - ...`. The
        // property it was protecting survives and is asserted more strongly here, because
        // "says it was not sent" cannot be met by a copy that claims delivery, while "does not
        // contain the letters s-e-n-t" could be met by one that says "delivered".
        assertTrue(
            "the notice never claims the text was sent -- it says the opposite, in the words " +
                "the copy sheet records",
            stale.copy.startsWith("Not sent"),
        )
    }

    // ---- the refusal Slice 0 exists to produce -------------------------------

    @Test
    fun inputBusyGetsTheSentenceTheShimEarned() {
        val busy = ComposerModel.noticeFor("INPUT_BUSY")
        assertEquals(
            "the shim refused rather than joining the reader's words to somebody's half-typed " +
                "line, having written nothing. The copy names the one fact it actually knows",
            "Not sent. Finish typing on your computer first.",
            busy.copy,
        )
        assertNotEquals(
            "before this arm existed it fell to `else` and read as the generic refusal, which " +
                "is indistinguishable from a dropped link, an expired session or a bug -- the " +
                "whole of Slice 0 arriving on screen saying nothing particular",
            ComposerModel.noticeFor("NOT_AUTHORIZED").copy,
            busy.copy,
        )
        assertTrue(
            "a refusal that eats the user's words punishes them for the machine's answer",
            busy.retainsDraft,
        )
    }

    @Test
    fun staleTurnKeepsTheDraft() {
        assertTrue(
            "gentle means the user's words survive the refusal: the draft is retained for " +
                "re-send against the refreshed turn",
            ComposerModel.noticeFor("STALE_TURN").retainsDraft,
        )
    }

    // ---- the status-driven placeholder (M2.5) -------------------------------

    @Test
    fun thePlaceholderFollowsTheSessionsStatus() {
        assertEquals("Message", ComposerModel.placeholderFor(working = false))
        assertEquals(
            "a working agent still accepts input -- as feedback into the running turn, and the " +
                "placeholder says so",
            "Add a note while it works",
            ComposerModel.placeholderFor(working = true),
        )
    }

    // ---- paste + IME discipline (M2.4) --------------------------------------

    @Test
    fun aMultilinePasteStaysOneDraftAndNeverAutoSubmits() {
        val paste = ComposerModel.acceptPaste("line one\nline two\nline three")
        assertEquals("line one\nline two\nline three", paste.draft)
        assertFalse(
            "a newline in pasted text is content, not a submit; auto-submitting a paste sends a " +
                "message nobody finished writing (the r3p submit-boundary rule, phone-side)",
            paste.submits,
        )
    }
}
