package dev.swarm.phone.ui

import dev.swarm.phone.keys.ConnectionState
import swarmmobile.App
import swarmmobile.Session
import swarmmobile.Snapshot

/**
 * Phase B slice S16 -- where the pure screen models meet the bound facade.
 *
 * WHY THIS FILE EXISTS AT ALL. The screen models in this package are pure Kotlin so their
 * mapping is testable without an Activity, which is right and is the shape
 * dev.swarm.phone.runtime.PermissionStateResolver already established here. The hazard is the
 * obvious one, and this project has hit it repeatedly: a beautifully-tested [TriageInbox] that
 * nothing constructs from `swarmmobile.App`, with the real screen reading the facade directly
 * and disagreeing with every assertion. This adapter is the only place the two meet, so there
 * is one mapping to review rather than one per screen.
 *
 * IT IS A MAPPING AND NOTHING ELSE. No policy is decided here -- every question of what to
 * show is answered by the model it hands the inputs to. Anything a call cannot answer is a
 * PARAMETER rather than a default, because a plausible-looking value invented at this seam is
 * indistinguishable on screen from one the machine sent.
 *
 * EVERY MODEL IN THIS PACKAGE HAS A PRODUCTION CALLER HERE. That is the property the file
 * exists for, so it is stated as a claim rather than left to be inferred: a model reachable
 * only from a test is one the real screen may quietly disagree with, which is the failure the
 * fence in android/gate/s16_ui_test.go is pointed at.
 */
class FacadeBridge(private val app: App) {

    /**
     * The roster as rows. gomobile has no list type, so a collection crosses as an opaque
     * handle with Count/At and is walked here once rather than at every screen.
     */
    fun roster(): List<SessionRow> = rosterView().rows

    fun sessionRow(sessionId: String): SessionRow = rowOf(app.session(sessionId))

    /**
     * PB-APP-2's screen, with its PB-APP-8 verdict READ OFF THE HANDLE THAT CARRIED IT.
     *
     * This used to ask `App.StreamState("journal")` beside the roster instead, and the two are
     * the same fact: `App.Roster` sets `SessionList.stale` from exactly that stream. Asking
     * separately is what `SessionList.Stale` exists to stop -- its own Go doc says the flag
     * rides on the handle "rather than being left to the caller ... a screen that has to
     * remember to call StreamState beside every Roster() is a screen that will forget once, and
     * the failure is silent and looks exactly like a working app".
     *
     * It had already gone wrong in the softer way: `SessionList.Stale` was reached by NO Kotlin
     * at all, while a unit test asserting the inbox is never presented as live carried the
     * comment "this is the assertion that makes swarmmobile.SessionList.Stale reach a user".
     * It did not -- that assertion drives `TriageInbox.from`'s parameter, and what fills the
     * parameter is this line.
     */
    fun triageInbox(): TriageInbox {
        val roster = rosterView()
        return TriageInbox.from(roster.rows, journalStale = roster.stale)
    }

    /**
     * PB-APP-3's event list, and the machine pane's activity log.
     *
     * It returns the PAGE and not the rows: the cursor to read from next and the stream's stale
     * mark are on the handle, and dropping them left a caller unable to advance and unable to
     * say the log has a hole. See [JournalPageView].
     */
    fun journal(afterCursor: Long, limit: Long): JournalPageView {
        val page = app.readJournal(afterCursor, limit)
        return JournalPageView(
            rows = (0 until page.count()).map { index ->
                val entry = page.at(index)
                JournalRow(
                    cursor = entry.getCursor(),
                    sessionId = entry.getSessionID(),
                    type = entry.getType(),
                    group = entry.getGroup(),
                )
            },
            nextCursor = page.nextCursor(),
            stale = page.stale(),
        )
    }

    /**
     * The roster handle read ONCE: its rows and its own staleness verdict, together.
     *
     * Together is the point. Two calls to `App.Roster` would answer two different questions
     * about two different reads, and the second could report a stream repaired between them --
     * which is a list drawn from the holed read, labelled with the whole one.
     */
    private fun rosterView(): RosterView {
        val list = app.roster()
        return RosterView(
            rows = (0 until list.count()).map { index -> rowOf(list.at(index)) },
            stale = list.stale(),
        )
    }

    private data class RosterView(val rows: List<SessionRow>, val stale: Boolean)

