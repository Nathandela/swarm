package dev.swarm.phone.ui.screens

import dev.swarm.phone.ui.InteractionItem
import dev.swarm.phone.ui.kit.ToolCard
import org.json.JSONArray
import org.json.JSONObject
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner

/**
 * FAILING-FIRST (TDD RED, GG-5) for the decision card's MODEL rules -- every one of them a
 * committee correction rather than a preference. Plan: docs/specifications/chat-surface-plan.md
 * §8, owner rulings R4 and R7. Bead: agents-tracker-tbpm.4.
 *
 * ## The card moves into the flow, and moving it is what puts these rules at risk
 *
 * R7 makes the conversation the ONE renderer of a pending decision: the inbox wears a badge and
 * the question is answered where it was asked. That deletes the second surface, and with it the
 * sheet's own guarantees -- a sheet is modal, so it could not be collapsed, could not be scrolled
 * past, and had nothing above it to lock. Inline, each of those becomes a property somebody has
 * to state. This file states them at the MODEL, where they are pure functions over the item and
 * can be checked without an Activity.
 *
 * ## What this surface may not do, and the fence that already exists for it
 *
 * IT MAY NOT AUTHOR A VERDICT. IS-APR-4 keeps `allow` | `deny` | `other` machine-side and off the
 * wire entirely, `internal/skeleton/interaction_chain_e2e_test.go` fails the build if one appears
 * beside `id`/`label`, and `ApprovalItem`'s own KDoc is the phone-side twin: there is nothing to
 * read a polarity out of, so no screen below can paint one. "Allow/Deny" is precisely the copy
 * this surface is forbidden from writing -- which is why the labels are asserted as an ORDERED
 * LIST equal to the wire's, and not as a set, a count, or a pair of buttons this file recognises.
 *
 * ## The four failures these tests are written against
 *
 *  - **A decision folded shut.** R3 made collapsing the default for tool runs, and a decision
 *    that inherited it would be a question the agent is blocked on, hidden behind a chevron.
 *  - **A decision answered twice.** Every choice is live until the answer lands, so a second tap
 *    -- or a tap on a different button -- sends a second `ActionApprove` for one `interaction_id`.
 *  - **A decision answered at the machine and still tappable here.** IS-LIFE-2 guarantees exactly
 *    one resolution "including when it is cancelled, superseded, expired, or answered at the
 *    machine", and says what that guarantee buys: a stale card dismisses on every surface.
 *  - **A decision scrolled past.** Stick-to-bottom is what makes a chat readable while an agent
 *    works, and it is exactly what carries a reader past the one item that is waiting on them.
 */
// Robolectric: ApprovalItem.of and InteractionItem.fields() both decode with Android's org.json,
// which throws outside the sandbox -- and a decode that threw would make every assertion here
// pass over an empty card.
@RunWith(RobolectricTestRunner::class)
class TranscriptDecisionTest {

    private fun decisions(vararg labels: String): JSONArray {
        val out = JSONArray()
        labels.forEachIndexed { index, label ->
            out.put(JSONObject().put("id", "d$index").put("label", label))
        }
        return out
    }

    private fun ask(
        id: String = "ap1",
        summary: String = "Run this command?",
        literal: String = "rm -rf ~/.swarm-relay/relay.db",
        resolved: Boolean = false,
        ts: Long = ASKED_AT,
        labels: Array<String> = arrayOf(
            "Yes",
            "Yes, and don't ask again this session",
            "No, and tell Codex what to do differently",
        ),
    ) = InteractionItem(
        sessionId = "m/s1", itemId = id, cursor = 2L, kind = "approval_request",
        status = "in_progress", turnId = "turn-A", resolved = resolved, tsUnixMs = ts,
        body = JSONObject()
            .put("summary", summary)
            .put("action", JSONObject().put("command", literal))
            .put("decisions", decisions(*labels))
            .toString(),
    )

    /** §3.6's `approval_resolved`: the item the daemon writes when the request reaches its end. */
    private fun resolved(decision: String, by: String) = InteractionItem(
        sessionId = "m/s1", itemId = "res1", cursor = 3L, kind = "approval_resolved",
        status = "completed", turnId = "turn-A",
        body = JSONObject()
            .put("interaction_id", "ap1")
            .put("decision", decision)
            .put("by", by)
            .toString(),
    )

