package dev.swarm.phone.ui

import dev.swarm.phone.keys.ConnectionState

/**
 * Phase B slice S16 -- PB-APP-8 (connection and stale UX) and PB-APP-10's transport half.
 *
 * ONLINE IS THE ONLY QUIET STATE. Everything else is a condition the user is entitled to see,
 * and several of them are not network conditions at all: a custody refusal or a revocation
 * rendered as "reconnecting" tells the user to wait for something that waiting never ends,
 * which is the defect S14 recorded.
 *
 * The state list is dev.swarm.phone.keys.ConnectionState rather than a second enum here.
 * That enum's `of` errors on a wire string it does not know, so the mapping is total in the
 * direction the UI reads it: a state the facade starts reporting is a loud failure until
 * someone adds it, not a screen that silently renders nothing.
 */

/** What the connection banner says, and whether it says "wait" or "do something". */
data class ConnectionBanner(
    val text: String,
    val visible: Boolean,
    val remedy: Remedy,
    /**
     * A spinner is a promise that waiting is enough. It is honest for exactly the states the
     * app is still retrying in, and dishonest for every state that ends only when the user
     * acts -- which is why the custody verdict and a revocation never carry one.
     */
    val showsSpinner: Boolean,
    /** The app has stopped retrying and must say so rather than looking busy forever. */
    val terminal: Boolean,
) {
    companion object {

        fun of(state: ConnectionState): ConnectionBanner = when (state) {
            ConnectionState.OFFLINE -> state.banner(
                "Not connected to your machine.",
                Remedy.WAIT_FOR_CONNECTION,
                showsSpinner = false,
            )

            ConnectionState.CONNECTING -> state.banner(
                "Connecting to your machine.",
                Remedy.WAIT_FOR_CONNECTION,
                showsSpinner = true,
            )

            ConnectionState.ONLINE -> state.banner(
                "Connected to your machine.",
                Remedy.NONE,
                showsSpinner = false,
            )

            ConnectionState.RECONNECTING -> state.banner(
                "Lost the link to your machine; reconnecting.",
                Remedy.WAIT_FOR_CONNECTION,
                showsSpinner = true,
            )

            ConnectionState.REPAIR_REQUIRED -> state.banner(
                "This phone's key was destroyed and cannot be recovered. Pair this device again.",
                Remedy.RE_PAIR,
                showsSpinner = false,
            )

            // Same remedy as the row above and NOT the same cause, so it must not read the
            // same: the machine still holds a registration the owner has to clear before a
            // re-pair can succeed, and a user told only to "pair again" will try and fail.
            ConnectionState.REVOKED -> state.banner(
                "The owner removed this device. Clear its registration on the machine, then " +
                    "pair this phone again.",
                Remedy.RE_PAIR,
                showsSpinner = false,
            )

            // The transport policy refused the relay. Neither of the two below is a link
            // that can come back, so neither may carry a spinner -- and they do not share a
            // remedy, which is why they are two rows and not one.
            ConnectionState.RELAY_UNTRUSTED -> state.banner(
                "This phone will not connect to that relay: it is not presenting the identity " +
                    "your machine published when you paired. Pair this phone again.",
                Remedy.RE_PAIR,
                showsSpinner = false,
            )

            ConnectionState.RELAY_INSECURE -> state.banner(
                "Your machine is configured to use an unencrypted relay, which this phone " +
                    "refuses. Fix the relay address on the machine, then pair this phone again.",
                Remedy.RE_PAIR,
                showsSpinner = false,
            )
        }

        /**
         * Visibility and terminality are DERIVED, not transcribed per row. `isTerminal` already
         * belongs to the connection state -- a second copy here could disagree with the enum
         * that the transport loop itself consults.
         */
        private fun ConnectionState.banner(
            text: String,
            remedy: Remedy,
            showsSpinner: Boolean,
        ) = ConnectionBanner(
            text = text,
            visible = this != ConnectionState.ONLINE,
            remedy = remedy,
            showsSpinner = showsSpinner,
            terminal = isTerminal,
        )
    }
}

/** How one stream is doing. Never a property of the phone: see [StreamView]. */
enum class StreamBadge { LIVE, STALE, RESYNCING }

/**
 * PB-APP-8's per-stream half. STALENESS BELONGS TO ONE STREAM, never to the handset: the
 * journal can have an unrepaired hole while the terminal is live, and a single global "stale"
 * mark either understates the first or slanders the second.
 *
 * [resyncPending] is ORTHOGONAL to [stale] rather than a third value of it. Expressing "repair
 * in flight" by clearing the stale mark is PB-SYNC-3's optimistic clear, which shows a known
 * hole as live -- the one thing PB-APP-8 forbids. The mark clears when the repair LANDS.
 */
data class StreamView(
    val stream: String,
    val stale: Boolean,
    val resyncPending: Boolean,
) {
    val badge: StreamBadge
        get() = when {
            resyncPending -> StreamBadge.RESYNCING
            stale -> StreamBadge.STALE
            else -> StreamBadge.LIVE
        }

    val notice: String
        get() = when (badge) {
            StreamBadge.LIVE -> ""
            StreamBadge.STALE -> "The $stream view has a gap and may be missing events."
            StreamBadge.RESYNCING -> "Repairing the $stream view; the gap clears when the " +
                "repair arrives."
        }
}

/**
 * PB-TIME-1's verdict, rendered from a PULL rather than from an event.
 *
 * The event plane alone cannot serve a screen that opens AFTER the measurement, which on
 * Android is most of them. It is not latched either: correcting the clock empties the verdict,
 * and a banner that kept the first one would tell a user with a correct clock to fix their
 * clock forever.
 *
 * It is deliberately NOT an error class. The daemon's refusal of a skewed command reads "not
 * authorized", which sends the user to pair again when the fix is to correct their clock.
 */
