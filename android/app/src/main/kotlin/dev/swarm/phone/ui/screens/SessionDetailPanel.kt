package dev.swarm.phone.ui.screens

import dev.swarm.phone.ui.CommandVerdict
import dev.swarm.phone.ui.JournalRow
import dev.swarm.phone.ui.SessionDetail
import dev.swarm.phone.ui.StopAction

/**
 * PB-APP-3 -- inventory C2's screen model: what the session detail SAYS about a [SessionDetail].
 *
 * WHY THERE IS A MODEL BESIDE [SessionDetail]. That one answers what the session is and what may be
 * done to it, and its semantics are RECORDED DECISIONS from 2026-07-25 which this file implements
 * rather than revisits:
 *
 *  - Stop is a KEYSTROKE and not a new signed action -- 0x03 through a PTY in ISIG mode. No action
 *    was minted for it, because one would need its own authz tuple and replay story and the
 *    daemon's CLOSED capability switch would refuse it one hop short while sealing no reply, so
 *    Stop would hang forever.
 *  - It REQUIRES THE LEASE. `SessionDetail.stop()` answers ACQUIRE_LEASE_FIRST for an observer, so
 *    this screen offers the step that would make Stop work instead of a button the machine will
 *    silently refuse.
 *  - It rides the LIVE-ONLY path (ADR-007 D7). An offline Stop is NOT_SENT and is never queued.
 *
 * What this answers is what a person READS: the title, the heading over the transcript, what each
 * control is called, what the confirmation asks, and what the screen says when something did not
 * reach the machine. All of it is copy or arrangement, and PB-DS-9 assigns both to the screen.
 *
 * THE TRANSCRIPT IS [ActivityEntry] AND THAT IS §2'S REUSE RULE. `activityRow` is documented as
 * "the activity feed's only structural element, and the machine pane's audit log -- one factory for
 * both, which is why it takes a body and an optional emphasis rather than a JournalRow". A
 * per-session journal is the third caller of that exact shape; a second entry type carrying the
 * same two fields would be the copy the rule exists to prevent.
 *
 * ## What inventory C2 draws that this does not
 *
 * **The quick-reply chips (derivation row 10).** Fully specified visually and NOT BUILT: there is
 * no facade verb behind a quick reply. Nothing in `mobile/screen_coverage.tsv` sends a canned
 * string, so a chip would be a control whose behaviour the wire does not define -- the same call
 * the machines screen made about a kill-switch toggle, for the same reason. Recorded here rather
 * than left for the next reader to wonder about.
 *
 * **Tool cards.** The mock draws structured cards per tool call; `swarmmobile.JournalEntry` is
 * `(Cursor, SessionID, Type, Group)` and carries no tool, no arguments and no result. There is
 * nothing on the wire to build a card out of.
 */
data class SessionDetailPanel(
    /** The drill-down header's title: the session the user opened, by the id the wire gave it. */
    val title: String,
    /** §4's back control. The label a screen reader reads; the chevron is the kit's. */
    val back: String,
    val transcript: TranscriptSection,
    /**
     * The daemon-rendered grid, or empty when no frame has arrived for this session.
     *
     * IT IS THE TEXT AND NOT A RENDERING. ADR-007 D2 puts the VT emulator on the machine; this is
     * `swarmmobile.Snapshot.Text` byte for byte, and a renderer on this side would reinterpret
     * bytes the daemon has already declared sanitized.
     */
    val snapshot: String,
    /**
     * Whether there is a snapshot card at all.
     *
     * ABSENT IS NOT EMPTY. A session the machine has sent no frame for gets no card, rather than a
     * card showing an empty terminal -- which would present "we have not heard" as "the screen is
     * blank", and those are different facts about the session.
     */
    val hasSnapshot: Boolean,
    /** What pressing Stop does NOW, from the model that decides it. */
    val stopAction: StopAction,
    /** What the Stop control reads as, which differs for an observer -- see [SessionDetailScreen]. */
    val stopLabel: String,
    /** What the confirmation asks before a Stop is sent. */
    val stopConfirmation: String,
    /** What pressing Stop THROUGH the confirmation would do, including refusing while offline. */
    val confirmedStopAction: StopAction,
    val killLabel: String,
    /** Kill ends the session outright and is never one tap away. */
    val killConfirmation: String,
    /** PB-INPUT-1: what did not reach the machine, in words that promise no retry. */
    val notSentNotice: String,
    /** PB-APP-8: what the screen says when the journal has a hole in it. */
    val staleNotice: String,
    /**
     * PB-APP-8 for the SNAPSHOT, which is a different fact (agents-tracker-0qe7).
     *
     * [staleNotice] is the journal's verdict -- the event stream had a gap -- and it was the only
     * stale mark on this screen, so a grid the machine had stopped sending frames for was drawn
     * with nothing beside it. A terminal is the one surface a user reads AS live, and the two
     * facts are independent: a repaired journal beside a frozen grid is an ordinary state.
     */
    val snapshotStaleNotice: String,
)

