package dev.swarm.phone.ui.screens

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * FAILING-FIRST (TDD RED, GG-5) for the obsidian migration plan's phase O6.1 -- what the
 * pull-quote sheet SAYS.
 *
 * PB-DS-9 assigns copy and arrangement to the screen, so the three lines are decided here and the
 * kit only paints them. Every one of them is read off a row the inbox already carries: the plan's
 * own clause is "no new information, reordered hierarchy", and a sheet that computed a fourth fact
 * would be a new screen wearing a reordering's clothes.
 *
 * **THIS MODEL PHRASES NOTHING ITSELF.** `InboxRow.need` USED TO BE documented as "the journal
 * record type verbatim, never an invented phrase", and this class's own KDoc read the question
 * line as that value, VERBATIM. agents-tracker-ksvb.2 gave the inbox row's need line a human
 * vocabulary for the seven record types -- `TriageInboxScreen.of` maps `launched` to `Started`,
 * `group_transition` to a phrase read off the row's own Group, and so on -- and `question` still
 * reads `need` unchanged, which is now sometimes that phrase. What survives is the narrower claim:
 * this model invents NOTHING OF ITS OWN. It is `InboxRow.need`, whatever that already says, and an
 * unrecognised token still reaches this screen verbatim because the mapping's own fallback is the
 * wire's word, never a guess (`NeedVocabularyTest` covers the mapping itself; this file covers only
 * that the sheet does not touch it a second time). What O6.1 changes is where the line sits and how
 * big it is, which is exactly what the plan asks for. The maquette's `Claude wants to push the
 * release commit to main.` is a drawing of a wire this product does not have yet; see the panel's
 * KDoc for the gap, stated rather than filled in.
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

    @Test
    fun `the context line is the project and the agent the row already carries`() {
        val panel = ApprovalSheetScreen.of(row(), snapshot = "")
        assertEquals(
            "the sheet's first line is who is asking, from the row the user tapped. Nothing is " +
                "looked up and nothing is derived.",
            "swarm · claude",
            panel?.contextLine,
        )
    }

    @Test
    fun `an absent agent leaves no separator behind`() {
        val panel = ApprovalSheetScreen.of(row(agent = ""), snapshot = "")
        assertEquals(
            "an EMPTY agent means the machine reported none, which InboxRow.agent states in as " +
                "many words -- so the line reads `swarm` and never `swarm · ` with a hanging " +
                "separator, and never `swarm · unknown`",
            "swarm",
            panel?.contextLine,
        )
    }

    /**
     * OLD ASSERTION, QUOTED: this test used to be `the question is the need verbatim`, feeding
     * `need = "needs_input"` and asserting `panel?.question == "needs_input"` on the ground that
     * "InboxRow.need is `the journal record type verbatim, never an invented phrase`". That ceased
     * to be InboxRow.need's whole rule at agents-tracker-ksvb.2: `TriageInboxScreen.of` now maps
     * the seven known record types to a human phrase before this model ever sees the row. What the
     * old assertion was actually protecting -- that THIS model invents nothing OF ITS OWN -- does
     * not depend on whether `need` happens to be a raw wire token or a mapped phrase, so it is
     * pinned here with a mapped-looking value instead, and the raw-token case moves to the test
     * below.
     */
    @Test
    fun `the question is InboxRow need, unchanged -- whatever that already says`() {
        val panel = ApprovalSheetScreen.of(row(need = "Waiting on you"), snapshot = "")
        assertEquals(
            "the question is InboxRow.need, unchanged. The mapping from record type to phrase " +
                "already happened upstream (TriageInboxScreen.of); a sheet that reworded it again " +
                "would be inventing a second phrase for the one sentence the user is deciding on.",
            "Waiting on you",
            panel?.question,
        )
    }

    /**
     * The other half of the old assertion: a token this build's vocabulary does not know still
     * reaches the sheet exactly as the wire sent it, because the fallback that produces that is
     * `TriageInboxScreen`'s, and this model must not add a second one.
     */
    @Test
    fun `an unmapped need still reaches the sheet verbatim`() {
        val panel = ApprovalSheetScreen.of(row(need = "a_future_record_type"), snapshot = "")
        assertEquals("a_future_record_type", panel?.question)
    }

    @Test
    fun `the well is the machine's own screen and is absent when there is none`() {
        val withSnapshot = ApprovalSheetScreen.of(row(), snapshot = "$ git push origin main")
        assertEquals("$ git push origin main", withSnapshot?.command)
        assertTrue(withSnapshot!!.hasCommand)

        val without = ApprovalSheetScreen.of(row(), snapshot = "")
        assertFalse(
            "a session this phone has never watched has no snapshot yet, and an empty well is a " +
                "recessed box saying nothing -- SessionDetailPanel.hasSnapshot draws no card at " +
                "all for the same reason",
            without!!.hasCommand,
        )
    }

    @Test
    fun `only a blocked session gets a sheet`() {
        assertNull(
            "the approval sheet is the moment of DECISION. A session that is working, ready for " +
                "review or done is not asking anything, and a sheet over one would be a question " +
                "with no answer that changes anything.",
            ApprovalSheetScreen.of(row(group = "working"), snapshot = "x"),
        )
        assertNull(ApprovalSheetScreen.of(row(group = "completed"), snapshot = "x"))
        assertNull(ApprovalSheetScreen.of(row(group = "ready_for_review"), snapshot = "x"))
    }
}
