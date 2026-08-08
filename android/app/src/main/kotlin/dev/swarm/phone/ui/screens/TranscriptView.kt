package dev.swarm.phone.ui.screens

import android.content.Context
import android.view.View
import android.view.ViewGroup
import android.widget.LinearLayout
import dev.swarm.phone.ui.kit.activityRow
import dev.swarm.phone.ui.kit.emptyState
import dev.swarm.phone.ui.kit.monoWell
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

    /** A tool's output or a file's diff, in the mono block every mono block in the app uses. */
    const val WELL = "transcript.well"

    /** Row 8's block, under a heading whose session has said nothing yet. */
    const val EMPTY = "transcript.empty"
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
            panel.blocks.forEach { block ->
                addView(rowFor(context, block, onApproval))
                // AND THE WELL IS A SIBLING RATHER THAN A CHILD OF THE ROW. `activityRow` is a
                // sentence with an optional marked span; a factory that also took a block of mono
                // would be a second component wearing the first one's name. The container's own
                // gap is what puts them together, which is the same arrangement every other pair
                // of stacked cards in this app gets.
                if (block.well.isNotEmpty()) {
                    // `.scrolledHorizontally()` (agents-tracker-ksvb.7): a unified diff or a
                    // tool's stdout is exactly the column-aligned text `setHorizontallyScrolling`
                    // refuses to wrap, and a line past this card's own width was silently
                    // clipped with nothing below it wide enough to reach the rest.
                    addView(
                        monoWell(context, block.well)
                            .apply { tag = TranscriptTag.WELL }
                            .scrolledHorizontally(),
                    )
                }
            }
        },
    )
    return column
}

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
): View = activityRow(
    context = context,
    body = block.line,
    emphasis = block.emphasis,
).apply {
    if (!block.approval) {
        tag = TranscriptTag.BLOCK
        return@apply
    }
    tag = TranscriptTag.APPROVAL
    // See [transcriptView]'s `onApproval`: no destination, no control. `setOnClickListener` is
    // what makes a view clickable, so NOT setting one is the whole of not offering the tap.
    onApproval?.let { open -> setOnClickListener { open(block.itemId) } }
}

private const val MATCH = ViewGroup.LayoutParams.MATCH_PARENT
private const val WRAP = ViewGroup.LayoutParams.WRAP_CONTENT
