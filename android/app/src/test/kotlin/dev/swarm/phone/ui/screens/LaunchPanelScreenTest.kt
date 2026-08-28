package dev.swarm.phone.ui.screens

import dev.swarm.phone.ui.LaunchDraft
import dev.swarm.phone.ui.LaunchRendering
import dev.swarm.phone.ui.LaunchResult
import dev.swarm.phone.ui.LaunchScreen
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * FAILING-FIRST (TDD RED, GG-5) for PB-DS-9 over the LAUNCH form's model.
 *
 * THE BRANCH THIS SUITE EXISTS FOR is the one that appends a sentence. `LaunchScreen` distinguishes
 * a refusal waiting fixes (`REFUSED_TRANSIENTLY`, retryable) from one it does not
 * (`REJECTED_BY_POLICY`, `REFUSED`), and the screen's job is to say so -- "This one is worth
 * trying again shortly" is the difference between a user who waits a minute and one who edits a
 * launch spec that was never the problem. That branch lived in a private `when` inside
 * `PhoneSurface` and nothing could reach it.
 *
 * THE REQUIRED-FIELD ASSERTION IS A CROSS-CHECK, NOT A SECOND BAR. `LaunchScreen.missingField` is
 * the authority on what the daemon refuses a launch without; this suite asserts the panel AGREES
 * with it rather than restating it, because a form that asked for less than the model requires
 * would send a launch the machine refuses after burning a durable command seq and a signature.
 */
class LaunchPanelScreenTest {

    private fun rendering(
        result: LaunchResult,
        reason: String = "",
        retryable: Boolean = false,
    ) = LaunchRendering(result = result, reason = reason, retryable = retryable)

    // ---- the form ----------------------------------------------------------

    @Test
    fun `the three fields LaunchDraft carries are asked, in order`() {
        assertEquals(
            listOf(LaunchFieldId.AGENT, LaunchFieldId.CWD, LaunchFieldId.PROMPT),
            LaunchPanelScreen.of().fields.map { it.id },
        )
    }

    @Test
    fun `every field carries the words that say what belongs in it`() {
        LaunchPanelScreen.of().fields.forEach { field ->
            assertTrue(
                "${field.id} has no hint, and the hint is this surface's only label -- a field " +
                    "without one is a box a user cannot identify",
                field.hint.isNotBlank(),
            )
        }
        assertEquals(
            listOf(
                "Which agent to start",
                "Working directory on your computer",
                "First message for the agent, if any",
            ),
            LaunchPanelScreen.of().fields.map { it.hint },
        )
    }

    @Test
    fun `the panel requires exactly what the model refuses a launch without`() {
        val required = LaunchPanelScreen.of().fields.filter { it.required }.map { it.id }.toSet()
        val model = LaunchScreen()

        // The model's own bar, asked field by field: a draft complete but for one field is
        // refused iff that field is required.
        assertNotNull(
            "the model accepts a draft with no agent, so the panel's `required` is stricter than " +
                "the bar the daemon actually enforces",
            model.missingField(LaunchDraft(agent = "", cwd = "/tmp", prompt = "hello")),
        )
        assertNotNull(
            model.missingField(LaunchDraft(agent = "claude", cwd = "", prompt = "hello")),
        )
        assertNull(
            "the model refuses a draft with no prompt, so the panel marking it optional would " +
                "let a user submit a launch the model will not send",
            model.missingField(LaunchDraft(agent = "claude", cwd = "/tmp", prompt = "")),
        )

        assertEquals(setOf(LaunchFieldId.AGENT, LaunchFieldId.CWD), required)
    }

    @Test
    fun `the prompt is optional and is still a field`() {
        val prompt = LaunchPanelScreen.of().fields.single { it.id == LaunchFieldId.PROMPT }

        assertFalse(prompt.required)
        // LaunchDraft models three fields; passing "" for the third would be a literal standing
        // in for something nobody was asked.
        assertTrue(prompt.hint.isNotBlank())
    }

    @Test
    fun `the submit control says what it does`() {
        assertEquals("Launch a session", LaunchPanelScreen.of().submit)
    }

    // ---- the machine's answer ----------------------------------------------