    /**
     * PB-APP-4's grid.
     *
     * A SESSION WITH NO FRAME YET IS NOT A FAILURE, and this is the one place that can say so
     * (agents-tracker-9ds). `App.Peek` reads a cache the MACHINE fills, and refuses with
     * [SwarmErrorTokens.NOT_FOUND] while it holds nothing for the session -- which is the state
     * every session is in for the whole round trip after `terminalWatch`. Propagated, that refusal
     * crossed `PhoneSurface.render`, reached `PhoneEvents`' `main.post { }` uncaught and killed the
     * app on the ordinary path of opening a session that has not printed. [TerminalPeek] models the
     * empty grid and `SessionDetailPanel.hasSnapshot` draws it; nothing had to invent a state, only
     * to stop treating "not yet" as "broken".
     *
     * ONLY THAT ONE CLASS IS ABSORBED. Every other refusal is a real failure and keeps propagating
     * to the router that already renders it with a remedy: a device revoked, a custody state
     * needing repair, a rate limit or a refused transport must never reach a user as a terminal
     * that has quietly printed nothing. `FacadeBridgeTest` asserts both directions and reads the
     * class list by reflection, so a token added to the taxonomy propagates by default.
     *
     * @param leaseHeld whether the machine has CONFIRMED a control lease. It is not on the
     *  snapshot and is not asked for here: the lease is the outcome of this screen's own
     *  take_control operation (PB-INPUT-3), and reading it back from a snapshot would be
     *  guessing at a fact the reply already carries.
     */
    fun terminalPeek(sessionId: String, leaseHeld: Boolean): TerminalPeek = try {
        peekOf(app.peek(sessionId), leaseHeld = leaseHeld, online = isOnline())
    } catch (refused: Exception) {
        if (!isAwaitingFirstFrame(classOf(refused))) throw refused
        noFrameYet(sessionId, leaseHeld = leaseHeld, online = isOnline())
    }

    /**
     * What the GO side calls this refusal, or nothing when it cannot say.
     *
     * `App.ErrorClass` fails through `a.ready()` like every other verb, so a phone whose core has
     * closed cannot classify its own error. AN UNNAMEABLE FAILURE IS NOT THE BENIGN ONE: the empty
     * string is refused by [isAwaitingFirstFrame], so the ORIGINAL refusal propagates rather than
     * being replaced by the secondary one raised while trying to describe it.
     */
    private fun classOf(refused: Exception): String = try {
        app.errorClass(refused.message.orEmpty())
    } catch (unreadable: Exception) {
        ""
    }

    fun connectionBanner(): ConnectionBanner =
        ConnectionBanner.of(ConnectionState.of(app.connectionState()))

    /**
     * PB-APP-11, and it is a SEPARATE question from [connectionBanner] on purpose.
     *
     * The transport can only report what it knows: the socket is up and the polls are
     * succeeding. A relay that answers every poll with an empty page while withholding the
     * machine's frames leaves that reading "online" forever, with no gap for any staleness
     * mechanism to key on. This asks the one thing the relay cannot fake -- how long since the
     * machine's own AAD-covered timestamp -- and it is PULLED for [clockBanner]'s reason: a
     * screen that opens after the fact was never sent an event.
     */
    fun machineFreshness(): MachineFreshness = app.machineFreshness().let {
        MachineFreshness(silent = it.getSilent(), lastHeardUnixMs = it.getLastHeardUnixMs())
    }

    /**
     * PB-APP-8 for one repair channel.
     *
     * Stale and repairing are asked for SEPARATELY because they are orthogonal facts: a stream
     * is stale-and-repairing or stale-and-idle, and PB-SYNC-3 forbids expressing the second by
     * clearing the first -- clearing on the request shows the user a known hole as live. So
     * `StreamState` keeps answering "stale" throughout a repair and `ResyncPending` carries the
     * in-flight half.
     */
    fun streamView(stream: String): StreamView = StreamView(
        stream = stream,
        stale = app.streamState(stream) == STREAM_STALE,
        resyncPending = app.resyncPending(stream),
    )

    /**
     * PB-TIME-1's verdict, PULLED rather than remembered from an event.
     *
     * A screen that opens after the measurement -- which on Android is most of them, because the
     * process is killed and rebuilt constantly -- was never told, and a screen that latched the
     * event has nothing to clear it with. An empty verdict is a healthy clock, and the banner
     * treats it as one.
     */
    fun clockBanner(): ClockBanner = ClockBanner.of(app.clockVerdict())

