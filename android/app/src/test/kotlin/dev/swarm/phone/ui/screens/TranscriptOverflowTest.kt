package dev.swarm.phone.ui.screens

import dev.swarm.phone.ui.InteractionItem
import org.json.JSONObject
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner

/**
 * FAILING-FIRST (TDD RED, GG-5) for owner rulings R8 and R9, and for the tear's own sentence.
 * Plan: docs/specifications/chat-surface-plan.md §7 D.5/D.6. Bead: agents-tracker-tbpm.3.
 *
 * ## One rule, three places it was broken
 *
 * THE FLOW CARRIES A LINE AND NOT A BODY. Every defect below is the same one: a block that pours
 * a machine-sized literal into the reading column, so the conversation stops being scrollable and
 * becomes a document. The owner photographed the result and needed two screenshots to capture one
 * session. R3 folded the tool cards; these three are what folding them left standing.
 *
 *  1. **An opened tool run had no bound at all.** `expanded` drew the whole excerpt -- up to
 *     `MaxTextBytes`, 4 KiB (interaction-schema.md §5), which is roughly two hundred lines of a
 *     test run. Opening one card to read a stack trace cost the reader their place in the
 *     conversation. R8: it opens IN PLACE up to a bound, and past the bound it keeps a head and
 *     offers the whole of it on its own screen.
 *  2. **A `file_change` drew its unified diff unconditionally.** Not collapsible, not bounded --
 *     `TranscriptPanel.kt`'s FILE_CHANGE arm put `diff_excerpt` straight into the well, so a
 *     refactor touching nine files cost nine screens. R9: the change is a chip carrying the verb,
 *     the path and the counts, and the diff opens on its own screen.
 *  3. **The tear apologised in three sentences.** A rule with a word on it is what a reader needs
 *     at a discontinuity; a paragraph in the middle of the flow is a notice that has to be read
 *     before the conversation can resume.
 *
 * ## What is asserted here and what is NOT
 *
 * NOTHING HERE IS ABOUT DRAWING. The bound, the head, the counts and the routing handle are model
 * facts -- pure functions over the item -- and `TranscriptView`/Wave E decide what a tap on a
 * handle opens. That split is `TranscriptPanelTest`'s own, and it is what lets the hard half of
 * R8 (does the whole of it survive the bound?) be checked without an Activity.
 *
 * THE BOUND IS NOT A TRUNCATION, and that is the assertion this file exists for. §2's `truncated`
 * is the MACHINE saying it clipped the body; the bound is THIS SCREEN saying it will not draw all
 * of what it holds. A bound that dropped the tail would be the phone inventing a truncation the
 * machine never made -- the exact failure IS-TOOL-3 forbids one field over -- so the whole literal
 * rides on the block and only its head is drawn.
 */
// Robolectric, because InteractionItem.fields() decodes the item's JSON with Android's own
// org.json. Outside the sandbox that parse throws, ItemFields() comes back empty, and every
// assertion below about a well, a diff or a count quietly tests nothing.
@RunWith(RobolectricTestRunner::class)
class TranscriptOverflowTest {

    /** A tool run whose output is [lines] lines long, each line naming its own number. */
    private fun toolRun(id: String, lines: Int, status: String = "completed"): InteractionItem {
        val output = (1..lines).joinToString("\n") { "line $it" }
        return InteractionItem(
            sessionId = "m/s1", itemId = id, cursor = 1L, kind = "tool_run", status = status,
            text = output, toolKind = "execute", turnId = "turn-A",
            body = JSONObject()
                .put("tool", "Bash")
                .put("action", JSONObject().put("command", "go test ./..."))
                .put("output_excerpt", output)
                .toString(),
        )
    }

    private fun change(
        id: String = "f1",
        change: String = "modify",
        path: String = "ui/kit/Composer.kt",
        oldPath: String = "",
        diff: String = "@@ -1 +1 @@\n-old\n+new",
        added: Int = 12,
        removed: Int = 4,
    ) = InteractionItem(
        sessionId = "m/s1", itemId = id, cursor = 2L, kind = "file_change", status = "completed",
        turnId = "turn-A",
        body = JSONObject()
            .put("change", change)
            .put("path", path)
            .put("old_path", oldPath)
            .put("diff_excerpt", diff)
            .put("added", added)
            .put("removed", removed)
            .toString(),
    )

