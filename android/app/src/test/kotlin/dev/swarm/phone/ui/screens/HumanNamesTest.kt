package dev.swarm.phone.ui.screens

import dev.swarm.phone.ui.ApprovalDecision
import dev.swarm.phone.ui.ApprovalItem
import dev.swarm.phone.ui.JournalPageView
import dev.swarm.phone.ui.ClockBanner
import dev.swarm.phone.ui.JournalRow
import dev.swarm.phone.ui.MachineFreshness
import dev.swarm.phone.ui.MachineLabel
import dev.swarm.phone.ui.SessionDetail
import dev.swarm.phone.ui.SessionLease
import dev.swarm.phone.ui.SessionRow
import dev.swarm.phone.ui.TriageInbox
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Test

/**
 * agents-tracker-ksvb.1's SCREEN half: the render sites that used to put an identifier in front of
 * a person, and the two that deliberately still do.
 *
 * **ONE RENDER SITE RETIRED, AND ONLY BECAUSE IT NO LONGER EXISTS.** The terminal peek's nav header
 * read `api refactor · 80x24` -- the session's name with the grid's shape after it -- and two tests
 * here pinned it in both directions. `docs/adr/ADR-009-structured-chat-interaction.md` (1)/(3)
 * deletes the peek, `peekPanelView` and `TerminalPeek` outright: "no terminal emulation and no raw
 * grid anywhere in the app". A header that does not exist cannot name a session well or badly, and
 * the `cols x rows` half had no surviving subject either -- no phone surface issues a watch, so
 * nothing on this handset knows a PTY's size. The guarantee those two cases carried is NOT lost:
 * the session detail's header is the drill-down header now, and its two cases below are the same
 * two assertions -- the name where there is one, TODAY'S EXACT ID where there is not.
 *
 * WHY ONE SUITE ACROSS THESE SCREENS. Every assertion here is the same assertion -- the human name
 * where there is one, TODAY'S EXACT STRING where there is not -- and the second half is the whole
 * risk of this change. A daemon that predates the wire field sends no name and a machine
 * generated where `os.Hostname()` failed publishes none, so the fallback is not a corner: it is
 * what an entire installed base renders. Spreading these across seven files would put the seven
 * fallbacks in seven places and make "did any of them start fabricating" a question nobody asks
 * in one sitting.
 *
 * THE FALLBACKS ARE ASSERTED AS LITERALS, not as `ifEmpty` re-implemented in the test. A test that
 * computed the expected string the way the code does would go green on any change they made
 * together, which is exactly the class of drift this bead is correcting.
 *
 * TWO SURFACES KEEP THE ID AND ARE ASSERTED TO, because a ruling nobody tests is a comment:
 *
 *  - **The Activity log.** It is the machine's own register, read when a line has to be matched
 *    against a daemon log or a signed command, and those carry the id.
 *  - **The scope chip's `machine` field**, and the session id everywhere it is an ACTOR. The chips
 *    filter on the endpoint id, the detail screen's controls act on the session id; only the
 *    LABEL changes. Keying either on a hostname would break the moment two machines shared a name,
 *    and quietly.
 */
class HumanNamesTest {

    // ---- the one decision, in the one place ---------------------------------

    @Test
    fun `machine label prefers the name and falls back to the endpoint id`() {
        assertEquals("nathans-mbp", MachineLabel.of("nathans-mbp", "ep-1a2b3c4d"))
        assertEquals("ep-1a2b3c4d", MachineLabel.of("", "ep-1a2b3c4d"))
    }

    /**
     * The gap gets the id and NEVER a word. `Unnamed`, `Unknown` or `This computer` would be
     * indistinguishable on screen from a machine actually called that (ADR-007 B135).
     */
    @Test
    fun `machine label invents nothing when both are empty`() {
        assertEquals("", MachineLabel.of("", ""))
    }

    // ---- the machine row: name cell and endpoint cell ------------------------
    //
    // THE ROW MOVED AND THE TWO ASSERTIONS DID NOT (agents-tracker-nx44.3). They read
    // `MachinesPanelScreen.of(pane(...), formatTime).machine`; the Machines destination is deleted
    // and derivation row 11's machine row is the settings screen's CONNECTION section now, so the
    // subject is `SettingsPanelScreen.connectionOf`. What is asserted is unchanged, which is the
    // point of re-pointing them rather than dropping them: this is one of the seven fallback sites
    // this suite exists to hold in one place.

    private fun machineRow(machineName: String = "") = SettingsPanelScreen.connectionOf(
        machineId = "ep-1a2b3c4d",
        machineName = machineName,
        presence = "online",
        freshness = MachineFreshness(silent = false, lastHeardUnixMs = 1_753_900_000_000),
        streams = emptyList(),
        clock = ClockBanner.of(""),
        killSwitchEngaged = false,
        nowUnixMs = 1_753_900_000_000L,
    ).machine

    @Test
    fun `the machine row names the machine and keeps the id in its own cell`() {
        val row = machineRow(machineName = "nathans-mbp")
        assertEquals("nathans-mbp", row.name)
        assertEquals("ep-1a2b3c4d", row.endpoint)
    }

    /**
     * A machine that published no name renders its id in the NAME cell and draws NO endpoint cell
     * -- one fact, once. The row's own KDoc argues it: the same string in both cells would be a
     * copy of the name wearing the mock's label.
     */
    @Test
    fun `an unnamed machine renders its id once`() {
        val row = machineRow(machineName = "")
        assertEquals("ep-1a2b3c4d", row.name)
        assertNull(row.endpoint)
    }

    // ---- the inbox scope chips ----------------------------------------------

