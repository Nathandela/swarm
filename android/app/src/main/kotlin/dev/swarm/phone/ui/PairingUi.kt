package dev.swarm.phone.ui

import dev.swarm.phone.runtime.PermissionState

/**
 * Phase B slice S16 -- PB-PAIR-2 (camera denial paths), PB-PAIR-4 (the persisted state
 * machine), PB-PAIR-5 (explicit terminal states), PB-PAIR-6 (nothing joined silently) and
 * PB-SAS-3 (the code is compared, never typed).
 *
 * NO CAMERA IS OPENED OR MODELLED HERE. The decode belongs to the scanner library ADR-007
 * still has to name (PB-PAIR-3), and whether a real camera yields a frame is PB-E2E-5, which
 * is deferred. What is modelled is the POLICY the requirement names: which state the screen is
 * in for each permission answer, that a manual fallback exists and is specified, and that the
 * destination is displayed before anything is joined.
 */

/** The three states PB-PAIR-2 enumerates for the scanner screen. */
enum class ScannerState {
    SCANNING,

    /** Re-askable: Android will show the prompt again. */
    PERMISSION_DENIED,

    /** Not re-askable: only the system settings screen can undo this one. */
    PERMISSION_PERMANENTLY_DENIED,
}

/**
 * The two answers PB-SAS-3 allows. "They do not match" is NOT "cancel": it is the only signal
 * this protocol has for a man-in-the-middle, and recording it as "I changed my mind" discards
 * the security event and invites the user to try again against the same attacker.
 */
enum class SasAnswer { MATCHES, DOES_NOT_MATCH }

/**
 * Every step a pairing attempt can be in, including every one of PB-PAIR-5's terminal states.
 *
 * COLLAPSING THE TERMINALS INTO ONE "pairing failed" is what the requirement exists to
 * prevent: the screen could then only show an error string beside it, which is the opaque
 * error, and each of these needs a genuinely different next step from the user.
 *
 * THE SET IS THE CORE'S, NOT THIS FILE'S. mobile/pairing.go owns the state alphabet and
 * android/gate/pairingstates_test.go compares the two, because they drifted silently once:
 * PB-PAIR-5's 2026-07-25 amendment retired `already-paired` and substituted
 * `different_machine`, the Go side followed it, and this enum did not -- so the state the
 * amendment created was the one state with no step, and the user got the generic
 * pairing-failed message for it. `rate_limited` and `failed` had never had a step either.
 */
enum class PairingStep {
    /** No attempt in progress. The scanner is offered. */
    SCAN,

    /** PB-PAIR-6: the destination is on screen and nothing has been joined. */
    CONFIRM_DESTINATION,

    /** The Noise handshake over the rendezvous. */
    HANDSHAKING,

    /** PB-SAS-3's screen: the six symbols are up and the person is comparing them. */
    COMPARING_CODES,

    /** The phone has answered; the machine has not yet confirmed. */
    AWAITING_MACHINE_DECISION,

    /** The one step in which anything is joined. */
    PAIRED,

    /** The confirmed destination was not the one displayed. */
    REFUSED_ORIGIN_MISMATCH,

    /** The user abandoned the attempt. Distinct from every refusal below. */
    CANCELLED,

    /** The machine's owner said no. */
    DECLINED,

    /** The two screens disagreed. A suspected interception, not a cancellation. */
    SAS_MISMATCH,

    /** Nobody arrived at the rendezvous before the deadline. */
    RENDEZVOUS_TIMEOUT,

    /** The code was scanned after it expired. */
    QR_EXPIRED,

    /**
     * The QR belonged to a machine OTHER than the one this phone is pinned to.
     *
     * PB-PAIR-5's fifth terminal state since the 2026-07-25 amendment, replacing the retired
     * `already-paired`. It is decided MID-HANDSHAKE, because PB-PAIR-7 kept the machine's Noise
     * static out of the QR and nothing before msg2 knows which machine a code belongs to.
     *
     * Nothing was joined and NOTHING WAS LOST: the guard exists because `pin()` used to assign
     * the machine identity unconditionally, so a phone paired to A that scanned B's code
     * silently re-pinned to B and abandoned A -- v1 is single-machine, so the user's first sign
     * of it was an empty roster.
     */
    DIFFERENT_MACHINE,

