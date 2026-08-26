package dev.swarm.phone.ui.screens

import android.content.Context
import android.view.View
import android.view.ViewGroup
import android.widget.LinearLayout
import dev.swarm.phone.ui.ApprovalDecision
import dev.swarm.phone.ui.kit.CtaKind
import dev.swarm.phone.ui.kit.activityRow
import dev.swarm.phone.ui.kit.approvalSheet
import dev.swarm.phone.ui.kit.ctaButton
import dev.swarm.phone.ui.kit.ctaStack
import dev.swarm.phone.ui.kit.emptyState
import dev.swarm.phone.ui.kit.fileChangeRow
import dev.swarm.phone.ui.kit.gapDivider
import dev.swarm.phone.ui.kit.markdownBody
import dev.swarm.phone.ui.kit.messageBubble
import dev.swarm.phone.ui.kit.monoWell
import dev.swarm.phone.ui.kit.notice
import dev.swarm.phone.ui.kit.scrolledHorizontally
import dev.swarm.phone.ui.kit.sectionLabel
import dev.swarm.phone.ui.kit.sessionList

/**
 * ADR-009-structured-chat-interaction (1) -- the chat transcript, composed from the component kit.
 *
 * IT IS THE SESSION'S CONTENT SURFACE, and after ADR-009 (3) deletes the terminal well it is the
 * only one. That raises the stakes on the rule this file follows rather than changing it: **not one
 * visual decision is made here.** Nine factories that already exist do all of the drawing --
 * `sectionLabel` over `sessionList` over one `activityRow` per block, `messageBubble` for the
 * reader's own words, `gapDivider` for a proven tear, `fileChangeRow` for a change, `approvalSheet`
 * for the decision, `monoWell` for the blocks that carry a machine-authored literal, `notice` for
 * the two offers that reach past this screen, `emptyState` for a conversation with nothing in it --
 * and `android/gate/s24_screens_test.go` fences this package so that an `R.color`, an `R.dimen`, an
 * `R.style`, a `setTextAppearance`, a `setPadding` or a `background =` here fails the build.
 *
 * THAT IS ADR-009-obsidian-visual-direction'S REQUIREMENT RESTATED AS ARRANGEMENT. A new screen
 * composes the vocabulary; it does not mint one. The champagne accent, the key light, the one
 * specular moment and the material ladder all reach this screen through the components, which is
 * the only way they can reach it consistently.
 *
 * **§2's REUSE RULE, SPENT TWICE.** `activityRow` is documented as taking "a body and an optional
 * emphasis rather than a JournalRow" precisely so a new caller costs no new component; the
 * transcript is its fourth caller and adds no type. `monoWell` is "one component for every mono
 * block in the app", and a tool's output is exactly that -- column-aligned text that a body-copy
 * layout would silently re-wrap, which misreports what the machine printed. It is NOT the terminal
 * variant: `terminal = true` is the escape-filtered VT snapshot's ink, and ADR-009 (1) leaves no
 * grid anywhere in the app.
 *
 * ## The four things the signed drawing changed here (2026-08-26)
 *
 * **THE APPROVAL BLOCK IS THE DECISION AND NO LONGER A POINTER AT ONE** (owner ruling R4, and R7's
 * "one renderer"). What stood here was an ordinary row that opened a sheet, on the reading that
 * D4.4 reserves the heaviest material in the app for "the moment of decision" and that the moment
 * belongs on its own surface. The ruling reversed the second half and left the first intact: the
 * material is the sheet's, drawn INLINE at the item, because a question the agent is blocked on is
 * a message in the stream and the reader answers it where they met it. A row that pointed
 * elsewhere was a second place for one question to live, and the drawing gives it one.
 *
 * **A PROVEN TEAR IS A RULE AND NOT A PARAGRAPH** (ADR-017 T2 rule 2, redrawn). `notice(ERROR)`
 * says the right thing in the right voice and takes the width of the reading column to say it --
 * between two rows of a conversation, which is something a reader has to finish before they can
 * carry on reading. `gapDivider` is that same statement at 48 dp of height with its own repair on
 * it, which is the component's own recorded derivation rather than a resemblance.
 *
 * **A FILE CHANGE IS A CHIP AND ITS DIFF OPENS ELSEWHERE** (owner ruling R9). The unified diff used
 * to be poured into the reading column unconditionally, so a refactor touching nine files cost nine
 * screens of the conversation. One tappable line per file carries the verb, the path and the
 * counts; the diff opens on [diffScreen], which has room to scroll sideways -- which is what a diff
 * needs and what a message stream cannot give it.
 *
 * **A BODY PAST THE IN-PLACE BOUND SAYS HOW MUCH MORE THERE IS** (owner ruling R8). An opened card
 * draws twenty lines and keeps the whole of the body on the block's route; without the offer this
 * file draws for it, the card is a head with no way on -- a truncation the phone invented, which is
 * what IS-TOOL-3 forbids one field over.
 *
 * ## The one rule all four obey
 *
 * **NO DESTINATION, NO CONTROL.** It is `navHeaderDrill(back = null)`'s ruling and the defect it
 * was written against (agents-tracker-2yb: "the chevron therefore looks like a control and does not
 * act"), and every new affordance below is drawn only when the caller has somewhere to send it. The
 * cost is stated rather than hidden: a host that has not wired [transcriptView]'s `onOutput` draws
 * a bounded card with no way on, which is a state the drawing does not allow. The answer to that is
 * for the host to wire it, never for this file to draw a control that does nothing -- and the two
 * halves are separable, so the conversation never hides the thing the machine is waiting on.
 */
