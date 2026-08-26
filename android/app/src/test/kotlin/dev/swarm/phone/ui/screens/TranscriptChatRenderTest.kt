package dev.swarm.phone.ui.screens

import android.content.Context
import android.text.Spanned
import android.text.style.TextAppearanceSpan
import android.view.View
import android.view.ViewGroup
import android.widget.TextView
import androidx.test.core.app.ApplicationProvider
import dev.swarm.phone.theme.SwarmTheme
import dev.swarm.phone.ui.InteractionItem
import dev.swarm.phone.ui.kit.KitTag
import dev.swarm.phone.ui.kit.MarkdownBlock
import dev.swarm.phone.ui.kit.ToolCard
import dev.swarm.phone.ui.kit.kitFind
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner

/**
 * FAILING-FIRST (TDD RED, GG-5) for Wave R6 review finding B8 -- the chat kit this wave built is
 * PARKED. Bead agents-tracker-hggx.7, Mirror rows M2.1, M2.2 and ADR-017's tear.
 *
 * ## What the finding is
 *
 * Nothing in production Kotlin referenced `Markdown`, `ToolCard`, `TranscriptIncremental`,
 * `ComposerModel` or `SessionDetailOpen`: the single grep hit across the whole app was a COMMENT
 * in `ErrorRouting.kt`. Every one of those files has an exhaustive JVM suite and none of them is
 * on a screen -- so M2.1 read "GREEN (model)" while no screen rendered markdown, and M2.2 read
 * "GREEN (model + wire + adapter)" while no screen drew a tool card. Playbook 8.1 is a PHYSICAL
 * demonstration on a real handset; with nothing rendered there is nothing to demonstrate.
 *
 * This suite drives the wiring from the SCREEN MODEL out, because that is the layer a test can
 * assert without a handset: what `TranscriptScreen` decides is on screen, and what
 * `transcriptView` actually composes for it.
 *
 * ## The three things it pins
 *
 * **M2.1, markdown on screen.** An `agent_message` is markdown-shaped prose. The block carries
 * the parsed blocks, the row's text is the prose WITHOUT its markers, a fenced code block lands
 * in the mono well rather than in the sentence, and the styling reaches the view as spans on the
 * body -- not as a second TextView, which is `activityRow`'s standing ruling.
 *
 * **M2.2, the tool card.** The glyph comes from the flat `tool_kind` and is its OWN cell, never
 * spliced into the sentence (the recorded-crossing golden pins those sentences byte for byte).
 * The timestamp is `ts`, drawn at a TURN BOUNDARY -- `ToolCard.separatorBefore`'s rule spent as
 * the visible separator a chat surface actually uses -- and never invented for `ts == 0`.
 *
 * **ADR-017's tear, rendered honestly.** A `structured_gap` element is a FIRST-CLASS ROW with
 * its own tag and its own words. Falling to the neutral arm would print the literal
 * `structured_gap` at the reader, which is a kind name and not a warning; drawing nothing at all
 * is the silent bridge ADR-017 exists to forbid.
 */
@RunWith(RobolectricTestRunner::class)
class TranscriptChatRenderTest {

    private val context: Context
        get() = SwarmTheme.applyTo(ApplicationProvider.getApplicationContext())

    private val session = "mbp/quanthome"

    private fun agent(text: String, turn: String = "t1", ts: Long = 0L) = InteractionItem(
        sessionId = session, itemId = "a-${text.hashCode()}", cursor = 1,
        kind = "agent_message", text = text, turnId = turn, tsUnixMs = ts,
    )

    private fun tool(
        id: String,
        toolKind: String,
        tool: String = "Read",
        target: String = "/tmp/x",
        output: String = "",
        turn: String = "t1",
        ts: Long = 0L,
        truncated: Boolean = false,
        detail: Boolean = false,
    ) = InteractionItem(
        sessionId = session, itemId = id, cursor = 2, kind = "tool_run",
        status = "completed", toolKind = toolKind, turnId = turn, tsUnixMs = ts,
        truncated = truncated, detail = detail,
        body = """{"tool":"$tool","action":{"path":"$target"},"output_excerpt":"$output"}""",
    )

    private fun gap(reason: String = "hook spool gap at seq 41") = InteractionItem(
        sessionId = session, itemId = "structured_gap:2026-08-19T21:00:00Z", cursor = 3,
        kind = "structured_gap", status = "completed", text = reason,
    )

