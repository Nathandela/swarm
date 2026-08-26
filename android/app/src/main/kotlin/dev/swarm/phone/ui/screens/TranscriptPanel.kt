package dev.swarm.phone.ui.screens

import dev.swarm.phone.ui.ApprovalItem
import dev.swarm.phone.ui.InteractionItem
import dev.swarm.phone.ui.ItemFields
import dev.swarm.phone.ui.kit.Markdown
import dev.swarm.phone.ui.kit.MarkdownBlock
import dev.swarm.phone.ui.kit.ToolCard

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
    /**
     * IS-ENV-1's OPEN turn (Wave R6, M2.4 / review finding B7, corrected in review round 2).
     *
     * BOTH HALVES OF THE RACE READ IT. `App.ComposerSend` and `App.Interrupt` each REQUIRE the
     * turn the screen drew them against -- a tap lands later than it was rendered, and the daemon
     * refuses `stale_turn` rather than typing into a turn nobody meant.
     *
     * **IT USED TO BE THE LATEST ITEM'S `turn_id`, AND THAT IS THE CLOSED TURN.** `turnIDLocked`
     * reads the open turn, STAMPS it onto the terminal `agent_message`, and THEN deletes it -- so
     * once a turn ends the last item still carries turn A while the daemon's current turn is the
     * empty string, and protocol.md states the matching half in as many words: "an idle session
     * is matched by the EMPTY expected_turn". The phone therefore named a turn that was over on
     * every idle session, and `composerSend` refused `stale_turn` 100% of the time: the agent
     * finishes, you read the answer, you reply from the phone, refused -- the ORDINARY path, and
     * re-reading the transcript (the refusal's own stated remedy) could not change the answer.
     *
     * So this mirrors IS-ENV-1's rule instead of sampling a row: a turn OPENS on a `user_message`
     * and CLOSES on any terminal `agent_message`. Empty means no turn is open -- an idle session
     * -- which is the value the daemon matches, never a default this screen invented.
     */
    val latestTurnId: String = "",
    /**
     * The oldest item this phone holds for the session -- ADR-014's `before_item`.
     *
     * AN ITEM ID AND NEVER A CURSOR (IS-ENV-2). A daemon restart's reconciliation legitimately
     * re-delivers the same items at new cursors, so a cursor-paged read would silently skip or
     * repeat after one.
     *
     * AND NEVER A TEAR'S SYNTHETIC ID (review round 2). `applyStructuredGapLocked` mints
     * `structured_gap:<ts>` for the boundary element finding B4 made first-class, and no producer
     * ever put that id on an item: on the daemon `historyItemID` answers "" for every
     * non-`interaction` record, so the boundary scan cannot match it and `interactionHistory`
     * refuses `invalid_field`. It was reachable whenever the oldest element this phone held was a
     * tear -- a reseed floor cutting just before a proven gap -- and PERMANENT once reached,
     * because only a successful page could change which element is oldest. Empty means there is
     * nothing this phone can name, and the control is not offered.
     */
    val oldestItemId: String = "",
    /**
     * Whether "load earlier" is worth offering: there is an item this phone can page BEFORE, the
     * machine has not said that nothing older is retained (ADR-014 §2's honest floor), and this
     * phone can still hold what a page would deliver. At any of those the control is DROPPED
     * rather than greyed -- a tap that can only ever come back empty is the dead-chevron defect
     * (agents-tracker-2yb) wearing a page.
     */
    val offersLoadEarlier: Boolean = false,
    /**
     * ADR-017 T2 rule 2: this session's structured record is PROVEN torn.
     *
     * IT IS READ OFF THE TRANSCRIPT because that is where the proof is. The daemon's degrade is
     * one-way and durable, and the phone's only sight of it is the `structured_gap` element the
     * daemon authored -- there is no capability read on this facade (Wave R6 disclosed residual,
     * docs/verification/r6-chat.md). So a session holding a gap has no structured composer, and
     * one whose gap the retention bound has since evicted gets the daemon's own
     * `structured_unsupported` refusal instead, visibly.
     */
    val structureTorn: Boolean = false,
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
    /**
     * Whether this is a `tool_run` still `in_progress` -- the block a reader cannot tell is
     * still live from one that has already finished (agents-tracker-dwwv.1.2).
     *
     * `InteractionItem.status` (§2/§4) crossed the wire and was READ BY NOTHING: populated at
     * `FacadeBridge.kt:120`, decoded nowhere in this screen. A completed tool and a running one
     * rendered the identical row, and the mono well carried the same nothing either way. This is
     * the flag [approval]'s own comment argues for a DIFFERENT reason -- IT IS ITS OWN FIELD AND
     * NOT READ OFF [well]'s COPY, so a caller (a test, or M2's card) can ask "is this block still
     * running" without re-parsing the sentence the screen wrote for a human.
     */
    val running: Boolean = false,
    /**
     * M2.2's glyph: `ToolCard.glyphFor(tool_kind)`, or "" for a row that is not a tool call.
     *
     * IT IS ITS OWN FIELD AND NOT PART OF [line] for the reason [emphasis] is its own field: the
     * sentence is copy, and a symbol inside copy is a symbol the recorded crossing pins byte for
     * byte. `activityRow` draws it as a leading cell.
     */
    val glyph: String = "",
    /**
     * M2.2's timestamp, formatted by `ToolCard.timestampLabel` -- and EMPTY except at a turn
     * boundary. See [turnStart]: a time on every row is the noise that makes a boundary invisible.
     */
    val timestamp: String = "",
    /**
     * `ToolCard.separatorBefore`: this row starts a new turn (IS-ENV-1).
     *
     * IT IS SPENT AS THE TIMESTAMP rather than as a rule across the column, and that is a design
     * decision recorded rather than a shortcut: this app's kit has no divider component and
     * minting one is a design act, while a time at the head of each turn is the separator every
     * chat surface already uses -- and it spends `ts`, which M2.2 asks for in the same breath.
     */
    val turnStart: Boolean = false,
    /**
     * M2.1's parsed prose, for the kinds whose text IS markdown (`agent_message`). Empty
     * everywhere else: a tool's target and a decision's verdict are wire values rather than
     * prose, and running a parser over them would let a path containing `*` render as emphasis.
     */
    val markdown: List<MarkdownBlock> = emptyList(),
    /**
     * ADR-017 T2 rule 2's tear: this row IS the boundary, not a message across it.
     *
     * A tear must be able to be DRAWN. What this replaces was the neutral row, which printed the
     * wire's kind name `structured_gap` at the reader -- a label for a machine -- and before
     * finding B4 the record was dropped outright, so the rows either side of a PROVEN
     * discontinuity were drawn adjacent with nothing between them.
     */
    val gap: Boolean = false,
    /**
     * Whether this is the READER'S own message, drawn as a bubble on their side rather than as
     * a row on the ground.
     *
     * IT REPLACES A SENTENCE. This screen used to write "You · hello", because every sender
     * shared one `activityRow` and the copy was the only thing that could say who spoke. A
     * bubble says it in the layout, so the prefix went with it -- keeping both would state the
     * same fact twice, which is what the owner's screenshot shows: a column of identical
     * bordered boxes each beginning with the same two words.
     */
    val bubble: Boolean = false,
    /**
     * Whether the reader typed a MACHINE WORD -- a `/command` -- which the bubble draws in the
     * machine's own face.
     *
     * IT IS DECIDED HERE AND NOT IN THE VIEW, because it is a reading of the text and the view
     * may not make one (PB-DS-9). The rule is deliberately narrow: the text BEGINS with a
     * slash. A slash inside a sentence is a path or a fraction, and every CLI this app talks to
     * puts its commands at the front of the line.
     */
    val command: Boolean = false,
    /**
     * What a CLOSED card must say about itself, or "" when there is nothing to say.
     *
     * COLLAPSING IS ONLY HONEST WHEN THE CLOSED LINE IS (owner ruling R3). Two facts would
     * otherwise become invisible the moment the default moved: a run that FAILED would read
     * exactly like one that worked, and a body the machine CLIPPED would lose its offer to
     * fetch the rest -- because that offer is drawn only while the card is open. So the one
     * mark the line has carries the worse of the two, in the wire's own word.
     *
     * IT IS THE WIRE'S VOCABULARY AND NEVER OURS: `failed` and `declined` are section 4's
     * statuses verbatim. `clipped` is the one word this screen writes, because §2's
     * `truncated` is a boolean and a reader needs a noun.
     */
    val mark: String = "",
    /** M2.2: this card has a body worth hiding, so collapsing it is offered. */
    val expandable: Boolean = false,
    /** Whether it is open. CLOSED is the default -- see [TranscriptScreen.of]'s `expanded`. */
    val expanded: Boolean = true,
    /**
     * IS-CAP-2/M3.3: the card is CLIPPED and the machine still holds the whole of it, so the
     * fetch is honest to offer. False means there is nothing to fetch and the card must not
     * promise one.
     */
    val offersDetail: Boolean = false,
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
     * The one character that makes a typed line a MACHINE WORD rather than a sentence.
     *
     * IT REPLACES `YOU`, the attribution this screen used to write into every user message.
     * That constant existed because a row could not say who spoke without saying it in words;
     * a bubble can, so what is left to decide about the reader's own text is only how to SET
     * it -- and a line beginning with a slash is a command in every CLI this app talks to.
     */
    private const val COMMAND_PREFIX = "/"

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

    /** §4's one non-terminal status: the item is open and more records will follow. */
    private const val STATUS_IN_PROGRESS = "in_progress"

    /**
     * §4's three terminal statuses. They are here because IS-ENV-1's turn CLOSES on a terminal
     * `agent_message` and this screen has to be able to say when one has -- see [openTurnOf].
     */
    private const val STATUS_COMPLETED = "completed"
    private const val STATUS_FAILED = "failed"
    private const val STATUS_DECLINED = "declined"

    /** The word a `tool_run` still `in_progress` leads its mono well with. See [TOOL_RUN]'s arm. */
    private const val RUNNING = "running"

    /**
     * The one word a CLIPPED body gets on its closed line. It is the only word in this
     * function this screen writes: §2's `truncated` is a boolean, and a reader scanning a
     * burst needs a noun.
     */
    private const val CLIPPED_MARK = "clipped"

    /**
     * What a closed card says about itself: the worse of "this did not succeed" and "this is
     * not all of it", or nothing at all.
     *
     * WORST STATUS WINS, and the order is the reader's interest rather than the schema's. A
     * failure is the thing they would have acted on; a clip is still there when they open it.
     * A run still `in_progress` gets NO mark -- [TranscriptBlock.running] already draws it,
     * and two marks for one fact is how a row stops being scannable.
     *
     * A SUCCESSFUL, WHOLE CARD IS UNMARKED, deliberately. A mark on every row is a mark that
     * means nothing, which is the failure mode of the client this rule was taken from: its
     * collapsed groups carry no outcome at all, so the only way to find a failure is to open
     * every one.
     */
    private fun closedMark(item: InteractionItem): String = when {
        item.status == STATUS_FAILED || item.status == STATUS_DECLINED -> item.status
        item.truncated -> CLIPPED_MARK
        else -> ""
    }

    /**
     * @param expanded the item ids the reader has OPENED (M2.2's expand/collapse).
     *
     *  CLOSED IS THE DEFAULT (owner ruling R3, 2026-08-26), and this reverses what stood here.
     *  The old argument was: `ToolCard`'s own KDoc says a burst should scan one line each, but
     *  the recorded Claude Code crossing draws a tool run's captured output as one of the two
     *  mono blocks a real turn produces, so a collapsed default "would silently stop drawing
     *  the first of them" -- and hiding what the machine said is the wrong side of this app's
     *  standing rule about absence.
     *
     *  IT WAS RIGHT ABOUT THE RISK AND WRONG ABOUT THE REMEDY, and the difference is one word
     *  in it: SILENTLY. A closed card that says nothing about itself does hide the record. A
     *  closed card that names its own worst outcome -- [TranscriptBlock.mark] -- hides nothing
     *  the reader would have acted on, and turns a burst of six tool calls from six screens
     *  into six lines. The owner's own screenshots needed two of them to capture one session
     *  for exactly the reason the old default caused.
     *
     *  So the parameter inverted with the decision rather than keeping its old sense: what the
     *  reader spends is now the OPEN, and it survives redraws the same way the collapse did.
     * @param atFloor the machine has said nothing older than [TranscriptPanel.oldestItemId] is
     *  retained (ADR-014 §2). False covers both "there is more" and "no page has been read yet",
     *  which are the same state to a screen: offer the control.
     */
    fun of(
        items: List<InteractionItem>,
        expanded: Set<String> = emptySet(),
        atFloor: Boolean = false,
        atCapacity: Boolean = false,
        withoutDetail: Set<String> = emptySet(),
    ): TranscriptPanel {
        // THE WIRE'S ORDER, KEPT. See the file KDoc: a conversation is read in the order it was
        // said, and `App.ReadTranscript` already walks the fold by ascending cursor.
        val blocks = items.mapIndexed { index, item ->
            blockFor(
                item,
                previous = items.getOrNull(index - 1),
                expanded = expanded,
                withoutDetail = withoutDetail,
            )
        }
        return TranscriptPanel(
            heading = HEADING,
            blocks = blocks,
            emptyCopy = EMPTY,
            // The turn the screen is DRAWING, which is what a send or a Stop tapped now is
            // rendered against -- the OPEN one, by IS-ENV-1's own rule. See [latestTurnId].
            latestTurnId = openTurnOf(items),
            oldestItemId = pageableAnchorOf(items),
            offersLoadEarlier = pageableAnchorOf(items).isNotEmpty() && !atFloor && !atCapacity,
            // ADR-017's degrade is ONE-WAY, so ANY tear this phone still holds means the session
            // has no structured composer -- not merely the latest one.
            structureTorn = items.any { it.kind == STRUCTURED_GAP },
        )
    }

    /**
     * IS-ENV-1's rule, mirrored: the turn that is OPEN over these items, or "" for an idle
     * session. See [TranscriptPanel.latestTurnId] for why sampling the last row was wrong.
     *
     * IT IS THE DAEMON'S `turnIDLocked` LINE FOR LINE -- a `user_message` opens a turn, every
     * item inside it carries that turn, and a terminal `agent_message` closes it -- because the
     * value this produces is compared against that function's own state by the daemon, and two
     * rules that were meant to agree are the thing that disagreed here.
     */
    private fun openTurnOf(items: List<InteractionItem>): String {
        var open = ""
        for (item in items) {
            open = item.turnId
            if (item.kind == AGENT_MESSAGE && terminal(item.status)) open = ""
        }
        return open
    }

    /** §4's three terminal statuses; `in_progress` is the only one that leaves an item running. */
    private fun terminal(status: String): Boolean =
        status == STATUS_COMPLETED || status == STATUS_FAILED || status == STATUS_DECLINED

    /**
     * The oldest element the DAEMON can match as a `before_item`, or "".
     *
     * A `structured_gap` is skipped rather than named: its id is minted by the phone from the
     * boundary's emission instant, and no producer ever stamped it on an item. See
     * [TranscriptPanel.oldestItemId].
     */
    private fun pageableAnchorOf(items: List<InteractionItem>): String =
        items.firstOrNull { it.kind != STRUCTURED_GAP }?.itemId.orEmpty()

    private fun blockFor(
        item: InteractionItem,
        previous: InteractionItem?,
        expanded: Set<String>,
        withoutDetail: Set<String> = emptySet(),
    ): TranscriptBlock {
        val fields = item.fields()
        val block = when (item.kind) {
            // THE READER'S OWN WORDS, AND NOTHING ELSE IN THE LINE. `joined(YOU, item.text)`
            // stood here and produced "You · hello"; the bubble says who spoke by which side
            // of the screen it sits on, so the attribution moved from the copy into the layout
            // rather than being drawn twice.
            USER_MESSAGE -> TranscriptBlock(
                itemId = item.itemId,
                kind = item.kind,
                line = item.text,
                bubble = true,
                command = item.text.startsWith(COMMAND_PREFIX),
            )

            // THE AGENT'S WORDS ARE THE TRANSCRIPT'S DEFAULT VOICE, so they carry no attribution
            // and no marked span: this screen exists to read as the conversation, and a marker on
            // every second line would be a label on the thing the screen is made of. The user's
            // own lines are marked because they are the interjections.
            //
            // agents-tracker-dwwv.1.2 LEFT NO [running] HERE, ON PURPOSE. §2's envelope makes
            // `status` generic to every kind, and a mid-stream `agent_message` COULD in principle
            // carry `in_progress` before its terminal record's `stop_reason` (§3.2) arrives -- but
            // no adapter emits that today (`internal/adapter/claude/interaction.go` always closes
            // an agent_message `StatusCompleted` in the same record that carries its text), so
            // there is no wire value to drive a test off yet. Marking it if one arrived would also
            // reopen the paragraph just above: the agent's prose carries "no attribution and no
            // marked span" as a stated design rule, and a running mark on flowing text is a design
            // question -- what it looks like on a SENTENCE rather than on a tool's single line --
            // that this bead leaves to M2's card work rather than guessing at.
            //
            // AND SINCE WAVE R6 IT IS MARKDOWN (M2.1). The agent's text is markdown-shaped prose
            // and was drawn with its markers in it, so a reader saw `**` and backticks rather
            // than emphasis. The parse happens HERE and not in the view for the reason every
            // other decision on this screen is here: what a message reads as is the model's, and
            // the view's job is to give the blocks their type. The fence goes to the mono well
            // rather than into the sentence -- column-aligned text inside a body-copy layout is
            // silently re-wrapped, which misreports what the machine printed.
            AGENT_MESSAGE -> {
                val markdown = Markdown.parse(item.text)
                TranscriptBlock(
                    itemId = item.itemId,
                    kind = item.kind,
                    line = Markdown.plainText(markdown),
                    markdown = markdown,
                    well = Markdown.codeText(markdown),
                )
            }

            TOOL_RUN -> {
                // agents-tracker-dwwv.1.2: the one place `status` (§4) is read at all. `RUNNING`
                // is deliberately NOT the wire's own word -- unlike a plan step's `in_progress`
                // (shown verbatim, `plan_update` below), a lone tool_run has no sibling values
                // beside it to read `in_progress` against, so the raw token would read as a
                // stray label rather than a state. "running" is copy the screen supplies, on
                // "You"'s precedent (see the file KDoc's three rules): the one word this row
                // needs and the wire has none for.
                val running = item.status == STATUS_IN_PROGRESS
                val body = lines(if (running) RUNNING else "", fields.output, fields.marker)
                // M2.2's card, decided by the kit MODEL and not re-derived here: the glyph comes
                // off ONE flat field (IS-TOOL-1 forbids the phone parsing `tool` or the arguments
                // to infer an action) and the expansion decides whether the well is drawn at all.
                //
                // `Model.well` IS DELIBERATELY NOT SPENT. It is the fold's `text` reconstruction,
                // which is the right body for a message and empty for a tool call; §3.3's own
                // `output_excerpt` and `truncation_marker` are this row's literal, and they are
                // read through `fields` like every other per-kind value on this screen.
                val card = ToolCard.modelFor(item, expanded = expanded.contains(item.itemId))
                TranscriptBlock(
                    itemId = item.itemId,
                    kind = item.kind,
                    // "Read src/main.rs" -- the adapter's tool and the action's own literal. An
                    // unclassified call (IS-TOOL-2) fills no literal and this is the tool alone.
                    line = phrase(fields.tool, fields.target),
                    emphasis = fields.target,
                    // IS-TOOL-3: the CLI's marker rides with the excerpt, verbatim, so the card
                    // never claims to hold output it only saw a marker for. RUNNING leads when
                    // the item is still open, so a tool with no output yet still draws a well
                    // saying so, rather than the empty one PB-DS-9's rule already refuses.
                    well = if (card.wellVisible) body else "",
                    running = running,
                    glyph = card.glyph,
                    expandable = body.isNotEmpty(),
                    expanded = card.wellVisible,
                    // ...unless the machine has already answered that the whole of this one is
                    // gone (round 3, finding F4). `card.offersDetail` reads fields journalled at
                    // CAPTURE time and cannot know that the daemon's bounded store has since
                    // evicted the body; the phone can, once it has asked and been refused, and
                    // an offer left standing over that answer invites a tap that can only fail.
                    offersDetail = card.offersDetail && !withoutDetail.contains(item.itemId),
                    mark = closedMark(item),
                )
            }

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

            // ADR-017 T2 rule 2's BOUNDARY, which is not one of §3's eight kinds and never came
            // from a producer: the daemon authors it when a shim/daemon spool gap is PROVEN, and
            // `phonecore` folds it into the transcript as its own element so that a client can
            // draw it. Drawing it is this arm.
            //
            // IT MUST NOT FALL TO [neutral]. That row says the kind and stops, which here would
            // print the literal `structured_gap` at a reader -- a label for a machine, in the one
            // place where the reader needs a sentence. And the alternative the finding actually
            // found was worse: with the record dropped, the rows either side of the tear were
            // drawn ADJACENT, so the phone showed a continuous conversation across a boundary the
            // daemon had proved was discontinuous. That is the silent bridge ADR-017 forbids.
            STRUCTURED_GAP -> TranscriptBlock(
                itemId = item.itemId,
                kind = item.kind,
                line = GAP_LINE,
                // The machine's own reason, VERBATIM and in the mono register every other
                // machine-authored literal on this screen takes (IS-TOOL-3's posture): it names a
                // spool and a sequence number, which is diagnosis rather than copy.
                well = item.text,
                gap = true,
            )

            else -> neutral(item)
        }
        return marked(block, item, previous)
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
    private fun marked(
        block: TranscriptBlock,
        item: InteractionItem,
        previous: InteractionItem?,
    ): TranscriptBlock {
        // THE FALLBACK KEEPS THE BLOCK AND REPLACES ONLY ITS WORDS. Rebuilding it as [neutral]
        // would drop what the kind decided about it -- an `approval_request` whose summary the
        // producer left empty would stop being answerable, which is the one property on this
        // screen that a rendering detail must never be able to take away.
        val base = if (block.line.isEmpty()) block.copy(line = item.kind) else block
        var line = base.line
        if (item.truncated) line += CLIPPED
        if (item.degraded) line = joined(line, DEGRADED)
        // M2.2's separator, applied here rather than in eight places for [marked]'s own reason:
        // it is an ENVELOPE fact (§2's `ts`, IS-ENV-1's `turn_id`) and holds for every kind.
        //
        // THE HEAD OF THE TRANSCRIPT COUNTS TOO, and that is the one place this differs from
        // `ToolCard.separatorBefore`, deliberately rather than by accident. That function answers
        // "is there a RULE to draw between these two rows", and there is nothing above the first
        // row to separate it from; the question here is "does this row head a turn", and the
        // first one always does. The kit model stays the authority for every other pair.
        val turnStart = previous == null || ToolCard.separatorBefore(previous, item)
        return base.copy(
            line = line,
            emphasis = spanIn(line, base.emphasis),
            turnStart = turnStart,
            timestamp = if (turnStart) ToolCard.timestampLabel(item.tsUnixMs) else "",
        )
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

    /**
     * The NINTH element, which is not one of §3's kinds: `phonecore.KindStructuredGap`, the
     * daemon's own proven boundary folded into the transcript (ADR-017 T2 rule 2).
     */
    private const val STRUCTURED_GAP = "structured_gap"

    /**
     * What the tear SAYS, which is the whole of why it is a kind of its own on this screen.
     *
     * THREE FACTS, IN THE ORDER A READER NEEDS THEM. That the record broke; that what the agent
     * did across the break never arrived, so the rows either side are NOT consecutive; and that
     * the phone's composer is gone for this session, because ADR-017's degrade is one-way and a
     * session in terminal fallback has no message sink to put a phone-typed message into. The
     * third is here rather than only beside the composer because this is where the reader is
     * looking when they find out.
     */
    private const val GAP_LINE = "The structured record broke here. What the agent did across " +
        "this break never reached this phone, so the rows above and below are not consecutive, " +
        "and this session can no longer be typed into from the phone."
}