object TranscriptTag {
    /** The heading over the conversation. */
    const val SECTION_LABEL = "transcript.section.label"

    /** One item. */
    const val BLOCK = "transcript.block"

    /**
     * The decision the machine is blocked on, drawn where it was asked (owner rulings R4 and R7).
     *
     * IT IS ITS OWN TAG AND NOT A FLAG ON [BLOCK], which is `navHeaderDrill`'s reasoning about the
     * root header one component over: the two are drawn from DIFFERENT factories now and answer to
     * different rules -- one is a line of a conversation, the other is the one element on this
     * screen that can cost a person something irreversible. A single tag over both would let a
     * test find either and assert the other's behaviour.
     *
     * **IT IS THE ANSWERABLE CARD AND NEVER [UNRENDERABLE]'s.** `TranscriptPanel.pendingDecisionId`
     * points a jump at this tag, and a card whose only sentence tells the reader to go to their
     * machine is not somewhere to send them.
     */
    const val APPROVAL = "transcript.approval"

    /**
     * A decision this build cannot draw, said as the card rather than as a row.
     *
     * ITS OWN TAG FOR [APPROVAL]'s REASON, at its sharpest: the two are the same component in the
     * same place and exactly one of them can be answered here. A shared tag would let the pill, or
     * a test, treat "the agent is waiting on you" and "this phone cannot show you what it is
     * waiting on" as one state.
     */
    const val UNRENDERABLE = "transcript.unrenderable"

    /**
     * One decision the CLI offered, labelled by `decisions[].label` (§3.5).
     *
     * IT IS NOT `SheetTag.ACTION`. The sheet's buttons and these are the same factory answering to
     * one rule -- IS-APR-4, the verdict is the machine's -- and to different surfaces; a shared tag
     * would let a test find a button in the sheet and assert the transcript's lock behaviour on it.
     */
    const val DECISION_CHOICE = "transcript.decision.choice"

    /**
     * §7's action, or IS-LIFE-6's sanitized prompt: the literal the answer commits to.
     *
     * IT IS NOT [WELL], AND THE DIFFERENCE IS WHERE IT IS DRAWN -- inside the card, between the
     * question and the buttons, because it is the thing being agreed to rather than a record of
     * something already done. See [TranscriptBlock.literal], which argues the same split from the
     * model's side.
     */
    const val DECISION_LITERAL = "transcript.decision.literal"

    /**
     * The block a `tool_run` is still `in_progress` on (agents-tracker-dwwv.1.2).
     *
     * ITS OWN TAG FOR [APPROVAL]'s REASON, restated for this row: a single tag over a running
     * block and an ordinary one would let a test find either and assert the other's behaviour.
     * What sets this block apart is drawn already -- [TranscriptTag.WELL] carries the word the
     * panel wrote -- and the tag is what lets a reader, or M2's card, find the row without
     * re-parsing that sentence.
     */
    const val RUNNING = "transcript.running"