    @Test
    fun `a form that has launched nothing says nothing`() {
        assertEquals(
            "the form reported a status about an operation that does not exist",
            "",
            LaunchPanelScreen.of(rendering = null).notice,
        )
    }

    @Test
    fun `an unresolved launch is reported as unresolved, never as either outcome`() {
        assertEquals(
            "Waiting for your computer to answer the launch.",
            LaunchPanelScreen.of(rendering(LaunchResult.PENDING)).notice,
        )
    }

    @Test
    fun `an accepted launch says the machine started it`() {
        assertEquals(
            "Your machine started the session.",
            LaunchPanelScreen.of(rendering(LaunchResult.LAUNCHED, reason = "ok")).notice,
        )
    }

    /**
     * FAILING-FIRST (TDD RED, GG-5) for agents-tracker-ksvb.10. THIS FORM HAD NO SENTENCE OF ITS
     * OWN: `noticeFor` returned `rendering.reason` and nothing else, so the whole notice under a
     * refused launch was whatever string the daemon happened to send -- `kill_switch: remote
     * control is disabled`, verbatim, in the form's own body type.
     *
     * The head is the screen's now, and it says what is TRUE OF THE SESSION rather than naming the
     * verb, which is [SessionDetailScreen]'s rule for the same shape one screen over. The machine's
     * words are unchanged and are the detail beside it.
     */
    @Test
    fun `a refusal says the form's own sentence, with the machine's words as the detail`() {
        // The user's next step depends on WHICH refusal it was. A kill-switch refusal told to the
        // user as "against policy" sends them to change a spec that was fine -- so the words are
        // demoted rather than dropped.
        val panel = LaunchPanelScreen.of(
            rendering(
                LaunchResult.REFUSED,
                reason = "Remote control is switched off at your machine.",
            ),
        )

        assertEquals(
            "the machine's own string is the whole notice, so a daemon error reads as the form's " +
                "own copy about a launch",
            "Your machine did not start the session.",
            panel.notice,
        )
        assertEquals(
            "the machine's reason reaches no cell, and it is the only thing that says which " +
                "refusal this was",
            "Remote control is switched off at your machine.",
            panel.noticeDetail,
        )
    }

    @Test
    fun `a refusal worth retrying says so, and one that is not stays silent about it`() {
        val transient = LaunchPanelScreen.of(
            rendering(
                LaunchResult.REFUSED_TRANSIENTLY,
                reason = "Too many launches just now.",
                retryable = true,
            ),
        ).notice
        val settled = LaunchPanelScreen.of(
            rendering(
                LaunchResult.REJECTED_BY_POLICY,
                reason = "That agent is not permitted here.",
                retryable = false,
            ),
        ).notice

        assertEquals(
            "Your machine did not start the session. This one is worth trying again shortly.",
            transient,
        )
        assertEquals(
            "a considered refusal is advertised as retryable, which sends the user to press the " +
                "same button against the same answer",
            "Your machine did not start the session.",
            settled,
        )
    }

    @Test
    fun `retryability is the model's distinction and is not inferred from the result`() {
        // The pairing of result and `retryable` is LaunchScreen's; a panel that decided it from
        // the result alone would be a second, silent copy of that mapping.
        val odd = LaunchPanelScreen.of(
            rendering(LaunchResult.REFUSED, reason = "Try later.", retryable = true),
        )

        assertEquals(
            "Your machine did not start the session. This one is worth trying again shortly.",
            odd.notice,
        )
        // THE RETRY HINT IS THE HEAD'S AND NOT THE DETAIL'S. It is copy this product wrote about
        // what to do next; the detail is what the machine said.
        assertEquals("Try later.", odd.noticeDetail)
    }

    /**
     * The three states that are not refusals carry no detail, which is what makes it a detail: a
     * mono line under "your machine started the session" is a diagnostic about nothing.
     */
    @Test
    fun `only a refusal has a detail`() {
        assertEquals("", LaunchPanelScreen.of().noticeDetail)
        assertEquals("", LaunchPanelScreen.of(rendering(LaunchResult.PENDING)).noticeDetail)
        assertEquals("", LaunchPanelScreen.of(rendering(LaunchResult.LAUNCHED, reason = "ok")).noticeDetail)
    }
}
