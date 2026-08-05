package dev.swarm.phone.ui.screens

import dev.swarm.phone.ui.JournalRow
import dev.swarm.phone.ui.SessionDetail
import dev.swarm.phone.ui.StopAction
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotEquals
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * FAILING-FIRST (TDD RED, GG-5) for PB-APP-3's session detail -- inventory C2's SCREEN MODEL.
 *
 * WHY THERE IS A MODEL BESIDE [SessionDetail]. That one answers what the session IS and what may be
 * done to it: the journal, the snapshot, whether a lease is held, and the three-way outcome of
 * pressing Stop. Those semantics were settled on 2026-07-25 and this file does not relitigate them
 * -- Stop is a KEYSTROKE (0x03 through a PTY in ISIG mode) and not a new signed action, it REQUIRES
 * the lease, and it rides the live-only path so an offline Stop is "not sent" and never queued.
 * This answers what the SCREEN says about all of that: the title, the heading over the transcript,
 * what each control reads as, what the confirmation asks, and what the screen says when the machine
 * did not receive something. Every one of those is copy or arrangement, and PB-DS-9 assigns both to
 * the screen.
 *
 * IT IS A PURE FUNCTION OVER [SessionDetail], which is the shape this package already uses
 * ([ActivityPanelScreen], [MachinesPanelScreen], [TriageInboxScreen]). No Android import, so the
 * interesting half is checkable without a device.
 *
 * THE TRANSCRIPT REUSES [ActivityEntry] AND THAT IS §2'S RULE, not a shortcut. `activityRow`'s own
 * KDoc says it is "the activity feed's only structural element, and the machine pane's audit log --
 * one factory for both, which is why it takes a body and an optional emphasis rather than a
 * JournalRow". A per-session journal is the third caller of exactly that shape, and a second entry
 * type carrying the same two fields would be the copy the reuse rule exists to prevent.
 */
class SessionDetailPanelTest {

    private fun record(cursor: Long, type: String, group: String = "") =
        JournalRow(cursor = cursor, sessionId = "mbp/api", type = type, group = group)

    private fun detail(
        journal: List<JournalRow> = listOf(record(1, "launched")),
        snapshotText: String = "$ git push",
        leaseHeld: Boolean = true,
        online: Boolean = true,
        journalStale: Boolean = false,
        stopNotSent: Boolean = false,
        snapshotStale: Boolean = false,
    ) = SessionDetail(
        sessionId = "mbp/api",
        journal = journal,
        snapshotText = snapshotText,
        leaseHeld = leaseHeld,
        online = online,
        journalStale = journalStale,
        stopNotSent = stopNotSent,
        snapshotStale = snapshotStale,
    )

    // ---- what the screen is about -----------------------------------------

