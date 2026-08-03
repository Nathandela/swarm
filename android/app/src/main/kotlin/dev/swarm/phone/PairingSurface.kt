package dev.swarm.phone

import android.Manifest
import android.content.Context
import android.content.Intent
import android.content.pm.PackageManager
import android.net.Uri
import android.os.Build
import android.os.Handler
import android.os.Looper
import android.provider.Settings
import android.view.View
import android.view.ViewGroup
import android.widget.Button
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
import dev.swarm.phone.ui.kit.monoWell
import dev.swarm.phone.ui.kit.textField
import dev.swarm.phone.ui.screens.PairingControl
import dev.swarm.phone.ui.screens.PairingPanel
import dev.swarm.phone.ui.screens.PairingPanelScreen
import dev.swarm.phone.ui.screens.PairingSlots
import dev.swarm.phone.ui.screens.pairingPanelView
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
 * AND IT IS NOW THE ONLY HUMAN-IN-THE-LOOP SECURITY STEP IN THE PRODUCT (ADR-007 B133). Until
 * that entry a person passed two checkpoints: this comparison at pairing, and a biometric at
 * every privileged action afterwards. The second is gone, so this one is load-bearing alone --
 * it is what defeats a relay MITM and there is nothing behind it. It must therefore get HARDER
 * to skip, never easier: no auto-confirm, no timeout that accepts, no "looks close enough"
 * affordance, and no path that reaches PAIRED without a human having answered these two
 * buttons. A change that shortens this step is a change to the security posture of the whole
 * product and needs an ADR entry of its own.
 *
 * THIS PANEL MUST BE REACHABLE ON AN UNPAIRED HANDSET, which B132 found it was not. Every draw
 * is downstream of [PhoneRuntime.phone], and the runtime used to refuse construction outright on
 * a handset with no enrolled Class-3 biometric -- so [renderUnavailable] hid every control here
 * and the app offered no way to pair at all. The refusal is gone with the gate that needed it;
 * this comment records the coupling so the next person to add a startup precondition knows it
 * takes the pairing flow down with it.
 *
 * IT IS NOT THE ACTIVITY. [PhoneActivity] is exported with a LAUNCHER filter, so PB-SEC-11 keeps
 * every facade verb one call away from it, behind a control a person pressed.
 *
 * PB-E2E-5 STAYS DEFERRED. Nothing here is evidence about a physical handset: no real camera and
 * no attested key. An emulator is not a handset.
 */
