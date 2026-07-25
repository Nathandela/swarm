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
    fun roster(): List<SessionRow> {
        val list = app.roster()
        return (0 until list.count()).map { index -> rowOf(list.at(index)) }
    }

    fun sessionRow(sessionId: String): SessionRow = rowOf(app.session(sessionId))

    /**
     * PB-APP-2's screen. The roster is rendered from the journal stream, so the stream's own
     * state is what says whether the list is whole -- asking the roster handle would be the
     * same question one hop further from the answer.
     */
    fun triageInbox(): TriageInbox =
        TriageInbox.from(roster(), journalStale = app.streamState(JOURNAL_STREAM) == STREAM_STALE)

    /** PB-APP-3's event list, and the machine pane's activity log. */
    fun journal(afterCursor: Long, limit: Long): List<JournalRow> {
        val page = app.readJournal(afterCursor, limit)
        return (0 until page.count()).map { index ->
            val entry = page.at(index)
            JournalRow(
                cursor = entry.getCursor(),
                type = entry.getType(),
                group = entry.getGroup(),
            )
        }
    }

    /**
     * PB-APP-4's grid.
     *
     * @param leaseHeld whether the machine has CONFIRMED a control lease. It is not on the
     *  snapshot and is not asked for here: the lease is the outcome of this screen's own
     *  take_control operation (PB-INPUT-3), and reading it back from a snapshot would be
     *  guessing at a fact the reply already carries.
     */
    fun terminalPeek(sessionId: String, leaseHeld: Boolean): TerminalPeek =
        peekOf(app.peek(sessionId), leaseHeld = leaseHeld, online = isOnline())

    fun connectionBanner(): ConnectionBanner =
        ConnectionBanner.of(ConnectionState.of(app.connectionState()))

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

    /**
     * PB-APP-7's screen.
     *
     * @param biometricGate the handset's own gate preference. It rides no command and the
     *  machine has never heard of it, so it does not come from the facade.
     */
    fun pushSettings(biometricGate: Boolean): SettingsScreen {
        val preference = app.pushPreference()
        return SettingsScreen(
            alerts = preference.getAlerts(),
            mentions = preference.getMentions(),
            biometricGate = biometricGate,
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

    private companion object {
        /** One of the four repair channels; the one the roster and the journal both ride. */
        const val JOURNAL_STREAM = "journal"

        /** `App.StreamState` answers "stale" or "live". */
        const val STREAM_STALE = "stale"
    }
}