    @Test
    fun `the header names the session the user drilled into`() {
        val panel = SessionDetailScreen.of(detail())

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

    @Test
    fun `the transcript is the session's own journal, newest first, verbatim`() {
        val panel = SessionDetailScreen.of(
            detail(
                journal = listOf(
                    record(1, "launched"),
                    record(2, "group_transition", group = "needs_input"),
                ),
            ),
        )

        // The record's own words. `TriageInbox` states the rule for the need line and it holds
        // here for the stronger reason: a table turning wire tokens into English would have to
        // fail on a value it did not know, and a server that added a record type would take this
        // screen down.
        assertEquals(
            listOf("group_transition · needs_input", "launched"),
            panel.transcript.rows.map { it.body },
        )
    }

    @Test
    fun `a session with no records says so rather than showing an empty area`() {
        val panel = SessionDetailScreen.of(detail(journal = emptyList()))

        assertTrue(
            "an empty transcript renders nothing at all, which is indistinguishable from a feed " +
                "that failed to load -- PB-DS-9's rule is that an empty section is still a section",
            panel.transcript.emptyCopy.isNotEmpty(),
        )
        assertTrue(
            "the empty copy claims nothing happened, which is a claim about the MACHINE that a " +
                "phone holding no records is in no position to make",
            !panel.transcript.emptyCopy.lowercase().contains("nothing has happened"),
        )
    }

    // ---- the controls, and what they say ----------------------------------

    @Test
    fun `Stop offers the take-control step when the lease is not held`() {
        val observing = SessionDetailScreen.of(detail(leaseHeld = false))

        // PB-INPUT-3: input is refused without a confirmed lease, so a Stop button that silently
        // did nothing is the failure the recorded decision names. The screen shows the step that
        // would make it work instead.
        assertEquals(StopAction.ACQUIRE_LEASE_FIRST, observing.stopAction)
        assertNotEquals(
            "an observer is offered the same Stop wording as a controller, so pressing it will " +
                "be refused by the machine with nothing on screen having warned them",
            SessionDetailScreen.of(detail(leaseHeld = true)).stopLabel,
            observing.stopLabel,
        )
    }

    @Test
    fun `Kill is never one tap away`() {
        val panel = SessionDetailScreen.of(detail())

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
        val panel = SessionDetailScreen.of(detail(online = false))

        assertEquals(StopAction.NOT_SENT, panel.confirmedStopAction)
        assertEquals(
            "the screen reports a Stop that never reached the machine to a user who has not " +
                "pressed Stop -- a failure in the past tense that they did not cause",
            "",
            panel.notSentNotice,
        )

        val pressed = SessionDetailScreen.of(detail(online = false, stopNotSent = true))
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
        val whole = SessionDetailScreen.of(detail(journalStale = false))
        val holed = SessionDetailScreen.of(detail(journalStale = true))

        assertEquals(
            "a journal with an unrepaired gap is drawn as a plain list, so the user reads it as " +
                "everything that happened",
            "",
            whole.staleNotice,
        )
        assertTrue(holed.staleNotice.isNotEmpty())
    }

    /**
     * FAILING-FIRST for agents-tracker-0qe7's second half: **the snapshot's staleness is the
     * snapshot's, and this screen only carried the journal's.**
     *
     * [SessionDetailPanel.staleNotice] is PB-APP-8 over the CHRONOLOGY -- "some records are
     * missing ... this is not a complete log of the session" -- and it is the only stale mark on
     * the screen. The card above it prints a grid the machine may have stopped sending frames for,
     * and a terminal is the one surface a user reads AS live: a snapshot with no mark is taken for
     * what the session is doing now. The two facts are independent (a repaired journal beside a
     * frozen grid is an ordinary state) and they have different remedies, so one sentence cannot
     * stand for both.
     */
    @Test
    fun `the snapshot carries its own stale mark and not the journal's verdict`() {
        val fresh = SessionDetailScreen.of(detail(snapshotStale = false, journalStale = false))
        val frozen = SessionDetailScreen.of(detail(snapshotStale = true, journalStale = false))

        assertEquals(
            "a snapshot mark is drawn over a grid the machine is still sending frames for",
            "",
            fresh.snapshotStaleNotice,
        )
        assertTrue(
            "the snapshot card has no stale mark of its own, so a grid the phone knows is out of " +
                "date is read as what the session is doing now",
            frozen.snapshotStaleNotice.isNotEmpty(),
        )
        assertEquals(
            "the snapshot's mark is the JOURNAL's verdict, which is a different fact with a " +
                "different remedy: an unrepaired event stream says nothing about whether the last " +
                "frame is current",
            "",
            frozen.staleNotice,
        )
        assertNotEquals(
            SessionDetailScreen.of(detail(journalStale = true)).staleNotice,
            frozen.snapshotStaleNotice,
        )
    }

    @Test
    fun `the snapshot card is absent when no frame has arrived, not blank`() {
        val quiet = SessionDetailScreen.of(detail(snapshotText = ""))
        val printing = SessionDetailScreen.of(detail(snapshotText = "$ git push"))

        assertTrue(
            "a session the machine has sent no frame for still draws a snapshot card, which is " +
                "an empty terminal presented as the session's screen",
            !quiet.hasSnapshot,
        )
        assertTrue(printing.hasSnapshot)
        assertEquals("$ git push", printing.snapshot)
    }
}
