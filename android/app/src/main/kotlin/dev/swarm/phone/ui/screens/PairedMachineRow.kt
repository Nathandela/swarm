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
 */
data class PairedMachineRow(
    /** [PairingPanelScreen.titleFor] for a completed pairing: the machine, named. */
    val label: String,
    /** What replacing costs, in the order the daemon performs it. */
    val sublabel: String,
    /** The one control the row offers. */
    val replaceLabel: String,
)

object PairedMachineRowScreen {

    /**
     * REPLACE, and never "add". `BeginPairing` refuses a second pairing while one device is
     * registered, so the only pairing change this product can perform is revoke-then-pair.
     */
    private const val REPLACE = "Replace this computer"

    /**
     * The cost, stated in the daemon's own order: the current pairing ends FIRST, and the new one
     * is what the revoke makes room for. A row that only said "replace" would let a user find out
     * afterwards that the machine they had is gone.
     *
     * THE SECOND HALF WAS A PROMISE THE APP COULD NOT KEEP until agents-tracker-d0b8. Ending the
     * pairing worked; "then pairs a new computer" did not, because the presentation gate read the
     * pinned machine -- which the revoke does not clear -- and went on showing the four-tab shell,
     * with the pairing entry point on the settings screen inside it. The press now leaves the phone
     * reading unpaired and redraws the whole window, so the sentence describes what happens.
     */
    private const val COST = "Replacing ends the current pairing, then pairs a new computer."

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
        sublabel = COST,
        replaceLabel = REPLACE,
    )
}