    private fun tear(reason: String = "hook spool gap at seq 41") = InteractionItem(
        sessionId = "m/s1", itemId = "structured_gap:2026-08-26T09:44:00Z", cursor = 3L,
        kind = "structured_gap", status = "completed", text = reason,
    )

    private fun blockOf(item: InteractionItem, expanded: Set<String> = emptySet()) =
        TranscriptScreen.of(listOf(item), expanded = expanded).blocks.single()

    /**
     * R8's bound, restated. It is a LITERAL here on purpose: the model's constant is private, and
     * a test that read the number out of the thing it is testing would assert that 20 equals 20.
     * If this and `TranscriptScreen.OPEN_IN_PLACE_LINES` ever disagree, the tests above fail --
     * which is the whole point of writing it twice.
     */
    private val OPEN_IN_PLACE = 20

    // ---- R8: the in-place bound ------------------------------------------

    @Test
    fun `output that fits the bound opens whole and routes nowhere`() {
        val block = blockOf(toolRun("t1", lines = 8), expanded = setOf("t1"))

        assertEquals(
            "output short enough to read in place was cut anyway, which puts a second screen " +
                "between the reader and eight lines they could already see",
            8,
            block.well.split("\n").size,
        )
        assertEquals(
            "a card that fits offers a screen of its own, so the overflow affordance is drawn " +
                "on every card and means nothing on any of them",
            TranscriptRoute.None,
            block.route,
        )
    }

    @Test
    fun `output past the bound keeps a head and offers the rest on its own screen`() {
        val block = blockOf(toolRun("t1", lines = 60), expanded = setOf("t1"))

        assertEquals(
            "the whole of a long run is still poured into the reading column: opening one card " +
                "to read a stack trace costs the reader their place in the conversation",
            20,
            block.well.split("\n").size,
        )
        assertEquals(
            "the head is not the head: a bound that drew the TAIL would hide the command and " +
                "the first error, which is what a reader opens a card for",
            "line 1",
            block.well.split("\n").first(),
        )
        val route = block.route
        assertTrue(
            "a bounded card offers no way to the rest of it, so the bound is a truncation this " +
                "screen invented and the reader cannot get past",
            route is TranscriptRoute.Output,
        )
        assertEquals(
            "the overflow does not say how much is behind it. A handle that hides its size is " +
                "the dead-chevron defect (agents-tracker-2yb) wearing a count",
            "Open in full · 40 more lines",
            (route as TranscriptRoute.Output).label,
        )
    }

    @Test
    fun `one line past the bound is one more line, not one more lines`() {
        val block = blockOf(toolRun("t1", lines = OPEN_IN_PLACE + 1), expanded = setOf("t1"))

        assertEquals(
            "the overflow reads `1 more lines`. The count is substituted into one plural " +
                "template, so the boundary case -- the commonest one, because a body one line " +
                "over the bound is the likeliest body to be over it at all -- is the case the " +
                "copy gets wrong",
            "Open in full · 1 more line",
            (block.route as TranscriptRoute.Output).label,
        )
    }

    @Test
    fun `two lines past the bound is plural again`() {
        val block = blockOf(toolRun("t1", lines = OPEN_IN_PLACE + 2), expanded = setOf("t1"))

        assertEquals(
            "the singular was applied to every count, so a two-hundred-line test run reads " +
                "`200 more line`",
            "Open in full · 2 more lines",
            (block.route as TranscriptRoute.Output).label,
        )
    }

    @Test
    fun `the bound is not a truncation - the whole of what the phone holds still rides on it`() {
        val block = blockOf(toolRun("t1", lines = 60), expanded = setOf("t1"))
        val route = block.route as TranscriptRoute.Output

        assertEquals(
            "the tail was DROPPED rather than routed, so the phone has invented a truncation " +
                "the machine never made -- the failure IS-TOOL-3 forbids one field over",
            60,
            route.text.split("\n").size,
        )
        assertEquals("line 60", route.text.split("\n").last())
    }

    @Test
    fun `a closed card offers no overflow, because there is nothing open to overflow`() {
        val block = blockOf(toolRun("t1", lines = 60))

        assertEquals("a closed card draws no well", "", block.well)
        assertEquals(
            "a closed card advertises a screen of its own beside the offer to open it, which is " +
                "two affordances for one body and a reader having to choose between them",
            TranscriptRoute.None,
            block.route,
        )
    }