    private fun roster(vararg ids: String) = TriageInbox.from(
        ids.map { id ->
            SessionRow(id = id, title = "api refactor", group = "working", need = "launched", present = true, agent = "claude")
        },
        journalStale = false,
    )

    @Test
    fun `a scope chip reads the machine name and still filters on the endpoint id`() {
        val chips = TriageInboxScreen.of(
            inbox = roster("ep-1a2b3c4d/kx7q2m4v9p1s6t8w"),
            machineNames = mapOf("ep-1a2b3c4d" to "nathans-mbp"),
        ).scopes
        val machine = chips.single { it.machine != null }
        assertEquals("nathans-mbp", machine.label)
        assertEquals("nathans-mbp, online", machine.description)
        // THE FILTER KEY IS UNTOUCHED. This is what the chip ACTS on and what `scope` is compared
        // against; a hostname here would collide the moment two machines shared a name.
        assertEquals("ep-1a2b3c4d", machine.machine)
    }

    @Test
    fun `a scope chip for a machine with no name renders exactly what it rendered before`() {
        val chips = TriageInboxScreen.of(inbox = roster("ep-1a2b3c4d/kx7q2m4v9p1s6t8w")).scopes
        val machine = chips.single { it.machine != null }
        assertEquals("ep-1a2b3c4d", machine.label)
        assertEquals("ep-1a2b3c4d, online", machine.description)
        assertEquals("ep-1a2b3c4d", machine.machine)
    }

    /** A name for ANOTHER machine changes nothing here: the lookup misses and the id renders. */
    @Test
    fun `a scope chip is not labelled from another machines name`() {
        val chips = TriageInboxScreen.of(
            inbox = roster("ep-99999999/kx7q2m4v9p1s6t8w"),
            machineNames = mapOf("ep-1a2b3c4d" to "nathans-mbp"),
        ).scopes
        assertEquals("ep-99999999", chips.single { it.machine != null }.label)
    }

    // ---- the nav header the drill-down is read under -------------------------
    //
    // IT IS THE ONE HEADER LEFT, and the file KDoc says what happened to the other: the terminal
    // peek's is deleted with the screen (ADR-009 (1)/(3)), and the two cases it carried are these
    // two. What a session is CALLED reaches the user here now.

    private fun detail(title: String) = SessionDetail(
        sessionId = "ep-1a2b3c4d/kx7q2m4v9p1s6t8w",
        online = true,
        journalStale = false,
        title = title,
    )

    private fun panelFor(title: String): SessionDetailPanel {
        val detail = detail(title)
        return SessionDetailScreen.of(
            detail,
            TranscriptScreen.of(emptyList()),
            SessionLease(sessionId = detail.sessionId, online = true),
            capabilities = SessionCapabilityFacts(structuredChat = true),
        )
    }

    @Test
    fun `the session detail header names the session`() {
        assertEquals("api refactor", panelFor("api refactor").title)
    }

    @Test
    fun `the session detail header falls back to the id`() {
        assertEquals("ep-1a2b3c4d/kx7q2m4v9p1s6t8w", panelFor("").title)
    }

    // ---- the approval sheet's context line ------------------------------------

    /**
     * The sheet reads [InboxRow.project], which is `Session.Title` -- so it names the session for
     * free, and this asserts that it DOES rather than assuming it. The agent stays beside it.
     *
     * THE SHEET IS BUILT FROM AN ITEM NOW and the roster row is what TITLES it: `ApprovalSheetScreen
     * .of` takes the `approval_request` the machine is blocked on plus the row it interrupted. The
     * name under test did not move -- it is still the row's `project` -- so this case is the one it
     * always was, on the signature the wire's own approvals brought.
     */
    @Test
    fun `the approval sheet names the session`() {
        val inbox = TriageInbox.from(
            listOf(
                SessionRow(
                    id = "ep-1a2b3c4d/kx7q2m4v9p1s6t8w",
                    title = "api refactor",
                    group = "needs_input",
                    need = "group_transition",
                    present = true,
                    agent = "claude",
                ),
            ),
            journalStale = false,
        )
        val row = TriageInboxScreen.of(inbox).sections.first { it.group == "needs_input" }.rows.single()
        assertEquals("api refactor", row.project)
        assertEquals(
            "api refactor · claude",
            ApprovalSheetScreen.of(pendingApproval(row.id), row).contextLine,
        )
    }

    /** One `approval_request` the machine is blocked on, which is what opens a sheet at all. */
    private fun pendingApproval(sessionId: String) = ApprovalItem(
        sessionId = sessionId,
        itemId = "01JQ0000000000000000000001",
        summary = "Claude wants to push the release commit to main.",
        command = "git push origin main",
        decisions = listOf(ApprovalDecision(id = "accept", label = "Allow")),
        promptCard = false,
    )

    // ---- the ruling: Activity keeps the raw id --------------------------------

    /**
     * THE RULING, ASSERTED. Activity is a mono log -- the machine's own register -- and its
     * emphasis is what a reader matches against a daemon log, a journal cursor or a signed
     * command, all of which carry the id. This test exists so that a later pass "finishing the
     * job" of humanising every surface has to read the reason before it can go green.
     */
    @Test
    fun `the activity log keeps the raw session id`() {
        val panel = ActivityPanelScreen.of(
            JournalPageView(
                rows = listOf(
                    JournalRow(cursor = 9, sessionId = "ep-1a2b3c4d/kx7q2m4v9p1s6t8w", type = "launched", group = "working"),
                ),
                nextCursor = 9,
                stale = false,
            ),
        )
        val entry = panel.sections.single().rows.single()
        assertEquals("ep-1a2b3c4d/kx7q2m4v9p1s6t8w", entry.emphasis)
    }
}
