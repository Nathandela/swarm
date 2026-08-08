package dev.swarm.phone.ui

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotEquals
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * FAILING-FIRST (TDD RED, GG-5) for PB-APP-3 (session detail) and PB-APP-4 (terminal peek and
 * take-control).
 *
 * TWO THINGS HERE ARE NOT OBVIOUS AND BOTH ARE RECORDED DECISIONS.
 *
 * 1. STOP IS NOT A VERB. There is no `interrupt` action anywhere in the signed set and the
 *    gateway's action map has no arm for one; the resolution recorded on 2026-07-25 is that an
 *    interrupt IS a keystroke -- Ctrl-C is byte 0x03 and a PTY in its default ISIG mode turns
 *    it into SIGINT for the foreground process group, which is exactly how a human stops a
 *    running agent. So Stop resolves to: acquire the lease if it is not held, then send 0x03,
 *    with `kill` remaining the escalation for a session that ignores SIGINT. Two consequences
 *    the screen must honour, and they are the two tests below: Stop REQUIRES the lease, so an
 *    observer is shown the take-control step rather than a button that silently does nothing;
 *    and 0x03 rides the LIVE-only path (ADR-007 D7), so an offline Stop resolves to "delivery
 *    unknown / not sent" and is NEVER queued -- a Stop that arrives ten minutes later, after
 *    the user gave up and did something else, is a genuine hazard.
 *
 * 2. THE GRID IS TEXT. ADR-007 D2 puts the VT emulator on the machine; the phone renders
 *    swarmmobile.Snapshot.Text verbatim. android/gate/s16_ui_test.go fences the absence of a
 *    second emulator; this file fences that the screen renders what it was handed.
 */
class SessionDetailTest {

    private fun detail(
        leaseHeld: Boolean = false,
        online: Boolean = true,
        journalStale: Boolean = false,
        stopNotSent: Boolean = false,
    ) = SessionDetail(
        sessionId = "m/sess-1",
        leaseHeld = leaseHeld,
        online = online,
        journalStale = journalStale,
        stopNotSent = stopNotSent,
    )

    /**
     * Stop is PERSISTENT -- on screen in every state, per PB-APP-3.
     *
     * BOTH THE SNAPSHOT AND THE JOURNAL HALVES OF THIS ASSERTION ARE DELETED, and each is deleted
     * for its own reason. The snapshot card: `docs/adr/ADR-009-structured-chat-interaction.md` (1)
     * leaves "no terminal emulation and no raw grid anywhere in the app" and (3) dates the well's
     * removal to slice I1's exit, so `snapshotText` and `hasSnapshotCard` are gone from the model.
     * The journal: IS-SS-1 splits the roster from the conversation ("a client renders the roster
     * from the latter and the transcript from the former"), so `SessionDetail` never carries the
     * conversation either -- it is [TranscriptPanel]'s now, handed to `SessionDetailScreen.of`
     * alongside this model rather than folded through it. `TranscriptPanelTest` and
     * `TranscriptViewTest` are where that coverage lives now. What is left here, and what this
     * asserts, is the one property `SessionDetail` alone still owns: Stop is on screen in EVERY
     * state.
     */
    @Test
    fun `stop is persistent regardless of what the session has done`() {
        assertTrue("Stop is PERSISTENT: it is on screen in every state", detail().stopVisible)
    }

    /**
     * Stop with no lease shows the take-control step first. A Stop button that silently did
     * nothing -- which is what an ungated one does, since PB-INPUT-2 refuses every keystroke
     * until the machine confirms a lease -- is the failure this asserts against.
     */
    @Test
    fun `stop without a lease offers take control rather than doing nothing`() {
        val action = detail(leaseHeld = false).stop()
        assertEquals(StopAction.ACQUIRE_LEASE_FIRST, action)
    }

    /** With the lease confirmed, Stop is the interrupt keystroke and it needs a confirmation. */
    @Test
    fun `stop with a confirmed lease sends the interrupt keystroke behind a confirmation`() {
        val d = detail(leaseHeld = true)
        assertEquals(StopAction.CONFIRM, d.stop())
        assertEquals(StopAction.SEND_INTERRUPT, d.confirmStop())
        assertEquals(
            "0x03 and nothing else: Ctrl-C through a PTY in ISIG mode",
            byteArrayOf(0x03).toList(),
            d.interruptBytes().toList(),
        )
    }

    /**
     * OFFLINE, and this is the hazard the resolution note calls out by name. Input is
     * live-only (ADR-007 D7); a Stop held for a reconnection arrives after the user has given
     * up and done something else, and interrupts whatever is running then.
     *
     * THE NOTICE CLAUSE IS REWRITTEN, and agents-tracker-4lta is why. It read:
     *
     *     assertTrue(
     *         "the user must be TOLD it did not reach the machine (PB-INPUT-1)",
     *         d.notSentNotice.isNotBlank(),
     *     )
     *
     * over a detail nobody had pressed Stop on -- because the notice was a pure function of
     * `!online`. PB-INPUT-1 is about telling the user what did not reach the machine, and NOTHING
     * had been sent to fail: the sentence "Stop did not reach your machine and was not held for
     * later" was on screen, in the past tense, reporting a failed Stop the user never pressed. The
     * requirement is unchanged and this asserts it in both directions instead of one -- silence
     * until a press resolves NOT_SENT, and the sentence once one has.
     */
    @Test
    fun `an offline stop resolves as not sent and is never queued`() {
        val d = detail(leaseHeld = true, online = false)
        assertEquals(StopAction.NOT_SENT, d.confirmStop())
        assertFalse("a queued Stop is ADR-007 D7's forbidden replay", d.stopQueued)
        assertTrue(
            "the screen reports a Stop that did not reach the machine before any Stop was " +
                "pressed, which is a failure the user is being told about in the past tense and " +
                "did not cause",
            d.notSentNotice.isBlank(),
        )
        assertTrue(
            "the user must be TOLD it did not reach the machine (PB-INPUT-1)",
            detail(leaseHeld = true, online = false, stopNotSent = true).notSentNotice.isNotBlank(),
        )
    }