    private fun agent(id: String, text: String) = InteractionItem(
        sessionId = "m/s1", itemId = id, cursor = 1L, kind = "agent_message",
        status = "completed", text = text, turnId = "turn-A",
        body = JSONObject().put("text", text).toString(),
    )

    private fun blockOf(item: InteractionItem, answering: Set<String> = emptySet()) =
        TranscriptScreen.of(listOf(item), answering = answering).blocks.single()

    /** §2's `ts` on the question. A real instant, because 0 is the ABSENT fact (PB-APP-11). */
    private companion object {
        private const val ASKED_AT = 1_776_000_000_000L
    }

    // ---- never collapsed --------------------------------------------------

    @Test
    fun `an unresolved decision is never collapsed`() {
        val block = blockOf(ask())

        assertFalse(
            "the decision offers to fold, so R3's collapsed default reaches the one item the " +
                "agent is BLOCKED on and hides the question behind a chevron",
            block.expandable,
        )
        assertTrue("a decision is open, always", block.expanded)
        assertTrue("an unresolved request is the block a reader can answer", block.approval)
    }

    // ---- the literal and the labels, verbatim and in order ----------------

    @Test
    fun `the exact literal survives, whole and unbounded`() {
        val literal = (1..40).joinToString("\n") { "rm -rf ~/.swarm-relay/shard-$it" }
        val block = blockOf(ask(literal = literal))

        assertEquals(
            "the decision's literal was bounded like a tool's output, or drawn in a mono well " +
                "under the card rather than inside it. R8's bound exists because an opened tool " +
                "run is a body a reader chose to look at; a literal is the thing being AGREED " +
                "TO, and a reader may not be asked to answer for the half of a command they " +
                "were shown",
            literal,
            block.literal,
        )
        assertEquals(
            "the literal was ALSO poured into the well, so the card's own command is drawn twice " +
                "-- once inside the question and once in a block underneath it",
            "",
            block.well,
        )
    }

    @Test
    fun `every CLI-defined label survives, in the order the CLI sent them`() {
        val labels = (1..8).map { "choice $it" }
        val block = blockOf(ask(labels = labels.toTypedArray()))

        assertEquals(
            "the labels are not the CLI's own, in the CLI's own order. §3.5 permits one to " +
                "eight (MaxDecisions), the ids are the CLI's own vocabulary rather than a " +
                "normalized one, " +
                "and a surface that reordered or re-labelled them would be authoring the verdict " +
                "IS-APR-4 keeps machine-side",
            labels,
            block.choices.map { it.label },
        )
        assertEquals(
            "the ids did not survive, so a tap has nothing to name back. IS-APR-1: the item_id " +
                "identifies the question and the decision id identifies the answer",
            (0..7).map { "d$it" },
            block.choices.map { it.id },
        )
    }

    // ---- one answer in flight locks all of them ---------------------------

    @Test
    fun `every choice locks while one answer is in flight`() {
        val block = blockOf(ask(), answering = setOf("ap1"))

        assertTrue(
            "the choices stay live while an answer is on the wire, so a second tap sends a " +
                "second ActionApprove for one interaction_id -- and the daemon that resolves " +
                "first decides which of the reader's two answers the agent acted on",
            block.locked,
        )
        assertTrue(
            "locking dropped the question. The card keeps its full height and every label " +
                "throughout, so nothing reflows under a descending thumb",
            block.approval && block.choices.size == 3,
        )
    }

    @Test
    fun `a decision nobody is answering is not locked`() {
        assertFalse(
            "every card locks as soon as any card is answered, so answering one question " +
                "freezes the others",
            blockOf(ask(), answering = setOf("some-other-item")).locked,
        )
    }

    // ---- the terminal answers first ---------------------------------------

    @Test
    fun `a decision answered at the machine stops being answerable here`() {
        val block = blockOf(ask(resolved = true))

        assertFalse(
            "IS-LIFE-2 resolves a request whoever answered it, and a card still offering its " +
                "buttons over a resolution is the stale card that guarantee was written to end",
            block.approval,
        )
        assertEquals(
            "the question left the transcript when it was answered. It stays: it is WHAT WAS " +
                "ASKED, and a conversation that deletes the questions is a conversation whose " +
                "answers have nothing to attach to",
            "Run this command?",
            block.line,
        )
    }

