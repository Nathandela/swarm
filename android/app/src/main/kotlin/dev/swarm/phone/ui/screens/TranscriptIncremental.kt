package dev.swarm.phone.ui.screens

import dev.swarm.phone.ui.InteractionItem

/**
 * Mirror M2.3 (Wave R6) -- the incremental transcript as a MODEL: [TranscriptIncremental.reconcile]
 * diffs two reads of the transcript BY item_id (IS-ENV-2's fold rule carried to the screen:
 * never by position) into the minimal mutation list a redraw applies -- insertions at their own
 * position for new ids, in-place rebinds for changed ones, removals for ids the new read no
 * longer holds, NOTHING for the unchanged. Pure JVM; TranscriptIncrementalTest and
 * TranscriptIncrementalPositionTest drive it and `sessionDetailRedraw` applies it.
 *
 * WHY A MODEL: the defect this row closes (esed) is the detail column rebuilt whole per journal
 * event, re-laying out under the reader's finger at whatever rate the agent works. WHICH rows
 * changed is a pure function over two lists and needs no Activity to check.
 *
 * THE MUTATION LIST IS A COMPLETE DESCRIPTION OF THE DIFFERENCE, and it was not until Wave R6's
 * review finding B12 -- which is the property that matters, because a redraw that applies an
 * INCOMPLETE description faithfully still ends up showing something neither read contained:
 *
 *  - An insertion carries WHERE IT GOES. ADR-014's "load earlier" pages BACKWARDS, so a page of
 *    history arrives at the FRONT; a positionless append could only add at the end, which put a
 *    week-old exchange below the message the reader was looking at. A conversation reordered by
 *    the phone is the one defect a chat surface cannot survive.
 *  - A row the new read no longer holds is REMOVED. `phonecore.MaxItemsPerSession` trims the head
 *    on every insert past the bound, and with no removal in the vocabulary that row stayed on
 *    screen forever -- an item the phone does not hold, drawn above ones it does.
 */
sealed class TranscriptMutation {
    /**
     * A new item_id, at [index] in the NEW read.
     *
     * IT IS STILL CALLED Append BECAUSE THE COMMON CASE IS ONE, and the index is what makes the
     * uncommon case expressible: an agent's next message arrives at the end, a paged read arrives
     * at the front, and a redraw can now tell those apart without guessing from the content.
     */
    data class Append(
        val index: Int,
        val item: InteractionItem,
        /**
         * Whether this insertion is the LAST row of the new read -- new conversation rather than
         * paged history. It is decided by [TranscriptIncremental.reconcile], which is the only
         * place that knows the new read's length, precisely so no caller has to reconstruct
         * "was this the end" from a position and a count it does not have.
         */
        val tail: Boolean = false,
    ) : TranscriptMutation()

    /** A known item_id whose facts changed: the row at [index] (in the NEW read) rebinds. */
    data class Rebind(val index: Int, val item: InteractionItem) : TranscriptMutation()

    /**
     * An item_id the new read no longer holds. Named by ID and not by position, because by the
     * time a redraw applies it the positions around it have already moved.
     */
    data class Remove(val itemId: String) : TranscriptMutation()
}

/**
 * The same three shapes over what the screen DRAWS rather than over what the wire delivered.
 *
 * Two vocabularies and ONE rule: see [TranscriptIncremental.reconcileBlocks] for why the redraw
 * cannot key off the items, and [TranscriptIncremental.diff] for the rule they share.
 */
sealed class BlockMutation {
    data class Insert(
        val index: Int,
        val block: TranscriptBlock,
        val tail: Boolean = false,
    ) : BlockMutation()

    data class Rebind(val index: Int, val block: TranscriptBlock) : BlockMutation()

    data class Remove(val itemId: String) : BlockMutation()
}

object TranscriptIncremental {

    /**
     * The diff, keyed by item_id. A superset read yields insertions ONLY -- unchanged rows keep
     * their views, and the finger on them keeps its scroll; a status flip, an approval
     * resolution or a streamed text growth is a single in-place rebind of its own row; an
     * unchanged read yields nothing at all (redrawing it is the esed defect itself).
     *
     * REMOVALS COME FIRST, which is not cosmetic: a caller applies the list in order against a
     * live view group, and taking the departed rows out before putting the new ones in is what
     * makes every [TranscriptMutation.Append] index name the same row in the list and on screen.
     */
    fun reconcile(old: List<InteractionItem>, new: List<InteractionItem>): List<TranscriptMutation> =
        diff(
            old, new, { it.itemId },
            insert = { index, item, tail -> TranscriptMutation.Append(index, item, tail) },
            rebind = { index, item -> TranscriptMutation.Rebind(index, item) },
            remove = { id -> TranscriptMutation.Remove(id) },
        )