    /**
     * The reader's own message (kit row 26).
     *
     * ITS OWN TAG FOR [APPROVAL]'s REASON, at its sharpest yet: a bubble and a row are drawn by
     * different factories and answer to different rules, and a single tag over both would let a
     * test find either and assert the other's behaviour.
     */
    const val BUBBLE = "transcript.bubble"

    /**
     * One file the agent changed, as R9's chip (`fileChangeRow`).
     *
     * ITS OWN TAG, AND HERE THE REASON IS MECHANICAL AS WELL AS PRINCIPLED: the chip carries three
     * cells and no `KitTag.ACTIVITY_BODY`, so a test that found it under [BLOCK] would not merely
     * assert the wrong thing -- every assertion written against a row would fail on it for a
     * reason that has nothing to do with what changed on disk.
     */
    const val FILE_CHANGE = "transcript.filechange"

    /**
     * R8's offer: the rest of a bounded body, on [outputScreen].
     *
     * ITS OWN TAG AND NOT [DETAIL]'s, because the two answer different questions and can be drawn
     * together. This one is about HEIGHT -- the screen chose to draw twenty lines of a body it
     * holds whole -- and [DETAIL] is about BYTES the machine clipped and still has. A card can be
     * both, and a reader tapping one would get the other.
     */
    const val ROUTE = "transcript.route"

    /** A tool's output, in the mono block every mono block in the app uses. */
    const val WELL = "transcript.well"

    /** Row 8's block, under a heading whose session has said nothing yet. */
    const val EMPTY = "transcript.empty"

    /**
     * `.prows` -- the container the blocks sit in.
     *
     * IT IS TAGGED SO THAT A PATCH CAN FIND IT (Mirror M2.3). `sessionDetailRedraw` mutates the
     * rows in place rather than recomposing the column, and the container is what it mutates; a
     * child index would start meaning something else the day the heading gains a sibling.
     */
    const val LIST = "transcript.list"

    /**
     * ADR-017 T2 rule 2's proven boundary (Wave R6), redrawn by the signed drawing as `gapDivider`.
     *
     * ITS OWN TAG AND ITS OWN COMPONENT, for [APPROVAL]'s reason at its sharpest: the tear is the
     * one element on this screen that is not something the agent said, and a single tag over it
     * and the conversation would let a test find either and assert the other. What changed is the
     * component and not the voice -- `gapDivider` is `notice(ERROR)` "minus the paragraph", the
     * same `--p-err` ink and the same machine-talking-about-its-own-state register, at a fraction
     * of the height and carrying its own repair.
     */
    const val GAP = "transcript.gap"

    /**
     * IS-CAP-2/M3.3's offer: the full pre-truncation body this card was clipped from, which the
     * machine still holds. Its own tag because it is the one control in the conversation that
     * reaches the wire, and a test must be able to find it without finding a row.
     */
    const val DETAIL = "transcript.detail"
}

/**
 * The transcript as a view.
 *
 * @param panel what the conversation says, decided by [TranscriptScreen]. This places it.
 * @param onApproval where a decision goes when the CARD ITSELF is tapped, called with the block's
 *  `item_id` -- which IS the `interaction_id` a signed `ActionApprove` names (IS-APR-1).
 *
 *  **IT IS THE ROUTE A HOST GETS UNTIL IT WIRES [onDecision], AND NOT A SECOND AFFORDANCE BESIDE
 *  IT.** Owner ruling R7 is one renderer, so a card that both carried the machine's buttons and
 *  opened a sheet holding the same buttons would be two pending states over one live-only act.
 *  Exactly one is drawn: with an answer wired the card carries the choices and is not itself
 *  tappable; without one it carries no choices and opens the sheet, which is what ships today.
 *  Wave F deletes the sheet and this parameter with it.
 *
 *  **NULL DRAWS THE BLOCK AND NOT A CONTROL**, which is `navHeaderDrill(back = null)`'s ruling and
 *  the defect it was written against (agents-tracker-2yb: "the chevron therefore looks like a
 *  control and does not act"). The two halves are separable and only one of them is optional: the
 *  conversation must never hide the thing the machine is waiting on, and a tap with no destination
 *  behind it is worse than no tap at all.
 * @param onDecision the answer, called with the block's `item_id` and the decision the reader
 *  pressed -- `ApprovalDecision` and not a label, because `App.Approve` names the `id` and a
 *  surface that sent back the words it drew would be re-deriving the machine's own key from copy.
 * @param onRepair ADR-017's repair, reachable ON the tear. `gapDivider`'s own row: the whole line
 *  is the control, because "an inline span cannot carry a 48 dp target".
 * @param onOutput R8's overflow, called with the `item_id` whose [TranscriptRoute.Output] the
 *  reader asked to see whole. The host holds the panel, so it can reach the route's text without
 *  this file handing a body back through a callback.
 * @param onDiff R9's diff, called with the `item_id` whose [TranscriptRoute.Diff] the chip opens.
 */