    /** The relay refused the attempt under its own per-source budget. Retryable, later. */
    RATE_LIMITED,

    /**
     * The handshake failed for a reason none of the states above name.
     *
     * It is the LAST resort and not a bucket: every condition the core can distinguish has its
     * own value above, and this one exists so that a failure the core could not classify still
     * lands on a step rather than falling through `stepOf` to the generic error router --
     * which is what happened to `different_machine` and `rate_limited` before this slice.
     */
    FAILED,
}

/**
 * PB-SAS-3's screen, which is a COMPARISON OF TWO DISPLAYS and never a form.
 *
 * The six-emoji alphabet was chosen because a human compares it. A field that collected the
 * symbols and compared them locally would move the comparison from the person -- who can see
 * the machine's screen -- to the phone, which sees one string and whatever an attacker
 * relayed. android/gate/s16_ui_test.go fences the absence of such a field in these sources;
 * mobile/conformance/s16_pairing_test.go fences that no facade verb would ingest one.
 *
 * @param code the whole display string `Pairing.SAS` returns, computed by the shared Go core
 *  (internal/remote/crypto/sas.go). The emoji table is never re-implemented in Kotlin.
 */
data class SasStep(val code: String) {

    /** The six symbols, split out only so the screen can lay them out. */
    val symbols: List<String> get() = code.trim().split(WHITESPACE)

    val instruction: String
        get() = "Check these six against the ones on your machine's screen."

    /** Always false, and it is the point of the type. */
    val acceptsTypedInput: Boolean = false

    val answers: Set<SasAnswer> get() = setOf(SasAnswer.MATCHES, SasAnswer.DOES_NOT_MATCH)

    private companion object {
        val WHITESPACE = Regex("\\s+")
    }
}

/**
 * One pairing attempt as the screen holds it.
 *
 * [joined] is derived from the step rather than stored, so there is no state in which a screen
 * can believe it joined something the step machine has not reached. Every refusal, every
 * timeout and every interrupted resume answers false by construction.
 */
data class PairingAttempt(
    val step: PairingStep,
    /** The destination shown to the user, and the only one [confirmDestination] will accept. */
    val originShown: String,
    val originIsLocalNetwork: Boolean,
    /** PB-PAIR-4: this attempt was restored from a persisted step, not started fresh. */
    val explainsInterruptedAttempt: Boolean,
) {
    val joined: Boolean get() = step == PairingStep.PAIRED

    val warnsAboutInterception: Boolean get() = step == PairingStep.SAS_MISMATCH

    /**
     * What the confirm sheet says about the destination. A private address is ALLOWED after
     * display and confirmation -- a blanket rule against them would reject the handset
     * demonstration PB-OPS-1 describes, a phone reaching the laptop over the LAN -- so it is
     * labelled instead of refused.
     */
    val destinationNotice: String
        get() = when {
            step != PairingStep.CONFIRM_DESTINATION -> ""
            originIsLocalNetwork -> "This is an address on your local network. Confirm it is " +
                "your own machine before joining."
            else -> "This phone will connect to this relay and to nothing else. Confirm it is " +
                "the address your machine showed you."
        }

    /**
     * PB-PAIR-6's second half: the confirmed destination is carried BACK, so a target swapped
     * after display is refused rather than merely unlikely.
     */
    fun confirmDestination(origin: String): PairingAttempt =
        if (origin == originShown) {
            copy(step = PairingStep.HANDSHAKING)
        } else {
            copy(step = PairingStep.REFUSED_ORIGIN_MISMATCH)
        }
}

/** The pairing screen's policy: permissions, the fallback, the steps and their wording. */
object PairingFlow {

