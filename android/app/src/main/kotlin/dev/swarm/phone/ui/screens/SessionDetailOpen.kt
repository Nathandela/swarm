package dev.swarm.phone.ui.screens

import dev.swarm.phone.ui.InteractionItem

/**
 * Mirror M3.2 + the M3 deep-link landing (Wave R6), as MODELS: the cold-open backfill plan
 * and the anchor a notification tap lands on. Pure JVM; SessionDetailOpenTest drives both,
 * and the intent plumbing that feeds them is the wave's disclosed view-side residual.
 */
object SessionDetailOpen {

    /**
     * M3.2's throttle: flipping in and out of a detail screen must not multiply reads
     * against the machine-to-phone 8/s append ceiling, so a re-open inside this window asks
     * for nothing. A duration, not a dimension: nothing visual is decided here.
     */
    const val BACKFILL_THROTTLE_MS: Long = 30_000L

    /**
     * Whether opening the detail screen requests a history backfill: a COLD open (no items
     * held for the session) outside the throttle window does -- a week-old session must show
     * its history on open, not an empty well and a Repair button -- and a transcript that
     * already has items backfills on the reader's own "load earlier", never on open.
     */
    fun plan(itemCount: Int, lastBackfillAtMs: Long, nowMs: Long): Boolean =
        itemCount == 0 && nowMs - lastBackfillAtMs >= BACKFILL_THROTTLE_MS
}

/**
 * Where a deep-link landed. [NotRetained] is a NAMED state the screen renders ("no longer in
 * the retained history"), never a silent landing at the top: a tap that lands on the wrong
 * thing is worse than one that says so.
 */
sealed class DeepLinkLanding {
    data class Found(val index: Int) : DeepLinkLanding()
    object NotRetained : DeepLinkLanding()
}

object DeepLinkAnchor {

    /**
     * The landing for one notification's item_id, BY ID and never by position or cursor
     * (IS-ENV-2): after a daemon restart's reconciliation re-delivered the transcript at new
     * cursors, the item_id is the notification's only stable coordinate -- and it is enough.
     */
    fun resolve(items: List<InteractionItem>, itemId: String): DeepLinkLanding =
        resolveById(items.map { it.itemId }, itemId)

    /**
     * The same landing over the ids a SCREEN is holding.
     *
     * IT EXISTS BECAUSE THE SURFACE HOLDS BLOCKS AND NOT ITEMS: `PhoneSurface` keeps the panel it
     * last drew, which is `TranscriptScreen`'s model of the conversation, and re-reading the
     * facade to turn it back into items would be a second read answering a different question
     * about a different instant. One rule, two entry points -- [resolve] delegates here.
     */
    fun resolveById(itemIds: List<String>, itemId: String): DeepLinkLanding {
        val index = itemIds.indexOf(itemId)
        return if (index >= 0) DeepLinkLanding.Found(index) else DeepLinkLanding.NotRetained
    }
}
