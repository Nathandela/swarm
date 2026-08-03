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
    /**
     * Why the scanner is not on screen, and empty in every state where it is.
     *
     * IT IS A SEPARATE FIELD FROM [notice] RATHER THAN A THIRD ARM OF IT. That one is documented
     * "never both" and its two arms are genuinely disjoint -- an interrupted attempt and a
     * destination confirmation cannot be the same draw. A blocked camera is disjoint from neither:
     * a phone whose pairing was interrupted can also have had its camera turned off, and folding
     * the two into one string would silently drop whichever arm lost the `when`.
     */
    val cameraNotice: String,
    /** PB-PAIR-6: the destination on screen, and empty until it is the step to show one. */
    val destination: String,
    /** PB-SAS-3's symbols, empty until the handshake has derived them. */
    val sas: List<String>,
    val sasInstruction: String,
    /**
     * How to get a code in the first place: empty everywhere but the step that has none yet.
     *
     * See [PairingPanelScreen.GUIDANCE] for what these say and why the screen has them at all.
     */
    val steps: List<PairingGuidance>,
    val controls: Set<PairingControl>,
)

/**
 * One numbered step of "how do I get a pairing code".
 *
 * THE COMMAND IS A FIELD ON THE STEP AND NOT A SEPARATE PANEL PROPERTY, because it belongs to one
 * step rather than to the screen: a `command` beside `steps` would let a composition draw the well
 * under whichever step it liked, including the one that says a QR code has appeared.
 *
 * @param command the shell line this step is telling the user to run, or empty where the step is
 *  telling them to do something else. Empty is not a placeholder -- it is what makes step 2 a
 *  sentence rather than a sentence over an empty mono well.
 */
data class PairingGuidance(
    val ordinal: String,
    val line: String,
    val command: String = "",
)

