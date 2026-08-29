package dev.swarm.phone.ui

/**
 * agents-tracker-nx44.2 -- what the app says about whether what is on screen is CURRENT.
 *
 * ## It replaces a stack of sentences with a rank
 *
 * `StatusBanner` carried up to four facts as four lines of body copy above every destination: the
 * transport's opinion, PB-SYNC-7's write-hold, the machine's own clock and the roster's
 * completeness. Field test 3 (2026-08-09) is what that looks like on a handset -- three transparent
 * sentences where the screen's title should be, pushing the whole app down, answering four questions
 * when the user has one: *is this current, and if not, what do I do*.
 *
 * THE FOUR FACTS COMPOSE INTO A PRIORITY AND NOT INTO A PARAGRAPH. A phone whose transport has
 * stopped retrying does not also need to be told its journal has a hole -- the hole is the dead
 * link, seen from further down. So the rank is [SyncState]'s declaration order read backwards:
 * broken beats quiet beats syncing beats live, and exactly one of them is on screen.
 *
 * **LIVE RENDERS NOTHING AT ALL**, which is the discipline `ConnectionBanner` has stated since S16
 * ("online is the only quiet state") and which the stack broke the moment two facts were true at
 * once. A mark that is always up is a mark nobody reads.
 *
 * ## The three facts are not deleted, they move behind a tap
 *
 * They are still the only evidence this phone has, so they are still readable -- as [SyncDetail]'s
 * three labelled rows, which the pill opens. What changes is that a person reads them when they ask
 * rather than instead of their screen.
 *
 * ## Duration, never a clock
 *
 * `MachineFreshness.notice` USED TO format `lastHeardUnixMs` through an Android time formatter,
 * which yielded a bare clock time: at 09:00 the next morning `since 14:57` was indistinguishable
 * from a machine heard from three minutes ago. agents-tracker-2pnu F5 retired that formatter --
 * `notice` spends `sinceLastHeard` now, the same duration this model reports -- so everything
 * here is an ELAPSED duration, which is monotonic in the one direction a reader cares about.
 */
enum class SyncState {
    /** Nothing to say. The only state that draws no pill. */
    LIVE,

    /** Work outstanding that ends by itself: a link coming back, a hold, a hole being mended. */
    SYNCING,

    /** The MACHINE has stopped speaking, past section 6.0's budget. Nothing here is retrying. */
    QUIET,

    /** The transport has stopped retrying. Only a person ends this one. */
    BROKEN,
}

/** One labelled fact in the detail sheet. The label is a heading; the value is the evidence. */
data class SyncRow(val label: String, val value: String)

/**
 * What the sheet says: the three facts the banner used to stack, the gaps by channel, and the one
 * thing to press.
 *
 * THE ROWS ALWAYS RENDER, INCLUDING THE HEALTHY ONES, and that is [LinkPanel]'s own ruling about
 * its four channels one level up: this sheet is opened deliberately, so hiding a healthy row makes
 * "all three are fine" indistinguishable from "the sheet forgot one" -- which is the failure
 * PB-APP-8 is about in the first place. The PILL is the fault report; this is the readout behind it.
 */
data class SyncDetail(
    /** HEARD, READING, VIEWS -- in that order, outward from the machine. */
    val rows: List<SyncRow>,
    /**
     * One line per repair channel with a hole in it, in `FacadeBridge.REPAIR_CHANNELS` order.
     *
     * [StreamView.notice]'S OWN WORDS, taken rather than re-typed. That model already decides what
     * a gap and a repair-in-flight say, and a second phrasing here would be a second answer able to
     * disagree with the one the session detail draws.
     */
    val gaps: List<String>,
    /** The words on the one control, or empty where there is nothing to repair. */
    val repair: String,
)

/**
 * The composed status: which state, what the pill reads, what the strip reads, and what is behind
 * the tap.
 */
