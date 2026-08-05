package dev.swarm.phone.ui

/**
 * Phase B slice S16 -- PB-APP-3 (session detail) and PB-APP-4 (terminal peek, take-control).
 *
 * TWO THINGS HERE ARE RECORDED DECISIONS RATHER THAN CHOICES MADE AT THE KEYBOARD.
 *
 * 1. STOP IS NOT A VERB. There is no `interrupt` action in the signed set and the gateway's
 *    action map has no arm for one; the resolution recorded on 2026-07-25 is that an interrupt
 *    IS a keystroke -- 0x03 through a PTY in its default ISIG mode is how a human stops a
 *    running agent -- with `kill` remaining the escalation for a session that ignores SIGINT.
 *    Two consequences the screen owes: Stop REQUIRES the lease, so an observer is shown the
 *    take-control step rather than a button that silently does nothing (PB-INPUT-2 refuses
 *    every keystroke until the machine confirms a lease); and 0x03 rides the LIVE-ONLY path
 *    (ADR-007 D7), so an offline Stop is "not sent" and is NEVER queued.
 *
 * 2. THE GRID IS TEXT. ADR-007 D2 puts the VT emulator on the MACHINE: the daemon renders the
 *    grid and the phone shows `swarmmobile.Snapshot.Text` byte for byte. A second renderer on
 *    the handset would disagree with the first in ways nothing tests, and it would re-interpret
 *    bytes the daemon has already declared sanitized -- which is how sanitisation gets undone
 *    one layer up. android/gate/s16_ui_test.go fences that no such code exists here.
 */

/** One journal record, as the wire describes it. Serves the detail screen and [MachinePane]. */
data class JournalRow(
    val cursor: Long,
    /**
     * `swarmmobile.JournalEntry.SessionID`, verbatim.
     *
     * IT HAS NO DEFAULT ON PURPOSE. The facade has always carried this field (mobile/types.go) and
     * [FacadeBridge.journal] did not read it, so the activity feed could report that a session
     * launched and not which one -- and the emphasised span the design gives every activity row,
     * which is the project name in the drawing, had nothing real to be. A default value here would
     * have made the field optional at nine construction sites and it would have gone unpopulated
     * at whichever one nobody revisited, which is how it was lost the first time.
     */
    val sessionId: String,
    /** `swarmmobile.JournalEntry.Type`, verbatim. */
    val type: String,
    /** `swarmmobile.JournalEntry.Group`, verbatim. */
    val group: String,
)

/**
 * One page of the activity log: the rows, where the NEXT page starts, and whether the stream
 * they came from has a hole.
 *
 * IT IS A PAGE AND NOT A LIST because both of the other two facts were being dropped, and each
 * loss was silent. `swarmmobile.JournalPage.NextCursor` is the only thing that says where to
 * read from next, so a caller handed a bare list could not advance -- it would re-request the
 * same page from the same cursor forever. `Stale` is PB-APP-8 for a surface that renders as a
 * CHRONOLOGY, which reads as complete unless it says otherwise: a log with an unrepaired hole
 * shown as a plain list tells the user the agent did nothing in the gap.
 *
 * Both were bound, tested on the Go side, and reached by no Kotlin at all
 * (android/gate/boundverbledger_test.go now asks about them).
 */
data class JournalPageView(
    val rows: List<JournalRow>,
    /** Feed this back as the next read's `afterCursor`. Unchanged when the page is empty. */
    val nextCursor: Long,
    val stale: Boolean,
)

/**
 * What pressing Stop resolves to. Four of the five are reached from [SessionDetail.stop] and
 * [SessionDetail.confirmStop]; [KILL] belongs to the separate escalation control and is here so
 * a caller cannot pass the two around as though they were interchangeable.
 */
enum class StopAction {
    /** No lease: the take-control step comes first, because the keystroke would be refused. */
    ACQUIRE_LEASE_FIRST,

    /** Ask before interrupting a running agent. */
    CONFIRM,

    /** Send 0x03 on the live input plane. */
    SEND_INTERRUPT,

    /**
     * The link is down. Input is live-only and this one is discarded, not held: a Stop that
     * arrived ten minutes later, after the user gave up and did something else, would interrupt
     * whatever is running then.
     */
    NOT_SENT,

    /** The escalation, for a session that ignores SIGINT. A different control, not this one. */
    KILL,
}

