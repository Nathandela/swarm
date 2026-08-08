package dev.swarm.phone.ui.screens

import dev.swarm.phone.ui.InteractionItem
import dev.swarm.phone.ui.SessionDetail
import dev.swarm.phone.ui.SessionLease
import dev.swarm.phone.ui.StopAction
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotEquals
import org.junit.Assert.assertSame
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * FAILING-FIRST (TDD RED, GG-5) for PB-APP-3's session detail -- inventory C2's SCREEN MODEL.
 *
 * WHY THERE IS A MODEL BESIDE [SessionDetail]. That one answers what the session IS and what may be
 * done to it: whether a lease is held, and the three-way outcome of pressing Stop. Those semantics
 * were settled on 2026-07-25 and this file does not relitigate them -- Stop is a KEYSTROKE (0x03
 * through a PTY in ISIG mode) and not a new signed action, it REQUIRES the lease, and it rides the
 * live-only path so an offline Stop is "not sent" and never queued. This answers what the SCREEN
 * says about all of that: the title, what each control reads as, what the confirmation asks, and
 * what the screen says when the machine did not receive something. Every one of those is copy or
 * arrangement, and PB-DS-9 assigns both to the screen.
 *
 * IT IS A PURE FUNCTION OVER [SessionDetail], [TranscriptPanel] AND [SessionLease] NOW, and that
 * three-way split is `docs/adr/ADR-009-structured-chat-interaction.md` landing on this test file
 * rather than a refactor made here. `SessionDetail` used to carry the journal AND the daemon-
 * rendered grid alongside the lease; IS-SS-1 splits the conversation from the roster ("a client
 * renders the roster from the latter and the transcript from the former"), and (1)/(2)/(3) delete
 * the grid and the terminal_watch that filled it. What is left on `SessionDetail` is the lease, the
 * link and the Stop/Kill facts; the conversation is [TranscriptPanel]'s, built by [TranscriptScreen]
 * off the session's interaction items and handed to [SessionDetailScreen.of] as its own parameter.
 *
 * WHAT MOVED RATHER THAN DIED. The transcript-content assertions this file used to carry --
 * ordering, and what an empty conversation says -- are `TranscriptPanelTest`'s and
 * `TranscriptViewTest`'s now, over the model and the view that actually own that surface. The
 * snapshot-content assertions (a grid present or absent, its own stale mark) have no new home:
 * ADR-009 (3) deletes the terminal well outright, so there is no grid anywhere in the app for an
 * assertion about one to be about.
 */
class SessionDetailPanelTest {

    private fun detail(
        leaseHeld: Boolean = true,
        online: Boolean = true,
        journalStale: Boolean = false,
        stopNotSent: Boolean = false,
    ) = SessionDetail(
        sessionId = "mbp/api",
        leaseHeld = leaseHeld,
        online = online,
        journalStale = journalStale,
        stopNotSent = stopNotSent,
    )

    /**
     * [detail] and the [SessionLease] a screen reads alongside it, KEPT IN THE SAME STATE they are
     * in production: `PhoneSurface.detailPanel` derives both `SessionDetail.leaseHeld` and
     * `SessionLease.leaseHeld` from the one verdict its own take_control earned, so a fixture that
     * let them disagree would test a combination the surface never produces.
     */
    private fun panelOf(detail: SessionDetail = detail()) = SessionDetailScreen.of(
        detail,
        TranscriptScreen.of(emptyList()),
        SessionLease(sessionId = detail.sessionId, leaseHeld = detail.leaseHeld, online = detail.online),
    )

    // ---- what the screen is about -----------------------------------------

    @Test
    fun `the header names the session the user drilled into`() {
        val panel = panelOf()

        assertEquals(
            "the drill-down header does not name the session, so a user who opened it from a " +
                "list of four cannot tell which one they are looking at",
            "mbp/api",
            panel.title,
        )
        assertTrue(
            "the header offers no way back to the list it was opened from",
            panel.back.isNotEmpty(),
        )
    }

    // ---- the controls, and what they say ----------------------------------

    @Test
    fun `Stop offers the take-control step when the lease is not held`() {
        val observing = panelOf(detail(leaseHeld = false))

        // PB-INPUT-3: input is refused without a confirmed lease, so a Stop button that silently
        // did nothing is the failure the recorded decision names. The screen shows the step that
        // would make it work instead.
        assertEquals(StopAction.ACQUIRE_LEASE_FIRST, observing.stopAction)
        assertNotEquals(
            "an observer is offered the same Stop wording as a controller, so pressing it will " +
                "be refused by the machine with nothing on screen having warned them",
            panelOf(detail(leaseHeld = true)).stopLabel,
            observing.stopLabel,
        )
    }

    @Test
    fun `Kill is never one tap away`() {
        val panel = panelOf()

        assertTrue(
            "Kill ends the session outright and is offered without a confirmation step",
            panel.killConfirmation.isNotEmpty(),
        )
    }

    /**
     * THE PRESS IS PART OF THIS TEST NOW, and agents-tracker-4lta is why. The middle assertion read:
     *
     *     assertTrue(
     *         "the screen does not tell the user their Stop never reached the machine",
     *         panel.notSentNotice.isNotEmpty(),
     *     )
     *
     * over `detail(online = false)` -- a session nobody had pressed Stop on. The notice was a pure
     * function of connectivity, so a phone that merely lost its link reported, in the past tense,
     * a Stop that was never pressed and therefore never failed. What PB-INPUT-1 asks for is that
     * the user is told what did not reach the machine; a report of a loss that did not happen is
     * not that, and it is the same defect the not-sent line's own view test was drawing.
     */
    @Test
    fun `an offline Stop says it was not sent and does not promise a retry`() {
        val panel = panelOf(detail(online = false))

        assertEquals(StopAction.NOT_SENT, panel.confirmedStopAction)
        assertEquals(
            "the screen reports a Stop that never reached the machine to a user who has not " +
                "pressed Stop -- a failure in the past tense that they did not cause",
            "",
            panel.notSentNotice,
        )

        val pressed = panelOf(detail(online = false, stopNotSent = true))
        assertTrue(
            "the screen does not tell the user their Stop never reached the machine",
            pressed.notSentNotice.isNotEmpty(),
        )
        // ADR-007 D7: input is live-only and NEVER queued. Copy that said "will be sent when you
        // reconnect" would be a promise the transport cannot keep.
        assertTrue(
            "the not-sent notice implies the Stop is queued and will be delivered later, which " +
                "is a promise ADR-007 D7 forbids this product from making",
            !pressed.notSentNotice.lowercase().contains("will be sent when"),
        )
    }

    @Test
    fun `a holed journal is never presented as a complete history`() {
        val whole = panelOf(detail(journalStale = false))
        val holed = panelOf(detail(journalStale = true))

        assertEquals(
            "a journal with an unrepaired gap is drawn as a plain list, so the user reads it as " +
                "everything that happened",
            "",
            whole.staleNotice,
        )
        assertTrue(holed.staleNotice.isNotEmpty())
    }

    /**
     * The conversation crosses this model UNTOUCHED, which is the one thing left to assert about it
     * here.
     *
     * WHY IT IS IDENTITY AND NOT CONTENT. The transcript-content assertions moved to
     * `TranscriptPanelTest` (see this file's KDoc), and re-asserting ordering or empty copy here
     * would be a second reading of one fold -- the copy §2's reuse rule exists to prevent. What is
     * genuinely this model's is that it PASSES THE PANEL THROUGH: a screen that rebuilt, filtered or
     * re-sorted the blocks on the way past would be a second transcript able to disagree with the
     * one its own test proved right, and `assertSame` is the only assertion that catches that.
     */
    @Test
    fun `the conversation the screen is handed is the conversation it carries`() {
        val chat = TranscriptScreen.of(
            listOf(InteractionItem(itemId = "i-1", cursor = 1, kind = "agent_message", text = "on it")),
        )

        assertSame(
            "the session detail rebuilt the transcript it was handed, so the conversation on this " +
                "screen is a second fold of the same items -- and only one of the two is the one " +
                "TranscriptPanelTest proved right",
            chat,
            SessionDetailScreen.of(
                detail(),
                chat,
                SessionLease(sessionId = "mbp/api", leaseHeld = true, online = true),
            ).transcript,
        )
    }

    // ---- PB-INPUT-2, RE-HOMED FROM THE DELETED PEEK ------------------------
    //
    // BOTH TESTS BELOW ARE `PeekPanelScreenTest`'S, WORD FOR WORD WHERE THE MODEL ALLOWS. That suite
    // is deleted with the terminal peek (ADR-009 (3)), and these two assertions are not: PB-INPUT-2
    // is untouched by the ADR -- (5) keeps the input substrate "exactly as decided" -- and the peek
    // carried the lease copy only because the peek was where the keyboard was. This is the screen a
    // session is read on now, so the sentence and the button are here, and so is their coverage.
    //
    // The third test of that group, `the keyboard follows both of the model's clauses, not just the
    // lease`, is NOT here: `keyboardEnabled` stayed on the model rather than moving to this screen,
    // and `SessionLeaseTest` in `ui/SessionScreensTest.kt` asserts it over both clauses.

    @Test
    fun `take control is offered exactly while the machine has not confirmed one`() {
        assertTrue(panelOf(detail(leaseHeld = false)).offersTakeControl)
        assertFalse(
            "the control to take a lease is offered over a lease the machine already granted",
            panelOf(detail(leaseHeld = true)).offersTakeControl,
        )
    }

    @Test
    fun `the two lease sentences go with the two lease states and not the other way round`() {
        val held = panelOf(detail(leaseHeld = true)).leaseNotice
        val notHeld = panelOf(detail(leaseHeld = false)).leaseNotice

        assertEquals(SessionDetailScreen.leaseNoticeFor(confirmed = true), held)
        assertEquals(SessionDetailScreen.leaseNoticeFor(confirmed = false), notHeld)
        assertTrue(
            "the two states read the same, which is the invisible suppression PB-INPUT-2 exists " +
                "to prevent: the user cannot tell until a keystroke vanishes",
            held != notHeld,
        )
        assertTrue(
            "the confirmed sentence does not say what it confirms",
            held.contains("confirmed you have control"),
        )
        assertTrue(
            "the unconfirmed sentence does not say what to do about it, which leaves a shut " +
                "keyboard with no reason beside it",
            notHeld.contains("Take control first"),
        )
    }
}
