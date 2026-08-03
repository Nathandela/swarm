package dev.swarm.phone.ui.screens

import dev.swarm.phone.ui.PairingAttempt
import dev.swarm.phone.ui.PairingFlow
import dev.swarm.phone.ui.PairingStep
import dev.swarm.phone.ui.SasStep
import dev.swarm.phone.ui.ScannerState

/**
 * Phase B slice S24 -- PB-DS-9: the PAIRING screen's model.
 *
 * WHAT IT ADDS TO [PairingFlow], WHICH ALREADY DECIDES A LOT. That object owns the policy: which
 * step a state is, what each step says, whether a manual fallback is offered, whether a denial
 * routes to Settings. What it does NOT own is the SCREEN: which of the eight controls is on
 * screen in which step, what heading a step carries, and whether the destination or the symbols
 * are visible. That lived in `PairingSurface.renderScanStep` / `renderDestinationStep` /
 * `renderSasStep` as three functions that set `View.visibility`, which is to say it was decided
 * where nothing could check it. This is the same decisions as data.
 *
 * IT RE-WORDS NOTHING. [body] is `PairingFlow.messageFor` verbatim and [notice] is
 * [PairingAttempt.destinationNotice] verbatim, because a second wording of a pairing state is a
 * second place for the security-relevant sentences to drift -- and the SAS step's sentence is the
 * one this product cannot afford to soften (ADR-007 B133 left it as the only human-in-the-loop
 * security check in the product).
 *
 * ## The heading, and the twelve steps the artifact never drew
 *
 * Inventory C7 draws three steps and gives each a heading: `Pair a computer`, `Check both
 * screens`, `Paired with nathans-mbp`. [PairingStep] has fifteen values. The twelve without a
 * recorded heading get NULL rather than an invented one, and that is a deliberate boundary: the
 * missing twelve are the handshake, the wait, and ten refusals, and every one of them already has
 * a full sentence from [PairingFlow.messageFor] that was argued line by line under PB-PAIR-5.
 * Writing twelve headings here would put a second, shorter statement of each refusal above the
 * one that was reviewed -- and "That code has expired." reads worse under a heading that
 * paraphrases it.
 */
data class PairingPanel(
    /** The step's heading, or null where the artifact draws none. See the class comment. */
    val title: String?,
    /** [PairingFlow.messageFor], verbatim. */
    val body: String,
    /** The interrupted-attempt line or the destination line, whichever is true. Never both. */
    val notice: String,
    /** PB-PAIR-6: the destination on screen, and empty until it is the step to show one. */
    val destination: String,
    /** PB-SAS-3's symbols, empty until the handshake has derived them. */
    val sas: List<String>,
    val sasInstruction: String,
    val controls: Set<PairingControl>,
)

/**
 * The eight controls the pairing screen can offer.
 *
 * THERE IS NO CONTROL THAT INGESTS A SAS, and the omission is the type's point rather than an
 * oversight: PB-SAS-3 requires the code to be COMPARED by the person who can see both screens and
 * never typed into this one. `android/gate/s16_ui_test.go` fences the absence of such a field in
 * the surface sources; this enum is the same absence one layer up, where a screen that grew one
 * would have to name it.
 */
enum class PairingControl {
    /** Start the camera. */
    SCAN,

    /** PB-PAIR-2's fallback field, offered exactly when the camera is not. */
    TYPED_PAYLOAD,

    USE_TYPED_PAYLOAD,

    /** Only a PERMANENT denial. An ordinary one is re-askable and Settings is a detour. */
    OPEN_SYSTEM_SETTINGS,

    /** PB-PAIR-6: nothing is joined until this is pressed. */
    CONFIRM_DESTINATION,

    CODES_MATCH,

    /** NOT "cancel". A mismatch is the only signal this protocol has for a man-in-the-middle. */
    CODES_DO_NOT_MATCH,

    STOP,
}

object PairingPanelScreen {

    /** Inventory C7 step 0. */
    private const val SCAN_TITLE = "Pair a computer"

    /** Inventory C7 step 1. */
    private const val COMPARE_TITLE = "Check both screens"

    /** Inventory C7 step 2, whose recorded form names the machine. */
    private const val PAIRED_TITLE = "Paired"

