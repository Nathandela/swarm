package dev.swarm.phone.ui.screens

import dev.swarm.phone.ui.ApprovalItem
import dev.swarm.phone.ui.InteractionItem
import dev.swarm.phone.ui.ItemFields

/**
 * ADR-009-structured-chat-interaction (1) -- the chat transcript's SCREEN MODEL: what a session
 * READS AS on the handset.
 *
 * THIS IS THE SURFACE THAT REPLACES THE GRID, and the ADR's reason for it is worth carrying here
 * rather than leaving in the document: "a character grid is the machine's view of an agent, shrunk
 * onto a handset. The thing the owner needs from a phone is not a smaller terminal. It is the two
 * questions a terminal makes them reconstruct by eye -- what is the agent doing, and does it need
 * me to say yes." Every rule below is one of those two questions answered from a structured item
 * instead of from pixels.
 *
 * IT WRITES THE COPY AND JOINS THE FIELDS; IT DECODES NOTHING. `InteractionItem.fields()` reads the
 * wire and composes no sentence; this composes the sentence and reads no JSON. PB-DS-9 draws that
 * line and `android/gate/i1_sheetandwell_test.go` fences it one file over.
 *
 * ## Three rules that hold across every kind
 *
 * **The wire's words, verbatim, wherever the wire has words.** A `change` is `modify`, a step's
 * state is `in_progress`, a resolution is `allowed`. `TriageInbox` states the rule for the need
 * line and `ApprovalSheetPanel` restates it for the question: a table turning wire tokens into
 * English would have to fail loudly on a value it did not know, and a machine that added one would
 * take this screen down at the moment it is being read. What this file writes is the SEPARATORS and
 * the one word the wire has no field for -- who "you" are.
 *
 * **A kind this build cannot render costs one row and nothing else.** IS-COMPAT-1 makes an unknown
 * kind a skip rather than a gap, IS-COMPAT-2 makes an unknown field free, and IS-ENV-3 makes an
 * unreadable item a skip that still advances. All three land in the same place here: the neutral
 * row, which says the kind and stops. The failure being guarded against is not cosmetic --
 * `PhoneEvents` posts a redraw to the main looper on every interaction event, so one item that
 * threw from a render would kill the app while the user watched an agent work.
 *
 * **Oldest first, which reverses the journal's rule on purpose.** `ActivityPanelScreen` and the
 * old session log put the newest record at the top, because a log is read from the top. A
 * CONVERSATION is read in the order it was said: newest-first would put the agent's answer above
 * the question it answers. `App.ReadTranscript` already walks the fold in ascending cursor order
 * (IS-LAYER-3), so this keeps that order rather than reversing it.
 *
 * ## What it deliberately does not draw
 *
 * **A timestamp.** `activityRow` has the cell and it is optional; the item has `ts`; and PB-APP-11
 * forbids substituting arrival time for it. What is missing is a ruling on what a transcript time
 * READS as -- absolute, relative, per-turn -- and inventing one here would put a format nobody
 * decided on the app's densest surface. Recorded rather than filled in.
 *
 * **A turn boundary.** `turn_id` groups items into one turn (IS-ENV-1) and a chat could rule a
 * separator off it. There is no kit component for a divider, and adding one is a design decision
 * rather than a composition; the transcript reads in order without it.
 *
 * **`incomplete from join`.** IS-DELTA-4 asks a client that holds no earlier record for an
 * `in_progress` item to render an explicit leading elision. The phone core folds items and does not
 * report whether it joined mid-item, so there is nothing here to key on; a mark drawn from
 * `status == in_progress` alone would elide the front of every message still being streamed.
 */
data class TranscriptPanel(
    /** The heading over the conversation. */
    val heading: String,
    /** One block per item, oldest first. */
    val blocks: List<TranscriptBlock>,
    /** PB-DS-9: what an empty section says, because an empty section is still a section. */
    val emptyCopy: String,
)

