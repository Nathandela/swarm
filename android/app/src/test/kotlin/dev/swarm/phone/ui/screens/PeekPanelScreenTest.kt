package dev.swarm.phone.ui.screens

import dev.swarm.phone.ui.TerminalPeek
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * FAILING-FIRST (TDD RED, GG-5) for PB-DS-9 over the TERMINAL PEEK's model.
 *
 * WHAT WAS UNTESTED BEFORE THIS. PB-INPUT-2's two sentences -- the ones the requirement's own word
 * "VISIBLY" is about -- were `const val`s in `PhoneSurface`'s companion object, chosen by a
 * one-line `if` inside a private `renderLease`. The requirement's failure mode is precise: the
 * surface showed the same Take control button and the same live keyboard whether the machine had
 * granted a lease or not, so a user could not tell until a keystroke vanished. Nothing asserted
 * which sentence went with which state, and nothing asserted that the keyboard followed the
 * MODEL's verdict rather than the screen's own lease flag.
 *
 * THE `keyboardEnabled` ASSERTION IS THE ONE THAT MATTERS MOST. `TerminalPeek.keyboardEnabled` is
 * `leaseHeld && online`, two clauses, and the second is separate for a stated reason -- a lease
 * cannot be live while the link is down. A screen that carried its own lease flag forward would
 * satisfy the first clause and drop the second, silently, while the model that states it stayed
 * green and unread. That is what this file did until now.
 */
class PeekPanelScreenTest {

    private fun peek(
        session: String = "mbp/quanthome",
        text: String = "$ go test ./...",
        cols: Int = 80,
        rows: Int = 24,
        stale: Boolean = false,
        leaseHeld: Boolean = false,
        online: Boolean = true,
    ) = TerminalPeek(
        sessionId = session,
        text = text,
        cols = cols,
        rows = rows,
        stale = stale,
        leaseHeld = leaseHeld,
        online = online,
    )

    // ---- the header --------------------------------------------------------

    @Test
    fun `the back control names a destination this product actually has`() {
        // C3 draws `< Chat`, which is the session-detail screen. Inventory C2 is unbuilt, so a
        // back control labelled Chat would name a screen that does not exist.
        assertEquals("Inbox", PeekPanelScreen.of(peek()).back)
    }

    @Test
    fun `the header names the session and the grid the machine is rendering it at`() {
        assertEquals(
            "mbp/quanthome · 120x40",
            PeekPanelScreen.of(peek(session = "mbp/quanthome", cols = 120, rows = 40)).title,
        )
    }

    // ---- the snapshot ------------------------------------------------------

    @Test
    fun `a fresh snapshot is the grid and nothing else`() {
        val panel = PeekPanelScreen.of(peek(text = "line one\nline two", stale = false))

        assertEquals("line one\nline two", panel.snapshot)
    }

    /**
     * THE BANNER IS NO LONGER PART OF THE SNAPSHOT, and agents-tracker-0qe7 is why. This read:
     *
     *     assertEquals("${view.staleNotice}\nline one", panel.snapshot)
     *
     * which is a sentence ABOUT the view rendered INSIDE the view. The well it lands in prints
     * `swarmmobile.Snapshot.Text` byte for byte -- ADR-007 D2 puts the VT emulator on the machine
     * precisely so the phone shows what the daemon rendered and nothing else -- and this put an
     * English sentence in the machine's own register, in the machine's own monospace, where a
     * reader has no way to tell it from output. It is a notice of its own now, and the two
     * assertions below are the same subject split: the grid is the grid, and the wording is still
     * the model's rather than a second one invented here.
     */
    @Test
    fun `a stale snapshot is the grid, with the model's own banner beside it`() {
        val view = peek(text = "line one", stale = true)
        val panel = PeekPanelScreen.of(view)

        assertEquals(
            "the stale sentence is inside the snapshot, so a line of English is printed in the " +
                "mono well that renders the machine's output byte for byte",
            "line one",
            panel.snapshot,
        )
        assertEquals(
            "the panel invented its own wording for a stale snapshot instead of carrying the " +
                "model's, so two files now decide what the user reads",
            view.staleNotice,
            panel.staleNotice,
        )
        assertTrue(
            "a fresh snapshot carries a stale notice, which warns about a view that is current",
            PeekPanelScreen.of(peek(text = "line one", stale = false)).staleNotice.isEmpty(),
        )
    }

    @Test
    fun `a stale snapshot does not shut the keyboard`() {
        // TerminalPeek is explicit: the hole is in what the phone was SHOWN, not in what it can
        // send. A screen that disabled typing over a stale grid would be inventing a refusal the
        // machine never made.
        assertTrue(
            PeekPanelScreen.of(peek(stale = true, leaseHeld = true, online = true)).keyboardEnabled,
        )
    }

    // ---- the read-only note ------------------------------------------------

    @Test
    fun `the note is C3's recorded first line`() {
        assertEquals(
            "Read-only · escape-filtered VT snapshot",
            PeekPanelScreen.of(peek()).note,
        )
    }

    // ---- PB-INPUT-2 --------------------------------------------------------

    @Test
    fun `take control is offered exactly while the machine has not confirmed one`() {
        assertTrue(PeekPanelScreen.of(peek(leaseHeld = false)).offersTakeControl)
        assertFalse(
            "the control to take a lease is offered over a lease the machine already granted",
            PeekPanelScreen.of(peek(leaseHeld = true)).offersTakeControl,
        )
    }

    @Test
    fun `the two lease sentences go with the two lease states and not the other way round`() {
        val held = PeekPanelScreen.of(peek(leaseHeld = true)).leaseNotice
        val notHeld = PeekPanelScreen.of(peek(leaseHeld = false)).leaseNotice

        assertEquals(PeekPanelScreen.leaseNoticeFor(confirmed = true), held)
        assertEquals(PeekPanelScreen.leaseNoticeFor(confirmed = false), notHeld)
        assertTrue(
            "the two states read the same, which is the invisible suppression PB-INPUT-2 exists " +
                "to prevent: the user cannot tell until a keystroke vanishes",
            held != notHeld,
        )
        assertTrue(
            "the confirmed sentence does not say what it confirms",
            held.contains("confirmed you have control"),
        )
        assertTrue(
            "the unconfirmed sentence does not say what to do about it, which leaves a shut " +
                "keyboard with no reason beside it",
            notHeld.contains("Take control first"),
        )
    }

    @Test
    fun `the keyboard follows both of the model's clauses, not just the lease`() {
        // The whole point. A lease cannot be live while the link is down, and a screen carrying
        // its own lease flag forward would type into a dead socket.
        assertTrue(PeekPanelScreen.of(peek(leaseHeld = true, online = true)).keyboardEnabled)
        assertFalse(
            "the keyboard is live over a machine this phone cannot reach",
            PeekPanelScreen.of(peek(leaseHeld = true, online = false)).keyboardEnabled,
        )
        assertFalse(
            PeekPanelScreen.of(peek(leaseHeld = false, online = true)).keyboardEnabled,
        )
    }
}
