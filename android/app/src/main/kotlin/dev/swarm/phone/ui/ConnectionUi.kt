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
    /**
     * PB-SYNC-7's fail-closed hold, or empty once the machine has published the rollback
     * authorities a mutating op needs (agents-tracker-pxz8).
     *
     * IT IS READ PROACTIVELY, WHICH IS THE WHOLE OF THE FIX. `StateSummary.Reconciled` crosses
     * the boundary and, until this field existed, was read by no Kotlin at all -- only `.paired`
     * and `.machine` were. So the hold was invisible until a mutating press ran into
     * `swarm/unreconciled`, and the screen that answer landed on read as THAT PRESS failing
     * rather than as a state the phone was already sitting in before it was pressed. The
     * sentence is [ErrorRouter]'s own for `ErrClassUnreconciled`, taken rather than re-typed, so
     * a user reads the same words whether this banner catches them first or a press does.
     *
     * IT IS ITS OWN FACT AND NOT [connection]'S. Every row [ConnectionBanner] can hold is either
     * a healthy link or one this phone cannot use at all; this reports a link that works FOR
     * READING throughout, with only writes on hold, which is a third condition neither of that
     * type's two states can express.
     *
     * DEFAULTED FOR [pairAgain]'S REASON. `StatusBanner(connection = ..., freshness = ..., stale
     * = ...)` is how the suites that predate this fact build one directly, and a banner assembled
     * without naming it should not silently start claiming a hold nobody asked for.
     */
    val syncing: String = "",
    /** The machine's own clock, or empty while it is inside section 6.0's budget. */
    val freshness: String,
    /** The journal stream's, or empty while the roster is whole. */
    val stale: String,
    /**
     * The words on the CONTROL this banner offers, or empty where it offers none
     * (agents-tracker-agre).
     *
     * IT IS NOT A FOURTH FACT AND IT IS DELIBERATELY OUT OF [lines]. The three above are sentences
     * a reader reads; this is a thing a finger presses, and folding it into the list would render
     * it as a fourth paragraph -- the defect it exists to end, one level down. The view draws it
     * from this field and the surface says where it leads.
     *
     * WHY IT IS DEFAULTED. `StatusBanner(connection = ..., freshness = ..., stale = ...)` is how
     * the suites build one directly, and a banner assembled from three facts alone offers no
     * control -- the offer is derived in [of], from the transport's own remedy, and nowhere else.
     */
    val pairAgain: String = "",
) {
    /**
     * The lines to draw, in the order a reader meets them: the link, then the write-hold, then
     * the machine, then the list. Outward from the phone -- a link that is down explains a
     * machine that is silent, which in turn explains a list that is holed, and the reverse order
     * asks the reader to work backwards. The write-hold sits second because, like the link, it
     * bears on what the user can DO rather than on what the screen can be trusted to show.
     */
    val lines: List<String>
        get() = listOf(connection, syncing, freshness, stale).filter { it.isNotEmpty() }

    /** True when the phone has nothing to warn about. The banner draws no air in that state. */
    val silent: Boolean get() = lines.isEmpty()

    companion object {
        /** Nothing to say -- the state a phone with no core to ask is honestly in. */
        val NONE = StatusBanner(connection = "", syncing = "", freshness = "", stale = "")

        /**
         * What the pair-again control reads (agents-tracker-agre).
         *
         * IT NAMES WHERE IT GOES RATHER THAN WHAT IT WISHES IT DID, and the difference is
         * PB-STATE-10. The banner offering it is drawn over a handset that is STILL PAIRED --
         * `RELAY_UNTRUSTED` and `RELAY_INSECURE` do not end a pairing -- and `swarm remote pair` is
         * refused while a device is registered, so a button labelled "Pair again" would promise an
         * act the machine declines and land the user back on this same banner. What the app does
         * have is the Settings destination, whose leading section is `Pairing`
         * ([dev.swarm.phone.ui.screens.SettingsPanel.machineSection]) and whose one control clears
         * the registration the re-pair is blocked on. The word is that section's own, so the
         * screen the press opens says what the button said.
         *
         * `RELAY_INSECURE` REACHES THE SAME PLACE FOR A DIFFERENT REASON, and it is why the label
         * is not an instruction: that state's remedy begins on the MACHINE (its relay.json names a
         * cleartext URL, and pairing again re-delivers it), so the banner's own sentence carries
         * the order and this control only opens the screen the sentence ends at.
         */
        const val PAIR_AGAIN = "Go to Pairing"

        /**
         * @param freshness [MachineFreshness.notice]'s answer: null while the machine is inside
         *  its budget. It arrives already formatted because the TIME is the caller's -- an Android
         *  formatter carrying the user's locale and time zone -- which is that method's own
         *  arrangement and is why this model needs no clock to be testable.
         * @param staleNotice [TriageInbox.staleNotice], which decided the wording in S16.
         * @param reconciled `StateSummary.Reconciled` (agents-tracker-pxz8): true once the machine
         *  has published the rollback authorities PB-SYNC-7 requires before it accepts a mutating
         *  op. IT DEFAULTS TO TRUE -- "nothing held" -- so a caller built from the three facts
         *  above alone, as every suite predating this fact already is, keeps reading as silent on
         *  this one rather than as a phone permanently waiting on a machine it never asked.
         */
        fun of(
            connection: ConnectionBanner,
            freshness: String?,
            staleNotice: String,
            reconciled: Boolean = true,
        ): StatusBanner = StatusBanner(
            connection = if (connection.visible) connection.text else "",
            // A TERMINAL LINK SILENCES ALL THREE WAITING FACTS (agents-tracker-agre,
            // agents-tracker-pxz8).
            //
            // `ConnectionBanner.showsSpinner` has said since S16 that "a spinner is a promise that
            // waiting is enough", and the requirement was met vacuously: this app draws no spinner,
            // so nothing ever read `terminal` to stop looking busy -- while the app went on looking
            // busy IN WORDS. All three end in a promise. The roster's is "some of your machine's
            // activity has not arrived yet"; the machine's is "Not heard from your machine yet"
            // (and, past the budget, a timestamp that will never advance); the hold's is "changes
            // are held until it does" -- and under a transport that has STOPPED RETRYING none of
            // the three resolves, ever. Each is strictly weaker than the line above it, which names
            // the cause and the remedy.
            //
            // IT IS SUPPRESSION AND NOT RE-WORDING. The sentences belong to `TriageInbox`,
            // `MachineFreshness` and `ErrorRouter`; a tense written for this case would be copy
            // authored here, at the seam PB-DS-9 keeps copy out of. The emptiness rule this model
            // already owns is "a fact with nothing to say says nothing", and a fact whose tense the
            // transport has just falsified has nothing to say.
            syncing = if (connection.terminal || reconciled) {
                ""
            } else {
                ErrorRouter.route(SwarmErrorTokens.SYNCING).message
            },
            freshness = if (connection.terminal) "" else freshness.orEmpty(),
            stale = if (connection.terminal) "" else staleNotice,
            // THE REMEDY BECOMES A CONTROL, and `visible` gates it for the reason it gates the
            // line: ONLINE's remedy is NONE, so this is belt and braces there -- but a state added
            // later that is quiet AND carries a remedy would otherwise put a button on a banner
            // with no sentence to explain it.
            pairAgain = if (connection.visible && connection.offersPairing) PAIR_AGAIN else "",
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