    @Test
    fun `a resolution landing over an answer in flight clears the lock`() {
        assertFalse(
            "the card resolved while this phone's own answer was still on the wire and stayed " +
                "disabled -- a dead card with every button greyed, which is the one state a " +
                "reader cannot leave",
            blockOf(ask(resolved = true), answering = setOf("ap1")).locked,
        )
    }

    // ---- the pill, and what suppresses the scroll -------------------------

    @Test
    fun `the model says an unanswered decision exists and which one it is`() {
        val panel = TranscriptScreen.of(
            listOf(agent("a1", "Found a reconnect storm."), ask(), agent("a2", "Standing by.")),
        )

        assertEquals(
            "nothing on the panel says a decision is waiting, so the screen cannot raise the " +
                "pill for it and an offscreen question is a session that looks idle",
            "ap1",
            panel.pendingDecisionId,
        )
    }

    @Test
    fun `a resolved decision raises no pill`() {
        assertEquals(
            "the pill points at a question that has already been answered, which sends the " +
                "reader to a card with nothing to do on it",
            "",
            TranscriptScreen.of(listOf(agent("a1", "Done."), ask(resolved = true)))
                .pendingDecisionId,
        )
    }

    @Test
    fun `stick to bottom is suppressed while a decision is unanswered`() {
        val arriving = listOf(
            TranscriptMutation.Append(index = 2, item = agent("a2", "Still going."), tail = true),
        )

        assertFalse(
            "the transcript follows the agent's next message past the question it is blocked " +
                "on, so the one item waiting on the reader scrolls off the top while they watch",
            TranscriptIncremental.stickToBottom(
                atBottom = true,
                mutations = arriving,
                decisionPending = true,
            ),
        )
        assertTrue(
            "suppression outlived the decision: with nothing unanswered the transcript must " +
                "still follow the conversation for a reader who was already at the bottom",
            TranscriptIncremental.stickToBottom(
                atBottom = true,
                mutations = arriving,
                decisionPending = false,
            ),
        )
    }

    // ---- a kind this build cannot draw ------------------------------------

    @Test
    fun `a decision this build cannot render says so and never dead-ends`() {
        val unreadable = InteractionItem(
            sessionId = "m/s1", itemId = "ap9", cursor = 2L, kind = "approval_request",
            status = "in_progress", turnId = "turn-A",
            body = """{"summary":"Approve this?","decisions":[]}""",
        )
        val block = blockOf(unreadable)

        assertTrue(
            "an approval this build cannot make a card out of falls to the neutral row, which " +
                "prints the wire's kind name `approval_request` at a reader and offers nothing. " +
                "A session is then stuck behind a card that cannot be answered anywhere",
            block.unrenderable,
        )
        assertEquals(
            "Update the app to answer here.",
            block.line,
        )
        assertFalse(
            "an unrenderable question still claims to be answerable here, which is a button " +
                "that cannot name a decision id",
            block.approval,
        )
        assertTrue("and it offers no choices to tap", block.choices.isEmpty())
    }

    // ---- the footer: when it was asked, and nothing else ------------------

    @Test
    fun `an unresolved decision says when it was asked`() {
        assertEquals(
            "the card does not say when the question arrived. It is the one fact a reader " +
                "waiting on a decision actually wants, and on a surface that is also streaming " +
                "an agent's prose it is what separates a question raised a moment ago from one " +
                "that has been sitting there since before the last three messages",
            "asked ${ToolCard.timestampLabel(ASKED_AT)}",
            blockOf(ask()).askedAt,
        )
    }

    @Test
    fun `the asked footer claims nothing about where the question can be answered`() {
        val footer = blockOf(ask()).askedAt

        assertFalse(
            "the footer carries `answerable here or at the terminal` again. It is a CAPABILITY " +
                "claim this block cannot verify -- the card draws identically on an offline " +
                "session and on one whose composer is shut -- and unlike a settled row it is on " +
                "screen precisely while the reader is deciding whether to act",
            footer.contains("answerable") || footer.contains("terminal"),
        )
    }

