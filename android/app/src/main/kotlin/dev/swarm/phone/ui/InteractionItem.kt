package dev.swarm.phone.ui

import org.json.JSONArray
import org.json.JSONObject

/**
 * ADR-009-structured-chat-interaction (1) -- one folded interaction item, as this app holds it.
 *
 * It is the Kotlin shape of `swarmmobile.TranscriptItem`, which is itself the bound shape of
 * `phonecore.Item`: every record that carried one `item_id`, merged by the rules of
 * `docs/specifications/interaction-schema.md` §6. The phone does not fold anything; the fold is
 * durable in the Go core and survives the process death Android hands out routinely.
 *
 * [text] AND [body] ARE NOT REDUNDANT, and `phonecore.Item`'s own KDoc is where the distinction is
 * argued: `body` is the LATEST record's item object verbatim, `text` is the RECONSTRUCTION. For a
 * streamed `agent_message` the latest record carries only the last increment (IS-DELTA-1), so a
 * client that read `text` out of `body` would render the tail of a message as the whole of it.
 * [fields] therefore never reads `text`, and [TranscriptScreen] takes it from here.
 *
 * IT CARRIES NO ARRIVAL TIME. `ts` is on the item and PB-APP-11's clock rule forbids substituting
 * the moment a record was received for the moment the machine says it happened -- so a transcript
 * that wanted a timestamp would have to carry `ts` and decide a format. Neither is done here: the
 * time cell is `activityRow`'s and it is optional, and no ruling exists for what a transcript
 * timestamp reads as. Recorded rather than filled in, which is this package's rule for a fact the
 * design has not decided.
 */
data class InteractionItem(
    /** The session this item belongs to. `App.Approve` takes it beside the item id. */
    val sessionId: String = "",
    /** §3.5/IS-APR-1: the item's `item_id`, which **is** ADR-007 D7's `interaction_id`. */
    val itemId: String,
    /** IS-LAYER-3's ordering cursor: the item's position in the transcript, ascending. */
    val cursor: Long,
    /** §3's item kind, verbatim. A kind this build does not know costs a row and nothing else. */
    val kind: String,
    /** §4's status, or empty where the kind has none. */
    val status: String = "",
    /** The fold's reconstruction -- see the class KDoc. Empty for kinds that carry no text. */
    val text: String = "",
    /**
     * The latest record's item object, as JSON.
     *
     * IT CROSSES AS A STRING BECAUSE GOMOBILE BINDS NO MAP AND NO VARIANT TYPE (`mobile/types.go`),
     * which is the same limit that makes `Snapshot.Text` a joined string. The consequence is the
     * one §9 wants: an unknown kind and an unknown field are both free on this boundary, because
     * nothing between the producer and here has a schema to reject them against.
     */
    val body: String = "",
    /** §2: some field of this item was clipped to a §5 cap. */
    val truncated: Boolean = false,
    /** IS-COMPAT-4: the item's `v` is higher than this build's. Rendered, never dropped. */
    val degraded: Boolean = false,
    /** IS-LIFE-2: an `approval_request` whose `approval_resolved` has been folded. */
    val resolved: Boolean = false,
    /**
     * IS-CAP-2 (Wave R6, M2.2/M3.3): the machine retains this item's FULL pre-truncation body,
     * so an expanded truncated card may OFFER the detail fetch. False means there is nothing to
     * fetch and the card must not promise one.
     */
    val detail: Boolean = false,
    /**
     * §3.3's `tool_kind` (Wave R6, M2.2): §7's action type mirrored flat, so [ToolCard] picks a
     * glyph from ONE field and parses nothing (IS-TOOL-1). Empty where the wire carried none.
     */
    val toolKind: String = "",
    /** IS-ENV-1's turn (Wave R6, M2.2): the separator draws exactly where this changes. */
    val turnId: String = "",
    /**
     * §2's `ts` as epoch milliseconds (Wave R6, M2.2). 0 is an ABSENT fact, never the epoch:
     * PB-APP-11 forbids substituting arrival time, and the label renders nothing for 0.
     */
    val tsUnixMs: Long = 0L,
    /**
     * §3.1's `source` (Wave R6, M2.4): honest phone-vs-terminal attribution, daemon-stamped at
     * injection time. Empty where the wire carried none -- never invented.
     */
    val source: String = "",
) {

    /**
     * This item's own §3 fields, decoded.
     *
     * IT COMPOSES NO SENTENCE. Every value below is the wire's, verbatim; which of them join, in
     * what order and with what words between them is copy, and PB-DS-9 gives copy to the screen.
     * The split is also what `android/gate/i1_sheetandwell_test.go` fences one file over: a screen
     * that parsed JSON would be a composition that also owned a wire format.
     *
     * A BODY THIS SIDE CANNOT READ YIELDS EMPTY FIELDS RATHER THAN AN EXCEPTION (IS-ENV-3,
     * IS-COMPAT-2). The failure that rule is guarding against is precise: `PhoneEvents` posts a
     * redraw to the main looper on every interaction event, so one malformed item thrown from a
     * render would take the app down on the ordinary path of watching an agent work.
     */
    fun fields(): ItemFields = try {
        decode(JSONObject(body))
    } catch (unreadable: Exception) {
        ItemFields()
    }
}

/**
 * §3's per-kind fields, flattened.
 *
 * ONE RECORD RATHER THAN ONE TYPE PER KIND, and the reason is what a transcript does with them:
 * every kind renders into the same three things -- a line, the span inside it worth marking, and
 * an optional mono block -- so a sealed hierarchy would be eight types the renderer immediately
 * flattens again. `ApprovalItem` is the exception and stays its own type, because a card is not a
 * line: it carries buttons, an id a signed command names, and a fallback mode.
 */
