package dev.swarm.phone.ui.screens

import dev.swarm.phone.ui.ErrorState
import dev.swarm.phone.ui.OperationOutcome
import dev.swarm.phone.ui.kit.ComposerModel
import dev.swarm.phone.ui.kit.SendState
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * FAILING-FIRST (TDD RED, GG-5) for Wave R6 review ROUND 2's headline blocker: **no R6 verb's
 * machine answer ever reached the screen.** Bead agents-tracker-hggx.7, Mirror M2.4.
 *
 * ## The finding
 *
 * `VerbDispatch.press` settles on the result of the FACADE CALL, and `App.ComposerSend`
 * returns its `*Op` the instant the envelope is appended to the mailbox. So the composer's
 * `settle` fired on LOCAL SEALING: it set `SendState.SENT` and ran `typed.text.clear()` before
 * the machine had seen the message. The comment two lines above it -- "THE FIELD IS EMPTIED
 * ONLY ON THE MACHINE'S ACCEPTANCE" -- was false, and a REFUSED send was shown as sent with
 * the user's words erased.
 *
 * `Press.refused` could then only fire for facade-LOCAL errors, so the daemon's `stale_turn`
 * never routed: `ErrorState.STALE_TURN` comes from the facade token `swarm/stale-turn` and
 * NOTHING mapped the wire code onto it. `SendState.STALE_TURN` and
 * `ComposerModel.noticeFor("STALE_TURN")` were dead code with an exhaustive suite over them --
 * finding B8's own defect one layer in: a state with tests and no producer.
 *
 * ## What this suite pins
 *
 * The DECISION, as a value, because the surface cannot be unit-tested: the phone core is a
 * gomobile AAR that does not load on this JVM (`CommandVerdict`'s own KDoc records the same
 * constraint). `android/gate/r6_chat_ui_test.go` is the other half -- it fences that the
 * production surface latches each R6 operation id and claims it -- and this file is what the
 * claim then MEANS.
 */
class SessionDetailVerdictTest {

    private val op = "op-composer-1"

    private fun outcome(code: String, message: String = "the machine said so") =
        OperationOutcome(operationId = op, code = code, message = message)

    // ---- the composer --------------------------------------------------------

    @Test
    fun `a send is not sent until the machine says so`() {
        val verdict = SessionDetailScreen.composerVerdictFor(
            OperationOutcome(operationId = "", code = "", message = ""), op,
        )
        assertFalse(
            "the composer treated LOCAL SEALING as the machine's acceptance, so it said Sent " +
                "and erased the draft before anything had been delivered",
            verdict.answered,
        )
        assertNull(verdict.state)
        assertFalse(verdict.clearsDraft)
    }

    @Test
    fun `an accepted send is the one and only thing that empties the field`() {
        val verdict = SessionDetailScreen.composerVerdictFor(outcome("ok"), op)
        assertTrue(verdict.answered)
        assertEquals(SendState.SENT, verdict.state)
        assertTrue(verdict.clearsDraft)
        assertEquals("", verdict.notice)
    }

    @Test
    fun `the daemon's stale_turn routes to the gentle state and keeps the draft`() {
        val verdict = SessionDetailScreen.composerVerdictFor(outcome("stale_turn"), op)
        assertEquals(
            "the wire code never reached PB-APP-9's table, so the one refusal M2.4 wrote " +
                "gentle copy for arrived as an unrecognised fault",
            SendState.STALE_TURN,
            verdict.state,
        )
        assertEquals(ErrorState.STALE_TURN.name, verdict.refusal)
        assertEquals(ComposerModel.noticeFor(ErrorState.STALE_TURN.name).copy, verdict.notice)
        assertFalse(
            "the conversation moving on is ORDINARY, and a composer that ate the user's words " +
                "for it punishes them for the machine's answer",
            verdict.clearsDraft,
        )
    }

    @Test
    fun `every other machine refusal keeps the draft and never claims the message went`() {
        for (code in listOf("structured_unsupported", "invalid_field", "not_implemented", "policy")) {
            val verdict = SessionDetailScreen.composerVerdictFor(outcome(code), op)
            assertEquals(code, SendState.REFUSED, verdict.state)
            assertFalse(code, verdict.clearsDraft)
            assertTrue(code, verdict.notice.isNotEmpty())
            assertEquals(
                "the machine's own words belong in the detail cell, verbatim",
                "the machine said so", verdict.detail,
            )
        }
    }

    /**
     * W2.2's caller (phone-refit-playbook §3): an unmapped code with a sentence is still
     * REFUSED, keeps the draft, and its notice is the code's own sentence rather than the generic
     * composer copy; the refusal token carries the code so the panel can say the same sentence.
     * A code this build has never seen keeps the generic copy.
     */
    @Test
    fun `an unmapped code with a sentence says that sentence and keeps the draft`() {
        val verdict = SessionDetailScreen.composerVerdictFor(outcome("structured_unsupported"), op)
        assertEquals(SendState.REFUSED, verdict.state)
        assertEquals("Chat is off for this session.", verdict.notice)
        assertEquals("structured_unsupported", verdict.refusal)
        assertFalse(verdict.clearsDraft)
        assertEquals("the machine said so", verdict.detail)

        val unseen = SessionDetailScreen.composerVerdictFor(outcome("some_future_code"), op)
        assertEquals(ErrorState.UNKNOWN.name, unseen.refusal)
        assertEquals(ComposerModel.noticeFor(ErrorState.UNKNOWN.name).copy, unseen.notice)
    }