/** PB-APP-3's session detail: the journal, the snapshot card, and a persistent Stop. */
data class SessionDetail(
    val sessionId: String,
    val journal: List<JournalRow>,
    /** The daemon-rendered grid, or empty when no snapshot has arrived for this session. */
    val snapshotText: String,
    val leaseHeld: Boolean,
    val online: Boolean,
    val journalStale: Boolean,
    /**
     * Whether a Stop press on THIS session has already resolved to [StopAction.NOT_SENT]
     * (agents-tracker-4lta).
     *
     * IT IS A PRESS AND NOT A LINK STATE, which is the whole of the fix. [notSentNotice] used to be
     * a function of `online`, so a phone that merely lost its connection put "Stop did not reach
     * your machine and was not held for later" on screen -- a sentence in the PAST TENSE about a
     * Stop nobody had pressed. PB-INPUT-1's subject is what did not reach the machine, and a phone
     * that sent nothing has lost nothing.
     *
     * IT IS THE SURFACE'S TO SET, because a press is not a fact about the session: `PhoneSurface`
     * latches it when its Stop plan takes the NOT_SENT arm, per session, and drops it when a later
     * press resolves to anything else or the drill-down closes.
     */
    val stopNotSent: Boolean = false,
) {
    val hasSnapshotCard: Boolean get() = snapshotText.isNotEmpty()

    /** PB-APP-8: a journal with a hole is never shown as a complete history. */
    val stale: Boolean get() = journalStale

    /** Stop is PERSISTENT -- on screen in every state, per PB-APP-3. */
    val stopVisible: Boolean = true

    /** ADR-007 D7 has no queue for input, and this screen must not invent one. */
    val stopQueued: Boolean = false

    /** Kill ends the session outright; it is never one tap away. */
    val killRequiresConfirmation: Boolean = true

    /**
     * PB-INPUT-1: the user must be TOLD what did not reach the machine.
     *
     * AND ONLY ONCE SOMETHING DID NOT (agents-tracker-4lta). This was `if (online) "" else ...`,
     * which put a report of a failed Stop on screen the moment the link dropped -- before any
     * press, in the past tense, about a keystroke the user had not sent. The requirement is
     * unchanged; what changed is that it now answers a press rather than a connection.
     */
    val notSentNotice: String get() = if (stopNotSent) NOT_SENT_NOTICE else ""

    /**
     * What pressing Stop does NOW.
     *
     * THE LINK IS ASKED HERE AND NOT ONLY IN [confirmStop] (agents-tracker-4lta). This read the
     * lease alone, so an offline press was answered with CONFIRM: the screen asked "Interrupt what
     * this session is doing?" over a link that could not carry the interrupt, the user answered it,
     * and [confirmStop] then resolved NOT_SENT -- a question whose answer did nothing. A
     * confirmation is a promise that answering it acts; this one was answered by a no-op.
     *
     * THE LEASE IS STILL THE FIRST CLAUSE. PB-INPUT-2 refuses every keystroke until the machine
     * confirms a lease, so an observer is shown the step that would make Stop work whatever the
     * link is doing -- offering take-control's remedy after a link check would hide the one that is
     * actually theirs.
     */
    fun stop(): StopAction = when {
        !leaseHeld -> StopAction.ACQUIRE_LEASE_FIRST
        !online -> StopAction.NOT_SENT
        else -> StopAction.CONFIRM
    }

    fun confirmStop(): StopAction = when {
        !leaseHeld -> StopAction.ACQUIRE_LEASE_FIRST
        !online -> StopAction.NOT_SENT
        else -> StopAction.SEND_INTERRUPT
    }

    /** 0x03 and nothing else: Ctrl-C through a PTY in ISIG mode. */
    fun interruptBytes(): ByteArray = byteArrayOf(INTERRUPT_BYTE)

    companion object {
        const val INTERRUPT_BYTE: Byte = 0x03

        /**
         * What the screen says once a Stop press has resolved to [StopAction.NOT_SENT].
         *
         * IT IS A CONSTANT AS WELL AS A PROPERTY because the two readers reach it at different
         * moments. The screen reads [notSentNotice], which is empty until the press is recorded;
         * `PhoneSurface` needs the same words AT the press, before the panel carrying the record
         * has been built, and a string typed there would be a second copy of this sentence with
         * nothing joining them (PB-DS-9 assigns copy to the screen).
         *
         * IT PROMISES NO RETRY. ADR-007 D7 has no queue for input: an interrupt held for a
         * reconnection would arrive after the user gave up and did something else, and would stop
         * whatever is running then.
         */
        const val NOT_SENT_NOTICE =
            "Stop did not reach your machine and was not held for later. Try again once the " +
                "connection is back."

        /**
         * What a toast says once the interrupt is away.
         *
         * IT IS THE MOCK'S OWN WORDS, `toast("Interrupt sent")`, and it is here rather than in the
         * surface because copy belongs to the screen model (PB-DS-9). It is the ONLY press on this
         * surface the design wrote a confirmation for that is not one of the two
         * agents-tracker-qlf9 owns; the rest stay silent, because a "done" nobody specified is a
         * sentence invented to fill a gap.
         *
         * IT SAYS "SENT" AND NOT "STOPPED", which is the whole of what this phone knows. The
         * interrupt is 0x03 on the live plane; whether the agent honoured it is a fact that
         * arrives later, in the journal, and a confirmation claiming otherwise would be the screen
         * asserting an outcome it has not been told.
         */
        const val INTERRUPT_SENT = "Interrupt sent"
    }
}

