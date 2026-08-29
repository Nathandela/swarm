package dev.swarm.phone.ui.screens

import dev.swarm.phone.ui.ApprovalDecision
import dev.swarm.phone.ui.ApprovalItem
import dev.swarm.phone.ui.CommandVerdict
import dev.swarm.phone.ui.OperationOutcome
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

    // ---- agents-tracker-dwwv.2.4: what the sheet says once the machine has answered a tap ----
    //
    // mirror-program.md M1.2 changed what `App.Approve`'s `ok` means: APPLIED, not RESOLVED.
    // Resolution arrives later, by observation, as an `approval_resolved` item TranscriptScreen
    // already renders -- so the one thing this sheet still has to say for itself is a REFUSAL,
    // in the calm, honest register mirror-m1.md's own M1.2 section describes: "no approval ...
    // is pending ... Already resolved, expired, superseded, or never existed. All four are the
    // same fact from the phone's side."
    //
    // THE VERDICT IS BUILT THROUGH THE REAL FACTORY (the qx9m lesson): every case below goes
    // through `CommandVerdict.of`, never a hand-assembled `CommandVerdict(...)`, so a change to
    // that table is exercised here exactly as `PhoneSurface` would exercise it.

    private fun verdictFor(code: String, message: String = "") = CommandVerdict.of(
        OperationOutcome(operationId = "op-approve-1", code = code, message = message),
        "op-approve-1",
        accepted = CommandVerdict.ACCEPTED_OK,
    )

    @Test
    fun `an approval nobody has answered yet says nothing`() {
        assertEquals("", ApprovalSheetScreen.refusalNoticeFor(CommandVerdict.UNANSWERED))
        assertEquals("", ApprovalSheetScreen.refusalDetailFor(CommandVerdict.UNANSWERED))
    }

    /**
     * SILENT ON ACCEPTED, `SessionDetailScreen.killNoticeFor`'s own rule: `ok` is APPLIED and not
     * RESOLVED, so there is nothing true to confirm yet. The `approval_resolved` item arriving is
     * the confirmation -- a sentence invented here to fill the gap before it lands would claim
     * something this phone is not yet in a position to know.
     */
    @Test
    fun `an applied approval says nothing -- the resolution is the transcript's, not this sheet's`() {
        val applied = verdictFor(code = CommandVerdict.ACCEPTED_OK)

        assertTrue("an applied tap is not a refusal", applied.accepted)
        assertEquals("", ApprovalSheetScreen.refusalNoticeFor(applied))
        assertEquals("", ApprovalSheetScreen.refusalDetailFor(applied))
    }

    /**
     * `stale_approval` covers four causes at once (already resolved, expired, superseded, never
     * existed) plus M1.2's two new ones (`already_applied`, `no_dialog`) -- and every one of them
     * is the same fact from the phone's side, which is why one calm sentence covers all of them
     * rather than a table this side would have to keep in sync with the daemon's.
     *
     * WHAT THE SENTENCE MAY CLAIM changed with the 2026-08-13 review (mirror-m1.md M1.8). The
     * assertion this replaces read, verbatim: `"the sheet's own sentence names no error and no
     * verb -- a card the daemon refused to type into is, from here, simply one that was already
     * answered", "This approval was already answered."`. That was true of the case it was
     * written against and FALSE of three others the very same head string covers -- `no_dialog`,
     * `unmappable_decision`, and the code-less `not_applicable`. The one that matters is
     * `no_dialog`: the recognizer anchors on claude 2.1.231's recorded title and label strings,
     * nothing checks the installed version at runtime, so the day claude auto-updates off that
     * version every tap refuses, the CLI stays blocked -- and the phone told the owner it had
     * been answered. The head now says what is true of ALL five, and `refusalDetailFor` still
     * carries which one it was.
     */
    @Test
    fun `a stale card reads calmly, and claims only that the machine did not apply it`() {
        val refused = verdictFor(
            code = "stale_approval",
            message = "approval \"i-1\" has already been answered from a phone; the machine is " +
                "waiting for its dialog to close",
        )

        assertTrue(refused.refused)
        assertEquals(
            "the sheet's own sentence names no error and no verb, and asserts no CAUSE -- one " +
                "head string covers five refusal reasons, so it may only claim what is true of " +
                "every one of them: the answer was not applied",
            "Couldn't send your answer. Try again.",
            ApprovalSheetScreen.refusalNoticeFor(refused),
        )
        assertEquals(
            "the machine's own words are dropped rather than demoted to the detail cell every " +
                "other refusal on this app carries them in (agents-tracker-ksvb.10's idiom)",
            "approval \"i-1\" has already been answered from a phone; the machine is waiting for " +
                "its dialog to close",
            ApprovalSheetScreen.refusalDetailFor(refused),
        )
    }

    /**
     * THE VERSION-SKEW CASE, which is the one the old head string lied about most expensively:
     * the daemon looked at the live grid and did not recognize a dialog it can answer. Nothing
     * about it was "already answered" -- the CLI is still blocked, and the owner's next move
     * depends on being told that.
     */
    @Test
    fun `a dialog the machine could not recognize does not read as already answered`() {
        val refused = verdictFor(
            code = "stale_approval",
            message = "approval \"i-1\" was not applied: the session's screen does not show the " +
                "permission dialog this request raised",
        )

        assertEquals(
            "the machine looked at the screen and did not know it; saying the approval was " +
                "already answered sends the owner away from a CLI that is still waiting",
            "Couldn't send your answer. Try again.",
            ApprovalSheetScreen.refusalNoticeFor(refused),
        )
        assertFalse(
            "no head string over a no_dialog refusal may assert the request was answered",
            ApprovalSheetScreen.refusalNoticeFor(refused).contains("already"),
        )
        assertEquals(
            "approval \"i-1\" was not applied: the session's screen does not show the permission " +
                "dialog this request raised",
            ApprovalSheetScreen.refusalDetailFor(refused),
        )
    }

    /** `invalid_field` (an offered decision the tuple did not name) reads the same calm way. */
    @Test
    fun `an invalid decision reads the same calm way, in the machine's own words`() {
        val refused = verdictFor(code = "invalid_field", message = "decision \"cancel\" was not offered")

        assertEquals("Couldn't send your answer. Try again.", ApprovalSheetScreen.refusalNoticeFor(refused))
        assertEquals("decision \"cancel\" was not offered", ApprovalSheetScreen.refusalDetailFor(refused))
    }

    @Test
    fun `a refused approval is never offered as worth retrying`() {
        val refused = verdictFor(code = "stale_approval", message = "no approval is pending")

        assertFalse(
            "waiting does not make a stale card answerable again, so the sentence must not " +
                "suggest trying again",
            ApprovalSheetScreen.refusalNoticeFor(refused).contains(CommandVerdict.RETRY_HINT.trim()),
        )
    }
}