    private fun blockOf(item: InteractionItem): TranscriptBlock =
        TranscriptScreen.of(listOf(item)).blocks.single()

    private fun View.allTagged(tag: String): List<View> {
        val found = mutableListOf<View>()
        fun walk(v: View) {
            if (v.tag == tag) found += v
            if (v is ViewGroup) for (i in 0 until v.childCount) walk(v.getChildAt(i))
        }
        walk(this)
        return found
    }

    private fun textOf(v: View?): String = (v as? TextView)?.text?.toString().orEmpty()

    // ---- M2.1: markdown ------------------------------------------------------

    @Test
    fun `an agent message carries its parsed markdown to the screen`() {
        val block = blockOf(agent("Ran **the suite**. See `internal/attach`."))
        assertTrue(
            "the agent's prose reached the screen as a flat string, so M2.1's renderer is a " +
                "model nothing renders",
            block.markdown.isNotEmpty(),
        )
        assertEquals(
            "the markers are still in the sentence, so the reader sees the markup rather than " +
                "the emphasis",
            "Ran the suite. See internal/attach.",
            block.line,
        )
    }

    @Test
    fun `a fenced code block in agent prose lands in the mono well and not in the sentence`() {
        val block = blockOf(agent("Here is the patch:\n```go\nfunc main() {}\n```"))
        assertEquals("Here is the patch:", block.line)
        assertEquals(
            "a fence rendered as body copy is column-aligned text a proportional layout re-wraps, " +
                "which misreports what the machine printed",
            "func main() {}",
            block.well,
        )
    }

    @Test
    fun `the markdown styling reaches the view as spans on the body`() {
        val panel = TranscriptScreen.of(listOf(agent("Ran **the suite**.")))
        val root = transcriptView(context, panel)
        val body = root.allTagged(TranscriptTag.BLOCK)
            .single().kitFind(KitTag.ACTIVITY_BODY) as TextView

        assertEquals("Ran the suite.", body.text.toString())
        val styled = body.text as? Spanned
        assertNotNull(
            "the row's body is a plain String, so the markdown the model parsed is invisible",
            styled,
        )
        val spans = styled!!.getSpans(0, styled.length, TextAppearanceSpan::class.java)
        assertTrue(
            "no type role was applied over the emphasised span. `activityRow`'s standing ruling " +
                "is that emphasis is a SPAN and not a second view -- two TextViews side by side " +
                "break the wrap and strand the marked words on the first line",
            spans.any { styled.getSpanStart(it) == 4 && styled.getSpanEnd(it) == 13 },
        )
    }

    @Test
    fun `plain prose survives the markdown pass byte for byte`() {
        // The recorded-crossing golden pins these sentences; a renderer that reflowed or
        // re-escaped ordinary prose would rewrite the machine's own words.
        val said = "Done. Changed 'line two' to 'line TWO EDITED' in edit-target3.txt."
        assertEquals(said, blockOf(agent(said)).line)
    }

    // ---- M2.2: the tool card -------------------------------------------------

    @Test
    fun `a tool run draws the flat tool_kind glyph as its own cell`() {
        val panel = TranscriptScreen.of(listOf(tool("t-1", toolKind = "read")))
        assertEquals(ToolCard.glyphFor("read"), panel.blocks.single().glyph)

        val root = transcriptView(context, panel)
        assertEquals(
            "the glyph is not on screen, so M2.2's card is a model nothing draws",
            ToolCard.glyphFor("read"),
            textOf(root.kitFind(KitTag.ACTIVITY_GLYPH)),
        )
        assertEquals(
            "the glyph was spliced into the sentence, which rewrites the line the recorded " +
                "crossing pins byte for byte",
            "Read /tmp/x",
            panel.blocks.single().line,
        )
    }

    @Test
    fun `an unknown tool_kind falls back rather than inventing a glyph`() {
        val panel = TranscriptScreen.of(listOf(tool("t-2", toolKind = "teleport")))
        assertEquals(ToolCard.glyphFor("other"), panel.blocks.single().glyph)
    }

    @Test
    fun `a message carries no glyph at all`() {
        assertEquals(
            "a glyph on the agent's prose labels the thing the screen is made of",
            "",
            blockOf(agent("hello")).glyph,
        )
    }

