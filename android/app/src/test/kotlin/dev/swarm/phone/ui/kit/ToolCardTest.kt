package dev.swarm.phone.ui.kit

import dev.swarm.phone.ui.InteractionItem
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * FAILING-FIRST (TDD RED, GG-5) for Mirror M2.2 -- the tool card as a kit MODEL: glyph from the
 * flat `toolKind` vocabulary, expand/collapse, a timestamp label, and the turn separator rule
 * (bead agents-tracker-hggx.7; compile-RED on the frozen symbols, including the four additive
 * InteractionItem fields toolKind / turnId / tsUnixMs / source).
 *
 * THE GLYPH READS ONE FLAT FIELD. IS-TOOL-1 forbids the phone parsing `tool` or arguments to
 * infer an action, and the same posture holds one hop later: the card picks its glyph from
 * `toolKind` (the §7 vocabulary, journalled flat by the machine) and never re-derives it from
 * Body. An unknown value renders as `other`'s glyph -- IS-COMPAT-2's unknown-field rule spelled
 * for a vocabulary that will grow.
 */
class ToolCardTest {

    private fun toolItem(
        id: String = "itm-1",
        toolKind: String = "execute",
        turnId: String = "turn-A",
        tsUnixMs: Long = 1_755_300_000_000L,
        status: String = "completed",
        text: String = "",
        truncated: Boolean = false,
        detail: Boolean = false,
    ) = InteractionItem(
        sessionId = "m/s1", itemId = id, cursor = 1L, kind = "tool_run", status = status,
        text = text, truncated = truncated, detail = detail,
        toolKind = toolKind, turnId = turnId, tsUnixMs = tsUnixMs,
    )

    // ---- glyphs -------------------------------------------------------------

    @Test
    fun everyKindInTheVocabularyHasItsOwnGlyph() {
        val kinds = listOf("read", "edit", "write", "search", "execute", "fetch", "other")
        val glyphs = kinds.map { ToolCard.glyphFor(it) }
        for (g in glyphs) assertTrue("a glyph is never empty", g.isNotEmpty())
        assertEquals(
            "distinct kinds draw distinct glyphs; a shared glyph makes two different acts look alike",
            glyphs.size, glyphs.toSet().size,
        )
    }

    @Test
    fun anUnknownKindFallsBackToOtherNeverInvents() {
        assertEquals(ToolCard.glyphFor("other"), ToolCard.glyphFor("some_future_kind"))
    }

    // ---- expand/collapse ----------------------------------------------------

    @Test
    fun collapsedHidesTheWellExpandedShowsIt() {
        val item = toolItem(text = "stdout line 1\nstdout line 2")
        assertFalse("collapsed by default: a burst of tool cards must scan as one line each",
            ToolCard.modelFor(item, expanded = false).wellVisible)
        val open = ToolCard.modelFor(item, expanded = true)
        assertTrue(open.wellVisible)
        assertEquals("stdout line 1\nstdout line 2", open.well)
    }

    @Test
    fun aTruncatedDetailItemOffersTheFullOutputFetch() {
        val open = ToolCard.modelFor(toolItem(text = "clipped...", truncated = true, detail = true), expanded = true)
        assertTrue(
            "IS-TOOL-3/IS-CAP-2: a truncated card never claims to hold the whole output; it " +
                "offers the detail fetch instead",
            open.offersDetail,
        )
        val whole = ToolCard.modelFor(toolItem(text = "all of it"), expanded = true)
        assertFalse("an untruncated card offers no fetch for bytes it already holds", whole.offersDetail)
    }

    // ---- timestamps ---------------------------------------------------------

    @Test
    fun aRealTimestampGetsALabelAndAnAbsentOneStaysAbsent() {
        assertTrue(ToolCard.timestampLabel(1_755_300_000_000L).isNotEmpty())
        assertEquals(
            "ts 0 is an absent fact, not the epoch; inventing 1970 on screen is worse than nothing",
            "", ToolCard.timestampLabel(0L),
        )
    }

    // ---- turn separators ----------------------------------------------------

    @Test
    fun theSeparatorDrawsExactlyOnTurnBoundaries() {
        val a1 = toolItem(id = "i1", turnId = "turn-A")
        val a2 = toolItem(id = "i2", turnId = "turn-A")
        val b1 = toolItem(id = "i3", turnId = "turn-B")
        assertFalse("no separator above the first item", ToolCard.separatorBefore(null, a1))
        assertFalse("no separator inside a turn", ToolCard.separatorBefore(a1, a2))
        assertTrue("a separator where turn_id changes (M2.2)", ToolCard.separatorBefore(a2, b1))
    }

    @Test
    fun itemsOutsideAnyTurnDrawNoSeparator() {
        val bare1 = toolItem(id = "i4", turnId = "")
        val bare2 = toolItem(id = "i5", turnId = "")
        assertFalse(
            "an empty turn_id is 'outside a turn' (interaction-schema.md §2), not a turn of its own",
            ToolCard.separatorBefore(bare1, bare2),
        )
        assertTrue(
            "entering a turn from outside one still draws the boundary",
            ToolCard.separatorBefore(bare2, toolItem(id = "i6", turnId = "turn-C")),
        )
    }
}
