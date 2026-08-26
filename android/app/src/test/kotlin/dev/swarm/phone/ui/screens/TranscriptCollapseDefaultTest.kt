package dev.swarm.phone.ui.screens

import dev.swarm.phone.ui.InteractionItem
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner

/**
 * FAILING-FIRST (TDD RED, GG-5) for owner ruling R3: a tool run is COLLAPSED by default.
 * Plan: docs/specifications/chat-surface-plan.md §7 D.3/D.4. Bead: agents-tracker-tbpm.3.
 *
 * THIS REVERSES A REASONED DECISION AND OWES IT AN ANSWER. `TranscriptScreen.of`'s own KDoc
 * argues for open-by-default: "a collapsed default would silently stop drawing" the mono
 * blocks a real turn produces, and "a reader who has not asked for anything gets the whole
 * record". That argument is right about the risk and wrong about the remedy, because it
 * assumes the closed line says nothing.
 *
 * THE ANSWER IS THAT COLLAPSING IS ONLY HONEST WHEN THE CLOSED LINE IS. A line that names
 * its own worst outcome hides nothing the reader would have acted on. A line that says only
 * "Bash" while the command failed is the dishonest version -- and it is what the leading
 * mobile client ships today: its collapsed groups carry no failure signal at all, so a user
 * has to open every one to find out. That gap is the thing this file exists to close, and it
 * is why the default may move.
 *
 * WHAT IT BUYS. The owner's own screenshot needed two of them to capture one session,
 * because every tool's captured output was drawn in full. Collapsing is the single biggest
 * cause of that wall of text.
 */
// Robolectric, because InteractionItem.fields() decodes the item's JSON with Android's own
// org.json. Outside the sandbox that parse throws, ItemFields() comes back empty, and every
// assertion about a well or a mark quietly tests nothing.
@RunWith(RobolectricTestRunner::class)
class TranscriptCollapseDefaultTest {

    private fun tool(
        id: String,
        status: String = "completed",
        truncated: Boolean = false,
        text: String = "some captured output",
    ) = InteractionItem(
        sessionId = "m/s1", itemId = id, cursor = 1L, kind = "tool_run", status = status,
        text = text, truncated = truncated, toolKind = "execute", turnId = "turn-A",
        body = """{"tool":"Bash","action":{"command":"go test ./..."},"output_excerpt":"$text"}""",
    )

    private fun blocksOf(vararg items: InteractionItem, expanded: Set<String> = emptySet()) =
        TranscriptScreen.of(items.toList(), expanded = expanded).blocks

    @Test
    fun `a tool run draws no well until the reader opens it`() {
        val b = blocksOf(tool("a")).single()
        assertTrue("a tool run must offer to open", b.expandable)
        assertFalse(
            "the default is CLOSED: a burst of tool calls costs one line each, which is what " +
                "makes the conversation readable on a phone",
            b.expanded,
        )
        assertTrue("a closed card draws no mono well", b.well.isEmpty())
    }

    @Test
    fun `the reader's own open survives`() {
        val b = blocksOf(tool("a"), expanded = setOf("a")).single()
        assertTrue(b.expanded)
        assertTrue("an opened card draws what the machine printed", b.well.isNotEmpty())
    }

    @Test
    fun `a failed run says so on the closed line`() {
        val b = blocksOf(tool("a", status = "failed")).single()
        assertFalse(b.expanded)
        assertEquals(
            "a collapsed card may never look successful. The wire's own word, not one of ours",
            "failed",
            b.mark,
        )
    }

    @Test
    fun `a declined run says so on the closed line`() {
        val b = blocksOf(tool("a", status = "declined")).single()
        assertEquals("declined", b.mark)
    }

    @Test
    fun `a clipped run says so on the closed line`() {
        val b = blocksOf(tool("a", truncated = true)).single()
        assertFalse(b.expanded)
        assertEquals(
            "the offer to fetch the whole of a clipped body is drawn only when the card is " +
                "OPEN, so collapsing by default would make truncation invisible by default " +
                "unless the closed line carries the fact instead",
            "clipped",
            b.mark,
        )
    }

    @Test
    fun `an ordinary finished run carries no mark`() {
        assertEquals(
            "a mark on every row is a mark that means nothing",
            "",
            blocksOf(tool("a")).single().mark,
        )
    }

    @Test
    fun `a failure outranks a clip on the one mark the line has`() {
        assertEquals(
            "worst status wins: a reader scanning a burst needs the failure, and the clip is " +
                "still there when they open it",
            "failed",
            blocksOf(tool("a", status = "failed", truncated = true)).single().mark,
        )
    }

    @Test
    fun `a file change is never hidden by the default`() {
        val fileChange = InteractionItem(
            sessionId = "m/s1", itemId = "f", cursor = 2L, kind = "file_change",
            status = "completed", text = "- old line\n+ new line", turnId = "turn-A",
            body = """{"change":"modify","path":"ui/kit/Composer.kt","diff_excerpt":"- old line\n+ new line","added":12,"removed":4}""",
        )
        val b = blocksOf(fileChange).single()
        assertTrue(
            "a diff is the only rendering of what actually changed on disk. Folding it behind " +
                "a default would hide the one thing a file change is FOR",
            b.well.isNotEmpty(),
        )
    }

    @Test
    fun `a running run is still visibly running when closed`() {
        val b = blocksOf(tool("a", status = "in_progress")).single()
        assertTrue(
            "a card the reader cannot tell is still live from one that finished is the defect " +
                "agents-tracker-dwwv.1.2 was filed for; collapsing must not reintroduce it",
            b.running,
        )
    }
}