    /**
     * The resolution itself is [dev.swarm.phone.runtime.PermissionStateResolver]'s, which is
     * already shipped and already tested under PB-RUN-2. A second copy of the
     * fresh-install-versus-permanently-denied rule is a second place to get it wrong, and
     * getting it wrong sends a user with a fresh install to a Settings screen where nothing is
     * wrong.
     */
    fun scannerState(permission: PermissionState): ScannerState = when (permission) {
        PermissionState.GRANTED -> ScannerState.SCANNING
        PermissionState.DENIED -> ScannerState.PERMISSION_DENIED
        PermissionState.PERMANENTLY_DENIED -> ScannerState.PERMISSION_PERMANENTLY_DENIED
        // CAMERA exists at every supported API level, so this answer belongs to
        // POST_NOTIFICATIONS below API 33 and cannot describe the scanner. Loud rather than
        // folded into a denial: a scanner screen that silently claimed the camera was denied
        // would send the user to fix a permission nobody withheld.
        PermissionState.NOT_APPLICABLE -> error(
            "PB-PAIR-2: CAMERA is never NOT_APPLICABLE; that state is POST_NOTIFICATIONS' below API 33",
        )
    }

    /**
     * PB-PAIR-2's first clause: the permission is REQUESTED. Offered wherever the camera is still
     * ASKABLE -- which is not the same question as whether this app already holds it.
     *
     * A PERMISSION NOBODY HAS ASKED FOR RESOLVES TO [ScannerState.PERMISSION_DENIED], because
     * [dev.swarm.phone.runtime.PermissionStateResolver] answers `!hasAskedBefore -> DENIED` and
     * that row is deliberate: `shouldShowRequestPermissionRationale` is false before the first ask
     * as well as after a permanent one. So a fresh install arrives at this screen DENIED, and the
     * app's ONLY `requestPermissions(CAMERA)` call is the scan control's own click listener
     * (`PairingSurface.beginScanning`, which already branches correctly on both denials).
     *
     * Answering this on GRANTED alone was therefore a closed loop: no permission, so no control;
     * no control, so nothing could ask; nothing asked, so no permission. The owner's handset
     * showed a paste field and no camera on the internal-testing build, and no sequence of taps
     * could have produced one (agents-tracker-qx9m).
     *
     * A PERMANENT DENIAL IS THE ONE STATE THAT WITHDRAWS IT, and it must stay withdrawn: Android
     * will not show the prompt again, so the control could only fail silently and leave the user
     * pressing a button that does nothing. [routesToSystemSettings] answers that state with the
     * only route that can undo it.
     */
    fun offersScanner(state: ScannerState): Boolean =
        state != ScannerState.PERMISSION_PERMANENTLY_DENIED

    /** PB-PAIR-2: a denied camera must not be a dead end. */
    fun offersManualEntry(state: ScannerState): Boolean = state != ScannerState.SCANNING

    /** Only a permanent denial. An ordinary one is re-askable, and Settings is a detour. */
    fun routesToSystemSettings(state: ScannerState): Boolean =
        state == ScannerState.PERMISSION_PERMANENTLY_DENIED

    /**
     * The manual fallback takes the SAME payload the code carries, so it reaches the same
     * DecodeQR and the same display-and-confirm step.
     */
    val manualEntryIsQrPayload: Boolean = true

    /**
     * An improvised "type the relay URL and a code" form would be a SECOND WIRE ENCODING, and
     * it would bypass PB-PAIR-6 by arriving as fields the user typed rather than as a
     * destination the phone must show them.
     */
    val manualEntryAcceptsSeparateFields: Boolean = false

    /**
     * The first step after a scan is confirming the destination -- never the handshake.
     *
     * mobile/pairing.go dialled on its second statement, so a code naming an attacker's relay
     * had the handset's connection (its address, the fact that it holds a pairing secret, and
     * the timing) before the user had seen the URL.
     *
     * @param qr the scanned or typed payload. Validated rather than stored: an empty payload
     *  cannot begin a pairing, and the payload itself is the phone core's to decode.
     */
    fun begin(qr: String, origin: String, originIsPrivate: Boolean): PairingAttempt {
        require(qr.isNotBlank()) { "PB-PAIR-2: a pairing cannot begin from an empty payload" }
        return PairingAttempt(
            step = PairingStep.CONFIRM_DESTINATION,
            originShown = origin,
            originIsLocalNetwork = originIsPrivate,
            explainsInterruptedAttempt = false,
        )
    }

    /**
     * Where the comparison's answer takes the attempt.
     *
     * "They match" is not a terminal answer and this returns the step the flow actually moves
     * to rather than inventing a success: the machine still has to confirm, and a screen that
     * showed the pairing as done would be reporting a decision nobody has made.
     */
    fun terminal(answer: SasAnswer): PairingAttempt = when (answer) {
        SasAnswer.MATCHES -> stepOnly(PairingStep.AWAITING_MACHINE_DECISION)
        SasAnswer.DOES_NOT_MATCH -> stepOnly(PairingStep.SAS_MISMATCH)
    }