    @Test
    fun `a question the machine gave no time for says nothing about when`() {
        assertEquals(
            "the footer read the epoch as a time and told the reader they were asked at 01:00 " +
                "on 1 January 1970. PB-APP-11: 0 is an ABSENT fact, and a card is a bad place " +
                "to learn that",
            "",
            blockOf(ask(ts = 0L)).askedAt,
        )
    }

    @Test
    fun `an answered decision carries no asked footer`() {
        assertEquals(
            "a settled card still says when it was asked, beside the sentence saying how it was " +
                "resolved -- two time claims on one card, one of which is about a moment that " +
                "stopped mattering when the question was answered",
            "",
            blockOf(ask(resolved = true)).askedAt,
        )
    }

    // ---- §3.6's six resolutions, and the three that are not answers -------

    @Test
    fun `a resolution driven from this phone says where it was answered`() {
        val block = blockOf(resolved(decision = "allowed", by = "phone"))

        assertEquals(
            "the resolution reads as two wire tokens, `allowed · phone`, which is a vocabulary " +
                "written for a machine in the one row that tells a reader what became of the " +
                "question they were asked",
            "allowed · answered on this phone",
            block.line,
        )
        assertTrue(
            "the model does not say this resolution IS an answer, so a card cannot tell it " +
                "apart from an expiry",
            block.resolution?.answered == true,
        )
        assertEquals(
            "the wire's own classification was dropped. It is the one path where the daemon " +
                "knows the verdict (IS-RES-1), and it is the reader's only record of it",
            "allowed",
            block.resolution?.decision,
        )
    }

    @Test
    fun `a resolution taken at the machine names the machine and claims no verdict`() {
        val block = blockOf(resolved(decision = "answered_locally", by = "owner"))

        assertEquals(
            "the machine-answered path either says nothing about where it happened, or repeats " +
                "the wire's `answered_locally` beside a sentence that means the same thing. The " +
                "daemon observed the dialog leaving the waiting state and never learned which " +
                "button was pressed (internal/skeleton/backend.go:671), so this row has a " +
                "WHERE and no verdict at all",
            "answered at your computer",
            block.line,
        )
        assertTrue(block.resolution?.answered == true)
        assertFalse(
            "the machine's answer is credited to the phone, which is the attribution the daemon " +
                "refuses to make in the same breath",
            block.line.contains("phone"),
        )
    }

    @Test
    fun `the three resolutions that are not answers never borrow an answer's words`() {
        for (decision in listOf("cancelled", "superseded", "expired")) {
            val block = blockOf(resolved(decision = decision, by = "agent"))

            assertEquals(
                "a resolution nobody answered is drawn with the only settled sentence the " +
                    "drawing had, which tells the reader they answered it at their machine. " +
                    "IS-LIFE-2 resolves a request six ways and three of them are not answers",
                "$decision · never answered",
                block.line,
            )
            assertFalse(
                "an expiry, a cancel and a supersede are reported as answers, so the transcript " +
                    "credits somebody with a decision nobody made",
                block.resolution?.answered ?: true,
            )
        }
    }

    @Test
    fun `a resolution vocabulary this build does not know keeps the wire's own words`() {
        val block = blockOf(resolved(decision = "revoked", by = "operator"))

        assertEquals(
            "an unknown resolution was translated by a table that had to be exhaustive, so a " +
                "machine one version ahead takes this row down -- or worse, is described by the " +
                "nearest sentence this build happened to have",
            "revoked · operator",
            block.line,
        )
        assertFalse(
            "an unrecognised resolution is presented as an answer. This build cannot know that " +
                "it is one, and a card that said `answered` over it would be inventing the fact",
            block.resolution?.answered ?: true,
        )
    }

    @Test
    fun `an unrenderable decision does not raise the pill it cannot answer`() {
        val unreadable = InteractionItem(
            sessionId = "m/s1", itemId = "ap9", cursor = 2L, kind = "approval_request",
            status = "in_progress", turnId = "turn-A", body = "{not json",
        )
        assertEquals(
            "the pill scrolls the reader to a card that tells them to go to their machine, " +
                "over and over, with no way to stop it",
            "",
            TranscriptScreen.of(listOf(unreadable)).pendingDecisionId,
        )
    }
}