/** The per-session journal: a heading, its rows, and what it says when it holds none. */
data class TranscriptSection(
    val heading: String,
    val rows: List<ActivityEntry>,
    val emptyCopy: String,
)

object SessionDetailScreen {

    /** The mock's own heading over a session's records. */
    private const val TRANSCRIPT = "Session log"

    /**
     * What the transcript says when this phone holds no records for the session.
     *
     * IT SAYS NOTHING HAS REACHED THIS PHONE, NOT THAT NOTHING HAPPENED -- [ActivityPanelScreen]'s
     * distinction, and it is sharper here: a session detail is opened from a row that exists, so
     * the user KNOWS the session is real, and a screen saying "nothing has happened" would be
     * contradicting the list they just tapped.
     */
    private const val TRANSCRIPT_EMPTY = "No records for this session have reached this phone yet."

    /**
     * Stop, in the two wordings the lease decides.
     *
     * THE OBSERVER'S WORDING IS A DIFFERENT SENTENCE AND NOT A DISABLED BUTTON. PB-INPUT-3 refuses
     * input without a confirmed lease, and the recorded failure is precise: the surface showed the
     * same control whether the machine had granted a lease or not, so a user could not tell until
     * a keystroke vanished. The step that would make Stop work is what the control offers instead.
     */
    private const val STOP = "Stop"
    private const val STOP_NEEDS_LEASE = "Take control to stop this"

    /**
     * The confirmation before an interrupt goes out.
     *
     * It names what will actually happen -- an interrupt, the same key a person would press at the
     * terminal -- rather than "are you sure", which asks the user to confirm something the sentence
     * never told them.
     */
    private const val STOP_CONFIRMATION =
        "Interrupt what this session is doing? This sends Ctrl-C, the same key you would press at " +
            "the terminal."

    private const val KILL = "Kill session"

    /**
     * Kill's confirmation, and it states the consequence rather than the action.
     *
     * Stop is recoverable -- the agent is interrupted and the session survives. Kill is not, and a
     * confirmation that read the same for both would train the user to dismiss the one that
     * matters.
     */
    private const val KILL_CONFIRMATION =
        "End this session? The agent stops and the session is gone; this cannot be undone."

    /**
     * What the screen says when the machine REFUSED the kill (agents-tracker-qlf9).
     *
     * WHY THIS ONE MATTERS MOST OF THE THREE. The control is behind [KILL_CONFIRMATION], which
     * states the consequence, so a user who answers it has decided something irreversible is about
     * to happen. What a refusal showed them was a cleared outcome line and the session still in
     * the inbox -- indistinguishable from a kill that succeeded one redraw before the roster
     * caught up. The next thing that user does is press it again, or walk away believing the agent
     * is dead.
     *
     * IT SAYS WHAT IS TRUE OF THE SESSION rather than naming the verb. "Kill failed" is a report
     * about a button; "your machine did not end this session" is the fact the user acted on, and
     * the machine's own reason follows it because a kill switch, a revoked device and a policy
     * refusal end in three different places.
     */
    private const val KILL_REFUSED = "Your machine did not end this session"

