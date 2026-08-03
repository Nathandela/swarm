package dev.swarm.phone.ui.screens

import org.junit.Assert.assertEquals
import org.junit.Test

/**
 * FAILING-FIRST (TDD RED, GG-5) for the one decision an unpaired phone turns on: **whether this
 * handset is shown the app at all.**
 *
 * THE DEFECT THIS ANSWERS IS ADR-007 B132'S, A SECOND TIME. B132 found the pairing panel
 * unreachable on an unpaired handset -- the runtime refused construction over a missing biometric
 * and took the only screen that could pair with it. The refusal is gone and the panel is reachable
 * again in the sense that it is somewhere in the hierarchy, which turned out not to be the same
 * thing as findable: an unpaired phone opens on a four-tab shell whose triage inbox draws four
 * always-present section headings over nothing, and the pairing block is appended BELOW all of
 * them. The owner scrolled that inbox on a real handset and did not find it. A screen a user cannot
 * find is a screen the product does not have, which is the same sentence PB-DS-6 was recorded NOT
 * MET over one level up.
 *
 * SO THE PRESENTATION IS A DECISION AND NOT A LAYOUT ACCIDENT, and it is here because it is the
 * kind of decision this package already owns: [PairingPanelScreen] decides which control a step
 * offers, [TriageInboxScreen] decides which section a Group renders, and both are pure so the
 * decision can be checked without a window. This is the same shape one level out -- which SCREEN
 * the window holds.
 *
 * ## Why the fact arrives as a reader and not as a plain value
 *
 * Reading the phone's durable state can THROW -- a state directory that will not open, a core
 * newer than this build -- and today three call sites each catch that separately
 * (`PairingSurface.isPinned`, `PhoneSurface.converge`, and the caller this change adds would be
 * the third). A plain parameter would leave the interesting case, the one where the phone cannot
 * tell, decided at each of those call sites and testable at none of them. A reader keeps it here,
 * which is the whole reason this decision is a function and not a Boolean at the call site.
 *
 * ## Why the fact is "is this phone paired" and not "what is its machine called"
 *
 * It read the machine NAME first (`stateSummary().machine`), on the reasoning that a completed
 * pairing clears the attempt record and the pinned machine is the only trace left. The trace is
 * real and the inference was wrong: the machine endpoint id is a COORDINATE -- `phonecore`
 * filters the durable blob on it and every mutating verb signs over it -- so nothing clears it,
 * including the revoke behind `Replace this computer`, which destroys both key tiers and leaves
 * the name exactly where it was. This gate answered FULL_APP for a handset whose registration was
 * gone, and the pairing entry point lives on the screen it was refusing to show
 * (agents-tracker-d0b8). The summary now states whether the phone is USABLY paired, and the
 * asymmetry below is unchanged by that: it is a better fact read the same way.
 *
 * THE ANSWER TO "I CANNOT TELL" IS PAIR_ONLY AND THAT IS A DELIBERATE ASYMMETRY. Guessing FULL_APP
 * lands a handset in a four-tab shell whose every screen reads a roster it has no connection to
 * fill -- an inbox announcing that nothing is waiting on the user, which is a claim about a machine
 * this phone is in no position to make. Guessing PAIR_ONLY lands it on the one screen that can get
 * it out of that state. The costs are not symmetric, so the default is not a coin toss.
 */
class PairOnlyScreenTest {

    @Test
    fun `unpaired phone is offered only the pairing screen`() {
        // Not paired is what a fresh install looks like once the attempt record is cleared.
        assertEquals(
            "a phone with no machine pinned to it is shown the full app: four tabs, a triage " +
                "inbox over an empty roster, and the pairing screen somewhere below the fold",
            Presentation.PAIR_ONLY,
            PairOnlyScreen.presentationOf { false },
        )
    }

    @Test
    fun `a pairing is what makes the rest of the app appear`() {
        assertEquals(
            "a paired phone is still being offered the pairing screen, so a user who has paired " +
                "cannot reach their sessions",
            Presentation.FULL_APP,
            PairOnlyScreen.presentationOf { true },
        )
    }

    /**
     * agents-tracker-d0b8, at the level of the decision, for EVERY way a registration ends.
     *
     * A REVOKED PHONE READS THE SAME AS ONE THAT NEVER PAIRED, and that is the design rather than a
     * simplification of it: what this screen has to offer is identical in every case -- one offer
     * to pair -- and the difference between "never had a machine" and "had one until this morning"
     * is a fact for the words on the screen to carry, not for the gate to branch on. What the gate
     * needs is that they are not told apart in the WRONG direction, which is what happened: the
     * revoked phone read as paired, and the one screen that could have repaired it was the one it
     * was refused.
     *
     * THE CAUSES ARE LISTED BECAUSE THE POPULATION IS THE DEFECT. The first version of this fix
     * covered the local press alone, which is the path almost nobody takes: `swarm remote revoke`
     * on the machine is the documented way to remove a device and the only mitigation ADR-007 B133
     * leaves for a lost handset, and nothing on the phone purges for it. Each cause reaches this
     * decision through `App.StateSummary.paired`, and each is proved to reach it by its own case in
     * `mobile/conformance/d0b8_unpair_test.go` -- what is asserted here is the presentation the
     * user is then given.
     */
    @Test
    fun `every way a registration ends leads back to the pairing screen`() {
        val ended = listOf(
            "the owner pressed Replace this computer, so both key tiers were destroyed and the " +
                "unpair was recorded durably",
            "the owner ran `swarm remote revoke` on the machine, so the relay refuses this " +
                "phone's handshake and no journal event will ever arrive again",
            "this handset's relay-auth key was destroyed, and ADR-007 B133 removed the " +
                "authentication that used to be offered for it",
        )

        for (cause in ended) {
            assertEquals(
                "a phone is still being shown the full app after its registration ended -- $cause. " +
                    "The pairing entry point lives on the settings screen INSIDE the shell it is " +
                    "being kept in, and the app's own error banner tells this user to pair again. " +
                    "There is no way out short of clearing the app's data",
                Presentation.PAIR_ONLY,
                PairOnlyScreen.presentationOf { false },
            )
        }
    }

    @Test
    fun `a phone that cannot read its own pairing state is treated as unpaired`() {
        // Every failure the read can produce, answered the same way. The class of the throw is not
        // the point -- what matters is that no branch of it reaches the app shell.
        val unreadable = listOf<() -> Boolean>(
            { throw IllegalStateException("the state directory could not be opened") },
            { throw RuntimeException("the core refused") },
            { throw NoSuchElementException("no summary") },
        )

        for (reader in unreadable) {
            assertEquals(
                "a phone whose pairing state could not be read is shown the full app. It has no " +
                    "roster to fill it, so every screen behind those tabs states something about " +
                    "a machine this phone cannot reach -- and the one screen that could repair " +
                    "the situation is the one it is not being offered",
                Presentation.PAIR_ONLY,
                PairOnlyScreen.presentationOf(reader),
            )
        }
    }
}