    /**
     * PB-PAIR-4's resumed attempt, in the words the surface already used.
     *
     * BOTH HALVES MATTER. The first says what happened, and the second is the one a user needs:
     * an interrupted pairing that might have joined something is a different situation from one
     * that provably did not, and this protocol only ever reaches PAIRED through a human answering
     * the comparison.
     */
    private const val INTERRUPTED =
        "This pairing was interrupted before it finished. Nothing was joined."

    /**
     * @param machine the machine this phone is pinned to, for the one heading that names it.
     *  Empty is not a placeholder -- a completed pairing whose machine this screen cannot read
     *  says `Paired` rather than `Paired with `, which is what a naive interpolation renders.
     */
    fun titleFor(step: PairingStep, machine: String = ""): String? = when (step) {
        PairingStep.SCAN -> SCAN_TITLE
        PairingStep.COMPARING_CODES -> COMPARE_TITLE
        PairingStep.PAIRED -> if (machine.isEmpty()) PAIRED_TITLE else "$PAIRED_TITLE with $machine"
        else -> null
    }

    /**
     * @param holding whether a pairing handle is live IN THIS PROCESS. It is not derivable from
     *  [attempt]: `Pairing` is a process-local handle and a relaunch mid-handshake comes back with
     *  the recorded step and no handle, which is precisely PB-PAIR-4's case -- the screen then says
     *  what was interrupted and offers the one action that can still be taken.
     * @param scanner the camera's answer, consulted only while there is no live attempt.
     * @param sas the derived code, or null while the handshake has not produced one. Null is not
     *  "no comparison": the core stays in `pairing` from the dial until the user answers, so the
     *  SAS is what says whether there is anything to compare yet.
     */
    fun of(
        attempt: PairingAttempt,
        scanner: ScannerState,
        sas: SasStep?,
        holding: Boolean,
        machine: String = "",
    ): PairingPanel {
        val step = attempt.step
        // THE THREE MODES ARE COMPUTED ONCE AND SHARED. They were three functions each deriving
        // their own, which is how `renderDestinationStep` came to overwrite the notice
        // `renderScanStep` had just written -- harmless there because the two conditions happen to
        // be disjoint, and harmless is not the same as checked.
        val scanning = !holding
        val confirming = holding && step == PairingStep.CONFIRM_DESTINATION
        // NULL SAS IS NOT "NO COMPARISON". The core stays in `pairing` from the dial until the
        // user answers, so the derived code is what says there is anything to compare yet -- and
        // offering the two answers over no symbols would ask a person to vouch for nothing.
        val comparing = holding && step == PairingStep.COMPARING_CODES && sas != null

        val controls = mutableSetOf<PairingControl>()
        if (scanning) {
            // The camera is consulted ONLY here. A live attempt has already got its payload, and
            // a scanner offered mid-handshake is the dead end PB-PAIR-4 describes: BeginPairing
            // fail-fasts while a device is registered, so the scan leads to a refusal the user
            // cannot resolve from the handset.
            // ASKABLE, not "already ours". This read `scanner == ScannerState.SCANNING`, so the
            // control was offered only where the permission was already granted -- and the app's
            // only `requestPermissions(CAMERA)` is that control's listener, so nothing could ever
            // ask for it. A fresh install resolves to PERMISSION_DENIED and got a paste field and
            // no camera, for the life of the install (agents-tracker-qx9m).
            if (PairingFlow.offersScanner(scanner)) controls += PairingControl.SCAN
            if (PairingFlow.offersManualEntry(scanner)) {
                controls += PairingControl.TYPED_PAYLOAD
                controls += PairingControl.USE_TYPED_PAYLOAD
            }
            if (PairingFlow.routesToSystemSettings(scanner)) {
                controls += PairingControl.OPEN_SYSTEM_SETTINGS
            }
        }
        if (confirming) controls += PairingControl.CONFIRM_DESTINATION
        if (comparing) {
            controls += PairingControl.CODES_MATCH
            controls += PairingControl.CODES_DO_NOT_MATCH
        }
        if (holding && !PairingFlow.isTerminal(step)) controls += PairingControl.STOP

        return PairingPanel(
            title = titleFor(step, machine),
            body = PairingFlow.messageFor(step),
            notice = when {
                scanning && attempt.explainsInterruptedAttempt -> INTERRUPTED
                confirming -> attempt.destinationNotice
                else -> ""
            },
            destination = if (confirming) attempt.originShown else "",
            sas = if (comparing) checkNotNull(sas).symbols else emptyList(),
            sasInstruction = if (comparing) checkNotNull(sas).instruction else "",
            controls = controls,
        )
    }
}
