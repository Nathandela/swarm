package dev.swarm.phone.ui.screens

import dev.swarm.phone.ui.CommandResult
import dev.swarm.phone.ui.CommandVerdict
import dev.swarm.phone.ui.TerminalPeek

/**
 * Phase B slice S24 -- PB-DS-9: the TERMINAL PEEK's screen model (inventory C3).
 *
 * WHY THERE IS A MODEL BESIDE [TerminalPeek]. That one answers what the SNAPSHOT is -- whether it
 * is stale, whether the machine confirmed a lease, whether the link is up. This answers what the
 * SCREEN says about it: the header, the read-only note, and the two sentences PB-INPUT-2 calls
 * "visibly confirmed". Those sentences were `const val`s in `PhoneSurface`'s companion object,
 * which is to say the copy of a screen lived in the file that also owns the transport, the
 * lifecycle and five other panels.
 *
 * ## Three places C3's recorded composition and this product disagree
 *
 * - **There is no back control at all, where C3 draws `< Chat`** (agents-tracker-joe7). C3 draws the
 *   peek as a drill-down from the session-detail screen; this screen is not a drill-down. It is
 *   composed UNDER the inbox list, in `PhoneSurface`'s own column, so it is not pushed over
 *   anything and there is nothing to pop. The label was `Inbox` for a while -- chosen because
 *   `Chat` named a screen that did not exist -- and that reasoning finishes the job: `Inbox` names
 *   the screen the user is already standing on. The affordance is expensive (§4 gives it a 48 dp
 *   target, a focus ring and a chevron) and it is a promise; drawn with no listener behind it, as
 *   it was, it is a control that looks like a control and does not act. `navHeaderDrill` takes a
 *   null destination for exactly this, and the rest of §4's header is unchanged.
 * - **The header's second field is the GRID, not the word `grid`.** C3 writes the title as
 *   `{proj} - grid`; `{proj}` is plainly a placeholder and the neighbouring field is read the same
 *   way, because a literal word "grid" beside a project name tells a reader nothing and the
 *   terminal's actual dimensions are the one fact a peek header can usefully carry. Recorded as an
 *   interpretation rather than a transcription, because it is one.
 * - **The note's tail is not shipped.** C3's `.ro-note` reads `Read-only - escape-filtered VT
 *   snapshot` then `[Take control]` then ` to send keys (desktop TUI is superseded)`. Derivation
 *   row 22 turns `[Take control]` from an inline span into a standalone button -- an inline span
 *   cannot carry a 48 dp target -- which leaves the trailing clause a sentence fragment with its
 *   subject removed. It is dropped rather than re-written, because re-writing it would be authored
 *   copy standing in for recorded copy, and the button says what it does.
 */
data class PeekPanel(
    /** C3.1: the session and the grid the machine is rendering it at. */
    val title: String,
    /**
     * What goes in the mono well: the grid the machine rendered, and nothing else.
     *
     * IT USED TO CARRY THE STALE BANNER TOO (agents-tracker-0qe7), joined to the grid with a
     * newline on the argument that a stale snapshot is still the snapshot, so the warning belonged
     * where the thing it warns about is. The argument is right about WHERE and wrong about WHAT:
     * this well prints `swarmmobile.Snapshot.Text` byte for byte -- ADR-007 D2 keeps the VT
     * emulator on the machine precisely so the phone shows what the daemon rendered and nothing
     * else -- so a sentence of English inside it is in the machine's own register, in the machine's
     * own monospace, indistinguishable from something the agent printed. [staleNotice] is the same
     * warning in the same place, one view up.
     */
    val snapshot: String,
    /**
     * The MACHINE's terminal row count, which is the well's floor (agents-tracker-ksvb.3).
     *
     * IT IS NOT `snapshot.lines().size`, AND THE DIFFERENCE IS THE WHOLE FIELD REPORT. This model
     * is rebuilt every time the agent writes a byte, and the line count of one frame is whatever
     * the daemon happened to render at that instant -- so a well sized to it grew and shrank under
     * the reader, taking the note, `[Take control]` and the lease sentence with it. The grid is
     * one size for as long as the terminal is one size, which is the number that does not move.
     */
    val snapshotRows: Int,
    /**
     * PB-APP-8's mark for the snapshot, ABOVE the well rather than inside it.
     *
     * Empty when the grid is current, which is the same call every other notice on this app makes:
     * a blank warning line over a healthy view is a warning nobody wrote.
     */
    val staleNotice: String,
    /** C3.3's first line, verbatim. */
    val note: String,
    /** PB-INPUT-2's "visibly", in whichever of the two states the machine has put the user in. */
    val leaseNotice: String,
    val offersTakeControl: Boolean,
    val keyboardEnabled: Boolean,
)

object PeekPanelScreen {

