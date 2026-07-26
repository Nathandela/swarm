package dev.swarm.phone.ui

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * FAILING-FIRST (TDD RED, GG-5) for PB-APP-2: the triage inbox.
 *
 * "The four Groups as sections with one-line need summaries. UI test covering all four Groups
 * and the empty state."
 *
 * WHY THE SCREEN MODEL IS A PURE FUNCTION AND THE TEST DRIVES THAT, not an Activity. It is
 * the shape PermissionStateResolver already established in this module, and its reason holds
 * here too: the interesting behaviour is a mapping from what the phone core knows onto what
 * the user sees, and hiding it behind a view hierarchy makes the mapping untestable while
 * proving nothing about the view. What must NOT happen is the model becoming a second source
 * of truth -- the Group strings below are VERBATIM from the wire (swarmmobile.Session.Group,
 * which is internal/status.Group's string form) and the model may never derive one on-device.
 * android/gate/s16_ui_test.go fences the production wiring back to swarmmobile.App.
 *
 * NOTHING HERE MODELS HARDWARE. PB-E2E-5 stays deferred.
 */
class TriageInboxTest {

    private fun session(id: String, group: String, need: String = "", present: Boolean = true) =
        SessionRow(id = id, title = id.substringAfter('/'), group = group, need = need, present = present)

    /**
     * All four Groups, each its own section, in a fixed order.
     *
     * The ORDER is part of the requirement rather than decoration: this is a triage screen, and
     * the group a user must act on has to be the one they see without scrolling. needs_input is
     * the agent blocked ON THEM; working is the one group that needs nothing (push deliberately
     * ignores it -- internal/remotegw/push.go isWakeWorthy) and belongs last.
     */
    @Test
    fun `every group is its own section in triage order`() {
        val inbox = TriageInbox.from(
            listOf(
                session("m/one", "working"),
                session("m/two", "needs_input", need = "approval_request"),
                session("m/three", "completed"),
                session("m/four", "ready_for_review"),
            ),
            journalStale = false,
        )

        assertEquals(
            listOf("needs_input", "ready_for_review", "completed", "working"),
            inbox.sections.map { it.group },
        )
        assertEquals(4, inbox.sections.size)
        inbox.sections.forEach { assertEquals(1, it.rows.size) }
    }

    /**
     * A Group with no sessions is still a section, and it says it is empty.
     *
     * Dropping empty sections is the obvious implementation and it is wrong for a triage
     * screen: the sections then move under the user as sessions change group, and "nothing is
     * waiting on me" -- the single most useful fact this screen can report -- becomes
     * indistinguishable from "this section scrolled away".
     */
    @Test
    fun `an empty group is still a section and says so`() {
        val inbox = TriageInbox.from(listOf(session("m/one", "working")), journalStale = false)

        val needsInput = inbox.sections.first { it.group == "needs_input" }
        assertTrue(needsInput.rows.isEmpty())
        assertTrue(needsInput.isEmpty)
        assertFalse(inbox.isEmpty)
    }

    /** The whole-screen empty state, which is a first launch after pairing. */
    @Test
    fun `an inbox with no sessions at all reports the empty state`() {
        val inbox = TriageInbox.from(emptyList(), journalStale = false)
        assertTrue(inbox.isEmpty)
        assertEquals(4, inbox.sections.size)
        assertTrue(inbox.sections.all { it.isEmpty })
    }

    /**
     * The one-line need summary is the journal record type VERBATIM (swarmmobile.Session.Need),
     * never a phrase the phone invented.
     */
    @Test
    fun `the need summary is one line and comes from the wire`() {
        val inbox = TriageInbox.from(
            listOf(session("m/two", "needs_input", need = "approval_request")),
            journalStale = false,
        )
        val row = inbox.sections.first { it.group == "needs_input" }.rows.single()
        assertEquals("approval_request", row.need)
        assertFalse(row.need.contains('\n'))
    }

    /**
     * PB-APP-8 at the screen PB-APP-2 owns: the roster is rendered from the JOURNAL stream, so
     * an inbox drawn while that stream has an unrepaired hole may be missing a session, an exit
     * or a needs_input. It must not be presented as live.
     *
     * IT IS HALF OF WHAT MAKES swarmmobile.SessionList.Stale REACH A USER, and it used to claim
     * to be all of it. This test drives `journalStale`, which is a PARAMETER: it proves the
     * model does the right thing with the fact, and nothing about whether the fact arrives. It
     * did not -- `SessionList.Stale` was called by no Kotlin at all, and FacadeBridge filled the
     * parameter from a separate `App.StreamState` read. The other half is FacadeBridge.triageInbox
     * reading the handle, fenced by android/gate/boundverbledger_test.go.
     */
    @Test
    fun `a roster built over a stale journal is never presented as live`() {
        val live = TriageInbox.from(listOf(session("m/one", "working")), journalStale = false)
        val stale = TriageInbox.from(listOf(session("m/one", "working")), journalStale = true)

        assertFalse(live.stale)
        assertTrue(stale.stale)
        assertTrue(
            "a stale inbox must carry a user-legible notice, not merely a boolean",
            stale.staleNotice.isNotBlank(),
        )
        assertTrue(live.staleNotice.isBlank())
    }

    /**
     * Presence is the machine's reachability and is NOT staleness. Collapsing them tells a user
     * whose machine is simply asleep that their view is untrustworthy, and a user whose view
     * really is holed that everything is fine as long as the machine answers.
     */
    @Test
    fun `an absent session is marked absent and not stale`() {
        val inbox = TriageInbox.from(
            listOf(session("m/one", "working", present = false)),
            journalStale = false,
        )
        val row = inbox.sections.first { it.group == "working" }.rows.single()
        assertFalse(row.present)
        assertFalse(inbox.stale)
    }
}