fun transcriptView(
    context: Context,
    panel: TranscriptPanel,
    onApproval: ((String) -> Unit)? = null,
    onToolTap: ((String) -> Unit)? = null,
    onDetail: ((View, String) -> Unit)? = null,
    onRepair: (() -> Unit)? = null,
    onOutput: ((String) -> Unit)? = null,
    onDiff: ((String) -> Unit)? = null,
    onDecision: ((String, ApprovalDecision) -> Unit)? = null,
): View {
    val column = LinearLayout(context).apply {
        orientation = LinearLayout.VERTICAL
        // A glowing dot and an inflated halo are drawn past their own bounds, and every container
        // between them and the window has to allow it. `sessionDetailView` carries the same two
        // lines for the same reason.
        clipChildren = false
        clipToPadding = false
        layoutParams = LinearLayout.LayoutParams(MATCH, WRAP)
    }

    column.addView(sectionLabel(context, panel.heading).apply { tag = TranscriptTag.SECTION_LABEL })

    // THE HEADING STAYS WHEN THE ROWS GO. PB-DS-9's rule is that an empty section is still a
    // section: dropping the heading with its rows leaves a gap where a section used to be, and
    // "this conversation has not reached this phone" stops being something the screen can say.
    if (panel.blocks.isEmpty()) {
        column.addView(emptyState(context, panel.emptyCopy).apply { tag = TranscriptTag.EMPTY })
        return column
    }

    column.addView(
        sessionList(context).apply {
            tag = TranscriptTag.LIST
            panel.blocks.forEach { block ->
                transcriptBlockViews(
                    context, block, onApproval, onToolTap, onDetail,
                    onRepair, onOutput, onDiff, onDecision,
                ).forEach(::addView)
            }
        },
    )
    return column
}

/**
 * The views ONE block composes, in order: its head, its mono well, its route offer, its detail
 * offer.
 *
 * IT IS A FUNCTION AND NOT A LOOP BODY because `sessionDetailRedraw` composes exactly one block at
 * a time when it patches the conversation in place (Mirror M2.3). A patch that rebuilt the rows
 * from its own copy of this arrangement would be a second statement of what a block looks like,
 * and the two would drift the first time a block gained a part.
 *
 * A BLOCK IS SEVERAL SIBLINGS AND NOT ONE CONTAINER. `activityRow` is a sentence with an optional
 * marked span; a factory that also took a block of mono would be a second component wearing the
 * first one's name, and wrapping the pair in a group of this file's own would spend a gap the
 * container already spends. So `sessionList`'s own gap is what puts them together -- which is the
 * arrangement every other pair of stacked cards in this app gets. It is also what makes the
 * decision READ IN ORDER with the message it belongs to: the card is a sibling at the item's own
 * position, so a screen reader walks the conversation in the order it was said without this file
 * having to describe the order to it.
 *
 * THE HEAD IS ONE OF SIX AND THE ORDER OF THE ARMS IS LOAD-BEARING. A tear is not a message; a
 * bubble is not a row; a change is a chip; a question is a card. Each of the first four is decided
 * by a field the MODEL set for exactly that purpose, so this file never reads a sentence back to
 * find out what it is drawing -- which is PB-DS-9 in the direction that is easy to lose.
 */
