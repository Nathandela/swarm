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
 * **NOTHING IS PHRASED HERE THAT THE WIRE DID NOT SAY.** `InboxRow.need` is documented as "the
 * journal record type verbatim, never an invented phrase", and `ActivityPanel`'s KDoc argues the
 * general case at length: a table turning a record type into English has to fail loudly on a value
 * it does not know, and a server that adds one would then take the screen down. So the question
 * line is the need VERBATIM -- the app's own statement of what the session is blocked on -- and
 * what O6.1 changes is where it sits and how big it is, which is exactly what the plan asks for.
 * The maquette's `Claude wants to push the release commit to main.` is a drawing of a wire this
 * product does not have yet; see the panel's KDoc for the gap, stated rather than filled in.
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

    @Test
    fun `the question is the need verbatim`() {
        val panel = ApprovalSheetScreen.of(row(need = "needs_input"), snapshot = "")
        assertEquals(
            "the question is the need the roster carries, VERBATIM. InboxRow.need is `the journal " +
                "record type verbatim, never an invented phrase`, and a sheet that reworded it " +
                "would be inventing the one sentence the user is deciding on.",
            "needs_input",
            panel?.question,
        )
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
