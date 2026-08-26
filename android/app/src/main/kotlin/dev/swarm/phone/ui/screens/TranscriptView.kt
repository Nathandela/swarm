package dev.swarm.phone.ui.screens

import android.content.Context
import android.view.View
import android.view.ViewGroup
import android.widget.LinearLayout
import dev.swarm.phone.ui.kit.NoticeKind
import dev.swarm.phone.ui.kit.activityRow
import dev.swarm.phone.ui.kit.emptyState
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
 * visual decision is made here.** Five factories that already exist do all of the drawing --
 * `sectionLabel` over `sessionList` over one `activityRow` per block, `monoWell` for the blocks
 * that carry a machine-authored literal, `emptyState` for a conversation with nothing in it -- and
 * `android/gate/s24_screens_test.go` fences this package so that an `R.color`, an `R.dimen`, an
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
 * block in the app", and a tool's output and a unified diff are exactly that -- column-aligned text
 * that a body-copy layout would silently re-wrap, which misreports what the machine printed. It is
 * NOT the terminal variant: `terminal = true` is the escape-filtered VT snapshot's ink, and ADR-009
 * (1) leaves no grid anywhere in the app.
 *
 * **THE APPROVAL BLOCK IS A POINTER AND NOT THE DECISION.** ADR-009-obsidian-visual-direction D4.4
 * reserves the heaviest material in the app -- the sheet's vertical gradient and its 0.22-alpha
 * edge -- for "the moment of decision". The transcript's job is to say that a decision is waiting
 * and to get the reader to it; the sheet is where it is taken. So the block is an ordinary row that
 * can be tapped, and carries no second copy of the question, no well and no buttons.
 */
object TranscriptTag {
    /** The heading over the conversation. */
    const val SECTION_LABEL = "transcript.section.label"

    /** One item. */
    const val BLOCK = "transcript.block"

    /**
     * The block the machine is blocked on.
     *
     * IT IS ITS OWN TAG AND NOT A FLAG ON [BLOCK], which is `navHeaderDrill`'s reasoning about the
     * root header one component over: the two are drawn from the same factory and answer to
     * different rules -- one is a line of a conversation, the other is the way to the one surface
     * in this app that takes a decision. A single tag over both would let a test find either and
     * assert the other's behaviour.
     */
    const val APPROVAL = "transcript.approval"

    /**
     * The block a `tool_run` is still `in_progress` on (agents-tracker-dwwv.1.2).
     *
     * ITS OWN TAG FOR [APPROVAL]'s REASON, restated for this row: a single tag over both a
     * running block and an ordinary one would let a test find either and assert the other's
     * behaviour. What sets this block apart is drawn already -- [TranscriptTag.WELL] carries the
     * word the panel wrote -- and the tag is what lets a reader, or M2's card, find the row
     * without re-parsing that sentence.
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

    /** A tool's output or a file's diff, in the mono block every mono block in the app uses. */
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
     * ADR-017 T2 rule 2's proven boundary (Wave R6).
     *
     * ITS OWN TAG AND ITS OWN COMPONENT, for [APPROVAL]'s reason at its sharpest: the tear is the
     * one element on this screen that is not something the agent said, and a single tag over it
     * and the conversation would let a test find either and assert the other. It is `notice` at
     * the ERROR variant rather than `activityRow`, which is §4's own split -- the machine is the
     * one talking, and the sentence is about the RECORD rather than about the work.
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
 * @param onApproval where an approval block goes when it is tapped, called with the block's
 *  `item_id` -- which IS the `interaction_id` a signed `ActionApprove` names (IS-APR-1).
 *
 *  **NULL DRAWS THE BLOCK AND NOT A CONTROL**, which is `navHeaderDrill(back = null)`'s ruling and
 *  the defect it was written against (agents-tracker-2yb: "the chevron therefore looks like a
 *  control and does not act"). The two halves are separable and only one of them is optional: the
 *  conversation must never hide the thing the machine is waiting on, and a tap with no destination
 *  behind it is worse than no tap at all.
 */
fun transcriptView(
    context: Context,
    panel: TranscriptPanel,
    onApproval: ((String) -> Unit)? = null,
    onToolTap: ((String) -> Unit)? = null,
    onDetail: ((View, String) -> Unit)? = null,
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
                transcriptBlockViews(context, block, onApproval, onToolTap, onDetail)
                    .forEach(::addView)
            }
        },
    )
    return column
}