internal fun transcriptBlockViews(
    context: Context,
    block: TranscriptBlock,
    onApproval: ((String) -> Unit)? = null,
    onToolTap: ((String) -> Unit)? = null,
    onDetail: ((View, String) -> Unit)? = null,
    onRepair: (() -> Unit)? = null,
    onOutput: ((String) -> Unit)? = null,
    onDiff: ((String) -> Unit)? = null,
    onDecision: ((String, ApprovalDecision) -> Unit)? = null,
): List<View> {
    val views = mutableListOf<View>()
    // BOUND BEFORE THE `when`, so the arm that uses it never depends on a smart cast surviving a
    // lambda. The chip's three cells are read from the model's own record of the change.
    val change = block.fileChange
    views.add(
        when {
            // THE TEAR IS NOT A ROW, and that is ADR-017's requirement landing on a screen. A
            // `structured_gap` drawn as an `activityRow` would be one more thing in the
            // conversation, in the same card, in the same ink, at the same rhythm -- which is how
            // a proven discontinuity ends up reading as something the agent said.
            block.gap -> gapDivider(context, block.line).apply {
                tag = TranscriptTag.GAP
                // ONE AFFORDANCE PER OPERATION, and it is HERE rather than in the overflow menu:
                // two routes to one live-only act are two pending states competing over which is
                // in flight. No repair wired, no control -- the tear is still drawn, because the
                // discontinuity is a fact whether or not this build can mend it.
                onRepair?.let { repair -> setOnClickListener { repair() } }
            }
            // THE READER'S OWN WORDS, ON THE READER'S OWN SIDE. A bubble rather than a row, and
            // its own tag rather than a flag on [TranscriptTag.BLOCK] -- the reasoning APPROVAL
            // and RUNNING already give, restated for this one: a single tag over a bubble and a
            // row would let a test find either and assert the other's behaviour, and they are
            // not the same claim. A row says "here is a record"; a bubble says "somebody said
            // this".
            block.bubble ->
                messageBubble(context, block.line, mono = block.command)
                    .apply { tag = TranscriptTag.BUBBLE }
            // R9's CHIP. The three cells are the model's, separate, because handing the row this
            // block's sentence and letting it split on the separator would be a view reading copy
            // back out of a sentence the screen wrote -- PB-DS-9 inverted.
            change != null -> fileChangeRow(
                context,
                verb = change.verb,
                path = change.path,
                counts = change.counts,
            ).apply {
                tag = TranscriptTag.FILE_CHANGE
                // A `delete` carries no `diff_excerpt` and the model routes it nowhere; an offer
                // onto an empty page is the dead-chevron defect wearing a route. The CHIP IS THE
                // HANDLE, so there is no second "open the diff" affordance drawn beside it.
                if (block.route is TranscriptRoute.Diff) {
                    onDiff?.let { open -> setOnClickListener { open(block.itemId) } }
                }
            }
            block.approval || block.unrenderable -> decisionCard(context, block, onApproval, onDecision)
            else -> rowFor(context, block, onToolTap)
        },
    )
    if (block.well.isNotEmpty()) {
        // `.scrolledHorizontally()` (agents-tracker-ksvb.7): a tool's stdout is exactly the
        // column-aligned text `setHorizontallyScrolling` refuses to wrap, and a line past this
        // card's own width was silently clipped with nothing below it wide enough to reach the
        // rest.
        views.add(
            monoWell(context, block.well)
                .apply { tag = TranscriptTag.WELL }
                .scrolledHorizontally(),
        )
    }
    // R8's OFFER, DIRECTLY UNDER THE HEAD IT IS ABOUT. It names the SIZE of what is behind it --
    // the drawing's `tool.more`, drawn verbatim from the model, which tables the singular and the
    // plural separately because one template renders `1 more lines` at exactly the count likeliest
    // to occur. A body that fits carries [TranscriptRoute.None] and nothing is drawn, which is the
    // difference between an offer and a chevron on every card.
    val overflow = block.route as? TranscriptRoute.Output
    val openOutput = onOutput
    if (overflow != null && openOutput != null) {
        views.add(
            notice(context, overflow.label).apply {
                tag = TranscriptTag.ROUTE
                setOnClickListener { openOutput(block.itemId) }
            },
        )
    }
    // IS-CAP-2's OFFER, UNDER THE CLIPPED THING IT IS ABOUT (M3.3). It is a sentence the reader
    // taps rather than a button, on this file's own standing rule about the surface: the
    // conversation's controls are its rows, and a second button register inside a chat column
    // would compete with the one control this screen reserves for a decision. No destination, no
    // tap -- `onApproval`'s ruling, which is `navHeaderDrill(back = null)`'s one component over.
    if (block.offersDetail && onDetail != null) {
        views.add(
            notice(context, DETAIL_OFFER).apply {
                tag = TranscriptTag.DETAIL
                // THE TAPPED VIEW RIDES WITH THE ID, which is `VerbDispatch.press`'s whole
                // contract: the control it is handed is disabled while the press is crossing, so
                // a tap that has been made does not look untapped. Handing it the screen's
                // container instead would disable the conversation.
                setOnClickListener { view -> onDetail(view, block.itemId) }
            },
        )
    }
    return views
}

