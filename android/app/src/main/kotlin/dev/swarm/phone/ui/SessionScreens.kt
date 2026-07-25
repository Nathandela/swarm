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
    /** `swarmmobile.JournalEntry.Type`, verbatim. */
    val type: String,
    /** `swarmmobile.JournalEntry.Group`, verbatim. */
    val group: String,
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

    /** PB-INPUT-1: the user must be TOLD what did not reach the machine. */
    val notSentNotice: String
        get() = if (online) {
            ""
        } else {
            "Stop did not reach your machine and was not held for later. Try again once the " +
                "connection is back."
        }

    fun stop(): StopAction =
        if (leaseHeld) StopAction.CONFIRM else StopAction.ACQUIRE_LEASE_FIRST

    fun confirmStop(): StopAction = when {
        !leaseHeld -> StopAction.ACQUIRE_LEASE_FIRST
        !online -> StopAction.NOT_SENT
        else -> StopAction.SEND_INTERRUPT
    }

    /** 0x03 and nothing else: Ctrl-C through a PTY in ISIG mode. */
    fun interruptBytes(): ByteArray = byteArrayOf(INTERRUPT_BYTE)

    companion object {
        const val INTERRUPT_BYTE: Byte = 0x03
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
