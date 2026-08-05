package dev.swarm.phone.ui.screens

import dev.swarm.phone.ui.CommandVerdict
import dev.swarm.phone.ui.OperationOutcome
import org.junit.Assert.assertEquals
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

    @Test
    fun `a refused kill names the machine's own reason`() {
        val notice = SessionDetailScreen.killNoticeFor(
            verdict("kill_switch", "remote control is disabled (kill switch off)"),
        )

        assertTrue(
            "a refused kill said nothing, so the session stays in the inbox and the screen looks " +
                "exactly like one where the kill worked",
            notice.isNotEmpty(),
        )
        assertTrue(
            "the machine's reason was dropped. A kill switch ends when the owner flips it, a " +
                "not_authorized ends when they re-grant the device, and a policy refusal ends " +
                "never -- the user cannot tell which without the words the machine sent",
            notice.contains("remote control is disabled (kill switch off)"),
        )
        assertTrue(
            "the sentence does not say the session is still running, which is the fact the user " +
                "acted on",
            notice.contains("did not end"),
        )
    }

    @Test
    fun `a refusal the machine sent no words with still says the session survived`() {
        val notice = SessionDetailScreen.killNoticeFor(verdict("kill_switch"))

        assertTrue(notice.contains("did not end"))
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