data class ClockBanner(
    val text: String,
    val visible: Boolean,
    val remedy: Remedy,
) {
    companion object {
        /** @param verdict `App.ClockVerdict`: empty while in budget, the legible reason otherwise. */
        fun of(verdict: String): ClockBanner =
            if (verdict.isBlank()) {
                ClockBanner(text = "", visible = false, remedy = Remedy.NONE)
            } else {
                ClockBanner(text = verdict, visible = true, remedy = Remedy.FIX_CLOCK)
            }
    }
}

/**
 * agents-tracker-e6mi -- what the app says about its link, ABOVE whatever screen the user is on.
 *
 * IT EXISTS BECAUSE THE THREE FACTS WERE ONE STRING. `PhoneSurface` built the line as
 * `listOfNotNull(banner.text, freshness, staleNotice).joinToString(" ")`, so a user whose link had
 * dropped while their machine was also silent read three sentences from three different
 * authorities as one run-on paragraph, with no way to tell where one ended. They are separate here
 * because they ARE separate: [ConnectionBanner] is the TRANSPORT's opinion, [MachineFreshness] is
 * the machine's own clock (that class argues at length why the second is not the first), and the
 * stale notice is the journal stream's completeness. Any one of the three can be true while the
 * other two are false.
 *
 * THE EMPTINESS RULE IS THE MODEL'S AND NOT THE VIEW'S. A join cannot express "hide this one":
 * an empty middle fact left two sentences with two spaces between them, so the surface filtered
 * empties on the way in and the rule lived at the call site. [lines] is where it lives now, which
 * is what makes it testable without a view and what stops a second view assembling its own.
 *
 * ONLINE CONTRIBUTES NOTHING, and that is [ConnectionBanner.visible] finally being read. The field
 * has said "ONLINE IS THE ONLY QUIET STATE" since S16 and no caller consulted it -- the surface
 * spent `.text` unconditionally -- so every phone with a working link carried a permanent
 * "Connected to your machine." in the place its warnings go. A banner that is always up is a
 * banner nobody reads.
 */
data class StatusBanner(
    /** The transport's, or empty while the link is quiet. */
    val connection: String,
    /** The machine's own clock, or empty while it is inside section 6.0's budget. */
    val freshness: String,
    /** The journal stream's, or empty while the roster is whole. */
    val stale: String,
) {
    /**
     * The lines to draw, in the order a reader meets them: the link, then the machine, then the
     * list. Outward from the phone -- a link that is down explains a machine that is silent, which
     * in turn explains a list that is holed, and the reverse order asks the reader to work
     * backwards.
     */
    val lines: List<String> get() = listOf(connection, freshness, stale).filter { it.isNotEmpty() }

    /** True when the phone has nothing to warn about. The banner draws no air in that state. */
    val silent: Boolean get() = lines.isEmpty()

    companion object {
        /** Nothing to say -- the state a phone with no core to ask is honestly in. */
        val NONE = StatusBanner(connection = "", freshness = "", stale = "")

        /**
         * @param freshness [MachineFreshness.notice]'s answer: null while the machine is inside
         *  its budget. It arrives already formatted because the TIME is the caller's -- an Android
         *  formatter carrying the user's locale and time zone -- which is that method's own
         *  arrangement and is why this model needs no clock to be testable.
         * @param staleNotice [TriageInbox.staleNotice], which decided the wording in S16.
         */
        fun of(
            connection: ConnectionBanner,
            freshness: String?,
            staleNotice: String,
        ): StatusBanner = StatusBanner(
            connection = if (connection.visible) connection.text else "",
            freshness = freshness.orEmpty(),
            stale = staleNotice,
        )
    }
}

/**
 * PB-APP-11 -- how recently the MACHINE itself last spoke, and whether anything on screen may
 * still be presented as current.
 *
 * IT IS NOT A CONNECTION STATE, and that is the whole reason it exists. The declared adversary
 * (ADR-007 D9) does not have to break the connection: it withholds the newest frames and keeps
 * answering the polls. No gap forms, so no stream is stale; the poll succeeds, so
 * [ConnectionBanner] reads "Connected to your machine."; and `App.Presence` asks that same
 * relay whether the machine is alive. The only signal left is the machine's own AAD-covered
 * timestamp, which a relay can make older by holding a frame and can never make newer.
 *
 * @param silent `swarmmobile.Freshness.Silent`: past section 6.0's five-minute budget.
 * @param lastHeardUnixMs the MACHINE's own stamp, not this phone's arrival time. Zero means
 *   this phone has never heard from its machine -- a first launch, or a restore that has not
 *   yet taken a frame -- which is silent, and honestly so.
 */
data class MachineFreshness(
    val silent: Boolean,
    val lastHeardUnixMs: Long,
) {
    /**
     * The user-facing line, or null while the machine is inside the budget.
     *
     * The time is formatted by the CALLER (an Android formatter carrying the user's locale and
     * time zone), so this model states WHAT to say and never has to be right about a time zone
     * to be testable.
     *
     * It says "not heard from" rather than "your machine is offline" deliberately: nothing on
     * this wire is a liveness beacon, so an idle machine and a withheld one are
     * indistinguishable from here. The phone reports what it knows.
     */
    fun notice(formatTime: (Long) -> String): String? = when {
        !silent -> null
        lastHeardUnixMs == 0L -> "Not heard from your machine yet."
        else -> "Not heard from your machine since ${formatTime(lastHeardUnixMs)}."
    }
}