/**
 * One item as the transcript renders it.
 *
 * THREE PARTS AND NOT EIGHT SHAPES. Every kind lands in the same row: a line, the span inside it
 * worth the eye, and an optional block of machine-authored literal underneath. That is §2's reuse
 * rule taken seriously -- `activityRow` is documented as taking "a body and an optional emphasis
 * rather than a JournalRow" precisely so a new caller costs no new component, and `monoWell` is
 * "one component for every mono block in the app".
 */
data class TranscriptBlock(
    /** IS-APR-1: the `item_id`, which is what a signed `ActionApprove` names as `interaction_id`. */
    val itemId: String,
    /** §3's kind, verbatim. Carried so the view can tag what it drew. */
    val kind: String,
    /** The sentence, as this screen writes it. */
    val line: String,
    /** The span of [line] that carries row 14's inline mono, or null where nothing is marked. */
    val emphasis: String? = null,
    /**
     * The machine's own literal: a tool's output, a unified diff. Empty draws no well.
     *
     * IT IS NEVER A GRID. ADR-009 (1) leaves no terminal emulation and no raw grid anywhere in the
     * app; this is excerpt text the producer already normalized, printed in the mono block every
     * other mono block in this app is printed in.
     */
    val well: String = "",
    /**
     * Whether this block is an approval the user can still answer.
     *
     * IT IS `approval_request` AND NOT RESOLVED. IS-LIFE-2 guarantees every request reaches exactly
     * one resolution "including when it is cancelled, superseded, expired, or answered at the
     * machine", and says what that guarantee buys: a stale card dismisses on every surface. So a
     * resolved request stays in the transcript -- it is what was asked -- and stops being a
     * decision.
     */
    val approval: Boolean = false,
)

/** The transcript, as a pure function over the items the phone holds. */
object TranscriptScreen {

    /** The heading over a session's conversation. It is a conversation, so it says so. */
    private const val HEADING = "Conversation"

    /**
     * What the transcript says when this phone holds no items for the session.
     *
     * IT SAYS NOTHING HAS REACHED THIS PHONE, NOT THAT NOTHING WAS SAID -- the distinction
     * `SessionDetailScreen` drew for the journal, and it is sharper for a conversation: a
     * transcript is opened from a row that exists, so the user KNOWS the session is real, and a
     * screen claiming the agent has said nothing would be a claim about the MACHINE that a phone
     * holding no items is in no position to make.
     */
    private const val EMPTY = "No messages for this session have reached this phone yet."

    /**
     * Who the human is, and the one word on this screen the wire has no field for.
     *
     * §3.1's `source` is `phone` | `owner` | `derived` -- where the message was typed and how it
     * was captured, not who typed it. All three are the same person: the owner of the machine,
     * holding this phone. So the attribution is one word rather than a table over a field that
     * answers a different question.
     */
    private const val YOU = "You"

    /** The separator between two of the wire's own values. `SessionDetailScreen` set the idiom. */
    private const val SEPARATOR = " · "

    /** A rename's two paths, in the direction the file moved. */
    private const val MOVED_TO = "->"

    /** §3.4's line counts, in the notation a diff already uses. */
    private const val ADDED_MARK = "+"
    private const val REMOVED_MARK = "-"

    /** §2's `truncated`: this item was clipped to a §5 cap and is not the whole of what was said. */
    private const val CLIPPED = "…"

    /**
     * IS-COMPAT-4's mark: the item's `v` is higher than this build understands.
     *
     * "Render what it understands and mark the item degraded. It SHALL NOT drop the transcript and
     * SHALL NOT error the connection." The mark is a phrase rather than a symbol because the reader
     * has to be able to act on it, and what they do about it is update the phone.
     */
    private const val DEGRADED = "from a newer version of swarm"

    fun of(items: List<InteractionItem>): TranscriptPanel = TranscriptPanel(
        heading = HEADING,
        // THE WIRE'S ORDER, KEPT. See the file KDoc: a conversation is read in the order it was
        // said, and `App.ReadTranscript` already walks the fold by ascending cursor.
        blocks = items.map(::blockFor),
        emptyCopy = EMPTY,
    )

