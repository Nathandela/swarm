package dev.swarm.phone

import android.Manifest
import android.content.Context
import android.content.Intent
import android.content.pm.PackageManager
import android.graphics.Typeface
import android.net.Uri
import android.os.Build
import android.os.Handler
import android.os.Looper
import android.provider.Settings
import android.view.View
import android.view.ViewGroup
import android.widget.Button
import android.widget.EditText
import android.widget.LinearLayout
import android.widget.TextView
import androidx.appcompat.app.AppCompatActivity
import dev.swarm.phone.runtime.AppPermission
import dev.swarm.phone.runtime.PermissionStateResolver
import dev.swarm.phone.scan.QrScanner
import dev.swarm.phone.ui.ErrorRouter
import dev.swarm.phone.ui.PairingAttempt
import dev.swarm.phone.ui.PairingFlow
import dev.swarm.phone.ui.PairingStep
import dev.swarm.phone.ui.SasAnswer
import dev.swarm.phone.ui.SasStep
import dev.swarm.phone.ui.ScannerState
import dev.swarm.phone.ui.SwarmErrorTokens
import swarmmobile.Pairing

/**
 * Phase B slice S19 -- the pairing screen PB-E2E-2 needs and the APK did not have.
 *
 * WHAT WAS MISSING AND WHY IT MATTERED. S18 shipped an Activity whose scope was bounded to
 * "enough Window and View to carry [PB-SEC-4's] assertions and S19's smoke", and what landed was
 * three buttons plus a line of text rendering [PairingFlow]'s SCAN message -- no scanner, no
 * destination confirmation, no SAS display and no confirm control. Four of PB-E2E-2's five
 * in-app actions therefore had no subject: the Go chain could pair and the app a user installs
 * could not.
 *
 * IT WIRES, IT DOES NOT REIMPLEMENT. Every string on screen comes from [PairingFlow], [SasStep]
 * or [ErrorRouter]; every security decision is made by the shared Go core reached through
 * `swarmmobile.Pairing`. This file's whole content is which control is on screen in which step,
 * and which verb a press reaches.
 *
 * THE ORDER IS PB-PAIR-6'S AND IT IS THE POINT. `App.BeginPairing` decodes and STOPS -- it dials
 * nothing -- and `Pairing.ConfirmOrigin` is the only thing that joins a destination, because a
 * connection is already the whole disclosure: the relay learns the handset's address, that it
 * holds a pairing QR, and when it was scanned, and refusing afterwards does not take that back.
 * So the confirm control below carries THE ORIGIN THE SCREEN RENDERED back to both halves --
 * [PairingAttempt.confirmDestination] on this side and `ConfirmOrigin` on the Go side -- which
 * is what makes a destination swapped after display a refusal rather than a race.
 *
 * THE SAS IS DISPLAYED AND NEVER COLLECTED (PB-SAS-3). Two answer buttons and no field: the six
 * symbols are compared by the person who can see both screens. "They do not match" reaches
 * `RejectSAS` and not `Cancel`, because a mismatch is the only signal this protocol has for a
 * man-in-the-middle and recording it as "I changed my mind" invites the user to try again
 * against the same attacker.
 *
 * IT IS NOT THE ACTIVITY. [PhoneActivity] is exported with a LAUNCHER filter, so PB-SEC-11 keeps
 * every facade verb one call away from it, behind a control a person pressed.
 *
 * PB-E2E-5 STAYS DEFERRED. Nothing here is evidence about a physical handset: no real camera, no
 * biometric, no attested key. An emulator is not a handset.
 */