data class SyncStatus(
    val state: SyncState,
    /** The nav-row pill's words, or empty while the phone is live. */
    val pill: String,
    /**
     * The one-line strip's words, or empty for every state but [SyncState.BROKEN].
     *
     * IT IS THE TRANSPORT'S OWN SENTENCE AND NOT A SECOND ONE. `ConnectionBanner` already says what
     * each terminal state means and what to do about it; a line authored here would be a second
     * copy of a remedy, and the two would disagree the first time either moved.
     */
    val strip: String,
    /** What a screen reader hears on the pill, or empty while there is no pill. */
    val description: String,
    val detail: SyncDetail,
) {
    /** True when the phone has nothing to report. Nothing is drawn in that state -- no slot, no air. */
    val silent: Boolean get() = state == SyncState.LIVE

    companion object {

        /** The pill's word for [SyncState.SYNCING]. */
        const val SYNCING = "Syncing…"

        /** The pill's words for [SyncState.QUIET], followed by the elapsed duration. */
        const val QUIET = "Last seen"

        /** The whole pill where the machine has never been heard from at all (phone refit W5.4). */
        const val NOT_SEEN = "Not seen yet"

        /** The pill's word for [SyncState.BROKEN]. */
        const val BROKEN = "Offline"

        /** The machine's own clock. */
        const val HEARD = "Last heard"

        /** The transport's opinion -- whether frames can arrive at all. */
        const val READING = "Link"

        /** The repair channels' completeness -- whether the ones that arrived are all of them. */
        const val VIEWS = "Up to date"

        /**
         * The HEARD row where `lastHeardUnixMs` is zero.
         *
         * A first launch, or a restore that has not taken a frame yet. There is no elapsed time to
         * state and `0m` would claim the machine spoke at the epoch.
         */
        const val NEVER = "Never"

        /**
         * What the resync control reads.
         *
         * IT NAMES THE WHOLE READOUT AND NOT ONE CHANNEL, which is what `App.Resync` actually does:
         * the rewind is the TRANSPORT's read position, shared by all four channels, so one press
         * mends every gap this sheet lists. `SessionDetailPanel.RESYNC` reads "Repair this record"
         * because there it sits beside one conversation; here the subject is the phone.
         */
        const val REPAIR = "Reload"

        /**
         * What the pairing control reads. It is `StatusBanner.PAIR_AGAIN`'s word, unchanged, and
         * the argument for it is unchanged too (agents-tracker-agre).
         *
         * IT NAMES WHERE IT GOES RATHER THAN WHAT IT WISHES IT DID. `RELAY_UNTRUSTED` and
         * `RELAY_INSECURE` do not end a pairing, and `swarm remote pair` is refused while a device
         * is registered (PB-STATE-10) -- so a button labelled "Pair again" would promise an act the
         * machine declines. The Settings destination leads with the `Pairing` section, whose one
         * control clears the registration the re-pair is blocked on.
         */
        const val PAIR_AGAIN = "Pair again"

        /** Nothing to say -- the state a phone with no core to ask is honestly in. */
        val NONE = SyncStatus(
            state = SyncState.LIVE,
            pill = "",
            strip = "",
            description = "",
            detail = SyncDetail(rows = emptyList(), gaps = emptyList(), repair = ""),
        )

        /**
         * @param connection the TRANSPORT's opinion. Its `terminal` is the whole of [SyncState
         *  .BROKEN] -- the app has stopped retrying, which is the one condition waiting cannot fix.
         * @param freshness the MACHINE's own clock (PB-APP-11). Its `silent` is [SyncState.QUIET],
         *  and `lastHeardUnixMs` is the only source of the duration on the pill.
         * @param nowUnixMs this phone's clock, for the elapsed duration alone. The model takes it
         *  rather than reading one so it is testable without a clock, which is
         *  `MachineFreshness.notice`'s own arrangement too, now that agents-tracker-2pnu F5
         *  retired the formatter it used to take.
         * @param streams `FacadeBridge.streamViews()` -- one per repair channel, in the adapter's
         *  order. The order is not decided here.
         * @param reconciled `StateSummary.Reconciled`: PB-SYNC-7's fail-closed hold. IT DEFAULTS TO
         *  TRUE -- "nothing held" -- so a caller that does not name it is not made to look like a
         *  phone permanently waiting on a machine it never asked.
         */
        fun of(
            connection: ConnectionBanner,
            freshness: MachineFreshness,
            nowUnixMs: Long,
            streams: List<StreamView>,
            reconciled: Boolean = true,
        ): SyncStatus {
            val gapped = streams.filter { it.badge != StreamBadge.LIVE }
            val since = freshness.sinceLastHeard(nowUnixMs)
            val state = when {
                connection.terminal -> SyncState.BROKEN
                freshness.silent -> SyncState.QUIET
                // EVERY REMAINING UNQUIET FACT IS ONE STATE, because all three end by themselves:
                // a link the app is still retrying, a write-hold the machine clears, and a hole the
                // resync verb mends. `connection.visible` is the transport's own emptiness rule --
                // ONLINE contributes nothing -- read here rather than re-derived.
                !reconciled || connection.visible || gapped.isNotEmpty() -> SyncState.SYNCING
                else -> SyncState.LIVE
            }
            return SyncStatus(
                state = state,
                pill = when (state) {
                    SyncState.LIVE -> ""
                    SyncState.SYNCING -> SYNCING
                    SyncState.QUIET -> if (since.isEmpty()) NOT_SEEN else "$QUIET $since"
                    SyncState.BROKEN -> BROKEN
                },
                // ONLY BROKEN ESCALATES. The strip is opaque chrome that displaces the destination,
                // which is the cost the stacked banner used to pay on every phone that had anything
                // at all to say; it is worth paying once, for the one state where the user has to
                // act and the pill alone cannot say what the act is.
                strip = if (state == SyncState.BROKEN) connection.text else "",
                description = descriptionOf(state, since),
                detail = SyncDetail(
                    rows = listOf(
                        SyncRow(HEARD, if (since.isEmpty()) NEVER else "$since ago"),
                        SyncRow(READING, connection.text),
                        SyncRow(VIEWS, "${streams.size - gapped.size} of ${streams.size} current"),
                    ),
                    gaps = gapped.map { it.notice },
                    // THE PAIRING OFFER OUTRANKS THE RESYNC, and the order is the point: a machine
                    // that has stopped answering this device cannot serve a rewind, so a resync
                    // offered here would be a control that earns a refusal. `offersPairing` is
                    // `ConnectionBanner`'s own derivation from its remedy, asked rather than
                    // re-decided.
                    repair = when {
                        connection.offersPairing -> PAIR_AGAIN
                        gapped.isNotEmpty() -> REPAIR
                        else -> ""
                    },
                ),
            )
        }

        /**
         * What the pill announces.
         *
         * THE PILL'S OWN TEXT IS NOT ENOUGH, which is `badge`'s recorded rule: the words are three
         * upper-case tokens sized for a nav row, and "QUIET 18h" read aloud is not a sentence. It
         * also has to say that it OPENS something -- a mark that takes a tap and announces itself
         * as a label is a control a screen-reader user cannot find.
         */
        private fun descriptionOf(state: SyncState, since: String): String = when (state) {
            SyncState.LIVE -> ""
            SyncState.SYNCING -> "Sync status: syncing. Open details."
            SyncState.QUIET -> if (since.isEmpty()) {
                "Sync status: your computer has not been heard from. Open details."
            } else {
                "Sync status: your computer has been quiet for $since. Open details."
            }
            SyncState.BROKEN -> "Sync status: broken. Open details."
        }
    }
}
