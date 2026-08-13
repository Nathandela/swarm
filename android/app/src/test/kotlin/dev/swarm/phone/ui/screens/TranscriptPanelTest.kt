package dev.swarm.phone.ui.screens

import dev.swarm.phone.ui.InteractionItem
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner

/**
 * FAILING-FIRST (TDD RED, GG-5) for ADR-009-structured-chat-interaction (1) -- the chat transcript
 * as a MODEL: what a real Claude Code session READS AS on the handset.
 *
 * WHAT THIS FILE IS RESPONSIBLE FOR AND `TranscriptViewTest` IS NOT. This decides what each of
 * `docs/specifications/interaction-schema.md` §3's eight kinds SAYS; that one decides that the
 * screen puts it on the glass, out of the kit, in this order. Splitting them is `SessionDetailPanel`
 * / `SessionDetailView`'s arrangement and it is sharper here, because the interesting half of a
 * transcript is a pure function over the item and needs no Activity to check.
 *
 * IT RUNS UNDER ROBOLECTRIC DESPITE BEING A PURE MODEL, and the reason is one import.
 * `TranscriptItem.Body` crosses the gomobile boundary as the item's JSON **as a raw string** --
 * gomobile binds no map or variant type (`mobile/types.go`) -- so the per-kind decoding is the
 * client's, and the client's JSON reader is `org.json`, which the unit-test android.jar stubs to
 * throw. Robolectric supplies the real one. No Android VIEW is touched here.
 *
 * THE ORDER IS OLDEST FIRST AND THAT REVERSES `SessionDetailScreen`'S OLD RULE ON PURPOSE. A
 * journal of record TYPES is a log and is read from the top, newest first; a CONVERSATION is read
 * in the order it was said, and a chat whose newest line is at the top puts the agent's answer
 * above the question it answers. `App.ReadTranscript` already walks the fold in ascending cursor
 * order (IS-LAYER-3), so this keeps what the wire gave it rather than reversing it.
 */
@RunWith(RobolectricTestRunner::class)
class TranscriptPanelTest {

    private fun item(
        kind: String,
        body: String = "{}",
        itemId: String = "01ITEM",
        cursor: Long = 1,
        text: String = "",
        status: String = "",
        truncated: Boolean = false,
        degraded: Boolean = false,
        resolved: Boolean = false,
    ) = InteractionItem(
        itemId = itemId,
        cursor = cursor,
        kind = kind,
        status = status,
        text = text,
        body = body,
        truncated = truncated,
        degraded = degraded,
        resolved = resolved,
    )

    private fun blockOf(item: InteractionItem): TranscriptBlock {
        val blocks = TranscriptScreen.of(listOf(item)).blocks
        assertEquals("one item rendered $blocks", 1, blocks.size)
        return blocks.first()
    }

    // ---- the two message kinds -------------------------------------------

    @Test
    fun `a user message is attributed to the person who sent it`() {
        val block = blockOf(
            item("user_message", body = """{"text":"ship it","source":"phone"}""", text = "ship it"),
        )

        assertTrue(
            "a user's own message is drawn as bare text, so a transcript of a conversation reads " +
                "as one voice and the reader cannot tell who said what",
            block.line.contains("ship it") && block.line.contains("You"),
        )
        assertEquals(
            "the attribution is not the marked span, so the one word the design puts the eye on " +
                "is whatever the sentence happened to start with",
            "You",
            block.emphasis,
        )
    }

    @Test
    fun `an agent message is the agent's own words and nothing added`() {
        val block = blockOf(item("agent_message", text = "I pushed the branch."))

        assertEquals("I pushed the branch.", block.line)
        assertNull(
            "the agent's message carries an emphasis, which marks a span of the agent's prose as " +
                "though it were an identifier",
            block.emphasis,
        )
    }

    @Test
    fun `a streamed message is the fold and never the last increment alone`() {
        // IS-DELTA-1: `text` on the WIRE is the increment this record appended; `Item.Text` on the
        // phone is the reconstruction. Reading `text` back out of Body would render the tail of a
        // message as the whole of it -- which is the one thing `phonecore.Item`'s own KDoc warns a
        // client against, and the only way to fail it is to decode the field this asserts is unread.
        val block = blockOf(
            item("agent_message", body = """{"text":" the branch."}""", text = "I pushed the branch."),
        )

        assertEquals("I pushed the branch.", block.line)
    }

