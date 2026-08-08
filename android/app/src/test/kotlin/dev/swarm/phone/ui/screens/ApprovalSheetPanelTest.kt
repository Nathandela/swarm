package dev.swarm.phone.ui.screens

import dev.swarm.phone.ui.ApprovalDecision
import dev.swarm.phone.ui.ApprovalItem
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * What the pull-quote sheet SAYS, now that the wire says it.
 *
 * **THIS SUITE IS AMENDED, AND THE AMENDMENT IS THE SLICE.** Every assertion here used to be about a
 * roster row: the question was `InboxRow.need` -- a journal record TYPE, `needs_input` -- the well
 * was the daemon-rendered terminal snapshot, and `of` returned null for a session the roster had not
 * marked. Those were not preferences; they were the honest readings of a wire that carried no prose,
 * no literal and no approve verb, and `ApprovalSheetPanel.kt` recorded all three as refusals.
 * `interaction-schema.md` §3.5 carries the prose (`summary`), the literal (§7's `action`, or
 * IS-APR-3's `prompt_lines`) and the buttons (`decisions[]`), so each old assertion is replaced by
 * the fact that filled it rather than deleted.
 *
 * ONE REFUSAL SURVIVES AND IS ASSERTED HERE TOO: the sheet paints no polarity. IS-APR-4 keeps a
 * decision's verdict machine-side, so the labels arrive in the CLI's own words and in the CLI's own
 * order, and this suite pins the order for that reason -- a sheet that sorted them would be ranking
 * choices it cannot classify.
 */
class ApprovalSheetPanelTest {

    private fun row(
        id: String = "mbp-m1/swarm",
        project: String = "swarm",
        agent: String = "claude",
        need: String = "needs_input",
        group: String = "needs_input",
    ) = InboxRow(
        id = id,
        project = project,
        agent = agent,
        need = need,
        group = group,
        stateDescription = "needs you",
        selected = false,
        lit = group == "needs_input",
    )

    private fun item(
        sessionId: String = "mbp-m1/swarm",
        itemId: String = "01JQ0000000000000000000001",
        summary: String = "Claude wants to push the release commit to main.",
        command: String = "git push origin main",
        decisions: List<ApprovalDecision> = listOf(
            ApprovalDecision(id = "accept", label = "Allow"),
            ApprovalDecision(id = "cancel", label = "Deny"),
        ),
        promptCard: Boolean = false,
    ) = ApprovalItem(
        sessionId = sessionId,
        itemId = itemId,
        summary = summary,
        command = command,
        decisions = decisions,
        promptCard = promptCard,
    )

    @Test
    fun `the context line is the project and the agent the row already carries`() {
        val panel = ApprovalSheetScreen.of(item(), row())
        assertEquals(
            "the sheet's first line is who is asking, from the row the item interrupted. Nothing " +
                "is looked up and nothing is derived.",
            "swarm · claude",
            panel.contextLine,
        )
    }

    @Test
    fun `an absent agent leaves no separator behind`() {
        val panel = ApprovalSheetScreen.of(item(), row(agent = ""))
        assertEquals(
            "an EMPTY agent means the machine reported none, which InboxRow.agent states in as " +
                "many words -- so the line reads `swarm` and never `swarm · ` with a hanging " +
                "separator, and never `swarm · unknown`",
            "swarm",
            panel.contextLine,
        )
    }

    @Test
    fun `with no roster row the session's own id is the context line`() {
        val panel = ApprovalSheetScreen.of(item(sessionId = "mbp-m1/swarm"), row = null)
        assertEquals(
            "a pending approval outlives a reconnect and a process death (IS-LIFE-3) while a " +
                "roster is whatever the last read returned, so the two can legitimately disagree " +
                "-- and a card is still answerable when the list it is not on has not caught up",
            "mbp-m1/swarm",
            panel.contextLine,
        )
    }

    /**
     * OLD ASSERTION, QUOTED: this test used to be `the question is the need verbatim`, feeding
     * `need = "needs_input"` and asserting `panel?.question == "needs_input"` on the ground that
     * "InboxRow.need is `the journal record type verbatim, never an invented phrase`". That ceased
     * to be InboxRow.need's whole rule at agents-tracker-ksvb.2: `TriageInboxScreen.of` now maps
     * the seven known record types to a human phrase before this model ever sees the row. What the
     * old assertion was actually protecting -- that THIS model invents nothing OF ITS OWN -- does
     * not depend on which field the question is read from, so it is pinned over §3.5's `summary`
     * here, and the raw-token case moves to the test below.
     */
    @Test
    fun `the question is the item's summary and not the roster's record type`() {
        val panel = ApprovalSheetScreen.of(item(), row(need = "Waiting on you"))
        assertEquals(
            "§3.5's `summary` is `one line for the card headline`, written machine-side by the " +
                "adapter that captured the permission. The question used to be `needs_input` -- a " +
                "journal record TYPE -- because no sentence existed to render.",
            "Claude wants to push the release commit to main.",
            panel.question,
        )
    }

    /**
     * The other half of the old assertion, over the field the question is now read from. It fed
     * `need = "a_future_record_type"` -- a token this build's vocabulary does not know -- and
     * asserted it reached the sheet unmapped, because the only honest fallback was
     * `TriageInboxScreen`'s and this model must not add a second one. The sheet reads no `need` at
     * all now, so the same rule is pinned over `summary`: whatever sentence the machine wrote
     * arrives verbatim, and nothing here recognises, rewords or ranks it.
     */
    @Test
    fun `the machine's own sentence reaches the sheet verbatim`() {
        val panel = ApprovalSheetScreen.of(item(summary = "a_future_record_type"), row())
        assertEquals("a_future_record_type", panel.question)
    }

    @Test
    fun `the well is the action's literal and is absent when the item names none`() {
        val executing = ApprovalSheetScreen.of(item(), row())
        assertEquals("git push origin main", executing.command)
        assertTrue(executing.hasCommand)

        val nothingNamed = ApprovalSheetScreen.of(item(command = ""), row())
        assertFalse(
            "IS-TOOL-2 makes `other` the adapter's own `I could not classify this`, so an action " +
                "naming no literal draws NO WELL rather than a recessed box saying nothing",
            nothingNamed.hasCommand,
        )
    }

    @Test
    fun `the prompt card takes the same sheet, with the prompt region in the well`() {
        val fallback = ApprovalSheetScreen.of(
            item(promptCard = true, command = "Bash command\n  rm -rf build/\nAllow? (y/n)"),
            row(),
        )
        assertEquals(
            "ADR-009 (4): the fallback is `a card, never a grid`. The same panel, the same well " +
                "and the same buttons -- only where the literal came from differs.",
            "Bash command\n  rm -rf build/\nAllow? (y/n)",
            fallback.command,
        )
        assertTrue(fallback.hasCommand)
    }

    @Test
    fun `the buttons are the offered decisions, in the wire's own order`() {
        val panel = ApprovalSheetScreen.of(
            item(
                decisions = listOf(
                    ApprovalDecision(id = "accept", label = "Allow"),
                    ApprovalDecision(id = "acceptWithExecpolicyAmendment", label = "Allow always"),
                    ApprovalDecision(id = "cancel", label = "Deny"),
                ),
            ),
            row(),
        )
        assertEquals(
            "the labels are §3.5's `decisions[].label` and the order is the adapter's. This " +
                "panel used to carry no actions at all, because the facade exported no verb to " +
                "answer with.",
            listOf("Allow", "Allow always", "Deny"),
            panel.actions.map { it.label },
        )
        assertEquals(
            "the ids stay the CLI's OWN vocabulary -- Codex offers `accept` and `cancel`, Claude " +
                "Code a numbered dialog -- because `App.Approve` hands one back untouched",
            listOf("accept", "acceptWithExecpolicyAmendment", "cancel"),
            panel.actions.map { it.id },
        )
    }

    @Test
    fun `the panel carries what an answer has to name`() {
        val panel = ApprovalSheetScreen.of(
            item(sessionId = "mbp-m1/swarm", itemId = "01JQ0000000000000000000009"),
            row(),
        )
        assertEquals("mbp-m1/swarm", panel.sessionId)
        assertEquals(
            "IS-APR-1: the item's `item_id` IS D7's `interaction_id`, and `App.Approve` takes it " +
                "beside the session and the tapped decision's id",
            "01JQ0000000000000000000009",
            panel.itemId,
        )
    }
}