    private fun blockFor(item: InteractionItem): TranscriptBlock {
        val fields = item.fields()
        val block = when (item.kind) {
            USER_MESSAGE -> TranscriptBlock(
                itemId = item.itemId,
                kind = item.kind,
                line = joined(YOU, item.text),
                emphasis = YOU,
            )

            // THE AGENT'S WORDS ARE THE TRANSCRIPT'S DEFAULT VOICE, so they carry no attribution
            // and no marked span: this screen exists to read as the conversation, and a marker on
            // every second line would be a label on the thing the screen is made of. The user's
            // own lines are marked because they are the interjections.
            AGENT_MESSAGE -> TranscriptBlock(
                itemId = item.itemId,
                kind = item.kind,
                line = item.text,
            )

            TOOL_RUN -> TranscriptBlock(
                itemId = item.itemId,
                kind = item.kind,
                // "Read src/main.rs" -- the adapter's tool and the action's own literal. An
                // unclassified call (IS-TOOL-2) fills no literal and this is the tool alone.
                line = phrase(fields.tool, fields.target),
                emphasis = fields.target,
                // IS-TOOL-3: the CLI's marker rides with the excerpt, verbatim, so the card never
                // claims to hold output it only saw a marker for.
                well = lines(fields.output, fields.marker),
            )

            FILE_CHANGE -> TranscriptBlock(
                itemId = item.itemId,
                kind = item.kind,
                line = joined(fields.change, movedPath(fields), sizeOf(fields)),
                emphasis = fields.path,
                well = fields.diff,
            )

            PLAN_UPDATE -> TranscriptBlock(
                itemId = item.itemId,
                kind = item.kind,
                // ONE BLOCK AND NOT ONE ROW PER STEP. IS-PLAN-1 makes a plan_update latest-state
                // rather than incremental -- the item IS the whole plan -- so splitting it into
                // rows would draw a plan the reader has to reassemble, and would let the next
                // item's row land between two of its steps.
                line = fields.steps.joinToString(separator = "\n") { joined(it.state, it.text) },
            )

            APPROVAL_REQUEST -> approvalBlock(item)

            APPROVAL_RESOLVED -> TranscriptBlock(
                itemId = item.itemId,
                kind = item.kind,
                // WHO ANSWERED IS HALF THE FACT. IS-LIFE-2 resolves a request whoever answered it,
                // and an approval the owner took at the machine, one this phone sent, and one the
                // daemon expired are three different things to have happened.
                line = joined(fields.decision, fields.by),
            )

            SESSION_STATUS -> TranscriptBlock(
                itemId = item.itemId,
                kind = item.kind,
                // THE NOTE REPLACES THE STATE LINE RATHER THAN JOINING IT. §3.8 calls `note` "a
                // neutral transcript marker", which is a sentence the machine wrote for a reader;
                // the three dimensions are a state for a screen. Printing both would put the
                // machine's sentence next to a restatement of what it is about.
                line = fields.note.ifEmpty {
                    joined(fields.process, fields.turn, fields.interaction)
                },
            )

            else -> neutral(item)
        }
        return marked(block, item)
    }

    /**
     * An `approval_request`, decoded by the model the sheet already uses.
     *
     * IT IS `ApprovalItem`'s DECODE AND NOT A SECOND ONE. The card in this transcript and the sheet
     * it opens are two renderings of one item, and two decoders would be two places for "what this
     * approval is about" to be answered differently -- including the rule that matters most, which
     * is that a request offering no decisions is not answerable at all.
     */
    private fun approvalBlock(item: InteractionItem): TranscriptBlock {
        val card = ApprovalItem.of(item.sessionId, item.itemId, item.body) ?: return neutral(item)
        return TranscriptBlock(
            itemId = item.itemId,
            kind = item.kind,
            line = card.summary,
            approval = !item.resolved,
        )
    }