    /**
     * PB-APP-9's classifier, for a message that has already crossed as an exception.
     *
     * The Android side holds the tokens as literals ([SwarmErrorTokens]) because the unit-test
     * JVM does not load the AAR, and this is the other direction: the same classification made
     * by the Go side, so a screen with a live App never has to be the one that gets it right.
     */
    fun routeFacadeError(message: String): RoutedError = ErrorRouter.route(app.errorClass(message))

    /** PB-SYNC-2: outcomes are claimed BY OPERATION ID, never by proximity. */
    fun launchOutcome(operationId: String): OperationOutcome {
        val outcome = app.outcome(operationId)
        return OperationOutcome(
            operationId = outcome.getOperationID(),
            code = outcome.getCode(),
            message = outcome.getMessage(),
        )
    }

    /** PB-APP-7's screen: the two push preferences, and nothing this seam has to invent. */
    fun pushSettings(): SettingsScreen {
        val preference = app.pushPreference()
        return SettingsScreen(
            alerts = preference.getAlerts(),
            mentions = preference.getMentions(),
        )
    }

    private fun isOnline(): Boolean =
        ConnectionState.of(app.connectionState()) == ConnectionState.ONLINE

    private fun rowOf(session: Session) = SessionRow(
        id = session.getID(),
        title = session.getTitle(),
        group = session.getGroup(),
        need = session.getNeed(),
        present = session.getPresent(),
        // VERBATIM, AND WITH NO FALLBACK. The daemon puts `persist.Meta.AgentType` on the journal
        // record and phonecore folds it through unchanged, so this is the machine's own word for
        // what is running. An empty one means the session's records carried none -- which is a
        // fact, not a gap to fill: substituting the title or the id here would put a fabricated
        // identity in the one cell a reader trusts to name the agent.
        agent = session.getAgent(),
    )

    /** The text crosses verbatim. There is no renderer on this side to put between them. */
    private fun peekOf(snapshot: Snapshot, leaseHeld: Boolean, online: Boolean) = TerminalPeek(
        sessionId = snapshot.getSessionID(),
        text = snapshot.getText(),
        cols = snapshot.getCols().toInt(),
        rows = snapshot.getRows().toInt(),
        stale = snapshot.getStale(),
        leaseHeld = leaseHeld,
        online = online,
    )

    /**
     * The two halves of [terminalPeek]'s recovery, lifted out of the instance ON PURPOSE.
     *
     * NEITHER TAKES AN `App`, AND THAT IS WHAT MAKES THEM TESTABLE. `swarmmobile.App` is a gomobile
     * class over .so files cross-compiled for Android ABIs, so it cannot be constructed on the
     * unit-test JVM and no test can call [terminalPeek] at all. The decision that can be got wrong
     * -- which refusals mean "no frame yet" and which must keep propagating -- is pure Kotlin and
     * is asserted in `FacadeBridgeTest`. What stays untested is the placement of the `try/catch`
     * around them, which is review's.
     */
    internal companion object {
        /** `App.StreamState` answers "stale" or "live". */
        const val STREAM_STALE = "stale"

        /**
         * Whether a refusal means the machine simply has not sent this session's grid yet.
         *
         * IT IS AN EQUALITY AND NOT A SET, because exactly one class in the taxonomy means "not
         * yet" and every widening of this is a real failure rendered as a quiet terminal.
         */
        fun isAwaitingFirstFrame(errorClass: String): Boolean =
            errorClass == SwarmErrorTokens.NOT_FOUND

        /**
         * A session the machine has sent no frame for.
         *
         * IT INVENTS NOTHING. The text is empty, which is what `SessionDetail.hasSnapshotCard`
         * reads to draw NO CARD rather than a well containing nothing; the grid is unmeasured
         * because no grid arrived; and it is not marked stale, because staleness is a property of a
         * view that exists and has stopped being refreshed. The lease and the link are the caller's
         * facts and cross unchanged.
         */
        fun noFrameYet(sessionId: String, leaseHeld: Boolean, online: Boolean) = TerminalPeek(
            sessionId = sessionId,
            text = "",
            cols = 0,
            rows = 0,
            stale = false,
            leaseHeld = leaseHeld,
            online = online,
        )
    }
}