/**
 * The nine controls the pairing screen can offer.
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

    /**
     * Only a PERMANENT denial. An ordinary one is re-askable and Settings is a detour.
     *
     * IT SITS WHERE [SCAN] SITS BECAUSE IT IS THE CONTROL THAT REPLACES IT. `pairingPanelView`
     * composes in THIS enum's order, and the two are never offered together -- a permanent denial
     * is the one state that withdraws the scanner -- so putting this second is what makes the
     * screen read as one that swapped its primary action rather than one that lost it.
     */
    OPEN_SYSTEM_SETTINGS,

    /**
     * "Enter code instead": the control that reveals [TYPED_PAYLOAD], and the reason the field is
     * not open already.
     *
     * THE OWNER ASKED FOR THIS AND THE FIELD REPORT IS WHY. On the internal-testing build the
     * pairing screen was a paste field and nothing else (agents-tracker-qx9m), and a person who has
     * only ever seen that reads pasting as how this product pairs. The typed payload is PB-PAIR-2's
     * FALLBACK -- for a camera that was denied or does not work -- and a fallback presented beside
     * the primary path with equal weight is not a fallback.
     *
     * IT IS NOT OFFERED ONCE THE FIELD IS UP. A disclosure beside the thing it has already
     * disclosed is a control with nothing left to do.
     */
    REVEAL_TYPED_PAYLOAD,

    /** PB-PAIR-2's fallback field, offered exactly when the camera is not. */
    TYPED_PAYLOAD,

    USE_TYPED_PAYLOAD,

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
     * The command a machine runs to produce a pairing code.
     *
     * IT IS WRITTEN DOWN ONCE AND `android/gate/guidedpairing_test.go` KEEPS IT THAT WAY. A person
     * retypes this into a shell off a phone screen, so a second spelling is a second thing that can
     * be a typo -- and the failure it produces is `command not found` on the machine they were told
     * to run it on, with the phone still showing them the instruction they followed correctly.
     */
    const val COMMAND = "swarm remote pair"

    /**
     * How to get a code, in two steps, on the one screen that has none yet.
     *
     * ## Why the screen needs this at all
     *
     * The owner installed the internal-testing build, found the pairing screen, and it gave them a
     * bare text field with no camera and no instructions (agents-tracker-qx9m). The camera half of
     * that is the permission catch-22 `PairingFlow.offersScanner` closes. This is the other half,
     * and it is not the same defect: even with a working scan button, nothing on the screen said
     * where a code comes from. `PairingFlow.messageFor(SCAN)` says "Scan the code your machine is
     * showing", which presupposes a machine that is already showing one -- and the phone is where a
     * person is standing when they need to be told to go and make that happen.
     *
     * ## "your computer" and never "your Mac"
     *
     * The handset cannot know which desktop OS is on the other end. Nothing has been connected yet,
     * and the pairing payload carries no such field -- so "On your Mac" is correct for the owner and
     * wrong for everyone on Linux. A confidently wrong instruction is worse than a general one on
     * the screen whose whole job is telling someone what to do first.
     *
     * ## Two steps and not three
     *
     * "Scan it below" is step 2's second sentence rather than a step 3, because the third thing is
     * not something the user does on the computer -- it is the control directly beneath, and a step
     * numbered for pressing the button under it reads as a list that has lost count of itself.
     */
    val GUIDANCE: List<PairingGuidance> = listOf(
        PairingGuidance("1", "On your computer, run", COMMAND),
        PairingGuidance("2", "It shows a QR code. Scan it below."),
    )

    /**
     * The primary action, named for what it produces rather than for what it operates.
     *
     * It was "Scan the code on your machine", which is the same sentence
     * [PairingFlow.messageFor] already puts on screen two lines above it. A button that restates
     * the instruction above it gives a reader nothing to distinguish them by; this names the thing
     * the user is looking for.
     */
    const val SCAN_CTA = "Scan QR code"

    /** The fallback, worded as one: "instead" is the word doing the work. */
    const val MANUAL_CTA = "Enter code instead"

    /**
     * Why the scanner is gone, in the one state that withdraws it for good.
     *
     * WITHOUT THIS THE SCREEN SIMPLY HAS A DIFFERENT BUTTON ON IT. A person who never saw the scan
     * control has no reason to connect "Open this app's settings" to a camera permission they
     * declined twice, possibly months ago -- and Android will not show the prompt again, so nothing
     * else on the phone is going to tell them either.
     *
     * IT NAMES THE PASTE PATH TOO, because in this state that is the only thing on the screen that
     * works without leaving the app.
     */
    const val CAMERA_BLOCKED = "This app cannot use the camera, so it cannot scan a code. Turn " +
        "the camera on in Settings, or paste the code your machine printed."

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
     * @param manualEntryRevealed whether the user has asked for the typed fallback. It is the
     *  SURFACE's state and not the attempt's, because it is a fact about what someone has pressed
     *  on this draw of this screen rather than about the pairing -- a resumed attempt has no
     *  business remembering that a field was once open.
     */
    fun of(
        attempt: PairingAttempt,
        scanner: ScannerState,
        sas: SasStep?,
        holding: Boolean,
        machine: String = "",
        manualEntryRevealed: Boolean = false,
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
                // OPEN WHERE IT IS THE WHOLE SCREEN, BEHIND A CONTROL WHERE IT IS THE FALLBACK,
                // AND THE ASYMMETRY IS THE DECISION. A permanent denial withdraws the scanner for
                // good -- Android will not show the prompt again -- so the typed code is the only
                // thing left that works without leaving the app, and collapsing it there would be
                // the dead end PB-PAIR-2 forbids rather than a tidier default. Everywhere else the
                // camera is still askable, the scan control is on screen, and a paste field of
                // equal weight beside it is what made the owner read pasting as how this product
                // pairs (agents-tracker-qx9m).
                if (manualEntryRevealed || PairingFlow.routesToSystemSettings(scanner)) {
                    controls += PairingControl.TYPED_PAYLOAD
                    controls += PairingControl.USE_TYPED_PAYLOAD
                } else {
                    controls += PairingControl.REVEAL_TYPED_PAYLOAD
                }
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
            // ONLY THE STATE THAT WITHDRAWS THE SCANNER EXPLAINS ITSELF. On an ordinary denial the
            // scan control is right there and pressing it re-asks; a sentence about a blocked
            // camera would be a warning about something that is not wrong, which is the same
            // defect as sending a fresh install to a Settings screen.
            cameraNotice = if (scanning && PairingFlow.routesToSystemSettings(scanner)) {
                CAMERA_BLOCKED
            } else {
                ""
            },
            destination = if (confirming) attempt.originShown else "",
            sas = if (comparing) checkNotNull(sas).symbols else emptyList(),
            sasInstruction = if (comparing) checkNotNull(sas).instruction else "",
            // THE SCAN STEP AND NOTHING ELSE. Every terminal state's message was argued line by
            // line under PB-PAIR-5 and each names a different next move -- "Ask your machine for a
            // new one", "Approve it there, then pair again", "Wait a minute" -- so stacking a
            // generic "run this command" over one of them is the second, shorter statement of a
            // refusal that this file's own comment refuses for headings. And over a LIVE attempt
            // it would be worse than redundant: an instruction to create a code, offered to a
            // phone that already has one, reads as an invitation to start again -- which is the
            // dead end PB-PAIR-4 describes, because BeginPairing fail-fasts while a device is
            // registered.
            steps = if (scanning && step == PairingStep.SCAN) GUIDANCE else emptyList(),
            controls = controls,
        )
    }
}