    @Test
    fun `an answer to somebody else's operation answers nothing here`() {
        val other = OperationOutcome(operationId = "op-someone-else", code = "ok", message = "")
        assertFalse(
            "PB-SYNC-2: an outcome attributed by proximity is the error operation ids exist " +
                "to prevent",
            SessionDetailScreen.composerVerdictFor(other, op).answered,
        )
    }

    // ---- the Stop --------------------------------------------------------------

    @Test
    fun `a refused interrupt says the turn was not stopped and shows the machine's reason`() {
        val verdict = dev.swarm.phone.ui.CommandVerdict.of(
            outcome("interrupt_unsupported", "agent \"opencode\" proves no semantic interrupt seam"),
            op,
            dev.swarm.phone.ui.CommandVerdict.ACCEPTED_OK,
        )
        val notice = SessionDetailScreen.interruptNoticeFor(verdict)
        assertTrue(
            "a Stop that the machine refused looked exactly like a Stop that worked: the " +
                "outcome line is cleared for both and the button comes back enabled either way",
            notice.isNotEmpty(),
        )
        assertEquals(
            "agent \"opencode\" proves no semantic interrupt seam",
            SessionDetailScreen.interruptDetailFor(verdict),
        )
    }

    // ---- the phone's own end of the history ------------------------------------

    @Test
    fun `the phone says why it stopped offering more history, rather than just stopping`() {
        val notice = SessionDetailScreen.historyCapacityNotice()
        assertTrue("the control vanished with nothing said", notice.isNotEmpty())
        assertTrue(
            "the sentence does not name the computer the rest is on when the screen knows it (W5.2)",
            SessionDetailScreen.historyCapacityNotice("MacBookPro").contains("on MacBookPro"),
        )
        assertTrue(
            "the reader must not be left believing they reached the beginning of a " +
                "conversation that goes further back: the sentence has to name where the rest is",
            notice.contains("computer"),
        )
    }

    // ---- the two M3 reads (round 3, finding F4) --------------------------------

    @Test
    fun `a refused history page says what the machine said and not a generic remedy`() {
        val verdict = dev.swarm.phone.ui.CommandVerdict.of(
            outcome("unavailable", "interaction history: this daemon has no journal wired"),
            op,
            "history",
        )
        val notice = SessionDetailScreen.historyReadNoticeFor(verdict)
        assertTrue(
            "a refused page reached the reader as ErrorState.UNKNOWN's \"Something failed in a " +
                "way the app does not recognise\", which is the app's shrug in place of the " +
                "daemon's own sentence",
            notice.isNotEmpty(),
        )
        assertTrue(
            "the sentence must say more could not be loaded, not name a verb or a category",
            notice.startsWith("Couldn't load more"),
        )
        assertEquals(
            "the machine's words were dropped by the detail-less ofRefusal overload",
            "interaction history: this daemon has no journal wired",
            SessionDetailScreen.historyReadDetailFor(verdict),
        )
    }

    @Test
    fun `an evicted body is named as gone and the offer is withdrawn`() {
        val code = dev.swarm.phone.ui.MachineRefusalCodes.UNAVAILABLE
        val verdict = dev.swarm.phone.ui.CommandVerdict.of(
            outcome(code, "interaction detail: no full body for item is retained (IS-CAP-3)"),
            op,
            "detail",
        )
        val notice = SessionDetailScreen.detailReadNoticeFor(code, verdict)
        assertTrue(
            "the tap on a clipped card whose body the machine has evicted answered \"Try again, " +
                "and report it if it keeps happening\" -- advice for a retry that can never work",
            notice.contains("all that's left"),
        )
        assertEquals(
            "interaction detail: no full body for item is retained (IS-CAP-3)",
            SessionDetailScreen.detailReadDetailFor(verdict),
        )
        assertTrue(
            "an unavailable body is settled: the offer must go, or the reader taps it forever",
            SessionDetailScreen.detailReadIsTerminal(code),
        )
        assertTrue(
            SessionDetailScreen.detailReadIsTerminal(
                dev.swarm.phone.ui.MachineRefusalCodes.INVALID_FIELD,
            ),
        )
        assertFalse(
            "a rate limit is exactly the refusal that tapping again fixes; withdrawing the offer " +
                "for it would take away the remedy",
            SessionDetailScreen.detailReadIsTerminal(
                dev.swarm.phone.ui.MachineRefusalCodes.RATE_LIMIT,
            ),
        )
    }

    @Test
    fun `a detail read the machine answered says nothing at all`() {
        val verdict = dev.swarm.phone.ui.CommandVerdict.of(outcome("detail", ""), op, "detail")
        assertEquals("", SessionDetailScreen.detailReadNoticeFor("detail", verdict))
        assertEquals("", SessionDetailScreen.detailReadDetailFor(verdict))
        assertEquals("", SessionDetailScreen.historyReadNoticeFor(verdict))
    }

    @Test
    fun `an interrupt the machine carried out says nothing`() {
        val verdict = dev.swarm.phone.ui.CommandVerdict.of(
            outcome("ok", ""), op, dev.swarm.phone.ui.CommandVerdict.ACCEPTED_OK,
        )
        assertEquals("", SessionDetailScreen.interruptNoticeFor(verdict))
        assertEquals("", SessionDetailScreen.interruptDetailFor(verdict))
    }
}
