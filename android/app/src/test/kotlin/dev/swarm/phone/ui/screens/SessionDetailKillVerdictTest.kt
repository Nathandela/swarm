package dev.swarm.phone.ui.screens

import dev.swarm.phone.ui.CommandVerdict
import dev.swarm.phone.ui.OperationOutcome
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * FAILING-FIRST (TDD RED, GG-5) for agents-tracker-qlf9's first verb: what the session detail says
 * when the machine refuses a kill.
 *
 * **WHY A REFUSED KILL IS THE WORST OF THE THREE TO LOSE.** The control is behind a confirmation
 * that states the consequence -- "the agent stops and the session is gone; this cannot be undone"
 * -- so a user who answers it has decided something irreversible is about to happen. What they see
 * on a refusal is a cleared outcome line and the session still in the inbox, which is
 * indistinguishable from a kill that succeeded one redraw before the roster caught up. The next
 * thing that user does is press it again, or walk away believing the agent is dead.
 *
 * **THE COPY IS THE SCREEN'S.** `SessionDetailScreen` already owns the two control labels and both
 * confirmations, and `sessionDetailView` already draws PB-APP-9's routed line immediately above
 * Stop and Kill -- "a notice goes above what it qualifies ... a refusal drawn at the top of a
 * scrolling transcript is a report the person who pressed the button is no longer looking at". The
 * sentence for a refused kill belongs in the same place as the question that preceded it.
 */
class SessionDetailKillVerdictTest {

    private val id = "op-kill-1"

    private fun verdict(code: String, message: String = "") = CommandVerdict.of(
        OperationOutcome(operationId = id, code = code, message = message),
        id,
        CommandVerdict.ACCEPTED_OK,
    )

    @Test
    fun `a kill nobody has answered says nothing at all`() {
        assertEquals(
            "an unanswered kill produced a sentence, so a command still crossing to the machine " +
                "reads as one that came back",
            "",
            SessionDetailScreen.killNoticeFor(CommandVerdict.UNANSWERED),
        )
        assertEquals("", SessionDetailScreen.killNoticeFor(verdict(code = "")))
    }

    @Test
    fun `a kill the machine carried out says nothing either`() {
        assertEquals(
            "an accepted kill announced itself. The session leaving the roster IS the " +
                "confirmation, and a sentence nobody specified is copy invented at this seam",
            "",
            SessionDetailScreen.killNoticeFor(verdict("ok", "ok")),
        )
    }

    /**
     * FAILING-FIRST (TDD RED, GG-5) for agents-tracker-ksvb.10. The machine's reason is still
     * carried and it is no longer INSIDE the sentence: `killDetailFor` is the second cell, drawn
     * mono and tertiary under prose that is neither.
     *
     * A kill switch ends when the owner flips it, a not_authorized ends when they re-grant the
     * device, and a policy refusal ends never -- so the words are still the only thing that tells
     * the three apart, and losing them would be the defect qlf9 closed reopening.
     */
    @Test
    fun `a refused kill carries the machine's reason as a detail, not inside the sentence`() {
        val refused = verdict("kill_switch", "remote control is disabled (kill switch off)")
        val notice = SessionDetailScreen.killNoticeFor(refused)

        assertTrue(
            "a refused kill said nothing, so the session stays in the inbox and the screen looks " +
                "exactly like one where the kill worked",
            notice.isNotEmpty(),
        )
        assertFalse(
            "the daemon's own error string is spliced into the middle of the screen's sentence, " +
                "so `kill switch off` reads as copy this product wrote about a session",
            notice.contains("remote control is disabled (kill switch off)"),
        )
        assertEquals(
            "the machine's reason reaches no cell at all, and the user cannot tell a kill switch " +
                "from a revoked device from a policy without it",
            "remote control is disabled (kill switch off)",
            SessionDetailScreen.killDetailFor(refused),
        )
        assertTrue(
            "the sentence does not say the session is still running, which is the fact the user " +
                "acted on",
            notice.contains("Couldn't end"),
        )
    }

    /**
     * The detail is a DETAIL: it exists only where there is a refusal to explain, and a screen
     * that has nothing to say says nothing in both cells rather than one.
     */
    @Test
    fun `a kill nobody refused has no detail either`() {
        assertEquals("", SessionDetailScreen.killDetailFor(CommandVerdict.UNANSWERED))
        assertEquals("", SessionDetailScreen.killDetailFor(verdict("ok", "ok")))
        assertEquals("", SessionDetailScreen.killDetailFor(verdict("kill_switch")))
    }

    @Test
    fun `a refusal the machine sent no words with still says the session survived`() {
        val notice = SessionDetailScreen.killNoticeFor(verdict("kill_switch"))

        assertTrue(notice.contains("Couldn't end"))
        assertTrue("a wordless refusal produced a dangling sentence", notice.endsWith("."))
    }

    @Test
    fun `a rate limited kill is the one worth pressing again`() {
        val notice = SessionDetailScreen.killNoticeFor(verdict("rate_limit", "too many requests"))

        assertTrue(
            "a refusal that waiting fixes reads the same as one that nothing fixes",
            notice.endsWith(CommandVerdict.RETRY_HINT),
        )
    }
}