    @Test
    fun `the timestamp is drawn where the turn changes and nowhere else`() {
        val panel = TranscriptScreen.of(
            listOf(
                agent("first", turn = "turn-a", ts = 1_776_000_000_000L),
                tool("t-1", toolKind = "read", turn = "turn-a", ts = 1_776_000_060_000L),
                agent("second", turn = "turn-b", ts = 1_776_000_120_000L),
            ),
        )
        assertEquals(
            "every row is stamped, or none is. M2.2's separator draws exactly where turn_id " +
                "changes, and a time on every row is the noise that makes a boundary invisible",
            listOf(true, false, true),
            panel.blocks.map { it.turnStart },
        )
        assertEquals(
            listOf(
                ToolCard.timestampLabel(1_776_000_000_000L),
                "",
                ToolCard.timestampLabel(1_776_000_120_000L),
            ),
            panel.blocks.map { it.timestamp },
        )
    }

    @Test
    fun `an absent ts renders no time at all`() {
        assertEquals(
            "0 is an ABSENT fact and not the epoch: inventing 1970 on screen is worse than nothing",
            "",
            blockOf(agent("no clock", ts = 0L)).timestamp,
        )
    }

    // MOVED (owner ruling R3): same subject -- a tool card's well follows the reader's own
    // expansion -- with the DEFAULT inverted. What the reader spends is now the open rather
    // than the close, so the two halves of this test swapped places; neither assertion was
    // dropped. See TranscriptCollapseDefaultTest for why the default moved and what the
    // closed line has to carry before it may.
    @Test
    fun `an expandable tool card shows its well only when the reader opens it`() {
        val item = tool("t-1", toolKind = "read", output = "line one")
        val shut = TranscriptScreen.of(listOf(item)).blocks.single()
        assertTrue("a tool run with output must be expandable", shut.expandable)
        assertEquals(
            "a closed card still printed its well, so a burst of tool calls cannot be " +
                "scanned one line each",
            "",
            shut.well,
        )
        assertFalse(shut.expanded)

        val open = TranscriptScreen.of(listOf(item), expanded = setOf("t-1")).blocks.single()
        assertEquals("line one", open.well)
        assertTrue(open.expanded)
    }

    // ---- M3.3: the detail affordance ----------------------------------------

    @Test
    fun `only a truncated card whose full body the machine retains offers the fetch`() {
        assertTrue(
            TranscriptScreen.of(
                listOf(tool("t-1", "read", output = "x", truncated = true, detail = true)),
                expanded = setOf("t-1"),
            ).blocks.single().offersDetail,
        )
        assertFalse(
            "a card promised bytes the machine does not hold, so the tap can only ever refuse",
            TranscriptScreen.of(
                listOf(tool("t-2", "read", output = "x", truncated = true)),
                expanded = setOf("t-2"),
            ).blocks.single().offersDetail,
        )
        assertFalse(
            "an untruncated card offered to fetch what it already shows",
            TranscriptScreen.of(
                listOf(tool("t-3", "read", output = "x", detail = true)),
                expanded = setOf("t-3"),
            )
                .blocks.single().offersDetail,
        )
    }

    @Test
    fun `a card the machine has answered unavailable for stops offering the fetch`() {
        // Wave R6 review round 3, finding F4. `offersDetail` is derived from `truncated`+`detail`,
        // both journalled when the item was CAPTURED, so a body the daemon's bounded store has
        // since evicted goes on advertising itself: the reader taps, is told it is gone, and is
        // invited to tap again forever. The surface learns otherwise from the refusal and says so
        // here.
        val clipped = tool("t-1", "read", output = "x", truncated = true, detail = true)
        assertTrue(TranscriptScreen.of(listOf(clipped), expanded = setOf("t-1")).blocks.single().offersDetail)
        assertFalse(
            "the offer survived the machine's own answer that the whole of it is gone, so the " +
                "app goes on inviting a tap it already knows the answer to",
            TranscriptScreen.of(listOf(clipped), expanded = setOf("t-1"), withoutDetail = setOf("t-1"))
                .blocks.single().offersDetail,
        )
        assertTrue(
            "a settled offer withdrew ANOTHER card's, so one evicted body silences the cards " +
                "beside it",
            TranscriptScreen.of(listOf(clipped), expanded = setOf("t-1"), withoutDetail = setOf("t-other"))
                .blocks.single().offersDetail,
        )
    }

