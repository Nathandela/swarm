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
    /**
     * Whether this state's remedy IS the pairing flow (agents-tracker-agre).
     *
     * IT IS [RoutedError.offersPairing]'S QUESTION, ASKED OF THE TRANSPORT. The two types carry the
     * same [Remedy] enum and only one of them could be asked, so a banner that told the user to
     * pair again had no way to become a control -- and the states where that matters are the two
     * this banner is the ONLY speaker for: `RELAY_UNTRUSTED` and `RELAY_INSECURE` are terminal and
     * do NOT end a pairing, so `mobile/relay.go`'s `transportEndsPairing` leaves the handset inside
     * the app with this sentence and nothing to press.
     */
    val offersPairing: Boolean get() = remedy.offersPairing

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
     * **IT SPENDS [sinceLastHeard] AND NO LONGER A CLOCK** (agents-tracker-2pnu F5,
     * agents-tracker-egjh). It used to take an Android time formatter and print
     * `Not heard from your machine since 14:57.` -- a wall-clock time with no date on it, which
     * at 09:00 the next morning is indistinguishable from a machine heard from three minutes ago.
     * Field test 3 photographed that, and the sync detail's HEARD row one tap away was already
     * showing the elapsed duration, so the app said the same fact two ways within one tap.
     *
     * ONE WORDING FOR BOTH READERS, which is why the duration is not re-derived here: this and
     * `SyncStatus.of` spend the same function on the same field, so a change to the coarsening
     * reaches both.
     *
     * It says "not heard from" rather than "your machine is offline" deliberately: nothing on
     * this wire is a liveness beacon, so an idle machine and a withheld one are
     * indistinguishable from here. The phone reports what it knows.
     *
     * @param nowUnixMs this phone's clock, for the elapsed duration alone. Taken rather than read
     *  so the model is testable without one -- `SyncStatus.of`'s own arrangement.
     */
    fun notice(nowUnixMs: Long): String? = when {
        !silent -> null
        lastHeardUnixMs == 0L -> "Not heard from your machine yet."
        else -> "Not heard from your machine for ${sinceLastHeard(nowUnixMs)}."
    }

    /**
     * How long since the machine's own last word, coarsely: `4m`, `18h`, `3d`. Empty where this
     * phone has never heard from its machine at all (agents-tracker-nx44.2).
     *
     * **IT EXISTS BECAUSE [notice]'S TIME IS A BARE CLOCK.** The formatter it takes carries the
     * user's locale and time zone and nothing else, so the sentence reads `since 14:57` with no
     * date on it -- and at 09:00 the next morning that is indistinguishable from a machine heard
     * from three minutes ago. Field test 3 photographed exactly that. An elapsed duration cannot
     * be misread that way: it only ever grows, and one glance says whether the number is minutes
     * or days.
     *
     * ONE UNIT, NEVER TWO. `18h` and not `18h 4m`: this is read on a nav-row pill sized for three
     * upper-case characters, and the second unit is precision nobody acts on -- the decision a
     * reader takes from it is *recent* or *stale*, which the leading unit already answers.
     *
     * A NEGATIVE ELAPSE FLOORS AT ZERO RATHER THAN RENDERING. The machine's stamp is its own
     * clock and this phone's is its own; PB-TIME-1 exists because the two can disagree, and a
     * pill reading `-3m` would report the skew as the machine's silence.
     */
    fun sinceLastHeard(nowUnixMs: Long): String {
        if (lastHeardUnixMs == 0L) return ""
        val elapsed = (nowUnixMs - lastHeardUnixMs).coerceAtLeast(0L)
        val minutes = elapsed / MINUTE_MS
        val hours = minutes / MINUTES_PER_HOUR
        val days = hours / HOURS_PER_DAY
        return when {
            minutes < MINUTES_PER_HOUR -> "${minutes}m"
            hours < HOURS_PER_DAY -> "${hours}h"
            else -> "${days}d"
        }
    }

    private companion object {
        const val MINUTE_MS = 60_000L
        const val MINUTES_PER_HOUR = 60L
        const val HOURS_PER_DAY = 24L
    }
}