    // ---- tool_run: §7's structured action --------------------------------

    @Test
    fun `a tool run reads as what the tool did, from the structured action`() {
        val block = blockOf(
            item(
                "tool_run",
                body = """{"tool":"Read","action":{"type":"read","path":"src/main.rs"}}""",
            ),
        )

        assertEquals("Read src/main.rs", block.line)
        assertEquals(
            "the card names the file and does not MARK it, so the target reads as prose rather " +
                "than as the identifier row 14's inline mono is for",
            "src/main.rs",
            block.emphasis,
        )
    }

    @Test
    fun `each action type is read from its own field rather than from the tool name`() {
        // IS-TOOL-1: the phone never parses `tool` or raw arguments to infer an action. Which FIELD
        // carries the target is the action's own `type`, and a client that keyed off the tool name
        // would break on the first CLI that spelled its tools differently.
        val search = blockOf(
            item("tool_run", body = """{"tool":"Grep","action":{"type":"search","query":"TODO"}}"""),
        )
        val execute = blockOf(
            item(
                "tool_run",
                body = """{"tool":"Bash","action":{"type":"execute","command":"go test ./..."}}""",
            ),
        )

        assertEquals("Grep TODO", search.line)
        assertEquals("Bash go test ./...", execute.line)
    }

    @Test
    fun `an unclassified call falls back to the tool and guesses nothing`() {
        // IS-TOOL-2 in as many words: "an unclassified call is never guessed at".
        val block = blockOf(item("tool_run", body = """{"tool":"WebFetch","action":{"type":"other"}}"""))

        assertEquals("WebFetch", block.line)
        assertNull(block.emphasis)
    }

    @Test
    fun `a truncated tool output shows the CLI's own marker verbatim`() {
        // IS-TOOL-3: the marker is the CLI's text, shown as-is, and the item never claims to hold
        // the output it only saw a marker for.
        val block = blockOf(
            item(
                "tool_run",
                body = """{"tool":"Bash","action":{"type":"execute","command":"ls"},""" +
                    """"output_excerpt":"a\nb","truncation_marker":"… +2039 lines"}""",
            ),
        )

        assertTrue("the tool's output is not shown at all", block.well.contains("a\nb"))
        assertTrue(
            "the CLI's own truncation marker is dropped, so the card presents an excerpt as the " +
                "whole output",
            block.well.contains("… +2039 lines"),
        )
    }

    // ---- agents-tracker-dwwv.1.2: the running marker ----------------------

    @Test
    fun `a tool still in_progress is named as running, and a completed one is not`() {
        // `InteractionItem.status` (§4, populated FacadeBridge.kt:120) was read by nothing until
        // this bead: a running tool and a finished one rendered the same block.
        val running = blockOf(
            item(
                "tool_run",
                body = """{"tool":"Bash","action":{"type":"execute","command":"go test ./..."}}""",
                status = "in_progress",
            ),
        )
        val completed = blockOf(
            item(
                "tool_run",
                body = """{"tool":"Bash","action":{"type":"execute","command":"go test ./..."}}""",
                status = "completed",
            ),
        )

        assertTrue(
            "an in_progress tool_run is not named as running, so a reader watching the " +
                "transcript cannot tell a live tool from a finished one",
            running.running,
        )
        assertTrue(
            "the running block carries no marker in its mono line",
            running.well.contains("running"),
        )
        assertFalse("a completed tool is still drawn as running", completed.running)
        assertFalse(
            "a completed tool's mono line carries the running marker",
            completed.well.contains("running"),
        )
        assertEquals(
            "a completed tool with no output of its own draws a well anyway, which is an empty " +
                "recessed box saying \"we have nothing\" in the shape of \"the machine printed " +
                "nothing\"",
            "",
            completed.well,
        )
    }

    // ---- file_change: the diff card --------------------------------------

