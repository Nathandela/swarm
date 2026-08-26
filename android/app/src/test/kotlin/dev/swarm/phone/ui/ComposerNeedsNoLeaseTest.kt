package dev.swarm.phone.ui

import dev.swarm.phone.ui.kit.ComposerAvailability
import dev.swarm.phone.ui.kit.ComposerModel
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * FAILING-FIRST (TDD RED, GG-5) for owner ruling R1: a live session is typeable from the
 * phone and the terminal at the same time, always, with nothing to switch on first.
 * Plan: docs/specifications/chat-surface-plan.md §2. Bead: agents-tracker-tbpm.7.
 *
 * THE CEREMONY BUYS NOTHING ON THE WIRE, AND THAT IS MEASURED RATHER THAN ASSERTED.
 * `composer_send` is lease-free at every layer: no lease reference in the gateway's action
 * arm, in `handleComposerSend`, or in `skeleton.composerSend`. The daemon's lease gates
 * exactly two things -- `forwardInput` and `forwardResize` -- and every verb behind those is
 * in `android/unbound-verbs.tsv` with zero Kotlin callers, because Wave R6 replaced them
 * with `composer_send`.
 *
 * SO THE COMPOSER IS GREYED BY ONE LINE OF THIS APP'S OWN. `PhoneSurface` calls
 * `setKeyboardEnabled(lease.keyboardEnabled)`, and [SessionLease.keyboardEnabled] reads
 * `leaseHeld && online` -- disabling a text field and a send button whose verb never needed
 * a lease. The screen then says "Read-only -- take control to type" over the top of it,
 * which is the third contradiction in the same region: the machine would have accepted that
 * send.
 *
 * WHAT STILL GATES, AND WHY IT IS NOT CEREMONY. `online` stays: input is live-only and
 * never queued (ADR-007 B43), so a composer over a dead link invites words that are
 * guaranteed to be dropped. And a session with no message sink has no composer at all
 * (ADR-017) -- that is the availability model one file over, not this flag.
 */
class ComposerNeedsNoLeaseTest {

    private fun lease(held: Boolean, online: Boolean) =
        SessionLease(sessionId = "s1", leaseHeld = held, online = online)

    @Test
    fun `a live session is typeable without holding anything`() {
        assertTrue(
            "the keyboard is shut on a session the machine would have accepted a message for. " +
                "composer_send takes no lease at any layer; greying the field is this app " +
                "refusing a capability the daemon grants",
            lease(held = false, online = true).keyboardEnabled,
        )
    }

    @Test
    fun `holding a lease changes nothing, because it never did`() {
        assertEquals(
            "whether a lease is held must make no difference to the composer: the verb behind " +
                "it is the same signed op either way",
            lease(held = true, online = true).keyboardEnabled,
            lease(held = false, online = true).keyboardEnabled,
        )
    }

    @Test
    fun `a dead link still shuts it, and that one is not ceremony`() {
        assertFalse(
            "input is live-only and never queued (ADR-007 B43); a composer over a dropped link " +
                "invites words that are guaranteed to be dropped",
            lease(held = false, online = false).keyboardEnabled,
        )
        assertFalse(lease(held = true, online = false).keyboardEnabled)
    }

    @Test
    fun `the availability model is what withholds a composer, not the lease`() {
        // A session with no message sink has no composer at all -- structural absence, one
        // file over, and nothing to do with who holds what.
        assertEquals(
            ComposerAvailability.NO_CHAT,
            ComposerModel.availabilityFor(online = true, structuredChat = false),
        )
        assertEquals(
            ComposerAvailability.AVAILABLE,
            ComposerModel.availabilityFor(online = true, structuredChat = true),
        )
    }
}
