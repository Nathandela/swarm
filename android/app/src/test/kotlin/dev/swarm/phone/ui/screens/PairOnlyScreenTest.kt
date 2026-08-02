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
 * ## Why the fact arrives as a reader and not as a Boolean
 *
 * The pinned machine is the durable trace a completed pairing leaves: `mobile/pairing.go` clears
 * the attempt record on `paired`, so after a success the ONLY thing distinguishing a paired phone
 * from one that has never paired is `stateSummary().machine`. Reading it can THROW -- a state
 * directory that will not open, a core newer than this build -- and today three call sites each
 * catch that separately (`PairingSurface.isPinned`, `PhoneSurface.converge`, and the caller this
 * change adds would be the third). A Boolean parameter would leave the interesting case, the one
 * where the phone cannot tell, decided at each of those call sites and testable at none of them.
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
        // No pinned machine is what "never paired" looks like once the attempt record is cleared.
        assertEquals(
            "a phone with no machine pinned to it is shown the full app: four tabs, a triage " +
                "inbox over an empty roster, and the pairing screen somewhere below the fold",
            Presentation.PAIR_ONLY,
            PairOnlyScreen.presentationOf { "" },
        )
    }

    @Test
    fun `a pinned machine is what makes the rest of the app appear`() {
        assertEquals(
            "a paired phone is still being offered the pairing screen, so a user who has paired " +
                "cannot reach their sessions",
            Presentation.FULL_APP,
            PairOnlyScreen.presentationOf { "nathans-mbp" },
        )
    }

    @Test
    fun `a phone that cannot read its own pairing state is treated as unpaired`() {
        // Every failure the read can produce, answered the same way. The class of the throw is not
        // the point -- what matters is that no branch of it reaches the app shell.
        val unreadable = listOf<() -> String>(
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
