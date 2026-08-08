package dev.swarm.phone.ui.screens

import dev.swarm.phone.ui.SessionRow
import dev.swarm.phone.ui.TriageInbox
import org.junit.Assert.assertEquals
import org.junit.Test

/**
 * FAILING-FIRST (TDD RED, GG-5) for agents-tracker-ksvb.2: a human vocabulary for the journal
 * record type, on the two surfaces THE RULING rates as human copy.
 *
 * THE RULING, RESTATED FOR THIS FILE. `docs/adr` and the field-test audit that opened this epic
 * settled which surfaces show the wire's own word and which show a phrase a person reads: mono
 * surfaces (Activity rows, the Session Detail transcript) are the machine's own register and stay
 * VERBATIM -- `ActivityPanelTest` and `SessionDetailPanelTest` pin that and neither changes here.
 * Sans surfaces are human copy, and there are exactly two of them: the inbox row's need line
 * ([InboxRow.need], `TriageInboxScreenTest` already covers its OTHER behaviour) and the approval
 * sheet's headline question ([ApprovalSheetPanel.question]). This file is the one place both are
 * driven from the SAME table, because they are the same fact read twice: the sheet's `question` is
 * `InboxRow.need`, unchanged (`ApprovalSheetPanel.kt:85`).
 *
 * THE PIPELINE IS THE REAL ONE, NOT A STAND-IN. [inboxRowFor] builds a [SessionRow] the way
 * `FacadeBridge.rowOf` does -- `need` carrying the wire's record type verbatim -- and drives it
 * through [TriageInbox.from] and [TriageInboxScreen.of], the actual production mapping. A suite
 * that hand-built an [InboxRow] with the phrase already on it would certify that the renderer reads
 * its argument, not that the mapping happens.
 *
 * WHY EVERY CASE USES THE `needs_input` GROUP. [ApprovalSheetScreen.of] returns null for any row
 * that is not [InboxRow.lit] -- "only a blocked session gets a sheet" is
 * `ApprovalSheetPanelTest`'s own rule -- and `lit` is `group == "needs_input"`. A table that also
 * wanted to vary the group would need a different row per surface, which would no longer be one
 * table read twice. The test below named for `group_transition` is where the group varies, on
 * its own.
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
     * AN UNRECOGNISED TOKEN RENDERS VERBATIM -- the last [Case] above, and the honesty fallback
     * `TriageInbox.kt`'s own rule for [dev.swarm.phone.ui.SessionRow.need] states: never an
     * invented phrase for a type this build does not know. It is folded into the table rather than
     * a separate test because it is answered by the same lookup, not a special case of it.
     */
    @Test
    fun `the approval sheet's question is the same phrase, because it is the row's need unchanged`() {
        CASES.forEach { case ->
            val panel = ApprovalSheetScreen.of(inboxRowFor(need = case.token), snapshot = "")
            assertEquals(
                "token '${case.token}' reached the approval sheet as something other than the " +
                    "inbox row's own need -- the sheet must not map or reword it a second time",
                case.phrase,
                panel?.question,
            )
        }
    }

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
