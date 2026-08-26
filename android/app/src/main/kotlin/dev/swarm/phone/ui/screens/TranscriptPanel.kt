package dev.swarm.phone.ui.screens

import dev.swarm.phone.ui.ApprovalDecision
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
    /**
     * The OLDEST decision nobody has answered yet, or "" when there is none.
     *
     * IT IS AN ID AND NOT A FLAG because the affordance it feeds is a JUMP. The drawing's
     * `decision.pill` -- *Decision needed* -- "appears only while an unanswered decision is off
     * screen, and it scrolls to it", and a boolean can raise a pill that has nowhere to go.
     *
     * OLDEST RATHER THAN NEWEST, on the same rule that orders the transcript: a conversation is
     * read in the order it was said, so the question a reader should answer first is the one that
     * has been waiting longest. Two unanswered decisions are possible -- IS-LIFE-2 resolves each
     * request exactly once and nothing serialises them -- and a pill that jumped to the newest
     * would step OVER an older one on every arrival.
     *
     * AN UNRENDERABLE REQUEST IS NOT ONE OF THESE. A card this build cannot draw cannot be
     * answered here at all (see [TranscriptBlock.unrenderable]), so pointing the pill at it would
     * send the reader, repeatedly, to a card whose only sentence tells them to go to their
     * machine.
     */
    val pendingDecisionId: String = "",
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
     * The machine's own literal, drawn UNDER the row: a tool's output, a fenced block out of the
     * agent's markdown. Empty draws no well.
     *
     * IT IS NEVER A GRID. ADR-009 (1) leaves no terminal emulation and no raw grid anywhere in the
     * app; this is excerpt text the producer already normalized, printed in the mono block every
     * other mono block in this app is printed in.
     *
     * AND IT IS NO LONGER A DIFF (owner ruling R9) NOR A DECISION'S LITERAL. A unified diff opens
     * on its own screen ([route]) and a decision's command is drawn inside the card that is asking
     * about it ([literal]). What is left here is what a well was always for: a record of something
     * already done, drawn beneath the sentence that names it. Two of the three literals that used
     * to arrive through this field did not have that shape, and neither of them read correctly in
     * a block below a row.
     */
    val well: String = "",
    /**
     * Where the REST of this block's body opens, when the flow will not carry the whole of it
     * (owner rulings R8 and R9). [TranscriptRoute.None] for every block whose body fits.
     *
     * IT IS THE OTHER HALF OF [well] AND NOT A SECOND ONE. Together they say: this much is drawn
     * here, and this is where the whole of it is. A block that carried only the head would be the
     * phone inventing a truncation the machine never made; one that carried only the route would
     * make a reader tap to see three lines.
     */
    val route: TranscriptRoute = TranscriptRoute.None,
    /**
     * R9's three cells for a `file_change`, or null for every other kind.
     *
     * ITS PRESENCE IS THE STATEMENT THAT THIS ROW IS A CHIP. A file change is never folded into a
     * group and never drawn as a diff in the flow -- see [FileChangeChip] -- so the view needs no
     * predicate of its own to tell one row from another.
     */
    val fileChange: FileChangeChip? = null,
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
     * §3.5's `decisions[]`, in the order the adapter offered them, or empty for every kind that
     * is not a decision.
     *
     * THE BUTTONS ARE THE AGENT'S AND NOT OURS. IS-APR-4 keeps the verdict -- `allow` | `deny` |
     * `other` -- machine-side and off the wire, `internal/skeleton/interaction_chain_e2e_test.go`
     * fails the build if one ever rides beside `id`/`label`, and "Allow/Deny" is precisely the
     * copy this surface may not author. So the list is the wire's, IN THE WIRE'S ORDER, at the
     * count the wire sent (one to eight, §5's `MaxDecisions`): a surface that reordered, merged
     * or re-labelled them would be deciding what the reader is agreeing to.
     *
     * IT IS `ApprovalDecision` AND NOT A SECOND SHAPE, for the reason [approvalBlock] takes
     * `ApprovalItem`'s decode rather than writing one: two models of "what this approval offers"
     * are two places for the card in the transcript and the command it sends to disagree.
     */
    val choices: List<ApprovalDecision> = emptyList(),
    /**
     * The decision's own literal -- §7's action rendered to the one field the adapter filled, or
     * IS-LIFE-6's sanitized `prompt_lines` -- and "" for every kind that is not a decision.
     *
     * IT IS NOT [well], AND THE DIFFERENCE IS WHERE IT IS DRAWN. A well is a mono block the
     * transcript puts UNDER a row; this is drawn INSIDE the card, between the question and the
     * buttons, because it is the thing being agreed to rather than a record of something already
     * done. Reusing the field would put `rm -rf ~/.swarm-relay/relay.db` in a block below the
     * card that is asking about it.
     *
     * AND IT IS NEVER BOUNDED. R8's bound ([TranscriptRoute.Output]) exists because an opened
     * tool run is a body the reader CHOSE to look at and can leave; a literal is what an answer
     * commits to, and a reader may not be asked to approve the half of a command they were shown.
     * §5 already caps what can arrive here -- `MaxSummaryBytes` for an action string,
     * `MaxPromptLines` for a prompt card -- so the whole of it is a bounded quantity the machine
     * chose, not one this screen has to choose again.
     */
    val literal: String = "",
    /**
     * Whether an answer to this decision is already on the wire, so every choice is disabled.
     *
     * ALL OF THEM AND NOT THE ONE THAT WAS TAPPED. A card that greyed only the tapped button
     * would leave the other seven live over an answer already sent -- two `ActionApprove`s for
     * one `interaction_id`, and the daemon that resolves first decides which of the reader's two
     * answers the agent acted on.
     *
     * IT IS FALSE THE MOMENT THE REQUEST RESOLVES, whoever resolved it. An answer that raced a
     * resolution and lost must not leave a dead card with every button greyed and no way out --
     * see [approval], which is the same fact stated for the tap rather than for the lock.
     */
    val locked: Boolean = false,
    /**
     * A decision this build cannot draw: the question is stated as unshowable and the reader is
     * told where it can be answered.
     *
     * IT REPLACES A DEAD END. An `approval_request` this side cannot decode -- a malformed body,
     * or one offering no decisions at all -- used to fall to the neutral row, which prints the
     * wire's own kind name `approval_request` at a reader and offers nothing to tap. That is a
     * session parked behind a card that cannot be answered on any surface, which is the one
     * outcome IS-COMPAT-1's skip rule exists to prevent for every OTHER kind: an unknown message
     * costs a row, an unanswerable question costs the session.
     */
    val unrenderable: Boolean = false,
    /**
     * §3.6's resolution, on the `approval_resolved` row that reports it, and null on every other
     * kind -- including on the `approval_request` it answers. See [TranscriptResolution] for why
     * the two cannot be joined here yet.
     */
    val resolution: TranscriptResolution? = null,
    /**
     * The drawing's `decision.footer` on an OPEN decision -- `asked HH:mm` -- and "" everywhere
     * else, including on a decision this build cannot draw and on one that has been answered.
     *
     * IT IS THE HALF OF THAT ROW THAT SURVIVED (owner ruling, 2026-08-26). The sheet tabled
     * "asked HH:mm · answerable here or at the terminal", and the second clause is a CAPABILITY
     * claim this block cannot verify: the card draws identically on an offline session and on one
     * whose composer is shut. That would be the same defect as the settled row that asserted
     * every resolution happened at the reader's machine -- except worse, because a settled row is
     * a record and this one is on screen precisely while the reader is deciding whether to act.
     * The alternative considered and rejected was gating the clause on a liveness input: it buys
     * one sentence at the cost of coupling this block to a fact it has no other reason to know,
     * and the sentence still has to be right in every state that input can be in.
     *
     * IT IS NOT [timestamp], AND THE TWO ANSWER DIFFERENT QUESTIONS. That one is the turn
     * boundary's marker -- empty on every row that does not head a turn, which is most of them --
     * and it is about the CONVERSATION's structure. This is about one item: when the agent
     * stopped and asked. A question raised a moment ago and one that has been waiting since
     * before the last three messages look identical without it, on a surface that keeps moving
     * while the reader reads.
     *
     * EMPTY WHEN THE MACHINE SENT NO INSTANT. §2's `ts` arrives as 0 for an absent fact and
     * PB-APP-11 forbids substituting arrival time, so a card with no time says nothing about when
     * rather than telling a reader they were asked on 1 January 1970.
     */
    val askedAt: String = "",
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

/**
 * Where a block's BODY opens, when the reading column will not carry the whole of it (owner
 * rulings R8 and R9, 2026-08-26).
 *
 * IT IS A ROUTE AND NOT A TRUNCATION, and that distinction is the whole reason this type exists.
 * §2's `truncated` is the MACHINE saying it clipped what it sent; this is the SCREEN saying it
 * will not draw all of what it holds. So the whole literal rides here -- one tap away, never
 * dropped -- and the flow shows a head. A phone that dropped the tail instead would be inventing
 * a truncation the machine never made, which is exactly what IS-TOOL-3 forbids one field over:
 * the item "SHALL NOT claim to hold the underlying output".
 *
 * ONE ROUTE PER BLOCK, WHICH IS WHY IT IS A SEALED TYPE RATHER THAN TWO NULLABLE FIELDS. A block
 * that could name a diff screen and an output screen at once is a row with two chevrons and a
 * reader having to choose between them; the drawing gives every row exactly one place to go.
 */
sealed class TranscriptRoute {

    /** The body fits in the flow. Nothing is offered, because nothing is being held back. */
    object None : TranscriptRoute()

    /**
     * R8's overflow: [text] is the whole of the tool's body, and [label] is what the open card
     * says about the part it is not drawing -- the drawing's `tool.more`, verbatim.
     */
    data class Output(val text: String, val label: String) : TranscriptRoute()

    /**
     * R9's diff: [text] is §3.4's `diff_excerpt`, the producer's own normalized unified diff --
     * "producers normalize ... consumers never see the raw pair" -- which this side neither
     * re-renders nor re-wraps.
     *
     * IT CARRIES NO LABEL BECAUSE THE CHIP IS THE HANDLE. A file change is one tappable line
     * already carrying its verb, its path and its counts; a second "open the diff" affordance
     * on it would be an offer drawn beside a row that is one.
     */
    data class Diff(val text: String) : TranscriptRoute()
}

/**
 * §3.6's `approval_resolved` as a state rather than as a sentence: what became of a question, and
 * whether that counts as an ANSWER at all.
 *
 * THE DRAWING HAD ONE SETTLED STATE AND THE WIRE HAS SIX. IS-LIFE-2 guarantees every request
 * reaches exactly one resolution "including when it is cancelled, superseded, expired, or answered
 * at the machine", and §3.6 spells the six out: `allowed`, `denied`, `answered_locally`,
 * `cancelled`, `superseded`, `expired`. Three of them are not answers -- nobody decided anything;
 * the question went away -- and a card with only an answer's sentence would tell a reader they
 * answered at their machine something that expired while they slept. That is the defect this type
 * exists to make unrepresentable: [answered] is what a card must read before it says the word.
 *
 * AND [answered] IS FALSE FOR A VALUE THIS BUILD DOES NOT KNOW, deliberately. An unrecognised
 * resolution may well be an answer; this build cannot know that it is, and the failure modes are
 * not symmetric -- drawing an unknown resolution without the word costs a reader nothing, and
 * drawing it WITH the word credits somebody with a decision this side invented. IS-COMPAT-2's
 * unknown-field rule, read for a value.
 *
 * IT IS NOT ON THE REQUEST'S OWN BLOCK, AND THAT IS A GAP RATHER THAN A DESIGN. §3.6 carries
 * `interaction_id` -- "the `approval_request`'s `item_id`" -- so the pairing is a wire fact and
 * not an inference; but `ItemFields` decodes `interaction` (§3.8's session_status dimension) and
 * NOT `interaction_id`, and this file may not read JSON to get it (PB-DS-9, fenced by
 * `android/gate/i1_sheetandwell_test.go`). Until that one field is decoded, the resolution is
 * reported by the row that carries it and a settled card may say only that it is settled -- never
 * where it was answered, because the block that knows is a different block.
 */
data class TranscriptResolution(
    /** §3.6's `decision`, verbatim: one of six, or a value this build has never seen. */
    val decision: String,
    /** §3.6's `by`, verbatim: `phone` | `owner` | `daemon` (expiry) | `agent` (cancel). */
    val by: String,
    /**
     * Whether this build RECOGNISES this resolution as an answer somebody gave -- `allowed`,
     * `denied` or `answered_locally`. False for the three that are not, and false for a value this
     * build does not know.
     */
    val answered: Boolean,
)

/**
 * R9's chip: one file change as three cells, in the flow, instead of a screen of diff.
 *
 * WHAT IT REPLACES. The `file_change` arm drew `diff_excerpt` into the mono well unconditionally
 * -- not collapsible, not bounded, on every changed file -- so a refactor touching nine files
 * cost nine screens of the conversation, and the owner's screenshots needed two frames to capture
 * one session. The counter-argument that stood in the collapse work was that "a diff is the only
 * rendering of what actually changed on disk", and it is answered rather than overruled: the diff
 * is not deleted, it is ROUTED ([TranscriptRoute.Diff]), onto a screen wide enough to scroll
 * sideways, which is where column-aligned text was always going to read better than in a reading
 * column at body width.
 *
 * AND IT IS NEVER GROUPED. Cross-item grouping is deferred (plan §10) and a file change is
 * excluded from it even when it lands: a count of files is not a record of what changed, and the
 * one thing a reader scanning a wide refactor needs is which files it touched.
 *
 * THE CELLS ARE SEPARATE BECAUSE THE ROW IS. `fileChangeRow(context, verb, path, counts)` takes
 * three values; handing it [TranscriptBlock.line] and letting it split on the separator would be
 * a view reading copy back out of a sentence this screen wrote, which is PB-DS-9 inverted.
 * [TranscriptBlock.line] stays the sentence for every surface that draws a row.
 */
data class FileChangeChip(
    /** §3.4's `change`, verbatim: `create` | `modify` | `delete` | `rename`. */
    val verb: String,
    /** §3.4's `path` -- or, on a rename, both paths in the direction the file moved. */
    val path: String,
    /** §3.4's counts of the WHOLE change as `+N -M`, or "" where the producer sent none. */
    val counts: String,
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
     * R8's IN-PLACE BOUND: how many lines of a tool's body an OPEN card draws before it keeps a
     * head and offers the whole of it on its own screen.
     *
     * TWENTY, AND HERE IS THE ARITHMETIC. The mono register a well is set in is 11.5 sp
     * (`TextAppearance.Swarm.Mono.Code`), which lands near a 17 dp line box: twenty lines is
     * about 340 dp of glass. A Pixel-class viewport gives this screen roughly 650 dp of reading
     * height once the header, the connection strip and the pinned composer are taken out, so an
     * opened card at the bound costs about HALF the reading area and the conversation is still
     * visible above and below it. At thirty it owns the screen; at forty a single opened card is
     * the wall of text collapsing the cards was meant to end.
     *
     * AND NOT LOWER, WHICH IS THE OTHER HALF OF THE CHOICE. Ten would push an ordinary `go test`
     * failure or a short stack trace onto a second screen it does not need, and a bound that
     * routes the common case is a bound that teaches a reader to distrust the head. Twenty holds
     * the ordinary body in place and routes only the genuinely long one -- a full suite run at
     * §5's `MaxTextBytes` is roughly two hundred lines, which is ten times this.
     *
     * IT IS LINES AND NOT BYTES on purpose. The machine already bounds bytes (`MaxTextBytes`,
     * 4 KiB); what a reader loses their place to is HEIGHT, and one line of a test run and one
     * line of a stack trace cost the same height and wildly different byte counts.
     */
    private const val OPEN_IN_PLACE_LINES = 20

    /**
     * The drawing's `tool.more`, in two halves: `Open in full · N more lines`.
     *
     * IT NAMES THE SIZE OF WHAT IS BEHIND IT. A handle that said only "open in full" would be a
     * chevron whose cost the reader cannot judge -- one more line or two hundred -- which is the
     * dead-chevron defect (agents-tracker-2yb) restated as an unmeasurable one. The count is the
     * remainder after the head, so it is what the reader has NOT already seen.
     *
     * AND THE SINGULAR IS ITS OWN STRING, tabled beside the plural (owner ruling, 2026-08-26).
     * One plural template with a number substituted into it reads `1 more lines` at exactly the
     * count that is likeliest to occur: a body ONE line over the bound is the commonest body that
     * is over it at all. English is not a formatting problem this screen may push onto the reader,
     * and two constants cost less than the one sentence explaining why the app cannot count.
     */
    private const val OPEN_IN_FULL = "Open in full"
    private const val MORE_LINES = "more lines"
    private const val MORE_LINE = "more line"

    /**
     * §3.6's six resolutions, spelled once. They are the WIRE's values and are MATCHED here, not
     * rendered as English -- with one exception, argued at [resolutionLine].
     */
    private const val RESOLVED_ALLOWED = "allowed"
    private const val RESOLVED_DENIED = "denied"
    private const val RESOLVED_LOCALLY = "answered_locally"
    private const val RESOLVED_CANCELLED = "cancelled"
    private const val RESOLVED_SUPERSEDED = "superseded"
    private const val RESOLVED_EXPIRED = "expired"

    /**
     * The one word of the decision footer this screen writes. The time beside it is
     * `ToolCard.timestampLabel`'s, which is the app's single clock format -- see [askedLabel].
     */
    private const val ASKED = "asked"

    /** §3.6's two `by` values a person can be told about. `daemon` and `agent` are not people. */
    private const val BY_PHONE = "phone"
    private const val BY_OWNER = "owner"

    /**
     * The three sentences a resolution can end in, and they are this screen's own words because
     * the wire has no field for any of them: `by` names a party, not a place, and nothing on the
     * item says "nobody answered this".
     */
    private const val ANSWERED_HERE = "answered on this phone"
    private const val ANSWERED_AT_MACHINE = "answered at your machine"
    private const val NEVER_ANSWERED = "never answered"

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
     * @param answering the item ids whose answer this phone has SENT and not yet seen resolved.
     *  Every choice on such a card is locked -- see [TranscriptBlock.locked]. It is a set and not
     *  a single id for [withoutDetail]'s reason: two cards can be waiting at once, and a screen
     *  that held one id would unlock the first card the moment the second was answered.
     */
    fun of(
        items: List<InteractionItem>,
        expanded: Set<String> = emptySet(),
        atFloor: Boolean = false,
        atCapacity: Boolean = false,
        withoutDetail: Set<String> = emptySet(),
        answering: Set<String> = emptySet(),
    ): TranscriptPanel {
        // THE WIRE'S ORDER, KEPT. See the file KDoc: a conversation is read in the order it was
        // said, and `App.ReadTranscript` already walks the fold by ascending cursor.
        val blocks = items.mapIndexed { index, item ->
            blockFor(
                item,
                previous = items.getOrNull(index - 1),
                expanded = expanded,
                withoutDetail = withoutDetail,
                answering = answering,
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
            // THE QUESTION THAT HAS BEEN WAITING LONGEST, read off what the blocks decided rather
            // than off the items: `approval` is already "an approval_request that is NOT resolved",
            // and re-deriving it here would be a second answer to the one question -- can this be
            // answered -- that IS-LIFE-2 requires every surface to agree on.
            pendingDecisionId = blocks.firstOrNull { it.approval }?.itemId.orEmpty(),
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
        answering: Set<String> = emptySet(),
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
                // R8's bound, applied to the COMPOSED body and not to `output_excerpt` alone:
                // `running` and the CLI's truncation marker are part of what the well says, and a
                // bound that counted only the excerpt would draw twenty-two lines on some cards
                // and twenty on others for no reason a reader could see.
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
                    well = if (card.wellVisible) inPlace(body) else "",
                    // AND ONLY WHILE IT IS OPEN. A closed card already offers one thing -- open
                    // me -- and a second offer beside it, onto a screen, would be two affordances
                    // for one body with nothing on the row to tell them apart.
                    route = if (card.wellVisible) overflowOf(body) else TranscriptRoute.None,
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

            // R9: THE CHANGE IS A CHIP AND THE DIFF OPENS ELSEWHERE. `well = fields.diff` stood
            // here and drew the unified diff into the flow on every changed file, unconditionally
            // -- see [FileChangeChip] for what that cost and for the answer to the argument that
            // kept it. The sentence is unchanged; what moved is where the diff is drawn.
            FILE_CHANGE -> TranscriptBlock(
                itemId = item.itemId,
                kind = item.kind,
                line = joined(fields.change, movedPath(fields), sizeOf(fields)),
                emphasis = fields.path,
                fileChange = FileChangeChip(
                    verb = fields.change,
                    path = movedPath(fields),
                    counts = sizeOf(fields),
                ),
                // AND A CHANGE THE PRODUCER SENT NO DIFF FOR OFFERS NO SCREEN. A `delete` carries
                // no excerpt worth routing, and an offer onto an empty page is the dead-chevron
                // defect (agents-tracker-2yb) wearing a route.
                route = if (fields.diff.isEmpty()) {
                    TranscriptRoute.None
                } else {
                    TranscriptRoute.Diff(fields.diff)
                },
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

            APPROVAL_REQUEST -> approvalBlock(item, answering)

            // WHO ANSWERED IS HALF THE FACT, and it stays half the fact: an approval the owner
            // took at the machine, one this phone sent, and one the daemon expired are three
            // different things to have happened. What changed is that `joined(decision, by)` said
            // it as two wire tokens -- `allowed · phone` -- in the one row whose whole job is to
            // tell a reader what became of the question they were asked. See [resolutionLine].
            APPROVAL_RESOLVED -> TranscriptBlock(
                itemId = item.itemId,
                kind = item.kind,
                line = resolutionLine(fields),
                resolution = TranscriptResolution(
                    decision = fields.decision,
                    by = fields.by,
                    answered = answeredResolution(fields.decision),
                ),
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
                // AND NO WELL, WHICH IS A DELETION AND IS RECORDED AS ONE. `well = item.text` put
                // the daemon's own reason -- "hook spool gap at seq 41" -- in a mono block under
                // the tear, on the argument that it is machine-authored diagnosis and IS-TOOL-3's
                // posture carries it verbatim. That argument survives; what changed is the place.
                // The drawing gives the tear ONE line, "at the place the record tore, carrying
                // its own repair", and a mono block of spool diagnosis beneath it is the
                // paragraph this row was just reduced FROM, restated in the machine's voice.
                // The reason is not lost to the app -- it is journalled -- it is no longer read
                // out mid-conversation to someone who wants the conversation back.
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
    private fun approvalBlock(item: InteractionItem, answering: Set<String>): TranscriptBlock {
        // AND AN UNDECODABLE ONE IS NOT A NEUTRAL ROW. `neutral(item)` stood here and printed the
        // wire's kind name `approval_request` at the reader, with nothing to tap: an agent blocked
        // on a question this build cannot draw, and a reader with no sentence telling them where
        // it CAN be answered. See [TranscriptBlock.unrenderable].
        val card = ApprovalItem.of(item.sessionId, item.itemId, item.body)
            ?: return TranscriptBlock(
                itemId = item.itemId,
                kind = item.kind,
                line = DECISION_UNRENDERABLE,
                unrenderable = true,
            )
        return TranscriptBlock(
            itemId = item.itemId,
            kind = item.kind,
            line = card.summary,
            // §7's literal and §3.5's buttons, both the machine's own and neither read twice:
            // see [TranscriptBlock.literal] and [TranscriptBlock.choices].
            literal = card.command,
            choices = card.decisions,
            approval = !item.resolved,
            // A RESOLUTION OUTRANKS AN ANSWER IN FLIGHT, whoever resolved it. IS-LIFE-2 guarantees
            // exactly one resolution "including when it is cancelled, superseded, expired, or
            // answered at the machine", so a phone answer that raced the terminal and lost must
            // not leave the card disabled forever -- a dead card with every button greyed is the
            // one state a reader cannot leave, and it would be reached by the ORDINARY path of
            // two people working one session.
            locked = !item.resolved && answering.contains(item.itemId),
            // AND ONLY WHILE IT IS OPEN. A settled card carries the resolution's own sentence,
            // and a footer saying when it was asked beside it would be two time claims on one
            // card -- one of them about a moment that stopped mattering when the question was
            // answered. See [TranscriptBlock.askedAt].
            askedAt = if (item.resolved) "" else askedLabel(item.tsUnixMs),
        )
    }

    /**
     * `asked HH:mm`, or "" for a question the machine gave no instant for.
     *
     * THE EMPTY CASE IS THE WHOLE OF THE CARE HERE. `phrase` drops an empty part, so composing
     * this without the guard would leave a footer reading `asked` -- a word with the fact
     * removed, which is worse than the absence it is reporting.
     */
    private fun askedLabel(tsUnixMs: Long): String {
        val at = ToolCard.timestampLabel(tsUnixMs)
        return if (at.isEmpty()) "" else phrase(ASKED, at)
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

    /**
     * What became of a question, in the row that reports it.
     *
     * ## Why a table exists here at all, when this file's first rule forbids one
     *
     * The file KDoc's rule is that a table turning wire tokens into English "would have to fail
     * loudly on a value it did not know, and a machine that added one would take this screen down
     * at the moment it is being read". That is an argument against an EXHAUSTIVE table, and it is
     * answered by the last arm rather than overruled: an unrecognised `decision` falls through to
     * `joined(decision, by)` -- the exact line this function replaces -- so a machine one version
     * ahead degrades to the old rendering instead of to an exception or to the nearest sentence
     * this build happened to hold.
     *
     * ## What each arm says, and why it is not the drawing's one sentence
     *
     * **The phone's own answers keep their verdict.** `allowed` and `denied` are classified from
     * the verdict the daemon supplied (IS-RES-1) and that only happens on the phone path, so this
     * is the one case where the wire knows what was decided. The token stays, verbatim, and the
     * screen adds the place.
     *
     * **The machine's answer has no verdict to keep, so it does not pretend to one.**
     * `answered_locally` is the ONE value whose token names an observation path rather than an
     * outcome: `internal/skeleton/backend.go:671` resolves it precisely when the daemon did NOT
     * type the answer -- "the daemon observes only that the session's interaction dimension LEFT
     * the waiting state, never which button on the dialog was pressed". Printing
     * `answered_locally · answered at your machine` states one fact twice and dresses a
     * non-verdict as a decision, so this arm drops the token and keeps the fact.
     *
     * **The three that are not answers say so.** `cancelled`, `superseded` and `expired` are
     * daemon-observed and "carry no verdict" (§3.6). Nobody decided anything: the question went
     * away. They keep the wire's own word for WHICH way it went away and gain the one fact the
     * wire has no field for -- that it was never answered. This is the arm the drawing did not
     * have, and without it the only settled sentence available would have told a reader they
     * answered at their machine something that expired while they slept.
     *
     * IT CARRIES NO TIME. The drawing tables `HH:mm` on the settled sentence; this app already has
     * one time format and one place for it -- [TranscriptBlock.timestamp], drawn at a turn
     * boundary, on the argument recorded there that a time on every row is what makes a boundary
     * invisible. A second clock inside a sentence would be a second format on the same screen.
     */
    private fun resolutionLine(fields: ItemFields): String = when (fields.decision) {
        RESOLVED_LOCALLY -> answeredAt(fields.by).ifEmpty { joined(fields.decision, fields.by) }
        RESOLVED_ALLOWED, RESOLVED_DENIED ->
            joined(fields.decision, answeredAt(fields.by).ifEmpty { fields.by })
        RESOLVED_CANCELLED, RESOLVED_SUPERSEDED, RESOLVED_EXPIRED ->
            joined(fields.decision, NEVER_ANSWERED)
        else -> joined(fields.decision, fields.by)
    }

    /**
     * Where an answer was given, or "" for a party that is not a person.
     *
     * A `daemon` expiry and an `agent` cancel reach [resolutionLine]'s non-answer arm and never
     * ask this; a `by` this build does not know falls back to the wire's own token, which is a
     * word for a machine but is at least the machine's own.
     */
    private fun answeredAt(by: String): String = when (by) {
        BY_PHONE -> ANSWERED_HERE
        BY_OWNER -> ANSWERED_AT_MACHINE
        else -> ""
    }

    /** §3.6's three answers. See [TranscriptResolution.answered] for the unknown value's arm. */
    private fun answeredResolution(decision: String): Boolean =
        decision == RESOLVED_ALLOWED ||
            decision == RESOLVED_DENIED ||
            decision == RESOLVED_LOCALLY

    /**
     * R8's head: the first [OPEN_IN_PLACE_LINES] lines of a body, or the whole of a body that
     * already fits.
     *
     * THE HEAD AND NOT THE TAIL, and the two are not interchangeable. A tool's body leads with
     * what it was asked to do and with the first thing that went wrong; a tail-bound card would
     * show a reader the last twenty lines of a passing suite, which is the part that says
     * nothing. The reader who wants the end taps through to it.
     */
    private fun inPlace(body: String): String {
        val all = body.split("\n")
        if (all.size <= OPEN_IN_PLACE_LINES) return body
        return all.take(OPEN_IN_PLACE_LINES).joinToString("\n")
    }

    /**
     * R8's overflow for a body past the bound, or [TranscriptRoute.None] for one that fits.
     *
     * THE WHOLE BODY RIDES ON THE ROUTE, not the remainder. What the screen opens is the tool's
     * output, and a screen that began at line twenty-one would be a page whose first line is
     * mid-sentence -- and would make the two renderings of one body disagree about what it is.
     */
    private fun overflowOf(body: String): TranscriptRoute {
        val all = body.split("\n")
        if (all.size <= OPEN_IN_PLACE_LINES) return TranscriptRoute.None
        val more = all.size - OPEN_IN_PLACE_LINES
        return TranscriptRoute.Output(
            text = body,
            label = joined(OPEN_IN_FULL, phrase("$more", if (more == 1) MORE_LINE else MORE_LINES)),
        )
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
     * What the tear SAYS -- reduced, by the drawing, from a paragraph to a rule with a word on it.
     *
     * IT USED TO CARRY THREE FACTS: that the record broke, that the rows either side are NOT
     * consecutive, and that this session can no longer be typed into from the phone. The argument
     * for all three was that this is where the reader is looking when they find out. That
     * argument is answered rather than dropped, and the answer has two halves.
     *
     * FIRST, TWO OF THE THREE BELONG TO OTHER COMPONENTS NOW. The composer says its own sentence
     * in its own place -- "This session's record has a gap / Still typeable at your machine"
     * (plan D.8) -- and repeating it here made the tear a second composer notice, drawn in the
     * middle of the conversation, for a reader who has not tried to type yet.
     *
     * SECOND, A PARAGRAPH IN THE FLOW IS A STOP. The tear sits BETWEEN two rows of a
     * conversation, and three sentences there is something a reader must finish before they can
     * carry on reading -- which is the shape of the notice this whole slice was filed to remove
     * (the owner's screen was 640-720 dp of chrome before one message). What is left is what a
     * reader at a discontinuity actually needs: that records are missing, and the way to repair
     * it. It is drawn thin and in position, so the FACT is carried by where it is rather than by
     * how much it says.
     *
     * THE REPAIR RIDES IN THE SAME LABEL because the divider is one target: `gapDivider` takes a
     * label and the whole rule is tappable. Two spans here would be a second affordance to aim
     * at, on the thinnest row on the screen.
     */
    private const val GAP_MISSING = "records missing"
    private const val GAP_REPAIR = "repair"
    private val GAP_LINE = joined(GAP_MISSING, GAP_REPAIR)

    /**
     * What a decision this build cannot draw says instead of dead-ending.
     *
     * TWO SENTENCES AND BOTH ARE LOAD-BEARING. The first says the question exists and that THIS
     * APP is what cannot show it -- never that the agent asked something invalid, which is a
     * claim about the machine a phone holding an undecodable body is in no position to make. The
     * second is the way out, and it names both of them: the terminal answers the same question
     * (IS-LIFE-2 resolves a request whoever answers it), and an updated phone would draw it.
     *
     * IT IS THE ONE PLACE THIS SCREEN NAMES ITSELF. Everywhere else the copy is about the session
     * or the machine; here the subject is the build in the reader's hand, because that is the
     * fact that explains the empty card.
     */
    // ONE CONTIGUOUS LITERAL AND NOT TWO JOINED, which is a formatting choice made for a
    // checker: `scripts/check-conversation-copy.py` compares the sheet's sentence against the
    // comment-stripped source as a SUBSTRING, so a sentence split across a `+` is one no row can
    // ever bind -- the string exists nowhere in the file as written. `tool.more` is unbound for
    // the neighbouring reason and says so in the checker itself; this one does not have to be.
    // The line is over this file's usual width, deliberately: the alternative is a tabled
    // sentence that can drift from the sheet without anything noticing.
    private const val DECISION_UNRENDERABLE =
        "This version of swarm cannot show this question. Answer it at your machine, or update the app."
}
