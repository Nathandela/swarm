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
    /**
     * Why this screen is asking for a relay address, and empty everywhere it is not asking.
     *
     * A THIRD SENTENCE FIELD RATHER THAN AN ARM OF [notice] OR [cameraNotice], for the reason
     * recorded on the second one: these are not disjoint. A permanently denied camera on a fresh
     * install draws both -- the scanner is gone for good AND the phone has no relay yet -- and
     * folding them together would silently drop whichever arm lost the `when`, on the one screen
     * where the typed path is the entire product.
     */
    val relayNotice: String,
    /** PB-PAIR-6: the destination on screen, and empty until it is the step to show one. */
    val destination: String,
    /**
     * Whether the line under the viewfinder reporting what the camera has looked at is on
     * screen (agents-tracker-av7k).
     *
     * IT IS A FLAG AND NOT THE SENTENCE, WHICH IS THE ONE DESIGN DECISION IN THIS FIELD. The
     * count changes several times a second, and this panel is compared against the last one to
     * decide whether the view tree is rebuilt -- so a panel carrying the number would tear the
     * camera preview out of the hierarchy and put it back multiple times a second, which is the
     * defect the counter exists to diagnose, caused by the counter. The number is written
     * straight into the slot by the surface; the COPY is still the screen's
     * ([PairingPanelScreen.scanProgress]) and so is the decision to show the line at all.
     */
    val showsScanProgress: Boolean,
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
 * The ten controls the pairing screen can offer.
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

    /**
     * Where the relay address goes on the ONE pairing that has to be told it
     * (agents-tracker-3fkm).
     *
     * IT SITS BETWEEN THE CODE AND THE BUTTON because the composition walks this enum in order,
     * and the order is the form's: the thing you were told to type, the thing it needs, then the
     * single action that takes both. A field below `Use this code` is a field a person meets
     * after pressing the button that wanted it.
     *
     * IT IS NOT A SECOND PAIRING-FORM FIELD ([PairingFlow.manualEntryAcceptsSeparateFields] is
     * still false). The typed code IS the QR's payload in another spelling; this is remembered
     * configuration, asked for when absent and kept afterwards, and the address it collects is
     * displayed and confirmed through PB-PAIR-6 exactly like a scanned one.
     */
    RELAY_URL,

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
     * where a code comes from. `PairingFlow.messageFor(SCAN)` said "Scan the code your machine is
     * showing", which presupposed a machine that is already showing one -- and the phone is where a
     * person is standing when they need to be told to go and make that happen. THAT SENTENCE IS
     * GONE NOW (agents-tracker-ksvb.6): it duplicated step 2 below word for word once this
     * guidance existed, and this comment is left in the past tense as the reason the guidance was
     * written, not as a claim about what the screen still says beside it.
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
     * It was "Scan the code on your machine", which was the same sentence
     * [PairingFlow.messageFor] put on screen two lines above it before agents-tracker-ksvb.6
     * deleted that sentence for the same reason. A button that restates the instruction above it
     * gives a reader nothing to distinguish them by; this names the thing the user is looking for.
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
     * Why the scanner is gone on a device with no camera at all, and why -- unlike
     * [CAMERA_BLOCKED] -- it names no settings route (agents-tracker-nz9h).
     *
     * NOTHING IN SETTINGS ADDS HARDWARE A HANDSET DOES NOT HAVE. Sending this device to the same
     * sentence [CAMERA_BLOCKED] uses would be the identical defect this screen already avoids for
     * a permission nobody withheld: a control that leads nowhere useful.
     */
    const val NO_CAMERA_NOTICE = "This device has no camera, so it cannot scan a code. Paste " +
        "the code your machine printed."

    /**
     * Why a first pairing asks for a relay address, and why only the first one does.
     *
     * IT DOES NOT SAY "the address your machine printed", WHICH WOULD BE THE FRIENDLIER
     * SENTENCE AND IS NOT TRUE. `swarm remote pair` prints the short code, the QR and the long
     * payload; the relay URL is inside the payload and the symbol, and no line of that output
     * spells it on its own (cmd/swarm/remote.go, printPairingQR). Telling a user to copy
     * something that is not on their screen is the guided screen's original defect
     * (agents-tracker-qx9m) in a new place, so this asks for what the phone needs and says what
     * becomes of it. That the desktop should print the address beside the code is filed
     * separately; the copy will follow the output, never lead it.
     *
     * THE SECOND CLAUSE IS THE ONE THAT MATTERS TO A PERSON HOLDING A PHONE. This is a
     * configuration question in the middle of a ceremony, and the honest reassurance is that it
     * is asked once: the address is remembered on the confirm step, so a pairing that fails
     * afterwards costs a re-run of the desktop verb and ten characters, not this field again.
     */
    const val RELAY_ASK = "This phone does not know your relay yet. Enter its address once; it " +
        "is remembered after this pairing."

    /**
     * What the camera has looked at so far, or nothing at all before it has looked at anything.
     *
     * THE OWNER'S REPORT IS "I HOLD IT OVER THE CODE AND NOTHING HAPPENS" (agents-tracker-av7k),
     * and nothing is what a dead pipeline, a camera that never opened and a symbol that will not
     * decode all look like. This is the one number that separates them, and it is on the phone
     * because that is where the person is standing.
     *
     * IT IS A COUNT AND NEVER A PROGRESS BAR. There is nothing to be a fraction of -- a scan
     * succeeds on the frame it succeeds on -- and a bar filling toward an end that does not exist
     * is the fake progress this screen has no business inventing. Zero frames says nothing rather
     * than "0 frames analysed", because a line claiming a camera is looking is a claim, and
     * before the first frame nobody has established it.
     */
    fun scanProgress(frames: Long): String = when {
        frames <= 0 -> ""
        frames == 1L -> "1 frame analysed, no code found yet"
        else -> "$frames frames analysed, no code found yet"
    }

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
     * @param relayKnown whether this phone already has a relay address to dial
     *  (`PhoneRuntime.knownRelay`). IT CARRIES NO DEFAULT, deliberately: "false" is the state of
     *  every fresh install and the state the typed short code cannot pair from without being
     *  asked, and this screen has now shipped three defects whose common shape is a first-run
     *  state no test ever constructed (agents-tracker-qx9m, -qun0, -v5qc). A caller that has to
     *  say which phone it means cannot forget that there are two.
     * @param cameraLive whether the analysis pipeline is actually running. It is NOT derivable
     *  from [scanner], which answers who may open the camera and knows nothing about whether one
     *  was opened -- that is a fact about a control someone pressed, so it belongs to the surface
     *  and arrives here as a parameter. It is what keeps the frame counter off a screen where no
     *  camera is looking at anything.
     * @param typedEntryCarriesItsOwnRelay what `PairingFlow.entryCarriesItsOwnRelay` answers
     *  about the text in the field right now. The decision is the FLOW's and the fact is the
     *  SURFACE's; what arrives here is the answer, so no composition has to look at a field to
     *  know what the screen offers.
     */
    fun of(
        attempt: PairingAttempt,
        scanner: ScannerState,
        sas: SasStep?,
        holding: Boolean,
        machine: String = "",
        manualEntryRevealed: Boolean = false,
        cameraLive: Boolean = false,
        relayKnown: Boolean,
        typedEntryCarriesItsOwnRelay: Boolean = false,
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
                // OPEN AUTOMATICALLY WHEREVER THE SCANNER IS WITHDRAWN FOR GOOD -- a permanent
                // denial and no camera hardware alike (agents-tracker-nz9h) -- because in both
                // states the typed path is the only thing left that works without leaving the
                // app, and collapsing it there is the dead end PB-PAIR-2 forbids.
                if (manualEntryRevealed || !PairingFlow.offersScanner(scanner)) {
                    controls += PairingControl.TYPED_PAYLOAD
                    controls += PairingControl.USE_TYPED_PAYLOAD
                } else {
                    controls += PairingControl.REVEAL_TYPED_PAYLOAD
                }
                // THE ADDRESS TEN CHARACTERS CANNOT CARRY (agents-tracker-3fkm). Asked only where
                // there is a code field to type into -- a relay box beside a collapsed fallback
                // is half a form -- and only where the answer is not already held: the URL is
                // remembered on the PB-PAIR-6 confirm, so a second ask would be this screen
                // asking for something it has, whose obvious answer is to retype it differently.
                // A pasted LONG payload carries its own, and asking beside it would collect a
                // value the string already holds and then ignore it.
                if (PairingControl.TYPED_PAYLOAD in controls &&
                    !relayKnown &&
                    !typedEntryCarriesItsOwnRelay
                ) {
                    controls += PairingControl.RELAY_URL
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
            // ONLY THE STATES THAT WITHDRAW THE SCANNER EXPLAIN THEMSELVES. On an ordinary denial
            // the scan control is right there and pressing it re-asks; a sentence about a blocked
            // camera would be a warning about something that is not wrong, which is the same
            // defect as sending a fresh install to a Settings screen. The two withdrawn states
            // read differently (agents-tracker-nz9h): only a permanent denial names Settings.
            cameraNotice = when {
                scanning && PairingFlow.routesToSystemSettings(scanner) -> CAMERA_BLOCKED
                scanning && PairingFlow.hasNoCamera(scanner) -> NO_CAMERA_NOTICE
                else -> ""
            },
            // THE SENTENCE FOLLOWS THE FIELD, so the two cannot disagree: a screen that carried
            // the explanation without the box, or the box without the explanation, would be one
            // decision written down twice.
            relayNotice = if (PairingControl.RELAY_URL in controls) RELAY_ASK else "",
            destination = if (confirming) attempt.originShown else "",
            // ONLY WHERE A CAMERA IS ACTUALLY LOOKING. A live attempt has its payload and the
            // preview is closed, so a count surviving into it would be reporting on a pipeline
            // that has stopped -- and before the scan control is pressed there is no pipeline to
            // report on at all.
            showsScanProgress = scanning && cameraLive,
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