    @Test
    fun `a file change names the change, the path and the size of it`() {
        val block = blockOf(
            item(
                "file_change",
                body = """{"path":"src/main.rs","change":"modify","added":12,"removed":3,""" +
                    """"diff_excerpt":"@@ -1 +1 @@"}""",
            ),
        )

        assertTrue("the change is not named: ${block.line}", block.line.contains("modify"))
        assertTrue("the path is not named: ${block.line}", block.line.contains("src/main.rs"))
        assertTrue("the size of the change is not shown: ${block.line}", block.line.contains("12"))
        assertEquals(
            "the diff is not carried, so a file-change card says a file changed and never shows " +
                "what changed in it",
            "@@ -1 +1 @@",
            block.well,
        )
        assertEquals("src/main.rs", block.emphasis)
    }

    @Test
    fun `a rename says where the file came from`() {
        val block = blockOf(
            item(
                "file_change",
                body = """{"path":"src/lib.rs","old_path":"src/main.rs","change":"rename"}""",
            ),
        )

        assertTrue(
            "a rename is drawn as a change to its new path alone, so the file appears to have " +
                "come from nowhere: ${block.line}",
            block.line.contains("src/main.rs") && block.line.contains("src/lib.rs"),
        )
    }

    // ---- plan_update ------------------------------------------------------

    @Test
    fun `a plan update renders its steps and the state of each`() {
        val block = blockOf(
            item(
                "plan_update",
                body = """{"revision":2,"steps":[{"text":"Read the spec","state":"completed"},""" +
                    """{"text":"Write the tests","state":"in_progress"}]}""",
            ),
        )

        assertTrue("a plan is drawn with no steps in it: ${block.line}", block.line.contains("Read the spec"))
        assertTrue(block.line.contains("Write the tests"))
        assertTrue(
            "the steps carry no state, so a finished plan and an untouched one read identically",
            block.line.contains("completed") && block.line.contains("in_progress"),
        )
    }

    // ---- approval_request: the one tappable block -------------------------

    @Test
    fun `an approval request is the block a user can answer`() {
        val block = blockOf(
            item(
                "approval_request",
                body = """{"summary":"Run go test ./...","action":{"type":"execute",""" +
                    """"command":"go test ./..."},"decisions":[{"id":"allow","label":"Allow"}]}""",
                status = "in_progress",
            ),
        )

        assertEquals("Run go test ./...", block.line)
        assertTrue(
            "an approval_request is drawn as an ordinary transcript line, so the one block in " +
                "this conversation that is waiting on the user is the one they cannot act on",
            block.approval,
        )
        assertEquals(
            "the block does not carry the interaction_id, so a tap cannot name what it is " +
                "answering (IS-APR-1: the item_id IS the interaction_id)",
            "01ITEM",
            block.itemId,
        )
    }

    @Test
    fun `an answered approval stops being a decision`() {
        // IS-LIFE-2: every approval_request reaches exactly one approval_resolved, and a stale card
        // dismisses on every surface. A resolved request stays in the transcript -- it is what was
        // asked -- and stops offering an answer.
        val block = blockOf(
            item(
                "approval_request",
                body = """{"summary":"Run go test","decisions":[{"id":"allow","label":"Allow"}]}""",
                resolved = true,
            ),
        )

        assertEquals(
            "the answered request was dropped from the transcript, so the conversation no longer " +
                "records what the machine asked",
            "Run go test",
            block.line,
        )

        assertFalse(
            "a resolved approval is still offered as answerable, so a user taps a decision the " +
                "machine settled hours ago",
            block.approval,
        )
    }

    @Test
    fun `a resolution says what was decided and who decided it`() {
        val block = blockOf(
            item("approval_resolved", body = """{"decision":"allowed","by":"phone"}"""),
        )

        assertTrue(block.line.contains("allowed"))
        assertTrue(
            "the resolution does not say who answered, so an approval the owner took at the " +
                "machine reads exactly like one this phone sent",
            block.line.contains("phone"),
        )
    }

    // ---- session_status ---------------------------------------------------

