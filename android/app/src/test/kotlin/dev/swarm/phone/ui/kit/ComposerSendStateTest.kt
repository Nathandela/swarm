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

    @Test
    fun aFallbackSessionHasNoComposerNotADisabledOne() {
        assertEquals(
            "structured_chat=false means NO message sink exists; a greyed composer would promise " +
                "a verb the session structurally lacks (ADR-017)",
            ComposerAvailability.ABSENT,
            ComposerModel.availabilityFor(online = true, structuredChat = false),
        )
    }

    // ---- the visible send lifecycle ----------------------------------------

    @Test
    fun aSendWalksPendingSentAndShowsEachState() {
        assertTrue(ComposerModel.stateLabel(SendState.PENDING).isNotEmpty())
        assertTrue(ComposerModel.stateLabel(SendState.SENT).isNotEmpty())
        assertTrue(ComposerModel.stateLabel(SendState.REFUSED).isNotEmpty())
        assertNotEquals(
            "pending and sent must be tellable apart; 'delivery unknown' rendered as 'sent' is a lie",
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
        assertFalse(
            "the notice never claims the text was sent",
            stale.copy.lowercase().contains("sent"),
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
            "Add feedback...",
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
