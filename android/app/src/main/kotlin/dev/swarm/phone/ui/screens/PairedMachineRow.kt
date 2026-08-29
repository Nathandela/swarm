package dev.swarm.phone.ui.screens

import dev.swarm.phone.ui.PairingStep

/**
 * agents-tracker-64rf: the paired machine, on the SETTINGS destination.
 *
 * WHY THE ROW EXISTS AT ALL. The pairing entry point was correctly built and correctly wired and
 * an owner still could not find it on a real handset: it was composed below the inbox list, on one
 * of four tabs, with nothing on screen saying it was there. The recorded decision moves it -- an
 * unpaired phone gets its own screen, and a PAIRED one meets the pairing here, as a row naming the
 * machine it is attached to and offering the one change that can be made to it.
 *
 * WHY IT IS NOT A [SettingsRow]. Every row that type describes is a push toggle: `checked`,
 * `enabled`, and a `PushToggle` bijection with the preference it drives. This row carries no
 * boolean the machine can be in, and its control does not flip a value back and forth -- it starts
 * a one-way, destructive action. A `checked` field here would mean nothing, which is exactly the
 * shape that invites a caller to wire a switch to it.
 *
 * THE LABEL IS [PairingPanelScreen]'S, ASKED RATHER THAN RE-DERIVED. "What does a completed
 * pairing say" is already answered by [PairingPanelScreen.titleFor] -- `Paired` for a machine name
 * this phone cannot read, `Paired with <name>` for one it can. Two conditionals computing "is the
 * machine name empty" independently is how the two screens drift the day only one is edited.
 *
 * THE COPY IS HONEST THAT REPLACING ENDS THE PAIRING, AND OFFERS NOTHING ELSE. swarm v1 is
 * single-device: `internal/skeleton/pairing.go`'s `BeginPairing` refuses a second pairing while a
 * device is registered -- fail-fast, before any rendezvous id, secret or QR is minted -- and names
 * revoke-then-pair as the remedy. So there is no "add another computer" here: that control would
 * promise something the daemon refuses on the first press. There is replace, and it says what it
 * costs before anyone presses it.
 *
 * AND IT ASKS (agents-tracker-mrq5). Saying what it costs is what the row does at rest -- and
 * since W5.4 (row 36) the row states no cost at all, so the confirmation is the only place either
 * fact reaches the user: [PairedMachineRow.replaceConfirmation] names the machine, and names the
 * way back (a new code), because a user reading a dialog has stopped reading the row underneath it.
 */
data class PairedMachineRow(
    /** [PairingPanelScreen.titleFor] for a completed pairing: the machine, named. */
    val label: String,
    /**
     * Empty (phone refit W5.4, row 36): the row is the computer's name and nothing under it.
     * The cost of replacing is [PairedMachineRow.replaceConfirmation]'s to state, once, at the
     * press.
     */
    val sublabel: String,
    /** The one control the row offers. */
    val replaceLabel: String,
    /**
     * What the confirmation asks before the revoke goes out (agents-tracker-mrq5).
     *
     * IT IS THE ONE FIELD ON THIS ROW THAT NAMES THE MACHINE TWICE OVER, and that is the point: a
     * confirmation is read in a second window, over a screen the user has stopped looking at, and
     * "Are you sure?" asks them to confirm something the sentence never told them.
     */
    val replaceConfirmation: String,
)

object PairedMachineRowScreen {

    /**
     * REPLACE, and never "add". `BeginPairing` refuses a second pairing while one device is
     * registered, so the only pairing change this product can perform is revoke-then-pair.
     */
    private const val REPLACE = "Replace this computer"

    /**
     * The confirmation's subject when this phone cannot read the machine's name.
     *
     * `Replace ?` is worse than no confirmation at all: it is the one sentence standing between a
     * user and an action nothing on this handset can undo. [labelFor]'s `Paired` answers the same
     * absence on the line above, and this answers it here rather than interpolating an empty string
     * into a question.
     */
    private const val UNNAMED = "the paired computer"

    /**
     * What replacing costs, in the register [SessionDetailScreen]'s kill confirmation set: the
     * CONSEQUENCE and not the action.
     *
     * THE TABLE'S RULING (row 37, phone refit W5.4; restated in the W5 review round, 2026-08-29,
     * SHOULD-FIX 6): the confirmation names the PAIRING ending, and says nothing about the key
     * purge that runs beside it in `PhoneRuntime.purgeKeys`'s `finally`. An earlier draft argued
     * both halves belonged here, on the reasoning that both are irreversible; the table's row is
     * the more specific instrument and this sentence follows it rather than re-litigating it.
     *
     * THE LAST CLAUSE IS THE WAY BACK, and it is stated because there is one and it is not on this
     * handset. Pairing again needs the code the computer shows, so a phone whose owner is nowhere
     * near their machine has just made itself useless until they are.
     */
    private const val CONFIRM_COST = "You'll need a new code to pair again."

    /** [CONFIRM_COST], asked about the machine that is actually about to be replaced. */
    private fun confirmationFor(machine: String): String =
        "Replace ${machine.ifEmpty { UNNAMED }}? $CONFIRM_COST"

    /**
     * @param machine the machine this phone is pinned to. Empty is not a placeholder and not "no
     *  pairing": it is a pairing whose name this phone cannot read, and the label says `Paired`
     *  rather than the dangling `Paired with ` a naive interpolation renders. Whether there is a
     *  pairing at all is a different question, and [SettingsPanelScreen.of] is where it is asked.
     */
    fun of(machine: String): PairedMachineRow = PairedMachineRow(
        label = checkNotNull(PairingPanelScreen.titleFor(PairingStep.PAIRED, machine)) {
            "PairingPanelScreen.titleFor answered null for PAIRED, so the one place this app " +
                "words a completed pairing no longer words it. This row must not invent a second."
        },
        // NO SUBLABEL (phone refit W5.4): the row is the computer's name; the cost of replacing
        // -- ending this pairing first, in the daemon's own order, before a new one can be paired
        // -- is the confirmation's to state, once, when the control is pressed.
        sublabel = "",
        replaceLabel = REPLACE,
        replaceConfirmation = confirmationFor(machine),
    )
}