    @Test
    fun `a session status renders inline in the wire's own words`() {
        val block = blockOf(
            item(
                "session_status",
                body = """{"process":"running","turn":"idle","interaction":"none","group":"working"}""",
            ),
        )

        assertTrue("the status says nothing: `${block.line}`", block.line.contains("running"))
        assertTrue(block.line.contains("idle"))
        assertEquals(
            "a status marker draws a mono well, which gives a one-line state marker the weight " +
                "of a tool's output",
            "",
            block.well,
        )
    }

    @Test
    fun `a status note is the machine's sentence and replaces the state line`() {
        val block = blockOf(
            item(
                "session_status",
                body = """{"process":"running","note":"the CLI redrew its screen"}""",
            ),
        )

        assertEquals("the CLI redrew its screen", block.line)
    }

    // ---- §9 compatibility: the two rules a transcript may never break -----

    @Test
    fun `an unknown kind renders as a neutral row and never crashes`() {
        // IS-COMPAT-1. A kind this build does not know is not a gap and not an error: it is one row
        // saying the only thing the envelope guarantees, which is the kind itself.
        val block = blockOf(item("holographic_message", body = """{"colour":"green"}"""))

        assertEquals("holographic_message", block.line)
        assertFalse(block.approval)
    }

    @Test
    fun `an unreadable body renders as a neutral row and never crashes`() {
        // IS-ENV-3 / IS-COMPAT-2: a consumer skips what it cannot read and still advances. The
        // failure this forbids is an exception on the main looper, which `PhoneEvents` posts a
        // redraw to on every interaction event -- one malformed item would kill the app.
        val blocks = TranscriptScreen.of(
            listOf(
                item("tool_run", body = "{not json at all", itemId = "01A"),
                item("agent_message", body = "", text = "still here", itemId = "01B"),
            ),
        ).blocks

        assertEquals("an unreadable item took the transcript down with it", 2, blocks.size)
        assertEquals("tool_run", blocks.first().line)
        assertEquals("still here", blocks.last().line)
    }

    @Test
    fun `an item from a newer machine is marked rather than dropped`() {
        // IS-COMPAT-4: render what is understood and mark the item degraded. Never drop the
        // transcript, never error the connection.
        val block = blockOf(item("agent_message", text = "hello", degraded = true))

        assertTrue("the degraded item was dropped", block.line.contains("hello"))
        assertTrue(
            "an item this build only half understands is drawn as though it were whole",
            block.line != "hello",
        )
    }

    @Test
    fun `a truncated item says that it is truncated`() {
        val whole = blockOf(item("agent_message", text = "hello"))
        val clipped = blockOf(item("agent_message", text = "hello", truncated = true))

        assertTrue(
            "a clipped message is drawn as a complete one, so the reader takes an excerpt for " +
                "the whole of what the agent said",
            clipped.line != whole.line,
        )
    }

    // ---- the transcript as a whole ---------------------------------------

    @Test
    fun `the conversation is oldest first, in the order it was said`() {
        val panel = TranscriptScreen.of(
            listOf(
                item("user_message", text = "run the tests", cursor = 1, itemId = "01A"),
                item("agent_message", text = "done", cursor = 2, itemId = "01B"),
            ),
        )

        assertEquals(
            "the newest line is drawn first, so the agent's answer sits above the question it " +
                "answers -- a log is read from the top and a conversation is not",
            listOf("01A", "01B"),
            panel.blocks.map { it.itemId },
        )
    }

    @Test
    fun `a session with no items says so rather than showing an empty area`() {
        val panel = TranscriptScreen.of(emptyList())

        assertTrue(panel.blocks.isEmpty())
        assertTrue(
            "an empty transcript renders nothing at all, which is indistinguishable from a " +
                "conversation that failed to load -- PB-DS-9's rule is that an empty section is " +
                "still a section",
            panel.emptyCopy.isNotEmpty(),
        )
        assertTrue(
            "the empty copy claims the session has said nothing, which is a claim about the " +
                "MACHINE that a phone holding no items is in no position to make",
            !panel.emptyCopy.lowercase().contains("nothing has happened"),
        )
        assertTrue(panel.heading.isNotEmpty())
    }
}