    /** Kill is the escalation and is a DIFFERENT control from Stop, with its own confirmation. */
    @Test
    fun `kill is a separate escalation and is not what stop does`() {
        val d = detail(leaseHeld = true)
        assertNotEquals(d.stop(), StopAction.KILL)
        assertTrue(d.killRequiresConfirmation)
    }

    /** PB-APP-8 on this screen: a journal with a hole is not shown as a complete history. */
    @Test
    fun `a stale journal is marked on the detail screen`() {
        assertTrue(detail(journalStale = true).stale)
        assertFalse(detail(journalStale = false).stale)
    }
}

/**
 * PB-INPUT-2's three lease facts, and WHAT SURVIVED `TerminalPeekTest`.
 *
 * **This class was `TerminalPeekTest` and it is amended rather than deleted, because half of what
 * it asserted is untouched by the ruling that removed the other half.**
 *
 * DELETED WITH THE GRID: `the grid is the daemon-rendered text verbatim` (that the screen renders
 * `swarmmobile.Snapshot.Text` byte for byte, escape-looking input included) and `a stale grid is
 * banner-marked and the keyboard stays available` (PB-APP-8 over the snapshot). Both were about a
 * surface `docs/adr/ADR-009-structured-chat-interaction.md` (1)/(3) retires: there is no grid on any
 * screen, and (2) stops this app issuing a `terminal_watch` at all, so no snapshot arrives to be
 * fresh or stale. The verbatim-rendering property is not lost -- `android/gate/s16_ui_test.go` still
 * fences that no VT emulator exists on this side, and the machine-side choke point in
 * `internal/daemon/terminalrender.go` is explicitly unchanged by (2).
 *
 * KEPT WORD FOR WORD: the two lease assertions. ADR-009 (5) keeps the input substrate "exactly as
 * decided", PB-INPUT-2 is untouched, and the three properties moved intact to [SessionLease].
 */
class SessionLeaseTest {

    private fun lease(
        leaseHeld: Boolean = false,
        online: Boolean = true,
    ) = SessionLease(
        sessionId = "m/sess-1",
        leaseHeld = leaseHeld,
        online = online,
    )

    /** Take control, then release. Both are on screen; neither is implicit. */
    @Test
    fun `the lease is acquired and released explicitly`() {
        val idle = lease(leaseHeld = false)
        assertTrue(idle.showsTakeControl)
        assertFalse(idle.showsRelease)

        val held = lease(leaseHeld = true)
        assertFalse(held.showsTakeControl)
        assertTrue(held.showsRelease)
    }

    /**
     * The on-screen keyboard is enabled ONLY with a confirmed lease. Ungated, the user types
     * happily at a machine that granted them nothing and the gateway drops every frame
     * silently -- a live keyboard over a dead terminal, which is PB-INPUT-2's whole subject.
     */
    @Test
    fun `the keyboard is enabled only while the lease is held`() {
        assertFalse(lease(leaseHeld = false).keyboardEnabled)
        assertTrue(lease(leaseHeld = true).keyboardEnabled)
        assertFalse(
            "a lease cannot be live while the link is down",
            lease(leaseHeld = true, online = false).keyboardEnabled,
        )
    }
}

class ControlLeaseTest {

    private fun outcome(code: String) =
        OperationOutcome(operationId = "op-take-control-1", code = code, message = "")

    @Test
    fun `only the machine's grant confirms a lease`() {
        assertTrue("protocol.OpLease IS the take_control reply", ControlLease.confirmedBy(outcome("lease")))
    }

    /**
     * The unresolved case, and it is the one a literal got wrong in the opposite direction. An
     * operation the machine has not answered carries an empty code, and PB-SYNC-2's rule is that
     * an unresolved operation is neither a success nor a failure -- so the keyboard stays shut
     * rather than opening on a grant nobody sent.
     */
    @Test
    fun `an unanswered take control confirms nothing`() {
        assertFalse(ControlLease.confirmedBy(outcome("")))
    }

    /**
     * A severance shuts the gate again with no second fact to track. The detach notice is
     * "tagged with the take_control's operation id so ReplyCache.TakeFor can attribute it", so
     * the phone's durable outcome for that operation BECOMES the detach -- and a screen that had
     * remembered the press instead would still be showing a lease that died.
     */
    @Test
    fun `a severance or a refusal is not a lease`() {
        assertFalse("protocol.OpDetach: the lease ended", ControlLease.confirmedBy(outcome("detach")))
        assertFalse("the daemon refused the take_control", ControlLease.confirmedBy(outcome("not_authorized")))
        assertFalse("and a kill switch refusal is not a grant", ControlLease.confirmedBy(outcome("kill_switch")))
    }
}