/**
 * How many siblings [transcriptBlockViews] composes for [block]. Kept HERE, immediately beside the
 * composition, because it is the same statement counted rather than built: a patch that walks the
 * container needs to know where one block's views end and the next block's begin, and a count
 * living anywhere else would drift the first time a block gained a part.
 *
 * **IT TAKES THE HANDLERS THAT DECIDE A VIEW'S EXISTENCE, AND EVERY CALLER MUST PASS THE SAME ONES
 * IT PASSES TO [transcriptBlockViews].** Two of the offers are drawn only when there is somewhere
 * to send them, so a redraw that counted with `onOutput = null` and composed with one wired would
 * splice the next block's row into the middle of this one. The parameters are defaulted null so
 * the two functions agree by construction for a caller that wires neither -- which is the state
 * every caller is in until the conversation composition lands.
 */
internal fun transcriptBlockViewCount(
    block: TranscriptBlock,
    onDetail: ((View, String) -> Unit)?,
    onOutput: ((String) -> Unit)? = null,
): Int =
    1 +
        (if (block.well.isNotEmpty()) 1 else 0) +
        (if (block.route is TranscriptRoute.Output && onOutput != null) 1 else 0) +
        (if (block.offersDetail && onDetail != null) 1 else 0)

/**
 * The question the agent is blocked on, drawn where it was asked (owner rulings R4 and R7).
 *
 * **IT IS `approvalSheet`'s OWN MATERIAL AND NOT A CARD THIS FILE BUILT.** ADR-009's visual
 * direction D4.4 reserves the heaviest material in the app -- the sheet's vertical gradient and its
 * 0.22-alpha edge -- for "the moment of decision", and the amendment moves that moment inline
 * rather than diluting it: "one word, no new material". A screen may not author a surface anyway
 * (`android/gate/s24_screens_test.go`), which is the fence agreeing with the design.
 *
 * **THE CONTEXT LINE IS EMPTY, DELIBERATELY.** The sheet's first line answers "who is asking" --
 * the project, the agent and the machine -- because a sheet can be raised over any screen. This
 * card is drawn INSIDE one session's conversation, under a header that has already named the
 * session and the machine, so repeating it here would be the same fact in two voices at the top of
 * the one element a reader must read carefully. Passing the empty string is this screen declining
 * to say it twice; the component drawing an empty cell rather than no cell is a gap in the kit,
 * recorded rather than worked around (see the report's REQUEST for a nullable `contextLine`, which
 * is `well`'s own arrangement in the same factory).
 *
 * **EVERY CHOICE IS `CtaKind.MORE`, AND THAT IS IS-APR-4 RATHER THAN A TASTE.** `.a2-ok` and
 * `.a2-no` ARE a polarity claim, and this side has no field to make one from: the verdict is
 * machine-side, the wire carries `{id, label}`, and painting a label green would assert a grant the
 * daemon never told this side about. `approvalSheetView` reached the same conclusion for the sheet
 * and the reasoning is copied here rather than cited, because this is the second surface that could
 * get it wrong.
 *
 * **THE LOCK IS ON EVERY CHOICE AND NOT ON THE ONE THAT WAS TAPPED.** A card that greyed only the
 * pressed button would leave the other seven live over an answer already sent -- two
 * `ActionApprove`s for one `interaction_id`, and whichever the daemon resolves first decides which
 * of the reader's two answers the agent acted on. The card keeps its full height while it is
 * locked, so nothing reflows under a descending thumb.
 *
 * **AND THERE IS NO DISMISS** (owner ruling, 2026-08-26). "Dismiss" implies the question goes away,
 * and it is still waiting at the machine: IS-LIFE-2 resolves a request exactly once, and a card
 * hidden here would have resolved nothing while telling the reader it had. The way out of a
 * question this build cannot draw is [TranscriptBlock.unrenderable]'s sentence, which names the two
 * places it can be answered.
 */