class PairingSurface(
    private val activity: AppCompatActivity,
    private val runtime: PhoneRuntime,
) {

    private val message = label()
    private val notice = label()

    /**
     * The destination the user is being asked to join, in the design's code face.
     *
     * PB-DS-11: it was `typeface = Typeface.MONOSPACE`, then `Mono.Code` applied here. It is now
     * the KIT'S mono well -- derivation row 18's own instruction is that the pairing command line
     * "reuses the `.cmd` mono well verbatim ... so every mono block in the app is one component",
     * and this is that component. It brings the recessed `--p-well` fill and the hairline with the
     * face. A relay URL is exactly the string a proportional face makes hard to compare character
     * by character, which is what this step asks a person to do.
     */
    private val destination = monoWell(activity, "")

    private val outcome = label()

    /**
     * The six symbols. Named for what it is -- a DISPLAY -- because the one thing this screen
     * must never grow is a field beside it; android/gate/s16_ui_test.go fences the shape and
     * mobile/conformance/s16_pairing_test.go fences that no verb would ingest one.
     *
     * PB-DS-11: it was `textSize = SAS_TEXT_SP` with `SAS_TEXT_SP = 28f`, a size chosen at a call
     * site. THE STYLE IT SHOULD TAKE DOES NOT EXIST: derivation row 7 specifies `Display.SAS` at
     * 34 sp and §7 calls it "the one style this document adds to PB-DS-2's 18" --
     * res/values/type.xml carries the 18 and not the 19th. `Display.NavTitle` at 27 sp is the
     * largest style the scale has, so the raw literal is gone and the size is 7 sp under the
     * design's. **That is a recorded approximation and not a fix.**
     *
     * IT IS NOT "ONE ENTRY IN type.xml", WHICH IS WHAT THIS COMMENT USED TO SAY, and the
     * correction matters because the cheap-sounding version invites someone to try it and get
     * stuck. `android/gate/s22b_type_test.go` joins the two sides bidirectionally and BY COUNT:
     * it asserts the design source declares 18 text styles, that type.xml defines 18, that every
     * style names an `origin:` selector which IS a text style in the design source, and that no
     * design rule is left unclaimed. `.sas` is absent from the shared CSS block entirely -- it is
     * a derived addition, which is the whole of §7's point -- so a 19th style would fail on the
     * count from both directions AND on an origin that resolves to nothing. Making it pass means
     * teaching that join the difference between a transcribed rule and a derived one, which is
     * rebuilding the join. `docs/design/substrate-components.md:333` already records that the
     * gate "must fail until it exists" and does not; a round-3 finding says the same. Closing
     * this belongs with that finding, not with a screens slice.
     */
    private val sasDisplay = label().apply {
        setTextAppearance(R.style.TextAppearance_Swarm_Display_NavTitle)
    }

    private val sasInstruction = label()

    /**
     * The viewfinder, sized by a rule rather than by a number.
     *
     * PB-DS-11: it was `LayoutParams(MATCH, SCANNER_HEIGHT)` with `SCANNER_HEIGHT = 720` -- raw
     * PIXELS, so the preview was about 2.4 inches tall on a 3x handset and about 7 on a 1x one.
     * That is the same defect class as `PADDING = 24`, and a reviewer found that a raw number in a
     * layout param was invisible to every scan in this repository; `android/gate/s24_screens_test.go`
     * now reads them.
     *
     * A QR SYMBOL IS SQUARE, so the replacement is not another constant but the aspect the thing
     * being framed actually has: the viewfinder is as tall as it is wide. There is no design
     * source for a scanner -- inventory B20 marks `.qr` as having no Substrate spec, and it
     * describes the CODE tile shown during pairing rather than the camera preview shown while
     * scanning one.
     */
    private val scannerHost = object : LinearLayout(activity) {
        /** The height IS the width: the second spec is deliberately the first one. */
        override fun onMeasure(widthMeasureSpec: Int, heightMeasureSpec: Int) {
            super.onMeasure(widthMeasureSpec, widthMeasureSpec)
        }
    }.apply {
        orientation = LinearLayout.VERTICAL
        layoutParams = LinearLayout.LayoutParams(MATCH, WRAP)
        // HIDDEN UNTIL A SCAN STARTS. It was VISIBLE from construction, which is a View's
        // default, and the draw only ever asked it to STAY visible
        // (`scanning && scannerHost.visibility == View.VISIBLE`) -- so a freshly opened app drew
        // an empty viewfinder-sized hole with no camera behind it. Invisible while it was 720 raw
        // pixels at the bottom of a scrolling column; not invisible now that the pairing panel
        // hangs under a real screen.
        visibility = View.GONE
    }

    private val startScan = touchFilteredButton("Scan the code on your machine") { beginScanning() }

    /**
     * PB-PAIR-2's fallback, and it takes the SAME payload the QR carries -- never a relay URL
     * and a code as separate fields ([PairingFlow.manualEntryAcceptsSeparateFields] is false).
     * Separate fields would be a second wire encoding, and they would arrive as something the
     * user asserted rather than as a destination the phone must show them.
     */
    private val typedPayload = textField(activity, "Paste the pairing code your machine printed")

    private val useTypedPayload = touchFilteredButton("Use this code") {
        acceptScannedPayload(typedPayload.text.toString().trim())
    }

    private val openSystemSettings = touchFilteredButton("Open this app's settings") {
        activity.startActivity(
            Intent(Settings.ACTION_APPLICATION_DETAILS_SETTINGS)
                .setData(Uri.fromParts("package", activity.packageName, null)),
        )
    }

    private val confirmDestination = touchFilteredButton("Join this destination") { confirmTheShownOrigin() }

    private val stopPairing = touchFilteredButton("Stop pairing") { cancelAttempt() }

    private val codesMatch = touchFilteredButton("They match") { answerSas(SasAnswer.MATCHES) }

    private val codesDoNotMatch = touchFilteredButton("They do not match") {
        answerSas(SasAnswer.DOES_NOT_MATCH)
    }

    /** The in-flight attempt as the SCREEN holds it (S16's model). */
    private var attempt: PairingAttempt = PairingFlow.restore(null)

    /** The in-flight attempt as the CORE holds it. Null until a QR has been decoded. */
    private var handle: Pairing? = null

    /** Set once the phone has been rebuilt for a completed pairing; see [renderReady]. */
    private var rebuilt = false

    /**
     * Told, once, that this phone is now paired.
     *
     * IT PUSHES AND IS NOT POLLED, which is [PhoneSurface.onDrillDownChanged]'s reason and one this
     * screen makes sharper: the handshake finishes on a Go goroutine and reaches this surface
     * through its own poller, so the moment a phone becomes paired is a moment nothing else on the
     * screen is executing. An unpaired phone is shown [dev.swarm.phone.ui.screens.pairOnlyView] and
     * NOTHING else (agents-tracker-64rf) -- so without this the user who has just finished pairing
     * sits on the flow's last step, with the app they earned behind a screen that is no longer for
     * them, until they leave the app and come back.
     */
    internal var onPaired: () -> Unit = {}

    /**
     * Built on the first scan rather than in the constructor. CameraX allocates a camera
     * provider and a preview implementation the moment it exists, and this screen is created on
     * every launch whether or not anyone is pairing.
     */
    private var scanner: QrScanner? = null

    private val poller = Handler(Looper.getMainLooper())

    /**
     * The views inventory C7's scaffold is composed FROM, handed to
     * [dev.swarm.phone.ui.screens.pairingPanelView].
     *
     * They are built here rather than in the screen package because they must be: `SecureWindow`
     * applies PB-SEC-12 clause 1's touch filter at construction, the listeners are this file's own
     * verbs, and [touchFilteredActions] has to name the views that are actually on screen. The two
     * that carry a text appearance are here for a second reason -- the screen package is fenced
     * against `setTextAppearance`, so moving them would cost the destination its `Mono.Code` and
     * the symbols their size while derivation rows 18 and 7 are unbuilt.
     */
    private val slots = PairingSlots(
        body = message,
        notice = notice,
        destination = destination,
        sas = sasDisplay,
        sasInstruction = sasInstruction,
        scanner = scannerHost,
        controls = mapOf(
            PairingControl.SCAN to startScan,
            PairingControl.TYPED_PAYLOAD to typedPayload,
            PairingControl.USE_TYPED_PAYLOAD to useTypedPayload,
            PairingControl.OPEN_SYSTEM_SETTINGS to openSystemSettings,
            PairingControl.CONFIRM_DESTINATION to confirmDestination,
            PairingControl.CODES_MATCH to codesMatch,
            PairingControl.CODES_DO_NOT_MATCH to codesDoNotMatch,
            PairingControl.STOP to stopPairing,
        ),
    )

    /** What the panel last drew, so a redraw that changes nothing rebuilds nothing. */
    private var drawn: PairingPanel? = null

    /**
     * The panel is rebuilt into this whenever the step changes.
     *
     * IT WAS A FLAT COLUMN OF FIFTEEN VIEWS, all of them added once and each shown or hidden by
     * one of three `render*Step` functions. PB-DS-9 replaces that with the screen inventory C7
     * records: [PairingPanelScreen] decides which of the eight controls the step offers and this
     * holds whatever [pairingPanelView] composed from that.
     */
    private val host = LinearLayout(activity).apply {
        orientation = LinearLayout.VERTICAL
        layoutParams = ViewGroup.LayoutParams(MATCH, WRAP)
    }

    val root: View = host

    /** PB-SEC-12 clause 1: every control here authorises something. */
    val touchFilteredActions: List<View> = listOf(
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
        // sentence twice. What this panel owes is to offer nothing it cannot perform -- so it
        // draws NOTHING rather than a scaffold whose every control would refuse.
        drawn = null
        (outcome.parent as? ViewGroup)?.removeView(outcome)
        host.removeAllViews()
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

        draw(
            PairingPanelScreen.of(
                attempt = attempt,
                // The camera is asked ONLY while there is no live attempt. Asking it mid-handshake
                // would be a permission check about a control the step does not offer.
                scanner = if (holding) ScannerState.SCANNING else scannerState(),
                sas = sas?.let { SasStep(it) },
                holding = holding,
                machine = machineOf(startup),
            ),
        )
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
            // AFTER THE REBUILD AND NOT BEFORE IT. Whoever is told is going to ask the runtime for
            // a phone, and the one this pairing ran on is the one that has to be replaced first --
            // otherwise the app is redrawn against a core that pairs and then never connects, which
            // is the defect the rebuild above exists to prevent.
            onPaired()
        }
    }

    /**
     * Draw the step, and rebuild the view hierarchy only when what it shows has changed.
     *
     * IT REPLACED THREE FUNCTIONS THAT SET `View.visibility`. `renderScanStep`,
     * `renderDestinationStep` and `renderSasStep` each re-derived their own conditions and each
     * wrote the same `notice` view -- the second overwrote the first, harmlessly, because the two
     * conditions happen to be disjoint. Nothing checked that they were. [PairingPanelScreen] now
     * decides once, and `PairingPanelScreenTest` is where the decision is checked.
     *
     * PB-PAIR-4's resumed step is the interesting case and it still reads the same: a relaunch
     * mid-handshake comes back with the recorded step and no handle, so the panel says what was
     * interrupted and offers the one action that can still be taken. Offering NOTHING would be the
     * dead end -- `Pairing` is a process-local handle, so there is no verb left to resolve the
     * recorded attempt, and a fresh `BeginPairing` is what overwrites the record.
     */
    private fun draw(panel: PairingPanel) {
        message.text = panel.body
        notice.text = panel.notice
        destination.text = panel.destination
        // Three spaces, so the six symbols read as six things rather than one word. The
        // separator is the screen's, and the alphabet is the shared Go core's -- never Kotlin's.
        sasDisplay.text = panel.sas.joinToString("   ")
        sasInstruction.text = panel.sasInstruction
        // The preview closes with the step that offers it. It never OPENS here: a camera is
        // started by someone pressing Scan, not by a redraw.
        if (PairingControl.SCAN !in panel.controls) scannerHost.visibility = View.GONE

        if (panel == drawn && host.childCount > 0) return
        drawn = panel
        (outcome.parent as? ViewGroup)?.removeView(outcome)
        host.removeAllViews()
        host.addView(pairingPanelView(activity, panel, slots, outcome))
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
     * Whether this phone is paired -- the durable fact a completed pairing leaves behind, and what
     * distinguishes a paired phone from one that has never paired once the attempt record is
     * cleared.
     *
     * IT READS `paired` AND NOT THE PINNED MACHINE (agents-tracker-d0b8), and here that difference
     * is the way OUT rather than the way in. This is what makes [render] construct a `PAIRED`
     * attempt -- "you are already paired", no scan offered -- so a phone whose registration the
     * owner revoked would arrive from [dev.swarm.phone.ui.screens.PairOnlyScreen]'s one offer at a
     * panel telling it there is nothing to do. The machine endpoint id survives a revoke by design;
     * the pairing does not.
     */
    private fun isPinned(startup: PhoneStartup.Ready): Boolean = try {
        startup.app.stateSummary().paired
    } catch (unreadable: Exception) {
        false
    }

    /**
     * The machine this phone is pinned to, for the one heading inventory C7 gives a name to.
     *
     * Empty rather than a placeholder where the state cannot be read: [PairingPanelScreen] renders
     * `Paired` for an empty machine rather than the dangling `Paired with ` a naive interpolation
     * produces.
     */
    private fun machineOf(startup: PhoneStartup.Ready): String = try {
        startup.app.stateSummary().machine
    } catch (unreadable: Exception) {
        ""
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
     * EVERY STATE mobile/pairing.go CAN REPORT HAS AN ARM HERE, and that is PB-PAIR-5's
     * criterion rather than a tidiness rule: a state with no arm answers null, and the screen
     * then shows the ROUTED CLASS message -- one wording shared with every other pairing
     * failure, which is exactly the opaque error the requirement exists to remove.
     *
     * THREE STATES USED TO HAVE NO ARM. `different_machine` was the sharp one: PB-PAIR-5's
     * 2026-07-25 amendment retired `already-paired` and substituted it, the Go core followed,
     * and this file did not -- so the requirement's own new state was the one that fell
     * through. `rate_limited` and `failed` had never had one.
     * android/gate/pairingstates_test.go compares this table against the core's constants so
     * the two cannot drift again.
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
        "different_machine" -> PairingStep.DIFFERENT_MACHINE
        "rate_limited" -> PairingStep.RATE_LIMITED
        "failed" -> PairingStep.FAILED
        else -> null
    }

    /**
     * The fallback for a state THIS BUILD has never heard of -- a core newer than the app.
     *
     * It stays now that every known state has a step. A facade that starts reporting a new one
     * must land somewhere legible rather than leaving the screen on its last step forever, and
     * the routed class message is the honest thing to say about a condition this build cannot
     * name. The gate keeps the KNOWN set covered, so this arm is reached only by version skew.
     */
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

    private fun show(view: View, visible: Boolean) {
        view.visibility = if (visible) View.VISIBLE else View.GONE
    }

    /**
     * A control, with PB-SEC-12 clause 1's touch filter applied by construction.
     *
     * IT WAS `gatedButton`, AND THE NAME WAS THE ONLY THING THAT CHANGED (ADR-007 B133). There
     * was never a biometric behind these: [SecureWindow.gate] sets `filterTouchesWhenObscured`,
     * which discards a tap that arrived while another window covered the view. That defence
     * SURVIVES the de-auth and matters more than before, so what the rename removes is a word
     * that would have read as a checkpoint this screen no longer has.
     */
    private fun touchFilteredButton(text: String, onPress: () -> Unit): Button =
        SecureWindow.gate(
            Button(activity).apply {
                this.text = text
                setOnClickListener { onPress() }
            },
        )

    /**
     * PB-DS-11: a heading takes a TEXT APPEARANCE, never a typeface. The same two lines were in
     * all three surface files.
     *
     * THE HEADING IS NO LONGER THIS FILE'S. [dev.swarm.phone.ui.kit.navHeader] draws the step
     * title now, in `Display.NavTitle`, which is what derivation row 18 specifies for it. What
     * this factory still produces is body copy, and the kit has no component for that -- so the
     * `heading` parameter is gone with the heading and the rest render at the theme's default.
     */
    private fun label() = TextView(activity).apply {
        layoutParams = LinearLayout.LayoutParams(MATCH, WRAP)
    }

    private companion object {
        const val MATCH = ViewGroup.LayoutParams.MATCH_PARENT
        const val WRAP = ViewGroup.LayoutParams.WRAP_CONTENT
        const val POLL_MILLIS = 400L
        const val CAMERA_ASK = 1
        const val ASK_STORE = "swarm-permission-asks"
        const val ASKED_CAMERA = "asked-camera"
    }
}