    /**
     * The row for an item this build cannot render: the kind, and nothing invented.
     *
     * IT SAYS THE ONE THING THE ENVELOPE GUARANTEES. IS-ENV-3 requires `kind` on every item a
     * producer emits, so this is the only field a row is certain of -- and a row saying the kind is
     * how a reader finds out their phone is behind their machine rather than that their agent went
     * quiet.
     */
    private fun neutral(item: InteractionItem) = TranscriptBlock(
        itemId = item.itemId,
        kind = item.kind,
        line = item.kind,
    )

    /**
     * §2's two envelope marks, applied to whatever the kind rendered.
     *
     * AND THE EMPTY-LINE FALLBACK, which is here rather than in eight places: a kind whose fields
     * were all empty -- an unreadable body, a producer that sent an item with nothing in it --
     * renders as the neutral row rather than as a card with no words on it. A blank row is the one
     * outcome that tells the reader nothing at all.
     */
    private fun marked(block: TranscriptBlock, item: InteractionItem): TranscriptBlock {
        // THE FALLBACK KEEPS THE BLOCK AND REPLACES ONLY ITS WORDS. Rebuilding it as [neutral]
        // would drop what the kind decided about it -- an `approval_request` whose summary the
        // producer left empty would stop being answerable, which is the one property on this
        // screen that a rendering detail must never be able to take away.
        val base = if (block.line.isEmpty()) block.copy(line = item.kind) else block
        var line = base.line
        if (item.truncated) line += CLIPPED
        if (item.degraded) line = joined(line, DEGRADED)
        return base.copy(line = line, emphasis = spanIn(line, base.emphasis))
    }

    /**
     * The marked span, or null when there is nothing to mark.
     *
     * IT CHECKS CONTAINMENT BECAUSE `activityRow` FAILS LOUDLY WITHOUT IT -- its own words: "a
     * caller that names a span not in the sentence has a copy bug, and this fails loudly rather
     * than rendering the row unemphasised". That is the right behaviour for a copy bug and the
     * wrong one for a wire value: a `path` the machine sent that this screen did not put in the
     * line is data, not a bug, and it must not take the transcript down.
     */
    private fun spanIn(line: String, span: String?): String? =
        span?.takeIf { it.isNotEmpty() && line.contains(it) }

    /** A rename's two paths; every other change has only the one. */
    private fun movedPath(fields: ItemFields): String =
        if (fields.oldPath.isEmpty()) fields.path else phrase(fields.oldPath, MOVED_TO, fields.path)

    /** §3.4's counts, of the whole change. Absent when the producer sent none. */
    private fun sizeOf(fields: ItemFields): String =
        if (fields.added == 0 && fields.removed == 0) {
            ""
        } else {
            phrase("$ADDED_MARK${fields.added}", "$REMOVED_MARK${fields.removed}")
        }

    /** The wire's values, separated. An empty one is left out rather than leaving a hanging dot. */
    private fun joined(vararg parts: String): String =
        parts.filter { it.isNotEmpty() }.joinToString(SEPARATOR)

    /** The same, for values that read as one phrase rather than as a list of facts. */
    private fun phrase(vararg parts: String): String =
        parts.filter { it.isNotEmpty() }.joinToString(" ")

    /** The same, for a mono block, where the machine's own line breaks are the structure. */
    private fun lines(vararg parts: String): String =
        parts.filter { it.isNotEmpty() }.joinToString("\n")

    // §3's eight kinds, spelled once. They are the WIRE's values and are matched, never rendered
    // as English -- see the file KDoc.
    private const val USER_MESSAGE = "user_message"
    private const val AGENT_MESSAGE = "agent_message"
    private const val TOOL_RUN = "tool_run"
    private const val FILE_CHANGE = "file_change"
    private const val APPROVAL_REQUEST = "approval_request"
    private const val APPROVAL_RESOLVED = "approval_resolved"
    private const val PLAN_UPDATE = "plan_update"
    private const val SESSION_STATUS = "session_status"
}