/**
 * The views ONE block composes, in order: its row (or its tear), its mono well, its detail offer.
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
 * arrangement every other pair of stacked cards in this app gets.
 */
internal fun transcriptBlockViews(
    context: Context,
    block: TranscriptBlock,
    onApproval: ((String) -> Unit)? = null,
    onToolTap: ((String) -> Unit)? = null,
    onDetail: ((View, String) -> Unit)? = null,
): List<View> {
    val views = mutableListOf<View>()
    // THE TEAR IS NOT A ROW, and that is ADR-017's requirement landing on a screen. A
    // `structured_gap` drawn as an `activityRow` would be one more thing in the conversation, in
    // the same card, in the same ink, at the same rhythm -- which is how a proven discontinuity
    // ends up reading as something the agent said. `notice(ERROR)` is §4's variant for the machine
    // talking about its own state, the register the stale mark and the refusal line already take.
    views.add(
        when {
            block.gap ->
                notice(context, block.line, NoticeKind.ERROR).apply { tag = TranscriptTag.GAP }
            // THE READER'S OWN WORDS, ON THE READER'S OWN SIDE. A bubble rather than a row, and
            // its own tag rather than a flag on [TranscriptTag.BLOCK] -- the reasoning APPROVAL
            // and RUNNING already give, restated for this one: a single tag over a bubble and a
            // row would let a test find either and assert the other's behaviour, and they are
            // not the same claim. A row says "here is a record"; a bubble says "somebody said
            // this".
            block.bubble ->
                messageBubble(context, block.line, mono = block.command)
                    .apply { tag = TranscriptTag.BUBBLE }
            else -> rowFor(context, block, onApproval, onToolTap)
        },
    )
    if (block.well.isNotEmpty()) {
        // `.scrolledHorizontally()` (agents-tracker-ksvb.7): a unified diff or a tool's stdout is
        // exactly the column-aligned text `setHorizontallyScrolling` refuses to wrap, and a line
        // past this card's own width was silently clipped with nothing below it wide enough to
        // reach the rest.
        views.add(
            monoWell(context, block.well)
                .apply { tag = TranscriptTag.WELL }
                .scrolledHorizontally(),
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
 */
internal fun transcriptBlockViewCount(block: TranscriptBlock, onDetail: ((View, String) -> Unit)?): Int =
    1 +
        (if (block.well.isNotEmpty()) 1 else 0) +
        (if (block.offersDetail && onDetail != null) 1 else 0)

/**
 * One block's row, and the one place a listener is attached.
 *
 * THE WHOLE ROW IS THE TARGET rather than a control inside it. There is no chevron component and no
 * trailing affordance in this kit, and a tap target smaller than the thing it belongs to is the
 * shape a reader misses on a moving surface.
 */
private fun rowFor(
    context: Context,
    block: TranscriptBlock,
    onApproval: ((String) -> Unit)?,
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
    if (!block.approval) {
        // agents-tracker-dwwv.1.2: a still-running tool_run is its own tag, same reasoning as
        // APPROVAL below -- see [TranscriptTag.RUNNING].
        tag = if (block.running) TranscriptTag.RUNNING else TranscriptTag.BLOCK
        // M2.2's expand/collapse: the whole row is the target, which is this file's standing
        // rule ("a tap target smaller than the thing it belongs to is the shape a reader misses
        // on a moving surface"). A card with nothing to hide is not clickable at all, so a row
        // that cannot collapse never looks like one that can.
        if (block.expandable) onToolTap?.let { toggle -> setOnClickListener { toggle(block.itemId) } }
        return@apply
    }
    tag = TranscriptTag.APPROVAL
    // See [transcriptView]'s `onApproval`: no destination, no control. `setOnClickListener` is
    // what makes a view clickable, so NOT setting one is the whole of not offering the tap.
    onApproval?.let { open -> setOnClickListener { open(block.itemId) } }
}

/**
 * What the clipped card's offer SAYS. It is copy, so it is here rather than in the kit (PB-DS-9),
 * and it names the ACTION and its cost: the bytes are on the machine and this asks for them.
 */
private const val DETAIL_OFFER = "This was clipped. Tap to fetch the whole of it from your machine."

private const val MATCH = ViewGroup.LayoutParams.MATCH_PARENT
private const val WRAP = ViewGroup.LayoutParams.WRAP_CONTENT
