package dev.swarm.phone.ui

import dev.swarm.phone.keys.ConnectionState

/**
 * Phase B slice S16 -- PB-APP-8 (connection and stale UX) and PB-APP-10's transport half.
 *
 * ONLINE IS THE ONLY QUIET STATE. Everything else is a condition the user is entitled to see,
 * and two of them are not network conditions at all: a custody refusal rendered as
 * "reconnecting" tells the user to wait for something only a biometric ends, which is the
 * defect S14 recorded.
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
     * acts -- which is why the two custody verdicts and a revocation never carry one.
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

            ConnectionState.REAUTH_REQUIRED -> state.banner(
                "Authenticate to reconnect -- the key that signs this phone in is behind your " +
                    "device unlock.",
                Remedy.AUTHENTICATE,
                showsSpinner = false,
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
