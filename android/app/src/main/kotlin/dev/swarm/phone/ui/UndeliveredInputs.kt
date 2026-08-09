package dev.swarm.phone.ui

/**
 * PB-INPUT-1: one unit of input this phone took from the user, acknowledged on screen, and could
 * not deliver.
 *
 * IT CARRIES HOW MUCH WAS LOST AND NEVER WHAT WAS LOST, and that is the wire's decision rather than
 * this side's: `mobile/types.go` puts a BYTE COUNT on `UndeliveredInput` and not the bytes, so
 * nothing on screen can echo what was typed. A password typed into a session whose link had already
 * dropped is the case that settles it, and widening this class to carry the text would reintroduce
 * exactly what the Go type refused.
 *
 * @param reason the MACHINE'S own word for what went wrong, rendered verbatim or not at all. It is
 *  a wire string, so it belongs in `noticeDetail`'s mono, tertiary cell and never inside a sentence
 *  this product wrote (agents-tracker-ksvb.10).
 * @param atMillis the unix-millisecond instant the loss was RESOLVED, not the instant it was typed.
 *  Nothing on this screen renders it today; it is carried because dropping a field the facade goes
 *  to the trouble of sending is the class of defect android/unbound-verbs.tsv's own header records
 *  four instances of.
 */
data class UndeliveredInput(
    val sessionId: String,
    val bytes: Int,
    val reason: String,
    val atMillis: Long,
)

/**
 * PB-INPUT-1's ledger as this side holds it: what did not reach the machine, and how much of it
 * the ledger's own bound threw away before anyone could be told.
 *
 * IT IS A READ AND NOT A DRAIN, which is `App.UndeliveredInputs`' own contract: "the state must
 * survive the call that produced it, and a screen that opens after the failure must still see it".
 * The acknowledgement is a separate verb (`App.ClearUndeliveredInputs`) for the reason that verb
 * gives -- a screen that OPENS must see the backlog, a user who DISMISSES it says so once.
 */
data class UndeliveredLedger(
    val entries: List<UndeliveredInput>,
    /**
     * How many OLDER entries the bound discarded.
     *
     * IT IS THE LEDGER'S AND NO SESSION'S, which is why [forSession] carries it through unchanged:
     * the discarded records took their session ids with them, so narrowing cannot attribute them
     * and zeroing them would tell a user nothing was lost beyond what is listed. That is the
     * silent discard PB-INPUT-1 names as a second defect wearing the first one's clothes.
     */
    val dropped: Int,
) {

    /** The losses that belong to one session, which is the only ledger a session's screen may show. */
    fun forSession(sessionId: String): UndeliveredLedger =
        copy(entries = entries.filter { it.sessionId == sessionId })

    companion object {
        /** A phone that has lost nothing, and the state a screen reads before it has asked. */
        val EMPTY = UndeliveredLedger(entries = emptyList(), dropped = 0)
    }
}