private fun decisionCard(
    context: Context,
    block: TranscriptBlock,
    onApproval: ((String) -> Unit)?,
    onDecision: ((String, ApprovalDecision) -> Unit)?,
): View {
    // AN UNRENDERABLE REQUEST IS NEVER ANSWERABLE HERE, whatever the host wired: the body did not
    // decode, so there is no `decisions[]` to press and no id to name. `block.approval` is the
    // model's own "this can still be answered", and the null is what keeps a host from drawing
    // buttons over a question this build could not read.
    val answer = if (block.approval) onDecision else null
    return approvalSheet(
        context = context,
        contextLine = "",
        question = block.line,
        // ABSENT IS NOT EMPTY: a decision whose action names no literal draws no well, rather than
        // a recessed box saying nothing in the shape of a command that is blank.
        //
        // AND IT IS NEVER BOUNDED. R8's twenty-line bound exists because an opened tool run is a
        // body the reader CHOSE to look at and can leave; a literal is what an answer commits to,
        // and a reader may not be asked to approve the half of a command they were shown.
        // `.scrolledHorizontally()` is the same fact for width: a long shell command is exactly
        // the line `setHorizontallyScrolling` refuses to wrap, and this card is asking the reader
        // to approve it.
        well = if (block.literal.isEmpty()) {
            null
        } else {
            monoWell(context, block.literal)
                .apply { tag = TranscriptTag.DECISION_LITERAL }
                .scrolledHorizontally()
        },
        // ONE ACTION, AND IT IS `.acts2` -- THE COLUMN THE KIT ALREADY HAS FOR TWO OR MORE CTAs.
        // The sheet lays its actions out in an equal-weight ROW, which is right for the two
        // buttons a sheet was drawn with and wrong for what §5 permits here: one to EIGHT labels
        // the CLI wrote, at whatever length the CLI wrote them ("Yes, and don't ask again this
        // session"). Three of those side by side are three columns of wrapped fragments. The
        // drawing stacks them, `ctaStack` IS that stack (`origin: .acts2`), and handing the sheet
        // a single full-width child keeps its own equal-weight rule intact rather than bending
        // it -- inside the column every button is `MATCH`, so no answer is drawn wider than
        // another. Recorded as a REQUEST against `approvalSheet` rather than worked around
        // silently: the sheet one screen over has the same defect at the same counts.
        actions = if (answer == null) {
            emptyList()
        } else {
            // BOUND NON-NULL HERE rather than invoked through the smart cast three lambdas deep.
            val send = answer
            listOf(
                ctaStack(context).apply {
                    block.choices.forEach { choice ->
                        addView(
                            ctaButton(context, choice.label, CtaKind.MORE).apply {
                                tag = TranscriptTag.DECISION_CHOICE
                                // PB-SEC-12 clause 1. An approval is the most privileged control
                                // this app draws: a tap that arrived through a window this
                                // surface does not own would authorise a command on the reader's
                                // machine.
                                filterTouchesWhenObscured = true
                                isEnabled = !block.locked
                                if (!block.locked) setOnClickListener { send(block.itemId, choice) }
                            },
                        )
                    }
                },
            )
        },
    ).apply {
        tag = if (block.unrenderable) TranscriptTag.UNRENDERABLE else TranscriptTag.APPROVAL
        // THE DRAWING'S OWN RULE: the decision announces itself to a screen reader when it
        // arrives. A live region is what makes an arrival an announcement rather than something
        // found by scrolling, and POLITE is the register: it waits for the reader to finish the
        // sentence they are on, which on a surface that is also streaming an agent's prose is the
        // difference between being told and being interrupted.
        accessibilityLiveRegion = View.ACCESSIBILITY_LIVE_REGION_POLITE
        // THE SHEET, UNTIL A HOST WIRES AN ANSWER -- and only then. See [transcriptView]'s
        // `onApproval`: exactly one affordance is ever drawn, because two routes to one live-only
        // act are two pending states competing over which is in flight.
        if (answer == null && block.approval) {
            onApproval?.let { open -> setOnClickListener { open(block.itemId) } }
        }
    }
}