    /**
     * The same diff over what the screen actually DRAWS.
     *
     * IT IS A SECOND ENTRY POINT AND NOT A SECOND RULE -- both call [diff], which is where the
     * whole of "keyed by item_id, insertions carry their position, departures are named" lives.
     * The redraw needs this one rather than [reconcile] because a difference the screen renders is
     * not always a difference in the item: collapsing a tool card changes the BLOCK and leaves the
     * item untouched, so an item-keyed diff would report nothing and the card would stay open.
     */
    fun reconcileBlocks(
        old: List<TranscriptBlock>,
        new: List<TranscriptBlock>,
    ): List<BlockMutation> = diff(
        old, new, { it.itemId },
        insert = { index, block, tail -> BlockMutation.Insert(index, block, tail) },
        rebind = { index, block -> BlockMutation.Rebind(index, block) },
        remove = { id -> BlockMutation.Remove(id) },
    )

    /**
     * The rule, once: removals first (so every insertion index names the same row in the list and
     * on screen), then one pass over the new read in order.
     */
    private fun <T, M> diff(
        old: List<T>,
        new: List<T>,
        key: (T) -> String,
        insert: (Int, T, Boolean) -> M,
        rebind: (Int, T) -> M,
        remove: (String) -> M,
    ): List<M> {
        val known = HashMap<String, T>(old.size)
        for (element in old) known[key(element)] = element
        val held = HashSet<String>(new.size)
        for (element in new) held.add(key(element))

        val out = mutableListOf<M>()
        for (element in old) {
            if (!held.contains(key(element))) out.add(remove(key(element)))
        }
        new.forEachIndexed { index, element ->
            val prev = known[key(element)]
            when {
                prev == null -> out.add(insert(index, element, index == new.size - 1))
                prev != element -> out.add(rebind(index, element))
            }
        }
        return out
    }

    /**
     * M2.3's two scroll rules in one predicate: the transcript follows the conversation ONLY
     * when the reader was already at the bottom AND the mutations carry new conversation AT THE
     * END. Scrolled up, a burst never yanks the reader down; a rebind alone -- a status pulse --
     * never scrolls anybody, from anywhere; and history arriving at the FRONT never scrolls
     * either, because the reader who asked for older messages must not be thrown to the newest.
     *
     * "At the end" is [TranscriptMutation.Append.tail], decided where the new read's length is
     * known, which is what lets ONE read that both trims the head and appends a message still do
     * the right thing on both halves.
     *
     * AND A THIRD RULE, WHICH IS THE COMMITTEE'S (plan §8): an UNANSWERED DECISION suppresses it
     * entirely. Sticking to the bottom is what makes a conversation readable while an agent
     * works, and it is exactly what carries a reader PAST the one item that is waiting on them --
     * the agent asks, keeps working, and its next two messages push the question off the top
     * while the reader watches the screen move. The suppression is here rather than at the call
     * site because this predicate is where "should the screen move" is decided, and a caller that
     * had to remember one extra condition is a caller that will forget it on the second surface.
     *
     * @param decisionPending [TranscriptPanel.pendingDecisionId] is not empty: some decision on
     *  this transcript is unanswered. Defaulted so a surface that has no decisions to draw does
     *  not have to say so.
     */
    fun stickToBottom(
        atBottom: Boolean,
        mutations: List<TranscriptMutation>,
        decisionPending: Boolean = false,
    ): Boolean =
        atBottom && !decisionPending && mutations.any { it is TranscriptMutation.Append && it.tail }

    /** The same predicate over what the screen draws. See [reconcileBlocks]. */
    @JvmName("stickToBottomAfterBlocks")
    fun stickToBottom(
        atBottom: Boolean,
        mutations: List<BlockMutation>,
        decisionPending: Boolean = false,
    ): Boolean =
        atBottom && !decisionPending && mutations.any { it is BlockMutation.Insert && it.tail }

    /**
     * The reader's anchor, found again BY ID after a burst moved every position: the row a
     * redraw restores the viewport to. -1 for an id the read no longer holds.
     */
    fun anchorIndex(items: List<InteractionItem>, itemId: String): Int =
        items.indexOfFirst { it.itemId == itemId }
}
