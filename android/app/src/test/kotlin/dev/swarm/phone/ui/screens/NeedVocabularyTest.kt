package dev.swarm.phone.ui.screens

import dev.swarm.phone.ui.ApprovalDecision
import dev.swarm.phone.ui.ApprovalItem
import dev.swarm.phone.ui.SessionRow
import dev.swarm.phone.ui.TriageInbox
import org.junit.Assert.assertEquals
import org.junit.Test

/**
 * agents-tracker-ksvb.2: a human vocabulary for the journal record type, on the surface THE RULING
 * rates as human copy.
 *
 * THE RULING, RESTATED FOR THIS FILE. `docs/adr` and the field-test audit that opened this epic
 * settled which surfaces show the wire's own word and which show a phrase a person reads: mono
 * surfaces (Activity rows, the session's own transcript) are the machine's own register and stay
 * VERBATIM -- `ActivityPanelTest` and `TranscriptPanelTest` pin that and neither changes here. The
 * sans surface is the inbox row's need line ([InboxRow.need], whose OTHER behaviour
 * `TriageInboxScreenTest` covers), and this file is where the whole table is driven through the
 * real mapping.
 *
 * **THE SECOND READER OF THAT TABLE IS GONE, AND WHAT REPLACED IT IS BETTER THAN THE VOCABULARY.**
 * The approval sheet's headline question WAS `InboxRow.need` unchanged, so the two were one fact
 * read twice and this file asserted both from one table. `interaction-schema.md` §3.5 gives an
 * `approval_request` a `summary` -- "one line for the card headline", written machine-side by the
 * adapter that captured the permission -- and `ApprovalSheetScreen.of` reads that instead: a
 * sentence about THIS decision, where the record type could only ever say which list the session
 * had moved to. The vocabulary was the honest best a screen could do over a wire carrying no prose;
 * it is not what a blocked machine has to say. The sheet's case below is therefore inverted rather
 * than dropped -- it asserts the vocabulary does NOT reach the sheet, which is the guarantee that
 * would otherwise have no test at all.
 *
 * THE PIPELINE IS THE REAL ONE, NOT A STAND-IN. [inboxRowFor] builds a [SessionRow] the way
 * `FacadeBridge.rowOf` does -- `need` carrying the wire's record type verbatim -- and drives it
 * through [TriageInbox.from] and [TriageInboxScreen.of], the actual production mapping. A suite
 * that hand-built an [InboxRow] with the phrase already on it would certify that the renderer reads
 * its argument, not that the mapping happens.
 *
 * WHY EVERY CASE USES THE `needs_input` GROUP. It is the state this vocabulary is read in: a
 * session on the Needs-you list is the one a person is looking at a need line to understand. It was
 * also a hard requirement while the sheet read the same field -- `ApprovalSheetScreen.of` returned
 * null for a row that was not [InboxRow.lit] -- and that gate is gone with the field (an
 * unresolved `approval_request` is now what a sheet is made of, not a display group). The table
 * stays on one group anyway, because varying two things at once would stop it being one table. The
 * test below named for `group_transition` is where the group varies, on its own.
 */
class NeedVocabularyTest {

    /** One (wire token, expected human phrase) pair. */
    private data class Case(val token: String, val phrase: String)

    /**
     * The seven journal record types (`internal/journal.RecordType`) plus one this build has
     * never heard of. `group_transition`'s row here carries `needs_input` for its Group, so its
     * phrase in this table is that one lookup's answer -- the dedicated test below covers the
     * other three Groups.
     */
    private val CASES = listOf(
        Case("launched", "Started"),
        Case("exited", "Ended"),
        Case("lost", "Connection lost"),
        Case("deleted", "Deleted"),
        Case("presence", "Connection updated"),
        Case("roster", "Synced"),
        Case("group_transition", "Waiting on you"),
        Case("a_future_record_type_this_build_has_never_seen", "a_future_record_type_this_build_has_never_seen"),
    )

    private fun inboxRowFor(need: String, group: String = "needs_input"): InboxRow {
        val session = SessionRow(
            id = "mbp/one",
            title = "one",
            group = group,
            need = need,
            present = true,
            agent = "claude",
        )
        val screen = TriageInboxScreen.of(TriageInbox.from(listOf(session), journalStale = false))
        return screen.sections.first { it.group == group }.rows.single()
    }

    @Test
    fun `the inbox row's need line is the human phrase for the wire's record type`() {
        CASES.forEach { case ->
            assertEquals(
                "token '${case.token}' did not map to its recorded phrase on the inbox row",
                case.phrase,
                inboxRowFor(need = case.token).need,
            )
        }
    }

    /**
     * THE INVERTED CASE, and the file KDoc argues why it is inverted rather than deleted. This used
     * to read `the approval sheet's question is the same phrase, because it is the row's need
     * unchanged`, driving the same table through [ApprovalSheetScreen] and asserting the phrase came
     * out. The sheet reads §3.5's machine-written `summary` now, so what has to be pinned is the
     * opposite fact: whatever the row's need line says -- a mapped phrase, or a token this build has
     * never seen -- the question is the machine's own sentence about THIS decision and never the
     * roster's word for which list the session is on.
     *
     * IT KEEPS THE WHOLE TABLE rather than one row of it, because the failure being fenced is a
     * lookup leaking one surface over, and a leak that only fired on `group_transition` would be
     * exactly the one a single-case test misses.
     */
    @Test
    fun `the approval sheet asks the machine's question, whatever the row's need line says`() {
        CASES.forEach { case ->
            val row = inboxRowFor(need = case.token)
            val panel = ApprovalSheetScreen.of(pendingApproval(row.id), row)
            assertEquals(
                "the inbox row's need line reached the approval sheet's headline question. The " +
                    "sheet asks what the machine is blocked on (§3.5's `summary`); a journal " +
                    "record type -- mapped to '${case.phrase}' or not -- says only which list the " +
                    "session moved to, which is not a question and cannot be answered",
                SUMMARY,
                panel.question,
            )
        }
    }

    /** §3.5's headline, as the adapter that captured the permission wrote it. */
    private val SUMMARY = "Claude wants to push the release commit to main."

    private fun pendingApproval(sessionId: String) = ApprovalItem(
        sessionId = sessionId,
        itemId = "01JQ0000000000000000000001",
        summary = SUMMARY,
        command = "git push origin main",
        decisions = listOf(ApprovalDecision(id = "accept", label = "Allow")),
        promptCard = false,
    )

    /**
     * `group_transition`'s own content beyond the session it names IS the Group it moved to
     * (`internal/journal/journal.go`: the field is "set on group_transition"), so its phrase is a
     * lookup over [InboxRow.group] -- which is already verbatim from the wire, `TriageInbox`'s own
     * rule -- and never a second derivation of it.
     */
    @Test
    fun `the group_transition phrase is a lookup over the row's own Group, not a fixed string`() {
        val expected = mapOf(
            "needs_input" to "Waiting on you",
            "working" to "Working",
            "ready_for_review" to "Ready for review",
            "completed" to "Done",
        )
        expected.forEach { (group, phrase) ->
            assertEquals(
                "group '$group' did not map to its recorded need-line phrase",
                phrase,
                inboxRowFor(need = "group_transition", group = group).need,
            )
        }
    }
}
