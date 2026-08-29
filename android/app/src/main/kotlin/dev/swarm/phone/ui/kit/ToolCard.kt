package dev.swarm.phone.ui.kit

import dev.swarm.phone.ui.InteractionItem
import java.text.SimpleDateFormat
import java.util.Date
import java.util.Locale
import java.util.TimeZone

/**
 * Mirror M2.2 (Wave R6) -- the tool card as a kit MODEL: the glyph vocabulary, the
 * collapsed/expanded shape, the timestamp label, and the turn-separator rule. Pure JVM, no
 * Context and no View: composition stays with the screen (PB-DS-9), and the model is what
 * ToolCardTest drives.
 *
 * THE GLYPH READS ONE FLAT FIELD. IS-TOOL-1 forbids the phone parsing `tool` or arguments to
 * infer an action, and the same posture holds one hop later: the card picks its glyph from
 * [InteractionItem.toolKind] (interaction-schema.md §7's vocabulary, journalled flat by the
 * machine as §3.3 `tool_kind`) and never re-derives it from Body -- which is why this file
 * mentions no JSON (android/gate/r6_chat_ui_test.go fences that). An unknown value renders
 * as `other`'s glyph: IS-COMPAT-2's unknown-field rule spelled for a vocabulary that will
 * grow.
 */
object ToolCard {

    /**
     * §7's closed vocabulary, one distinct text glyph each. ASCII on purpose: the glyph is a
     * MODEL fact the screen styles (mono, ink2), and a pictographic character would be copy
     * this repository's no-emoji rule forbids.
     */
    private val glyphs = mapOf(
        "read" to "R",
        "edit" to "E",
        "write" to "W",
        "search" to "/",
        "execute" to "$",
        "fetch" to "@",
        "agent" to "A",
        // A tool, not a question: the phone never draws "?" (phone-refit-playbook W6.1).
        "other" to "T",
    )

    /** The glyph for one `tool_kind`. Unknown falls back to `other`'s -- never invented. */
    fun glyphFor(toolKind: String): String = glyphs[toolKind] ?: glyphs.getValue("other")

    /**
     * One card's rendered shape at a given expansion. [well] is the output the expanded card
     * shows in its mono well; [offersDetail] is IS-TOOL-3/IS-CAP-2's honest affordance -- a
     * truncated card whose full body the machine retains OFFERS the fetch, and an untruncated
     * card never promises bytes it already holds.
     */
    data class Model(
        val glyph: String,
        val wellVisible: Boolean,
        val well: String,
        val offersDetail: Boolean,
    )

    /** The model for one tool_run row. Collapsed hides the well: a burst scans one line each. */
    fun modelFor(item: InteractionItem, expanded: Boolean): Model = Model(
        glyph = glyphFor(item.toolKind),
        wellVisible = expanded,
        well = if (expanded) item.text else "",
        offersDetail = expanded && item.truncated && item.detail,
    )

    /**
     * The timestamp label for §2's `ts`, or "" for the absent fact: 0 is NOT the epoch, and
     * inventing 1970 on screen is worse than nothing (PB-APP-11's clock honesty one field
     * over). The format is the reader's wall clock, hour and minute -- a transcript is read
     * within its day, and a date would repeat on every row.
     */
    fun timestampLabel(tsUnixMs: Long): String {
        if (tsUnixMs <= 0L) return ""
        val fmt = SimpleDateFormat("HH:mm", Locale.getDefault())
        fmt.timeZone = TimeZone.getDefault()
        return fmt.format(Date(tsUnixMs))
    }

    /**
     * M2.2's separator rule: a rule draws EXACTLY where `turn_id` changes. No separator above
     * the first item, none inside a turn, and none between items OUTSIDE any turn -- an empty
     * turn_id is "outside a turn" (interaction-schema.md §2), not a turn of its own -- while
     * entering a turn from outside one still draws the boundary.
     */
    fun separatorBefore(previous: InteractionItem?, item: InteractionItem): Boolean {
        if (previous == null) return false
        if (item.turnId.isEmpty()) return false
        return previous.turnId != item.turnId
    }
}