    @Test
    fun `the detail affordance is on screen and reports the item it names`() {
        var asked = ""
        // MOVED (owner ruling R3): the offer to fetch a clipped body is drawn under an OPEN
        // card, so the fixture opens it. The closed line is not left silent about the clip --
        // it carries the mark instead (TranscriptCollapseDefaultTest).
        val panel = TranscriptScreen.of(
            listOf(tool("t-1", "read", output = "clipped", truncated = true, detail = true)),
            expanded = setOf("t-1"),
        )
        val root = transcriptView(context, panel, onDetail = { _, id -> asked = id })
        val control = root.kitFind(TranscriptTag.DETAIL)
        assertNotNull(
            "IS-CAP-2's retained body has no way to be asked for, so a clipped card is the end " +
                "of the road on the handset",
            control,
        )
        control!!.performClick()
        assertEquals("t-1", asked)
    }

    // ---- ADR-017: the tear ---------------------------------------------------

    @Test
    fun `a structured gap is its own row with its own words`() {
        val block = blockOf(gap())
        assertTrue("the tear is not marked, so it renders as an ordinary row", block.gap)
        assertFalse(
            "the tear printed the wire's kind name at the reader. `structured_gap` is a label " +
                "for a machine; a reader needs to be told the conversation is not continuous",
            block.line == "structured_gap",
        )
        assertTrue(
            "the tear's sentence does not say the record is broken",
            block.line.isNotEmpty() && block.line.length > "structured_gap".length,
        )
        assertEquals(
            "the machine's own reason was dropped rather than carried verbatim",
            "hook spool gap at seq 41",
            block.well,
        )
    }

    @Test
    fun `the tear is drawn between the rows either side of it and is tagged as itself`() {
        val panel = TranscriptScreen.of(listOf(agent("before"), gap(), agent("after")))
        val root = transcriptView(context, panel)

        assertNotNull(
            "the tear has no view of its own, so the conversation reads as continuous across a " +
                "boundary the daemon PROVED was discontinuous -- the one thing ADR-017 forbids",
            root.kitFind(TranscriptTag.GAP),
        )
        assertEquals(
            "the tear was drawn as an ordinary conversation block",
            2,
            root.allTagged(TranscriptTag.BLOCK).size,
        )
    }

    @Test
    fun `a session whose latest element is a tear has no structured composer`() {
        assertTrue(
            "ADR-017 T2 rule 2's degrade is ONE-WAY, so a gap in the transcript is a session " +
                "with no message sink -- and a composer over one is the silent bridge",
            TranscriptScreen.of(listOf(agent("before"), gap())).structureTorn,
        )
        assertFalse(
            TranscriptScreen.of(listOf(agent("before"))).structureTorn,
        )
    }

    // ---- M3.1: the load-earlier affordance ----------------------------------

    @Test
    fun `the transcript offers load-earlier until the machine says there is no more`() {
        val items = listOf(agent("oldest"), agent("newest"))
        assertTrue(TranscriptScreen.of(items).offersLoadEarlier)
        assertEquals("the page is asked for BY ITEM ID, never by cursor (IS-ENV-2)",
            TranscriptScreen.of(items).blocks.first().itemId,
            TranscriptScreen.of(items).oldestItemId)
        assertFalse(
            "the control was offered over a floor the machine has already declared, so the tap " +
                "can only ever come back empty",
            TranscriptScreen.of(items, atFloor = true).offersLoadEarlier,
        )
        assertFalse(
            "an empty transcript offered to page before nothing",
            TranscriptScreen.of(emptyList()).offersLoadEarlier,
        )
    }

    @Test
    fun `the panel names the turn a composer send must be rendered against`() {
        val panel = TranscriptScreen.of(
            listOf(agent("a", turn = "turn-a"), agent("b", turn = "turn-b")),
        )
        assertEquals(
            "the screen has no turn to pass to composer_send/turn_interrupt, so every send " +
                "would name the empty turn and the daemon's stale_turn precondition asserts nothing",
            "turn-b",
            panel.latestTurnId,
        )
        assertEquals("", TranscriptScreen.of(emptyList()).latestTurnId)
    }

    @Test
    fun `a markdown link that lies about its destination is not drawn as written`() {
        // The screen-side end of B11: the renderer's honesty rule has to survive the flatten.
        val block = blockOf(agent("See [https://your-bank.example](https://evil.example)"))
        assertTrue(
            "the transcript drew a span reading like a bank and pointing somewhere else",
            block.line.contains("https://evil.example"),
        )
        assertFalse(block.line.contains("your-bank.example"))
        assertNull(
            "a paragraph that is only a link should still be one block",
            block.markdown.firstOrNull { it !is MarkdownBlock.Paragraph },
        )
    }
}