data class ItemFields(
    /** §3.3: the adapter-reported tool name (`Read`, `Bash`, `Edit`, ...). */
    val tool: String = "",
    /**
     * §7's action literal: the one field the adapter filled, whichever it was.
     *
     * THE FIRST NON-EMPTY OF `command`/`path`/`query` IS THE ACTION'S LITERAL, because §7 gives
     * each `type` exactly one filled field. Nothing is composed out of two of them: that would be
     * this side deciding what the tool did, which IS-TOOL-1 puts on the machine. An unclassified
     * call (IS-TOOL-2's `other`) fills none and this is empty, which is what makes the card fall
     * back to [tool] rather than guess.
     */
    val target: String = "",
    /** §3.3's `output_excerpt`. */
    val output: String = "",
    /** §3.3's `truncation_marker`: the CLI's own text, carried VERBATIM per IS-TOOL-3. */
    val marker: String = "",
    /** §3.4: `create` | `modify` | `delete` | `rename`. */
    val change: String = "",
    /** §3.4's `path`, and §3.4's `old_path` on a rename. */
    val path: String = "",
    val oldPath: String = "",
    /** §3.4's line counts, of the WHOLE change rather than of the excerpt. */
    val added: Int = 0,
    val removed: Int = 0,
    /** §3.4's `diff_excerpt`: unified diff text the producer already normalized. */
    val diff: String = "",
    /** §3.7's steps, latest revision only -- IS-PLAN-1 discards a late lower one in the core. */
    val steps: List<PlanStep> = emptyList(),
    /** §3.6: `allowed` | `denied` | `cancelled` | `superseded` | `expired` | `answered_locally`. */
    val decision: String = "",
    /** §3.6's `by`: `phone` | `owner` | `daemon` | `agent`. */
    val by: String = "",
    /** §3.8's three dimensions and its neutral marker. */
    val process: String = "",
    val turn: String = "",
    val interaction: String = "",
    val note: String = "",
)

/** §3.7: one step of a plan, and where it has got to. */
data class PlanStep(val text: String, val state: String)

private fun decode(item: JSONObject): ItemFields = ItemFields(
    tool = item.optString(TOOL),
    target = actionOf(item.optJSONObject(ACTION)),
    output = item.optString(OUTPUT_EXCERPT),
    marker = item.optString(TRUNCATION_MARKER),
    change = item.optString(CHANGE),
    path = item.optString(PATH),
    oldPath = item.optString(OLD_PATH),
    added = item.optInt(ADDED),
    removed = item.optInt(REMOVED),
    diff = item.optString(DIFF_EXCERPT),
    steps = stepsOf(item.optJSONArray(STEPS)),
    decision = item.optString(DECISION),
    by = item.optString(BY),
    process = item.optString(PROCESS),
    turn = item.optString(TURN),
    interaction = item.optString(INTERACTION),
    note = item.optString(NOTE),
)

/** §7's four fields, read the way `ApprovalItem` already reads them. See [ItemFields.target]. */
private fun actionOf(action: JSONObject?): String {
    if (action == null) return ""
    for (field in listOf(COMMAND, PATH, QUERY)) {
        val value = action.optString(field)
        if (value.isNotEmpty()) return value
    }
    return ""
}

/**
 * §3.7's `steps`, as `{text, state}`.
 *
 * A STEP WITH NO TEXT IS DROPPED, on `ApprovalDecision`'s precedent: the only string this side
 * could put on one is its state, which would render a plan whose every line said `pending`.
 */
private fun stepsOf(array: JSONArray?): List<PlanStep> {
    if (array == null) return emptyList()
    val out = mutableListOf<PlanStep>()
    for (i in 0 until array.length()) {
        val step = array.optJSONObject(i) ?: continue
        val text = step.optString(TEXT)
        if (text.isEmpty()) continue
        out.add(PlanStep(text = text, state = step.optString(STATE)))
    }
    return out
}

// §3 and §7's key names, spelled once. They are the WIRE's, so they are snake_case and are not
// translated on the way in.
private const val TOOL = "tool"
private const val ACTION = "action"
private const val COMMAND = "command"
private const val PATH = "path"
private const val QUERY = "query"
private const val OUTPUT_EXCERPT = "output_excerpt"
private const val TRUNCATION_MARKER = "truncation_marker"
private const val CHANGE = "change"
private const val OLD_PATH = "old_path"
private const val ADDED = "added"
private const val REMOVED = "removed"
private const val DIFF_EXCERPT = "diff_excerpt"
private const val STEPS = "steps"
private const val TEXT = "text"
private const val STATE = "state"
private const val DECISION = "decision"
private const val BY = "by"
private const val PROCESS = "process"
private const val TURN = "turn"
private const val INTERACTION = "interaction"
private const val NOTE = "note"

/**
 * One page of a session's transcript: the items, where the NEXT page starts, and whether the
 * stream they came from has a hole.
 *
 * IT IS A PAGE AND NOT A LIST for [JournalPageView]'s reason, restated by the handle itself:
 * `TranscriptPage.NextCursor` is the only thing that says where to read from next, and `Stale` is
 * PB-APP-8 over a surface that reads as a CHRONOLOGY -- a conversation with an unrepaired hole
 * shown as a plain list tells the user the agent said nothing in the gap.
 *
 * The staleness is the JOURNAL's, because an interaction item IS a journal record (IS-LAYER-1) and
 * inherits that repair channel rather than creating one (IS-LAYER-4).
 */
data class TranscriptPageView(
    val items: List<InteractionItem>,
    /** Feed this back as the next read's `afterCursor`. Unchanged when the page is empty. */
    val nextCursor: Long,
    val stale: Boolean,
)