/**
 * PB-APP-4's terminal peek.
 *
 * [rendered] is the identity of [text] on purpose. It is the whole obligation this type has:
 * the screen displays what the daemon handed it and there is no code path by which it could
 * display anything else.
 */
data class TerminalPeek(
    val sessionId: String,
    /** `swarmmobile.Snapshot.Text`: the daemon-sanitized grid, already flattened to lines. */
    val text: String,
    val cols: Int,
    val rows: Int,
    val stale: Boolean,
    val leaseHeld: Boolean,
    val online: Boolean,
) {
    val rendered: String get() = text

    val showsTakeControl: Boolean get() = !leaseHeld

    val showsRelease: Boolean get() = leaseHeld

    /**
     * PB-INPUT-2. Ungated, the user types happily at a machine that granted them nothing and
     * the gateway drops every frame silently: a live keyboard over a dead terminal. A lease
     * cannot be live while the link is down either, so both conditions are required.
     */
    val keyboardEnabled: Boolean get() = leaseHeld && online

    /**
     * A stale grid is banner-marked and the keyboard STAYS available: the hole is in what the
     * phone was shown, not in what it can send.
     */
    val staleNotice: String
        get() = if (stale) {
            "This view of the terminal is out of date; the machine has not sent a fresh one yet."
        } else {
            ""
        }
}

/**
 * PB-INPUT-2's fact: whether the MACHINE has confirmed the control lease this phone asked for.
 *
 * IT IS DECIDED FROM THE TAKE_CONTROL OPERATION'S OWN OUTCOME, claimed by operation id, which is
 * PB-SYNC-2's discipline and the only honest source a screen has. [TerminalPeek.leaseHeld] is a
 * PARAMETER for exactly that reason -- `FacadeBridge.terminalPeek`'s own doc says the lease "is
 * the outcome of this screen's own take_control operation ... reading it back from a snapshot
 * would be guessing at a fact the reply already carries" -- and until this existed the surface
 * passed the literal `false` in its place, so every session rendered as one the user did not
 * hold while Send was enabled anyway (ADR-007 B83(3)).
 *
 * A SEVERANCE ANSWERS THE SAME QUESTION, and that is why asking the machine beats remembering
 * the press. internal/remotegw/lease_sever.go seals the detach notice "tagged with the
 * take_control's operation id so ReplyCache.TakeFor can attribute it", so the phone's durable
 * outcome for that operation BECOMES the detach and the gate shuts again with no second fact to
 * track. A daemon refusal of the take_control lands on the same id in the same way.
 *
 * THE CODE IS A WIRE OP HELD AS A LITERAL, for the reason [SwarmErrorTokens] is: the unit-test
 * JVM does not load the AAR, so this side cannot read the Go constant. It names the constant it
 * pins, and mobile/conformance is where the two are held equal.
 *
 * WHAT IT CANNOT SEE, recorded rather than assumed away: a lease that lapsed at its horizon with
 * no notice yet delivered still reads as confirmed -- the horizon does not ride the outcome. The
 * consequence is bounded, because `App.SendInput` gates on the core's own `Leases.Require`
 * regardless: what the user gets is a keyboard open one redraw too long and then a routed
 * refusal, not a keystroke silently dropped.
 */
object ControlLease {

    /** protocol.OpLease -- the daemon's grant, which is what a take_control is answered with. */
    private const val GRANTED = "lease"

    /**
     * Whether the machine GRANTED the lease, which is the one clause the keyboard turns on.
     *
     * IT IS DERIVED FROM [verdictOf] AND NOT A SECOND READING OF THE SAME REPLY. The boolean and
     * the sentence the screen shows beside it come from one table, so a phone cannot end up with a
     * shut keyboard and a notice claiming control -- which is this issue's own defect one level
     * down.
     */
    fun confirmedBy(outcome: OperationOutcome): Boolean =
        verdictOf(outcome, outcome.operationId).accepted

    /**
     * The machine's FULL answer to the take_control [operationId] (agents-tracker-qlf9).
     *
     * WHAT [confirmedBy] ALONE COULD NOT SAY. Every refusal collapsed to `false`, so the screen
     * showed "your machine has not confirmed control of this session ... Take control first" to a
     * user whose take_control had just been REFUSED -- copy that reads as "you have not pressed
     * the button yet" and names as the remedy the very step that was declined. The reply's code
     * and message are what tell a kill switch from a revoked device from a policy, and the three
     * have three different remedies.
     *
     * A SEVERANCE STAYS A SEVERANCE. [CommandVerdict] keeps `protocol.OpDetach` in its own arm for
     * the reason this object's own comment gives: the detach notice rides the take_control's
     * operation id, so an ordinary lease that ended leaves a `detach` here -- and reporting it as
     * a refusal would accuse the machine of declining a lease it had granted.
     */
    fun verdictOf(outcome: OperationOutcome, operationId: String): CommandVerdict =
        CommandVerdict.of(outcome, operationId, accepted = GRANTED)
}