/**
 * One block's row, and the one place a listener is attached.
 *
 * THE WHOLE ROW IS THE TARGET rather than a control inside it. There is no chevron component and no
 * trailing affordance in this kit, and a tap target smaller than the thing it belongs to is the
 * shape a reader misses on a moving surface.
 *
 * IT NO LONGER HAS AN APPROVAL ARM, and that is owner ruling R4 rather than a tidy-up. A question
 * is [decisionCard] now; what still reaches this function from an `approval_request` is the
 * RESOLVED one, which IS-LIFE-2 guarantees stops being a decision -- it stays in the transcript
 * because it is what was asked, and it is an ordinary record of it.
 */
private fun rowFor(
    context: Context,
    block: TranscriptBlock,
    onToolTap: ((String) -> Unit)?,
): View = activityRow(
    context = context,
    // M2.1: the prose the model already flattened, given its type here. `markdownBody` styles
    // exactly the string the model decided the row reads as -- passed in rather than recomputed,
    // so a span can never land at an offset naming different words -- and a block with no
    // markdown (every kind but `agent_message`) is handed its line unchanged.
    body = if (block.markdown.isEmpty()) {
        block.line
    } else {
        markdownBody(context, block.markdown, block.line)
    },
    emphasis = block.emphasis,
    // M2.2's separator, spent as the head-of-turn time. Absent everywhere else, which is what
    // makes the boundary legible: `activityRow`'s cell costs nothing when it is null.
    timestamp = block.timestamp.ifEmpty { null },
    // M2.2's glyph, read from ONE flat field by the kit's card model.
    glyph = block.glyph.ifEmpty { null },
).apply {
    // agents-tracker-dwwv.1.2: a still-running tool_run is its own tag, same reasoning as
    // APPROVAL -- see [TranscriptTag.RUNNING].
    tag = if (block.running) TranscriptTag.RUNNING else TranscriptTag.BLOCK
    // M2.2's expand/collapse: the whole row is the target, which is this file's standing rule
    // ("a tap target smaller than the thing it belongs to is the shape a reader misses on a
    // moving surface"). A card with nothing to hide is not clickable at all, so a row that
    // cannot collapse never looks like one that can.
    if (block.expandable) onToolTap?.let { toggle -> setOnClickListener { toggle(block.itemId) } }
}

/**
 * What the clipped card's offer SAYS. It is copy, so it is here rather than in the kit (PB-DS-9),
 * and it names the ACTION and its cost: the bytes are on the machine and this asks for them.
 *
 * THE DRAWING TABLES A SENTENCE THIS ONE CANNOT SAY YET, AND THE REASON IS PLUMBING RATHER THAN
 * PROTOCOL. `tool.clipped`'s opened form is "Clipped at N KB. Tap to fetch the whole of it from
 * your machine.", and this file said N was "a quantity nothing on this side holds". That was
 * wrong in the way that matters to whoever picks this up: the MACHINE authors it. §2 carries
 * `full_bytes` -- "byte length of the untruncated payload, carried only with `truncated`"
 * (interaction-schema.md:87) -- and IS-CAP-1 (:269) REQUIRES it be set alongside `truncated`, so
 * a clipped item on the wire already names its own full size. `internal/skeleton/interaction.go`
 * measures and sets it; `internal/daemon/interaction.go` refuses a record that carries one
 * without the other.
 *
 * WHAT IS MISSING IS THE FOUR HOPS BETWEEN THAT FIELD AND THIS SENTENCE. `phonecore.Item` folds
 * `Truncated` and `Detail` and drops `FullBytes`, so it reaches neither `mobile/types.go` nor
 * `InteractionItem` nor the block. Writing a number here would therefore still be this screen
 * inventing a fact -- not because the fact does not exist, but because this side never received
 * it -- so the shipped sentence stands and the divergence is recorded rather than papered over.
 * It closes by carrying `full_bytes` those four hops, which is a plumbing request with a known
 * source and not a protocol change.
 */
private const val DETAIL_OFFER = "This was clipped. Tap to fetch the whole of it from your machine."

private const val MATCH = ViewGroup.LayoutParams.MATCH_PARENT
private const val WRAP = ViewGroup.LayoutParams.WRAP_CONTENT