    /** Inventory C3.3, first line, verbatim. */
    private const val NOTE = "Read-only · escape-filtered VT snapshot"

    /**
     * PB-INPUT-2's "visibly", in two sentences. The second says what to do about it, because a
     * shut keyboard with no reason beside it is the invisible suppression the requirement is
     * against.
     *
     * THEY MOVED HERE FROM `PhoneSurface`'s COMPANION OBJECT, which is where the copy of a screen
     * had been living. PB-DS-9 assigns copy to the screen; nothing else about them changed, and
     * `PhoneSurfaceControlsTest` and the instrumented smoke read them by value.
     */
    private const val LEASE_CONFIRMED =
        "Your machine has confirmed you have control of this session, so what you type is " +
            "sent live."

    private const val LEASE_NOT_CONFIRMED =
        "Your machine has not confirmed control of this session, so the keyboard stays shut " +
            "-- anything typed would be dropped without a word. Take control first."

    /**
     * The two sentences a REFUSAL and a SEVERANCE get instead (agents-tracker-qlf9).
     *
     * [LEASE_NOT_CONFIRMED] was shown for both, and it is wrong for both in the same way: it reads
     * as "you have not pressed the button yet", and the step it offers is the one that was just
     * declined. The machine's own words follow, because a kill switch, a revoked device and a
     * policy refusal have three different remedies and only the reply says which one this is.
     *
     * THEY ARE TWO SENTENCES AND NOT ONE. A lease the machine GRANTED and later ended is not a
     * lease it refused; `internal/remotegw/lease_sever.go` seals the detach under the
     * take_control's own operation id, so the difference arrives on this very outcome and a single
     * wording would accuse the machine of declining a lease it had given.
     */
    private const val LEASE_REFUSED = "Your machine refused this phone control of the session"

    private const val LEASE_ENDED = "Your machine ended this phone's control of the session"

    /** What every not-granted state shares, said once rather than in each sentence. */
    private const val KEYBOARD_SHUT = " The keyboard stays shut."

    /**
     * @param verdict the machine's answer to the take_control THIS screen issued. It is defaulted
     *  to [CommandVerdict.UNANSWERED] rather than required, because a phone that has asked for no
     *  lease has not been refused one -- and the two must not read the same.
     */
    fun leaseNoticeFor(
        confirmed: Boolean,
        verdict: CommandVerdict = CommandVerdict.UNANSWERED,
    ): String = when {
        // THE PEEK IS THE AUTHORITY ON WHETHER A LEASE IS HELD and this clause is first for that
        // reason: `leaseHeld` is what shuts the keyboard, and a notice announcing control over a
        // shut keyboard is the contradiction PB-INPUT-2's "visibly" exists to prevent.
        confirmed -> LEASE_CONFIRMED
        verdict.result == CommandResult.ENDED -> verdict.sentence(LEASE_ENDED) + KEYBOARD_SHUT
        verdict.refused -> verdict.sentence(LEASE_REFUSED) + KEYBOARD_SHUT
        else -> LEASE_NOT_CONFIRMED
    }

    fun of(peek: TerminalPeek, lease: CommandVerdict = CommandVerdict.UNANSWERED): PeekPanel = PeekPanel(
        // THE SESSION'S OWN NAME BEFORE THE GRID'S SHAPE, and the id only where there is no name
        // (agents-tracker-ksvb.1). The `cols x rows` half is untouched: it is the fact this
        // header carries that no other surface does.
        title = "${peek.title.ifEmpty { peek.sessionId }} · ${peek.cols}x${peek.rows}",
        snapshot = peek.rendered,
        // THE GRID, WHICH THE TITLE ABOVE ALREADY SPENDS. `peek.rows` is what the daemon opened
        // the PTY at; the well takes it as a floor so the card stops resizing per frame.
        snapshotRows = peek.rows,
        // THE MODEL'S OWN WORDING, carried rather than re-decided: `TerminalPeek.staleNotice` is
        // the sentence, and a second one written here would be two files deciding what the user
        // reads about one fact.
        staleNotice = peek.staleNotice,
        note = NOTE,
        // THE VERDICT IS THE MODEL'S, not the press's. `showsRelease` is `leaseHeld`, which is
        // what the MACHINE answered this screen's own take_control with, claimed by operation id
        // -- and [lease] is the rest of that same answer, which used to be discarded on the way in.
        leaseNotice = leaseNoticeFor(peek.showsRelease, lease),
        offersTakeControl = peek.showsTakeControl,
        // BOTH CLAUSES, and they are the model's. `keyboardEnabled` is `leaseHeld && online`; a
        // screen that enabled the keyboard from its own lease flag would satisfy the requirement's
        // first clause and drop the second, silently, while the model that states it stayed green
        // and unread.
        keyboardEnabled = peek.keyboardEnabled,
    )
}
