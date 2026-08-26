package dev.swarm.phone.ui

import dev.swarm.phone.ui.screens.SessionCapabilityFacts
import dev.swarm.phone.ui.screens.SessionDetailScreen
import dev.swarm.phone.ui.screens.TranscriptScreen
import org.junit.Assert.assertEquals
import org.junit.Test

/**
 * FAILING-FIRST (TDD RED, GG-5) for agents-tracker-4lta: **a Stop that cannot be sent must not be
 * put behind a question.**
 *
 * THE DEAD END THIS IS ABOUT, in the order a user meets it. `SessionDetail.stop()` reads the LEASE
 * and nothing else, so a phone whose link is down answers CONFIRM; the surface asks "Interrupt what
 * this session is doing? This sends Ctrl-C, the same key you would press at the terminal"; the user
 * answers it; `confirmStop()` then reads the link and answers NOT_SENT; and the surface's plan has
 * no arm for that value, so the press resolves to null. Nothing is sent, nothing is written, and
 * the screen is pixel-identical to the one the question was asked over. A confirmation is a promise
 * that answering it does something, and this one is answered by a no-op.
 *
 * WHAT THE FIX IS NOT. It is not a disabled Stop: PB-APP-3 makes the control PERSISTENT
 * (`SessionDetail.stopVisible` is a `val`, not a condition) for the same reason the observer gets a
 * different WORDING rather than a dead button -- a control that vanishes or greys out tells the
 * user nothing about why. What changes is that the press resolves IMMEDIATELY to the outcome the
 * link decides, with no question in front of it, and that outcome is said out loud.
 *
 * THE SEND PATH IS UNTOUCHED AND IS ASSERTED BELOW SO IT STAYS THAT WAY. `confirmStop()` still
 * answers NOT_SENT while offline -- ADR-007 D7 has no queue for input and this screen must not
 * invent one -- and a held lease on a live link still asks before interrupting a running agent.
 *
 * WHAT THIS FILE CANNOT SEE is the surface's own arm for the value: no JVM test can reach
 * `PhoneStartup.Ready` (the phone core is a gomobile AAR of .so files cross-compiled for Android
 * ABIs), so that the press SAYS something rather than resolving to null is fenced in
 * android/gate/4lta_offlinestop_test.go, against checked-in source.
 */
class SessionStopOfflineTest {

    private fun detail(
        leaseHeld: Boolean = true,
        online: Boolean = true,
    ) = SessionDetail(
        sessionId = "mbp/api",
        online = online,
        journalStale = false,
    )

    @Test
    fun `an offline Stop resolves on the press rather than behind a question it cannot honour`() {
        assertEquals(
            "an offline Stop still resolves to CONFIRM, so the screen asks \"Interrupt what this " +
                "session is doing?\" over a link that cannot carry the interrupt -- and the " +
                "answer resolves to NOT_SENT with nothing sent and nothing written. A " +
                "confirmation is a promise that answering it does something",
            StopAction.NOT_SENT,
            detail(online = false).stop(),
        )
    }

    // DELETED (owner ruling R1): "the lease is still the first question, offline or not".
    // It asserted that the lease clause ran BEFORE the link clause, so an observer was shown
    // take-control's remedy whatever the link was doing. Both halves are gone: turn_interrupt
    // takes no lease, so there was no first question and no remedy to offer. The link is now
    // the only question, which the rest of this file already asserts.


    @Test
    fun `a Stop that can be sent is still asked about first`() {
        // The confirmation belongs to the press that INTERRUPTS A RUNNING AGENT and nothing here
        // removes it: it names what will actually happen rather than asking "are you sure".
        assertEquals(StopAction.CONFIRM, detail(online = true).stop())
        assertEquals(StopAction.SEND_INTERRUPT, detail(online = true).confirmStop())
    }

    @Test
    fun `an offline Stop is still never queued`() {
        // ADR-007 D7: input is live-only. A Stop held for a reconnection arrives after the user
        // gave up and did something else, and interrupts whatever is running then.
        assertEquals(StopAction.NOT_SENT, detail(online = false).confirmStop())
    }

    @Test
    fun `the screen the surface reads carries the same refusal, so no dialog is built for it`() {
        // `PhoneSurface.stopQuestion()` asks the panel and offers the confirmation only for
        // CONFIRM. The panel is where that gate reads its answer, so the fix has to arrive here or
        // the dialog is built anyway.
        val d = detail(online = false)
        val offline = SessionDetailScreen.of(
            d,
            TranscriptScreen.of(emptyList()),
            SessionLease(sessionId = d.sessionId, online = d.online),
            capabilities = SessionCapabilityFacts(structuredChat = true),
        )

        assertEquals(StopAction.NOT_SENT, offline.stopAction)
    }
}