    /**
     * PB-APP-8's sentence for one session's chronology, in the register the other screens set.
     *
     * A transcript reads as complete unless it says otherwise, and for a chronology the honest
     * failure is INCOMPLETE rather than merely old -- which is the reading a user would not assume.
     */
    private const val STALE_NOTICE =
        "Some records are missing: the event stream from your machine had a gap that has not been " +
            "repaired, so this is not a complete log of the session."

    /** §4's back control, by where it goes rather than by a glyph a screen reader cannot read. */
    private const val BACK = "Back to inbox"

    /**
     * The machine's answer to the kill this screen issued, or NOTHING where it has none to give.
     *
     * SILENCE ON SUCCESS IS DELIBERATE and is [PressFeedback]'s rule: the session leaving the
     * roster IS the confirmation, and `remote-control-mock.html` wrote no toast for a kill. A
     * "done" nobody specified is a sentence invented to fill a gap, at the one seam PB-DS-9 keeps
     * copy out of.
     *
     * SILENCE WHILE PENDING IS THE SAME RULE ONE STEP EARLIER. An unresolved operation is neither
     * a success nor a failure, and saying either is worse than saying nothing.
     */
    fun killNoticeFor(verdict: CommandVerdict): String =
        if (verdict.refused) verdict.sentence(KILL_REFUSED) else ""

    /** The separator between a record's type and its group. `PeekPanelScreen` set the idiom. */
    private const val FIELD_SEPARATOR = " · "

    fun of(detail: SessionDetail): SessionDetailPanel = SessionDetailPanel(
        title = detail.sessionId,
        back = BACK,
        transcript = TranscriptSection(
            heading = TRANSCRIPT,
            // NEWEST FIRST, for `ActivityPanelScreen`'s reason: `ReadJournal` walks the page in
            // ascending cursor order because that is what a cursor is for, and a log is read from
            // the top.
            rows = detail.journal.sortedByDescending { it.cursor }.map(::entryFor),
            emptyCopy = TRANSCRIPT_EMPTY,
        ),
        snapshot = detail.snapshotText,
        hasSnapshot = detail.hasSnapshotCard,
        stopAction = detail.stop(),
        stopLabel = if (detail.leaseHeld) STOP else STOP_NEEDS_LEASE,
        stopConfirmation = STOP_CONFIRMATION,
        confirmedStopAction = detail.confirmStop(),
        killLabel = KILL,
        killConfirmation = KILL_CONFIRMATION,
        // THE MODEL'S OWN SENTENCE, not a second one written here. `SessionDetail.notSentNotice`
        // already says the Stop did not arrive and was not held for later, and PB-INPUT-1's whole
        // subject is that the user is TOLD -- two files deciding that separately is how one of them
        // ends up promising a delivery this transport never makes.
        notSentNotice = detail.notSentNotice,
        staleNotice = if (detail.stale) STALE_NOTICE else "",
        // THE MODEL'S SENTENCE AGAIN, and it is `TerminalPeek`'s: the peek and this card show the
        // same object for the same session, so the wording is read from one place rather than
        // written twice.
        snapshotStaleNotice = detail.snapshotStaleNotice,
    )

    /**
     * One record as the transcript renders it: the wire's own words, and the span the row marks.
     *
     * The emphasis is the GROUP rather than the session: every row here is about the same session,
     * so emphasising it would put the eye on the one token every row shares. `ActivityPanelScreen`
     * emphasises the session for the opposite reason -- its feed spans all of them.
     */
    private fun entryFor(row: JournalRow): ActivityEntry {
        val group = row.group.ifEmpty { null }
        return ActivityEntry(
            cursor = row.cursor,
            body = listOfNotNull(row.type, group).joinToString(FIELD_SEPARATOR),
            emphasis = group,
        )
    }
}
