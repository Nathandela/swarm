package dev.swarm.phone.ui.screens

import dev.swarm.phone.ui.PairingStep
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotEquals
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * FAILING-FIRST (TDD RED, GG-5) for the paired-machine row.
 *
 * agents-tracker-64rf's design note moves the pairing entry point off the inbox: once a phone is
 * PAIRED, it moves to the SETTINGS destination "as a machine row (paired machine's name +
 * replace-this-computer, which is revoke-then-pair while v1 is single-device)". This is that
 * row's model -- [PairedMachineRow] and [PairedMachineRowScreen], neither of which exists yet.
 *
 * WHY THIS IS NOT A [SettingsRow]. Every row that type describes is a push toggle: `checked`,
 * `enabled`, a `PushToggle` bijection with the preference it drives. The paired-machine row has
 * none of that -- it carries no boolean the machine can be in, and its one control does not flip
 * a value back and forth, it starts a destructive, one-way action (revoke, then pair again).
 * Forcing it into `SettingsRow` would give it a `checked` state that means nothing, which is
 * precisely the shape that invites a caller to wire a switch to it -- a control the user could
 * flip and nothing would happen, or worse, something destructive would.
 *
 * THE LABEL IS NOT A SECOND WORDING OF [PairingPanelScreen]'S. That screen already answers "what
 * does a completed pairing say" in [PairingPanelScreen.titleFor]: `Paired` for a machine name
 * this phone cannot read, `Paired with <name>` for one it can -- the exact dangling-string bug
 * class ("Paired with " and nothing after it) [PairingPanelScreen]'s own doc comment names, and
 * that `PairingSurface.machineOf` answers `""` rather than a placeholder to avoid. This row asks
 * the SAME question about the SAME fact, so [PairedMachineRowScreen.of] is expected to ask
 * [PairingPanelScreen.titleFor] rather than re-deriving the answer: two conditionals computing
 * "is the machine name empty" independently is how the two screens drift the day only one of
 * them is edited.
 *
 * THE COPY IS HONEST THAT REPLACING ENDS THE PAIRING, AND OFFERS NOTHING ELSE. swarm v1 is
 * single-device: `internal/skeleton/pairing.go`'s `BeginPairing` refuses a second pairing while
 * one device is registered, fail-fast, before any rendezvous is minted ("a device is already
 * paired (single-device v1) ... pair again"). There is therefore no "add another computer" --
 * that would describe an action the daemon refuses on the first press -- and the one action this
 * row does offer says what it costs before anyone presses it.
 */
class PairedMachineRowTest {

    // ---- the paired machine's name -----------------------------------------

    @Test
    fun `the row's label names the paired machine`() {
        assertEquals(
            "Paired with nathans-mbp",
            PairedMachineRowScreen.of("nathans-mbp").label,
        )
    }

    @Test
    fun `the label is the same fact PairingPanelScreen already renders for a completed pairing`() {
        // Same question, same answer, asked once. A second, independently-derived "Paired with
        // <name>" here is the exact drift PB-DS-9's "copy lives in one place" argument warns
        // about -- it is only a matter of time before someone edits one and not the other.
        listOf("nathans-mbp", "office-imac", "").forEach { machine ->
            assertEquals(
                "the row's label disagrees with PairingPanelScreen's own title for machine " +
                    "'$machine', so this app now has two opinions about the same fact",
                PairingPanelScreen.titleFor(PairingStep.PAIRED, machine),
                PairedMachineRowScreen.of(machine).label,
            )
        }
    }

    @Test
    fun `an unreadable machine name renders Paired, never a dangling Paired with`() {
        // PairingSurface.machineOf already answers "" rather than a placeholder for exactly this
        // case; this row must not turn that "" back into "Paired with " by naive interpolation.
        val label = PairedMachineRowScreen.of("").label

        assertEquals("Paired", label)
        assertFalse(
            "the row rendered a dangling string for an unreadable machine name: '$label'",
            label.endsWith("with "),
        )
    }

    // ---- the replace control -------------------------------------------------

    @Test
    fun `the row offers a control that replaces the computer`() {
        assertEquals(
            "Replace this computer",
            PairedMachineRowScreen.of("nathans-mbp").replaceLabel,
        )
    }

    @Test
    fun `the replace control's own copy does not depend on which machine is paired`() {
        // Its wording is about what pressing it DOES, not about the machine on screen -- the
        // machine name already lives in the label above it, and repeating it here would be the
        // row saying the same fact twice in two different words.
        val withMachine = PairedMachineRowScreen.of("nathans-mbp")
        val withUnreadableMachine = PairedMachineRowScreen.of("")

        assertEquals(withMachine.replaceLabel, withUnreadableMachine.replaceLabel)
        assertEquals(withMachine.sublabel, withUnreadableMachine.sublabel)
    }

    @Test
    fun `the copy is honest that replacing ends the current pairing`() {
        val row = PairedMachineRowScreen.of("nathans-mbp")

        assertTrue(
            "the row's copy does not say the pairing ends: '${row.sublabel}'",
            row.sublabel.contains("ends", ignoreCase = true) &&
                row.sublabel.contains("pairing", ignoreCase = true),
        )
    }

    // ---- the confirmation ----------------------------------------------------
    //
    // FAILING-FIRST (TDD RED, GG-5) for agents-tracker-mrq5. `Replace this computer` deregisters
    // this handset, rotates the epoch, severs the gateway and destroys BOTH key tiers -- the purge
    // is in a `finally`, so it runs whether or not the command reached the machine -- and it did all
    // of that on one tap of a chip in a row's trailing slot. `kill`, which ends ONE session, has
    // asked since S24. The question is the row's because PB-DS-9 puts copy on the screen model, and
    // because the row is the only thing here that knows WHICH machine is about to be replaced.

    @Test
    fun `the row asks before it replaces, and the question names the machine`() {
        val question = PairedMachineRowScreen.of("nathans-mbp").replaceConfirmation

        assertTrue(
            "the confirmation does not name the machine it ends the pairing with: '$question'. " +
                "'Are you sure?' asks the user to confirm something the sentence never told them, " +
                "and the row already knows the name",
            question.contains("nathans-mbp"),
        )
        assertTrue(
            "the confirmation is not a question: '$question'",
            question.contains("?"),
        )
    }

    @Test
    fun `the confirmation names what replacing destroys, not just what it does`() {
        // SessionDetailScreen.KILL_CONFIRMATION's ruling, and it applies harder here: a
        // confirmation that stated the ACTION would read the same as every other one in the app,
        // which is how a user learns to dismiss the one that matters. Both halves are irreversible
        // on this phone -- the registration ends on the machine, and PhoneRuntime.purgeKeys
        // destroys the key tiers in a `finally` beside the verb.
        val question = PairedMachineRowScreen.of("nathans-mbp").replaceConfirmation

        assertTrue(
            "the confirmation does not say the pairing ends: '$question'",
            question.contains("pairing", ignoreCase = true),
        )
        assertTrue(
            "the confirmation does not say this phone's keys are destroyed: '$question'. That is " +
                "the half a user cannot see and cannot undo -- both tiers go, whether or not the " +
                "command ever reached the machine",
            question.contains("keys", ignoreCase = true),
        )
    }

    @Test
    fun `a machine whose name this phone cannot read still gives the question a subject`() {
        // PairingSurface.machineOf answers "" rather than a placeholder, and the label above
        // already renders that as `Paired` rather than a dangling `Paired with `. The question has
        // the same obligation: `Replace ?` is worse than no confirmation, because it is the one
        // sentence standing between the user and an action nothing here can undo.
        val question = PairedMachineRowScreen.of("").replaceConfirmation

        assertFalse(
            "the confirmation rendered a dangling subject for an unreadable machine name: " +
                "'$question'",
            question.contains("Replace ?") || question.contains("Replace?"),
        )
        assertTrue(
            "the confirmation lost its question for an unreadable machine name: '$question'",
            question.contains("?"),
        )
    }

    @Test
    fun `the confirmation is a second sentence and not the row's own copy repeated`() {
        val row = PairedMachineRowScreen.of("nathans-mbp")

        assertNotEquals(row.sublabel, row.replaceConfirmation)
        assertNotEquals(row.replaceLabel, row.replaceConfirmation)
    }

    @Test
    fun `there is no add-another-computer affordance, because v1 cannot perform one`() {
        // internal/skeleton/pairing.go's BeginPairing refuses a second pairing while one device
        // is registered, fail-fast, before any rendezvous id, secret or QR is minted. Offering to
        // "add" a second computer would be a control promising something the daemon refuses on
        // the first press -- the row offers replace (revoke-then-pair) and nothing else.
        val row = PairedMachineRowScreen.of("nathans-mbp")

        listOf(row.label, row.sublabel, row.replaceLabel).forEach { copy ->
            assertFalse(
                "found an 'add another computer' style affordance in: '$copy'",
                copy.contains("add another", ignoreCase = true) ||
                    copy.contains("add a computer", ignoreCase = true),
            )
        }
    }
}
