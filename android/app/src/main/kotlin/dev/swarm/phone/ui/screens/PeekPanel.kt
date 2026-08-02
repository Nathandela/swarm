package dev.swarm.phone.ui.screens

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
 * - **The back label is `Inbox`, not the artifact's `Chat`.** C3 draws the peek as a drill-down
 *   from the session-detail screen. There is no session-detail screen in this product -- inventory
 *   C2 is unbuilt -- and the peek hangs under the inbox, so a back control labelled `Chat` would
 *   name a destination that does not exist.
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
    /** The destination the back control returns to. The chevron is the component's, not copy. */
    val back: String,
    /** C3.1: the session and the grid the machine is rendering it at. */
    val title: String,
    /**
     * What goes in the mono well: the stale banner, then the grid.
     *
     * THE BANNER IS ABOVE THE GRID AND INSIDE THE SAME BLOCK. A stale snapshot is still the
     * snapshot -- `TerminalPeek` is explicit that the keyboard STAYS available, because the hole
     * is in what the phone was shown rather than in what it can send -- so the warning belongs
     * where the thing it warns about is, not on a line the eye skips on its way to the grid.
     */
    val snapshot: String,
    /** C3.3's first line, verbatim. */
    val note: String,
    /** PB-INPUT-2's "visibly", in whichever of the two states the machine has put the user in. */
    val leaseNotice: String,
    val offersTakeControl: Boolean,
    val keyboardEnabled: Boolean,
)

object PeekPanelScreen {

    /** Inventory C3.1, retargeted -- see the class comment. */
    private const val BACK = "Inbox"

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

    fun leaseNoticeFor(confirmed: Boolean): String =
        if (confirmed) LEASE_CONFIRMED else LEASE_NOT_CONFIRMED

    fun of(peek: TerminalPeek): PeekPanel = PeekPanel(
        back = BACK,
        title = "${peek.sessionId} · ${peek.cols}x${peek.rows}",
        snapshot = listOf(peek.staleNotice, peek.rendered)
            .filter { it.isNotEmpty() }
            .joinToString("\n"),
        note = NOTE,
        // THE VERDICT IS THE MODEL'S, not the press's. `showsRelease` is `leaseHeld`, which is
        // what the MACHINE answered this screen's own take_control with, claimed by operation id.
        leaseNotice = leaseNoticeFor(peek.showsRelease),
        offersTakeControl = peek.showsTakeControl,
        // BOTH CLAUSES, and they are the model's. `keyboardEnabled` is `leaseHeld && online`; a
        // screen that enabled the keyboard from its own lease flag would satisfy the requirement's
        // first clause and drop the second, silently, while the model that states it stayed green
        // and unread.
        keyboardEnabled = peek.keyboardEnabled,
    )
}