class PairingSurface(
    private val activity: AppCompatActivity,
    private val runtime: PhoneRuntime,
) {

    private val message = label(bold = true)
    private val notice = label()
    private val destination = label().apply { typeface = Typeface.MONOSPACE }
    private val outcome = label()

    /**
     * The six symbols. Named for what it is -- a DISPLAY -- because the one thing this screen
     * must never grow is a field beside it; android/gate/s16_ui_test.go fences the shape and
     * mobile/conformance/s16_pairing_test.go fences that no verb would ingest one.
     */
    private val sasDisplay = label(bold = true).apply { textSize = SAS_TEXT_SP }
    private val sasInstruction = label()

    private val scannerHost = LinearLayout(activity).apply {
        orientation = LinearLayout.VERTICAL
        layoutParams = LinearLayout.LayoutParams(MATCH, SCANNER_HEIGHT)
    }

    private val startScan = gatedButton("Scan the code on your machine") { beginScanning() }

    /**
     * PB-PAIR-2's fallback, and it takes the SAME payload the QR carries -- never a relay URL
     * and a code as separate fields ([PairingFlow.manualEntryAcceptsSeparateFields] is false).
     * Separate fields would be a second wire encoding, and they would arrive as something the
     * user asserted rather than as a destination the phone must show them.
     */
    private val typedPayload = EditText(activity).apply {
        hint = "Paste the pairing code your machine printed"
        layoutParams = LinearLayout.LayoutParams(MATCH, WRAP)
    }

    private val useTypedPayload = gatedButton("Use this code") {
        acceptScannedPayload(typedPayload.text.toString().trim())
    }

    private val openSystemSettings = gatedButton("Open this app's settings") {
        activity.startActivity(
            Intent(Settings.ACTION_APPLICATION_DETAILS_SETTINGS)
                .setData(Uri.fromParts("package", activity.packageName, null)),
        )
    }

    private val confirmDestination = gatedButton("Join this destination") { confirmTheShownOrigin() }

    private val stopPairing = gatedButton("Stop pairing") { cancelAttempt() }

    private val codesMatch = gatedButton("They match") { answerSas(SasAnswer.MATCHES) }

    private val codesDoNotMatch = gatedButton("They do not match") {
        answerSas(SasAnswer.DOES_NOT_MATCH)
    }

    /** The in-flight attempt as the SCREEN holds it (S16's model). */
    private var attempt: PairingAttempt = PairingFlow.restore(null)

    /** The in-flight attempt as the CORE holds it. Null until a QR has been decoded. */
    private var handle: Pairing? = null

    /** Set once the phone has been rebuilt for a completed pairing; see [renderReady]. */
    private var rebuilt = false

    /**
     * Built on the first scan rather than in the constructor. CameraX allocates a camera
     * provider and a preview implementation the moment it exists, and this screen is created on
     * every launch whether or not anyone is pairing.
     */
    private var scanner: QrScanner? = null

    private val poller = Handler(Looper.getMainLooper())

    val root: View = LinearLayout(activity).apply {
        orientation = LinearLayout.VERTICAL
        layoutParams = ViewGroup.LayoutParams(MATCH, WRAP)
        for (child in listOf(
            message, notice, scannerHost, startScan, typedPayload, useTypedPayload,
            openSystemSettings, destination, confirmDestination, sasDisplay, sasInstruction,
            codesMatch, codesDoNotMatch, stopPairing, outcome,
        )) {
            addView(child)
        }
    }

    /** PB-SEC-12 clause 1: every control here authorises something. */
    val gatedActions: List<View> = listOf(
        startScan, useTypedPayload, openSystemSettings, confirmDestination,
        codesMatch, codesDoNotMatch, stopPairing,
    )

    /**
     * Draw the step the attempt is actually in.
     *
     * The step is re-derived from the CORE on every draw rather than remembered from the last
     * button press: the handshake runs on a Go goroutine, so every interesting transition
     * happens while nothing on this side is executing.
     */
    fun render() {
        when (val startup = runtime.phone()) {
            is PhoneStartup.Unavailable -> renderUnavailable()
            is PhoneStartup.Ready -> renderReady(startup)
        }
    }

    /** Release the camera and stop polling. Called from the Activity's pause. */
    fun release() {
        scanner?.stop()
        poller.removeCallbacksAndMessages(null)
    }

    /**
     * The seam a payload enters this screen through -- from [QrScanner] and from the typed
     * fallback alike, so both reach the same display-then-confirm step.
     *
     * It is public because [QrScanner] is in another package and calls it. What it is NOT is an
     * intent surface: nothing reads a payload off an Intent and [PhoneActivity] never calls it.
     */
    fun acceptScannedPayload(payload: String) {
        val app = (runtime.phone() as? PhoneStartup.Ready)?.app ?: return
        if (payload.isBlank()) {
            // PairingFlow.begin throws on an empty payload and that throw carries no error
            // class, so it would not route. An empty field is a user slip, not a classification.
            outcome.text = "Paste the code your machine printed, then press Use this code."
            return
        }
        stopScanning()
        try {
            // DECODES AND STOPS. Nothing is dialled here; see the class comment.
            val started = app.beginPairing(payload)
            handle = started
            attempt = PairingFlow.begin(payload, started.origin(), started.originIsPrivate())
            outcome.text = ""
        } catch (refused: Exception) {
            handle = null
            outcome.text = routed(refused)
        }
        render()
    }

    // -----------------------------------------------------------------------
    // The four controls PB-E2E-2 names.
    // -----------------------------------------------------------------------

    private fun beginScanning() {
        val state = scannerState()
        if (state != ScannerState.SCANNING) {
            // DENIED is re-askable and PERMANENTLY_DENIED is not; PermissionStateResolver
            // already made that call and this screen must not make it a second time.
            if (state == ScannerState.PERMISSION_DENIED) {
                rememberTheAsk()
                activity.requestPermissions(arrayOf(AppPermission.CAMERA.manifestName), CAMERA_ASK)
            }
            render()
            return
        }
        val live = scanner ?: QrScanner(activity).also {
            scanner = it
            scannerHost.addView(it.view, LinearLayout.LayoutParams(MATCH, MATCH))
        }
        live.view.visibility = View.VISIBLE
        scannerHost.visibility = View.VISIBLE
        live.start { payload -> acceptScannedPayload(payload) }
    }

    private fun stopScanning() {
        scanner?.stop()
        scannerHost.visibility = View.GONE
    }

    /**
     * PB-PAIR-6's confirmation, carrying back THE STRING THE SCREEN RENDERED.
     *
     * Reading it off the view rather than off the model is deliberate: the two comparisons that
     * follow are then both against what the user actually read.
     * [PairingAttempt.confirmDestination] checks it against the origin the attempt was built
     * with, and `ConfirmOrigin` checks it against the payload the Go core decoded. Passing
     * `attempt.originShown` to the first would make it compare a value with itself.
     */
    private fun confirmTheShownOrigin() {
        val live = handle ?: return
        val shown = destination.text.toString()

        attempt = attempt.confirmDestination(shown)
        if (attempt.step == PairingStep.REFUSED_ORIGIN_MISMATCH) {
            render()
            return
        }
        try {
            live.confirmOrigin(shown)
            // The QR's relay is where this phone lives from now on. The handshake dials the URL
            // it decoded; this is what the NEXT App construction dials, and without it a handset
            // that restarts after pairing comes back with nowhere to connect to and nothing on
            // screen saying why.
            runtime.rememberRelay(shown)
            outcome.text = ""
        } catch (refused: Exception) {
            outcome.text = routed(refused)
        }
        render()
    }

    private fun answerSas(answer: SasAnswer) {
        val live = handle ?: return
        try {
            when (answer) {
                SasAnswer.MATCHES -> live.confirm()
                // NOT Cancel. See the class comment.
                SasAnswer.DOES_NOT_MATCH -> live.rejectSAS()
            }
            // The step the flow moves to, from S16's own table, so the screen does not spend a
            // poll interval still showing the comparison the user has answered. The next draw
            // re-derives it from the core regardless.
            attempt = PairingFlow.terminal(answer)
            outcome.text = ""
        } catch (refused: Exception) {
            outcome.text = routed(refused)
        }
        render()
    }

    private fun cancelAttempt() {
        try {
            handle?.cancel()
        } catch (refused: Exception) {
            outcome.text = routed(refused)
        }
        handle = null
        attempt = PairingFlow.restore(null)
        stopScanning()
        render()
    }

    // -----------------------------------------------------------------------
    // Drawing.
    // -----------------------------------------------------------------------

    private fun renderUnavailable() {
        // PhoneSurface renders the routed startup failure; a second copy here would be the same
        // sentence twice. What this panel owes is to offer nothing it cannot perform.
        message.text = ""
        notice.text = ""
        destination.text = ""
        sasDisplay.text = ""
        sasInstruction.text = ""
        for (view in root.children()) show(view, false)
        stopScanning()
    }

    private fun renderReady(startup: PhoneStartup.Ready) {
        val live = handle
        val sas = live?.let { sasOrNull(it) }
        val state = if (live == null) persistedState(startup) else stateOf(live)
        val step = stepOf(state, sasKnown = sas != null)

        if (step == null && state.isNotEmpty()) {
            // A terminal state S16's PairingStep does not enumerate -- see stepOf.
            outcome.text = routedStateMessage(state)
            handle = null
        }
        attempt = when {
            live != null -> attempt.copy(step = step ?: attempt.step)
            // A COMPLETED pairing leaves NO record -- mobile/pairing.go clears it on `paired`
            // because after a success the phone simply is paired -- so PairingFlow.restore would
            // answer SCAN and tell a paired handset to go and scan a code. The pinned machine is
            // what says otherwise, and it is a fact only the durable state carries.
            //
            // Constructed rather than restored: restore() raises explainsInterruptedAttempt for
            // every step but SCAN, and nothing about a finished pairing was interrupted.
            step == null && isPinned(startup) -> PairingAttempt(
                step = PairingStep.PAIRED,
                originShown = "",
                originIsLocalNetwork = false,
                explainsInterruptedAttempt = false,
            )

            else -> PairingFlow.restore(step)
        }

        val current = attempt.step
        val holding = handle != null
        message.text = PairingFlow.messageFor(current)

        renderScanStep(holding)
        renderDestinationStep(current, holding)
        renderSasStep(current, sas, holding)

        show(stopPairing, holding && !PairingFlow.isTerminal(current))
        show(outcome, outcome.text.isNotEmpty())

        if (holding && !PairingFlow.isTerminal(current)) {
            poller.removeCallbacksAndMessages(null)
            poller.postDelayed({ render() }, POLL_MILLIS)
        } else if (PairingFlow.isTerminal(current)) {
            handle = null
            stopScanning()
        }
        if (current == PairingStep.PAIRED && !rebuilt) {
            // ONCE, and only on success. The App that ran this pairing was built before its
            // relay URL was known; see PhoneRuntime.rebuildAfterPairing for why a phone that
            // skipped this pairs and then never connects.
            rebuilt = true
            runtime.rebuildAfterPairing()
        }
    }

    /**
     * The scanner is offered exactly when there is no live attempt in THIS process.
     *
     * PB-PAIR-4's resumed step is the interesting case. A relaunch mid-handshake comes back
     * with the recorded step and no handle -- the goroutine died with the process -- so the
     * screen says what was interrupted ([PairingFlow.messageFor] plus the notice below) and
     * offers the one action that can still be taken. Offering NOTHING would be the dead end:
     * `Pairing` is a process-local handle, so there is no verb left to resolve the recorded
     * attempt, and a fresh `BeginPairing` is what overwrites the record -- succeeding, or
     * failing legibly through PB-APP-9 if the machine has this device registered (PB-STATE-10).
     */
    private fun renderScanStep(holding: Boolean) {
        val scanning = !holding
        val state = if (scanning) scannerState() else ScannerState.SCANNING
        notice.text = if (scanning && attempt.explainsInterruptedAttempt) {
            "This pairing was interrupted before it finished. Nothing was joined."
        } else {
            ""
        }
        show(notice, notice.text.isNotEmpty())
        show(startScan, scanning && state == ScannerState.SCANNING)
        show(scannerHost, scanning && scannerHost.visibility == View.VISIBLE)
        // PB-PAIR-2: a denied camera must not be a dead end, and only a PERMANENT denial is a
        // trip to system settings -- an ordinary one is re-askable and Settings is a detour.
        show(typedPayload, scanning && PairingFlow.offersManualEntry(state))
        show(useTypedPayload, scanning && PairingFlow.offersManualEntry(state))
        show(openSystemSettings, scanning && PairingFlow.routesToSystemSettings(state))
    }

    private fun renderDestinationStep(step: PairingStep, holding: Boolean) {
        val confirming = holding && step == PairingStep.CONFIRM_DESTINATION
        if (confirming) {
            destination.text = attempt.originShown
            notice.text = attempt.destinationNotice
        }
        show(destination, confirming)
        show(notice, notice.text.isNotEmpty())
        show(confirmDestination, confirming)
    }

    private fun renderSasStep(step: PairingStep, sas: String?, holding: Boolean) {
        val comparing = holding && step == PairingStep.COMPARING_CODES && sas != null
        if (comparing) {
            val code = SasStep(checkNotNull(sas))
            sasDisplay.text = code.symbols.joinToString("   ")
            sasInstruction.text = code.instruction
        } else {
            sasDisplay.text = ""
            sasInstruction.text = ""
        }
        show(sasDisplay, comparing)
        show(sasInstruction, comparing)
        show(codesMatch, comparing)
        show(codesDoNotMatch, comparing)
    }

    // -----------------------------------------------------------------------
    // The core's state, read rather than remembered.
    // -----------------------------------------------------------------------

    private fun stateOf(live: Pairing): String = try {
        live.state()
    } catch (gone: Exception) {
        ""
    }

    private fun persistedState(startup: PhoneStartup.Ready): String = try {
        startup.app.pairingState()
    } catch (unreadable: Exception) {
        ""
    }

    /**
     * Whether a machine is pinned -- the durable fact a completed pairing leaves behind, and the
     * only thing that distinguishes a paired phone from one that has never paired once the
     * attempt record is cleared.
     */
    private fun isPinned(startup: PhoneStartup.Ready): Boolean = try {
        startup.app.stateSummary().machine.isNotEmpty()
    } catch (unreadable: Exception) {
        false
    }

    /** The SAS, or null while the handshake has not derived one. Erroring IS the "not yet". */
    private fun sasOrNull(live: Pairing): String? = try {
        live.sas().takeIf { it.isNotBlank() }
    } catch (notYet: Exception) {
        null
    }

    /**
     * `swarmmobile`'s pairing state as one of S16's [PairingStep]s.
     *
     * THREE OF THE CORE'S TERMINAL STATES HAVE NO STEP, and that is reported rather than papered
     * over: `different_machine` (the QR belongs to a machine this phone is not pinned to),
     * `rate_limited` and `failed` are terminal in mobile/pairing.go and [PairingStep] enumerates
     * none of them. They answer null here and [routedStateMessage] shows the routed CLASS
     * message instead -- true and general, rather than a fourth wording invented at this seam.
     * Closing the gap properly means adding the constants to S16's model, which is that slice's
     * decision and not this file's.
     */
    private fun stepOf(state: String, sasKnown: Boolean): PairingStep? = when (state) {
        "confirm_destination" -> PairingStep.CONFIRM_DESTINATION
        // The core stays in `pairing` from the dial until the user answers, so the SAS is what
        // says whether there is anything to compare yet.
        "pairing" -> if (sasKnown) PairingStep.COMPARING_CODES else PairingStep.HANDSHAKING
        "confirming" -> PairingStep.AWAITING_MACHINE_DECISION
        "paired" -> PairingStep.PAIRED
        "declined" -> PairingStep.DECLINED
        "sas_mismatch" -> PairingStep.SAS_MISMATCH
        "rendezvous_timeout" -> PairingStep.RENDEZVOUS_TIMEOUT
        "expired" -> PairingStep.QR_EXPIRED
        "cancelled" -> PairingStep.CANCELLED
        "refused_origin_mismatch" -> PairingStep.REFUSED_ORIGIN_MISMATCH
        else -> null
    }

    private fun routedStateMessage(state: String): String = ErrorRouter.route(
        if (state == "rate_limited") SwarmErrorTokens.RATE_LIMITED else SwarmErrorTokens.PAIRING_FAILED,
    ).message

    // -----------------------------------------------------------------------
    // Permission, and the one bit the platform will not keep.
    // -----------------------------------------------------------------------

    /**
     * PB-RUN-2's resolution, with the persisted "have we asked" bit the platform does not offer.
     *
     * `shouldShowRequestPermissionRationale` is false BEFORE the first ask as well as after a
     * permanent denial, so reading it alone reports PERMANENTLY_DENIED on a fresh install and
     * sends a user with nothing wrong to a Settings screen.
     */
    private fun scannerState(): ScannerState = PairingFlow.scannerState(
        PermissionStateResolver.resolve(
            permission = AppPermission.CAMERA,
            sdkInt = Build.VERSION.SDK_INT,
            granted = activity.checkSelfPermission(Manifest.permission.CAMERA) ==
                PackageManager.PERMISSION_GRANTED,
            hasAskedBefore = asks().getBoolean(ASKED_CAMERA, false),
            showRationale = activity.shouldShowRequestPermissionRationale(Manifest.permission.CAMERA),
        ),
    )

    private fun rememberTheAsk() {
        asks().edit().putBoolean(ASKED_CAMERA, true).apply()
    }

    /**
     * The bit lives in the app's own preferences, under the data root
     * `res/xml/data_extraction_rules.xml` excludes from both cloud backup and device-to-device
     * transfer (PB-SEC-10). It is a UX coordinate and carries nothing else -- no payload, no
     * origin, no key material.
     */
    private fun asks() = activity.getSharedPreferences(ASK_STORE, Context.MODE_PRIVATE)

    // -----------------------------------------------------------------------

    private fun routed(failure: Exception) = ErrorRouter.route(failure.message.orEmpty()).message

    private fun View.children(): List<View> =
        if (this is ViewGroup) (0 until childCount).map { getChildAt(it) } else emptyList()

    private fun show(view: View, visible: Boolean) {
        view.visibility = if (visible) View.VISIBLE else View.GONE
    }

    private fun gatedButton(text: String, onPress: () -> Unit): Button =
        SecureWindow.gate(
            Button(activity).apply {
                this.text = text
                setOnClickListener { onPress() }
            },
        )

    private fun label(bold: Boolean = false) = TextView(activity).apply {
        if (bold) setTypeface(typeface, Typeface.BOLD)
        layoutParams = LinearLayout.LayoutParams(MATCH, WRAP)
    }

    private companion object {
        const val MATCH = ViewGroup.LayoutParams.MATCH_PARENT
        const val WRAP = ViewGroup.LayoutParams.WRAP_CONTENT
        const val SCANNER_HEIGHT = 720
        const val SAS_TEXT_SP = 28f
        const val POLL_MILLIS = 400L
        const val CAMERA_ASK = 1
        const val ASK_STORE = "swarm-permission-asks"
        const val ASKED_CAMERA = "asked-camera"
    }
}