    /**
     * PB-PAIR-4: a relaunch mid-pairing resumes the recorded step.
     *
     * Offering a fresh scanner here is the failure: the machine may have committed, and
     * BeginPairing fail-fasts while a device is registered (PB-STATE-10), so the scan would
     * lead to a refusal the user cannot resolve from the handset.
     */
    fun restore(persistedStep: PairingStep?): PairingAttempt {
        val step = persistedStep ?: PairingStep.SCAN
        return PairingAttempt(
            step = step,
            originShown = "",
            originIsLocalNetwork = false,
            explainsInterruptedAttempt = step != PairingStep.SCAN,
        )
    }

    /** True where the attempt has stopped and the screen owes the user an explanation. */
    fun isTerminal(step: PairingStep): Boolean = when (step) {
        PairingStep.SCAN,
        PairingStep.CONFIRM_DESTINATION,
        PairingStep.HANDSHAKING,
        PairingStep.COMPARING_CODES,
        PairingStep.AWAITING_MACHINE_DECISION,
        -> false

        PairingStep.PAIRED,
        PairingStep.REFUSED_ORIGIN_MISMATCH,
        PairingStep.CANCELLED,
        PairingStep.DECLINED,
        PairingStep.SAS_MISMATCH,
        PairingStep.RENDEZVOUS_TIMEOUT,
        PairingStep.QR_EXPIRED,
        PairingStep.DIFFERENT_MACHINE,
        PairingStep.RATE_LIMITED,
        PairingStep.FAILED,
        -> true
    }

    /**
     * Every step reads differently, because two states that read identically are one state and
     * the user's next move differs between them.
     */
    fun messageFor(step: PairingStep): String = when (step) {
        PairingStep.SCAN ->
            "Scan the code your machine is showing."

        PairingStep.CONFIRM_DESTINATION ->
            "Check the destination below before this phone joins anything."

        PairingStep.HANDSHAKING ->
            "Reaching your machine."

        PairingStep.COMPARING_CODES ->
            "Compare the six symbols with the ones on your machine."

        PairingStep.AWAITING_MACHINE_DECISION ->
            "Waiting for your machine to confirm this device."

        PairingStep.PAIRED ->
            "This phone is paired with your machine."

        PairingStep.REFUSED_ORIGIN_MISMATCH ->
            "The destination changed after it was shown to you, so nothing was joined. Scan " +
                "the code again."

        PairingStep.CANCELLED ->
            "You stopped this pairing. Nothing was joined."

        PairingStep.DECLINED ->
            "Your machine declined this device. Approve it there, then pair again."

        PairingStep.SAS_MISMATCH ->
            "The symbols did not match, so someone may be sitting between this phone and your " +
                "machine. Nothing was joined. Pair again on a network you trust."

        PairingStep.RENDEZVOUS_TIMEOUT ->
            "Your machine did not answer in time. Check it is awake and online, then pair again."

        PairingStep.QR_EXPIRED ->
            "That code has expired. Ask your machine for a new one."

        // It names the CAUSE and then says nothing changed, in that order. Both halves are
        // load-bearing: retrying the same code fails the same way, and the defect this state
        // closes used to abandon the pairing the user already had -- so a message that does not
        // say the old one is intact leaves them believing it happened.
        PairingStep.DIFFERENT_MACHINE ->
            "That code belongs to a different machine from the one this phone is paired with. " +
                "Nothing changed, and your existing pairing is intact. Scan the code from your " +
                "own machine."

        PairingStep.RATE_LIMITED ->
            "Too many pairing attempts from here. Wait a minute, then ask your machine for a " +
                "new code and try again."

        PairingStep.FAILED ->
            "The pairing did not finish and nothing was joined. Ask your machine for a new " +
                "code and try again."
    }

    private fun stepOnly(step: PairingStep) = PairingAttempt(
        step = step,
        originShown = "",
        originIsLocalNetwork = false,
        explainsInterruptedAttempt = false,
    )
}