    @Test
    fun `a running card still leads with the word before the bound is applied`() {
        val block = blockOf(
            toolRun("t1", lines = 60, status = "in_progress"),
            expanded = setOf("t1"),
        )

        assertEquals(
            "the bound cut the state off the top of the well: a card the reader cannot tell is " +
                "still live from one that finished is agents-tracker-dwwv.1.2 all over again",
            "running",
            block.well.split("\n").first(),
        )
        assertTrue(block.running)
    }

    // ---- R9: a file change is a chip -------------------------------------

    @Test
    fun `a file change draws no diff in the flow`() {
        assertEquals(
            "the unified diff is still drawn inline and unconditionally, so a refactor touching " +
                "nine files costs nine screens of the conversation",
            "",
            blockOf(change()).well,
        )
    }

    @Test
    fun `a file change carries the verb, the path and the counts as its own cells`() {
        val chip = blockOf(change()).fileChange

        assertEquals(
            "the change has no cells of its own, so the row can only be drawn by splitting the " +
                "sentence this screen wrote -- a view reading copy back is PB-DS-9 inverted",
            "modify",
            chip?.verb,
        )
        assertEquals("ui/kit/Composer.kt", chip?.path)
        assertEquals(
            "the counts are the wire's own numbers and they are what makes a chip worth a tap",
            "+12 -4",
            chip?.counts,
        )
    }

    @Test
    fun `a rename's chip names both paths, in the direction the file moved`() {
        val chip = requireNotNull(
            blockOf(
                change(
                    change = "rename",
                    path = "ui/ConversationView.kt",
                    oldPath = "ui/SessionDetailView.kt",
                    added = 0,
                    removed = 0,
                ),
            ).fileChange,
        ) { "a file change carries no chip at all, so there is no row to draw" }

        assertTrue(
            "a rename drawn as a change to its new path alone makes the file appear to have come " +
                "from nowhere: ${chip.path}",
            chip.path.contains("ui/SessionDetailView.kt") &&
                chip.path.contains("ui/ConversationView.kt"),
        )
        assertEquals(
            "a rename with no line counts draws an empty counts cell rather than none at all",
            "",
            chip.counts,
        )
    }

    @Test
    fun `the diff opens on its own screen and is never grouped away`() {
        val block = blockOf(change())
        val route = block.route

        assertTrue(
            "the diff is not reachable from the block at all, so R9 moved it off the screen and " +
                "nowhere else -- what changed on disk would stop being readable from the phone",
            route is TranscriptRoute.Diff,
        )
        assertEquals(
            "the diff reached the routed screen changed. It is the producer's own normalized " +
                "unified diff (§3.4) and this side neither re-renders nor re-wraps it",
            "@@ -1 +1 @@\n-old\n+new",
            (route as TranscriptRoute.Diff).text,
        )
    }

    @Test
    fun `a file change the producer sent no diff for offers no screen`() {
        assertEquals(
            "an empty diff still offers a screen, so a tap opens a page with nothing on it -- " +
                "ADR-014's honest-floor rule read for a route rather than for a page",
            TranscriptRoute.None,
            blockOf(change(diff = "")).route,
        )
    }

    // ---- the tear says one thing -----------------------------------------

    @Test
    fun `the tear is a word on a rule and not an apology`() {
        val block = blockOf(tear())

        assertTrue("the tear is not marked, so it renders as an ordinary row", block.gap)
        assertEquals(
            "the tear still reads as a paragraph in the middle of the conversation. A reader at " +
                "a discontinuity needs to know the record is not continuous and how to repair " +
                "it; three sentences of explanation is a notice they must finish before the " +
                "conversation resumes",
            "Missing messages · Reload",
            block.line,
        )
        assertFalse(
            "the tear's line still explains itself at length",
            block.line.contains("."),
        )
    }

    @Test
    fun `the tear draws no mono well under its rule`() {
        assertEquals(
            "the machine's spool diagnosis is drawn in the flow under the divider, which is the " +
                "paragraph again in the machine's own voice -- and the drawing gives the tear " +
                "ONE line",
            "",
            blockOf(tear()).well,
        )
    }
}
