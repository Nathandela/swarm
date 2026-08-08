package dev.swarm.phone

import android.Manifest
import android.content.Intent
import android.content.pm.PackageManager
import android.net.Uri
import android.os.Build
import android.os.Handler
import android.os.Looper
import android.provider.Settings
import android.text.Editable
import android.text.TextWatcher
import android.view.Gravity
import android.view.View
import android.view.ViewGroup
import android.view.accessibility.AccessibilityNodeInfo
import android.widget.Button
import android.widget.LinearLayout
import android.widget.TextView
import androidx.appcompat.app.AppCompatActivity
import dev.swarm.phone.runtime.AppPermission
import dev.swarm.phone.runtime.PermissionAsks
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
import dev.swarm.phone.ui.kit.CtaKind
import dev.swarm.phone.ui.kit.NoticeKind
import dev.swarm.phone.ui.kit.ctaButton
import dev.swarm.phone.ui.kit.monoWell
import dev.swarm.phone.ui.kit.notice
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

    private val message = noticeLine()

    /**
     * PB-PAIR-4's interrupted-attempt sentence and the destination's own caveat.
     *
     * IT WAS `notice` UNTIL agents-tracker-ksvb.4, and the rename is not cosmetic: with the kit's
     * `notice` factory imported into this file, a property of that name shadows it everywhere in
     * the class and the compiler reports "Type checking has run into a recursive problem" on the
     * declaration itself. The slot it fills is still `PairingSlots.notice`.
     */
    private val stepNotice = noticeLine()

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

    /**
     * What just happened, and it is NOT the error variant -- unlike the outcome lines on the other
     * two surfaces, which look identical and are not.
     *
     * `PhoneSurface`'s and `SettingsSurface`'s outcome lines are only ever non-empty on a refusal:
     * `PressFeedback.ofSuccess` and `ofUnsent` both leave the line "" and speak in the toast, so
     * every value those hold is a verdict and they take `NoticeKind.ERROR`. This one is written
     * from five places and two of them are not verdicts at all -- the empty-field prompt in
     * [acceptScannedPayload] ("Paste the code your machine printed...") and the frame-dump
     * confirmation in [dumpOneAnalysisFrame]. Painting those `--p-err` would tell a user that
     * being told what to type next is a failure.
     */
    private val outcome = noticeLine()

    /**
     * What the camera has looked at, under the viewfinder (agents-tracker-av7k).
     *
     * ITS TEXT IS WRITTEN HERE AND ITS PRESENCE IS DECIDED BY THE SCREEN, and the split is not a
     * compromise -- it is what keeps the diagnostic from causing the thing it diagnoses. The
     * count changes several times a second; [PairingPanel] is compared against the last drawn one
     * to decide whether the view tree is rebuilt, so a count carried on the panel would tear the
     * live PreviewView out of the hierarchy and put it back at that rate. The words are still the
     * screen model's ([PairingPanelScreen.scanProgress]).
     */
    private val scanProgress = noticeLine()

    /**
     * The six symbols. Named for what it is -- a DISPLAY -- because the one thing this screen
     * must never grow is a field beside it; android/gate/s16_ui_test.go fences the shape and
     * mobile/conformance/s16_pairing_test.go fences that no verb would ingest one.
     *
     * PB-DS-11: it was `textSize = SAS_TEXT_SP` with `SAS_TEXT_SP = 28f`, a size chosen at a call
     * site, and then `Display.NavTitle` at 27 sp with a paragraph here explaining that the style
     * derivation row 7 asks for "does not exist". IT DOES (agents-tracker-ksvb.4). `Display.SAS`
     * has been in `res/values/type.xml` since the type ladder landed -- 34 sp / 400 / sans, the
     * one style §7 adds to PB-DS-2's 18, carrying a `derived:` citation rather than an `origin:`
     * because `.sas` is absent from the shared CSS block. The join the old paragraph said would
     * have to be rebuilt was rebuilt: `android/gate/s22b_type_test.go` counts the two citation
     * classes separately. So this is the design's own size, and the seven-sp approximation is gone.
     *
     * IT IS CENTRED, which is row 7's "row, gap `space_14`" read for what it is: six symbols a
     * person is holding up against another screen are a display and not a paragraph, and
     * left-aligned they read as a value in a form. The gaps are the SCREEN's -- the separator
     * below joins them -- because there is no SAS row component and one built here would be this
     * file choosing spacing.
     *
     * IT CARRIES NO INK, and `Display.SAS` declares none either: row 7 records the exception --
     * emoji glyphs are drawn by the platform's colour emoji font, which ignores textColor.
     */
    private val sasDisplay = TextView(activity).apply {
        setTextAppearance(R.style.TextAppearance_Swarm_Display_SAS)
        gravity = Gravity.CENTER_HORIZONTAL
        layoutParams = LinearLayout.LayoutParams(MATCH, WRAP)
    }

    private val sasInstruction = noticeLine()

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

    /**
     * The primary action, as row 18's own CTA.
     *
     * PB-DS-11: it was a bare `Button` carrying the platform's default look, on a screen whose
     * design row says "CTA is `.a2-ok` unchanged (`--p-cta-bg` / `--p-cta-ink` / `--p-cta-fx` /
     * `--p-btn-r`)". It is now [ctaButton] with [CtaKind.APPROVE], which is that rule -- the same
     * component the approval sheet's primary action spends, with the phosphor bloom row 18 asks for.
     *
     * IT STILL GOES THROUGH [ctaAction], so PB-SEC-12 clause 1's filter is applied at construction
     * exactly as before. What changed is the view the filter is applied to.
     *
     * A `TextView` ANNOUNCES ITSELF AS TEXT. The kit records that gap and cannot close it -- it has
     * no click to hang the role on -- so the role is set where the click is, which is
     * `PairOnlyView`'s arrangement for the one control it owns.
     */
    private val startScan = ctaAction(PairingPanelScreen.SCAN_CTA, CtaKind.APPROVE) {
        beginScanning()
    }

    /**
     * PB-PAIR-2's fallback, and it takes the SAME payload the QR carries -- never a relay URL
     * and a code as separate fields ([PairingFlow.manualEntryAcceptsSeparateFields] is false).
     * Separate fields would be a second wire encoding, and they would arrive as something the
     * user asserted rather than as a destination the phone must show them.
     */
    private val typedPayload = textField(activity, "Paste the pairing code your machine printed")

    /**
     * The relay address, on the one pairing that has to be told it (agents-tracker-3fkm).
     *
     * WHY THERE IS A SECOND FIELD ON A SCREEN WHOSE COMMENT ABOVE SAYS THERE MUST NOT BE. The
     * rule that stays is [PairingFlow.manualEntryAcceptsSeparateFields]: a pairing MESSAGE is
     * never split into fields the user asserts. This is not half of a message. The ten-character
     * code derives the whole ceremony -- the same rendezvous and the same secret the QR carries
     * -- and what it cannot carry is the address of the relay to meet at, which is configuration
     * this handset simply does not have yet. It is asked for when absent, remembered on the
     * PB-PAIR-6 confirm, and never asked again.
     *
     * THE DESTINATION IS STILL DISPLAYED AND CONFIRMED. What is typed here reaches the core as
     * `payload.RelayURL`, comes back out as `Pairing.origin()`, and is rendered into
     * [destination] for the same confirm step a scanned QR goes through. A typed address gets no
     * shortcut through the step that exists to make a destination something the user has read.
     */
    private val relayUrl = textField(activity, "Relay address, like wss://host:8443")

    /**
     * The fallback path's own action, and the one CTA on this screen whose champagne is CONTESTED.
     *
     * IT IS `.a2-ok` BECAUSE IT IS WHAT PAIRS THE PHONE. In the two states where the camera is
     * withdrawn for good -- a permanent denial, or a handset with no camera at all -- this is the
     * only way through the screen, and a screen whose only action is tertiary has no primary.
     *
     * **AND IN ONE STATE THERE ARE THEN TWO CHAMPAGNE BUTTONS ON SCREEN, which is recorded rather
     * than hidden.** `PairingFlow.offersManualEntry` answers true unconditionally
     * (agents-tracker-qun0), so once [revealTypedPayload] has been pressed on a handset whose
     * camera still works, `Scan` and this sit in the same column and both carry `--p-cta-bg`. That
     * contradicts what [revealTypedPayload] argues one declaration down -- "two phosphor-green
     * buttons on one screen would be two primary paths". The variant cannot be decided per draw
     * here: these controls are built once and re-placed, because they carry PB-SEC-12 clause 1's
     * touch filter and a listener that must survive a rebuild. Which of the two paths is primary
     * when both are open is a design decision and it is left to the owner rather than taken here.
     */
    private val useTypedPayload = ctaAction("Use this code", CtaKind.APPROVE) {
        acceptScannedPayload(typedPayload.text.toString().trim())
    }

    /**
     * "Enter code instead": the control that opens [typedPayload], and the reason it is not open.
     *
     * IT IS `.a2-more` AND NOT A SECOND HERO CTA, which is §2's reuse rule read literally: "every
     * accent-text affordance in the mock becomes either a bordered control or a plain `--p-ink`
     * glyph", and `.a2-more` is Substrate's one tertiary control. Two phosphor-green buttons on one
     * screen would be two primary paths, which is exactly the reading the owner arrived at when the
     * paste field was all there was (agents-tracker-qx9m).
     *
     * IT REVEALS AND NEVER HIDES. There is no way back to the collapsed state and that is
     * deliberate: a disclosure that toggles would let a mis-tap take the field away with whatever
     * the user had already pasted into it, and nothing on this screen is worth that.
     */
    private val revealTypedPayload = ctaAction(PairingPanelScreen.MANUAL_CTA, CtaKind.MORE) {
        manualEntryRevealed = true
        render()
    }

    /**
     * The detour, and `.a2-more` because a detour is not a path through the screen.
     *
     * It replaces [startScan] rather than sitting beside it -- `PairingControl.OPEN_SYSTEM_SETTINGS`
     * is offered only on a permanent denial, which is the one state that withdraws the scanner --
     * so the champagne on that state belongs to [useTypedPayload], the thing that still works
     * without leaving the app.
     */
    private val openSystemSettings = ctaAction("Open this app's settings", CtaKind.MORE) {
        activity.startActivity(
            Intent(Settings.ACTION_APPLICATION_DETAILS_SETTINGS)
                .setData(Uri.fromParts("package", activity.packageName, null)),
        )
    }

    /** PB-PAIR-6's confirm: the one action of the step it belongs to, so `.a2-ok`. */
    private val confirmDestination = ctaAction("Join this destination", CtaKind.APPROVE) {
        confirmTheShownOrigin()
    }

    /**
     * `.a2-no`, and it is a DENIAL rather than a neutral escape.
     *
     * Abandoning a handshake is destructive in the one sense that matters here: `Pairing` is a
     * process-local handle, so the attempt this cancels cannot be resumed by anything -- a fresh
     * `BeginPairing` is what overwrites the record. Substrate's tertiary would read as "not now".
     */
    private val stopPairing = ctaAction("Stop pairing", CtaKind.DENY) { cancelAttempt() }

    /**
     * The human-in-the-loop security check, in the two variants the design already declares.
     *
     * ADR-007 B133 left these as the ONLY checkpoint in the product, and the pair is the one place
     * in this app where the two treatments carry their literal meaning: `.a2-ok` is the user
     * vouching that two screens agree, `.a2-no` is the user reporting a man in the middle. They are
     * the sole champagne and the sole denial on the comparing step, beside [stopPairing].
     */
    private val codesMatch = ctaAction("They match", CtaKind.APPROVE) { answerSas(SasAnswer.MATCHES) }

    private val codesDoNotMatch = ctaAction("They do not match", CtaKind.DENY) {
        answerSas(SasAnswer.DOES_NOT_MATCH)
    }

    /** The in-flight attempt as the SCREEN holds it (S16's model). */
    private var attempt: PairingAttempt = PairingFlow.restore(null)

    /** The in-flight attempt as the CORE holds it. Null until a QR has been decoded. */
    private var handle: Pairing? = null

    /** Set once the phone has been rebuilt for a completed pairing; see [renderReady]. */
    private var rebuilt = false

    /**
     * Whether the user has asked for the typed fallback.
     *
     * IT IS THIS SURFACE'S AND NOT THE ATTEMPT'S. What it records is a press on this draw of this
     * screen, not a fact about a pairing -- and `PairingFlow.restore` rebuilds an attempt from a
     * persisted step, so a resumed one that remembered an open field would be remembering something
     * that was never about it. It resets with the process, which is the right lifetime: a person who
     * relaunches the app is looking at the screen again from the top.
     */
    private var manualEntryRevealed = false

    /**
     * Whether what is in [typedPayload] right now announces itself as the long payload.
     *
     * IT IS WATCHED RATHER THAN READ AT THE PRESS, because it decides what is ON SCREEN: the
     * relay field is for the code spelling and would be noise beside a pasted payload that
     * carries its own address. Read only when the button is pressed, the screen would be showing
     * a box for a value it had already decided to ignore.
     *
     * THE WATCHER REDRAWS ON THE ANSWER AND NOT ON THE KEYSTROKE. Every draw re-derives the step
     * from the Go core, so a render per character would be a facade call per character; what
     * changes the screen is the answer flipping, which happens at most twice in a paste.
     */
    private var typedEntryCarriesItsOwnRelay = false

    init {
        typedPayload.addTextChangedListener(
            object : TextWatcher {
                override fun afterTextChanged(entry: Editable?) {
                    val carries = PairingFlow.entryCarriesItsOwnRelay(entry?.toString().orEmpty())
                    if (carries == typedEntryCarriesItsOwnRelay) return
                    typedEntryCarriesItsOwnRelay = carries
                    render()
                }

                override fun beforeTextChanged(s: CharSequence?, start: Int, count: Int, after: Int) = Unit

                override fun onTextChanged(s: CharSequence?, start: Int, before: Int, count: Int) = Unit
            },
        )
    }

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

    /**
     * Whether the analysis pipeline is running, as against whether the permission would allow it.
     *
     * IT IS NOT DERIVABLE FROM [scannerState], which answers who MAY open the camera. A granted
     * permission on a screen nobody has pressed Scan on has no pipeline, no frames and nothing to
     * report -- and a frame counter over that is a claim that something is looking.
     */
    private var cameraLive = false

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
        notice = stepNotice,
        destination = destination,
        sas = sasDisplay,
        sasInstruction = sasInstruction,
        scanner = scannerHost,
        scanProgress = scanProgress,
        controls = mapOf(
            PairingControl.SCAN to startScan,
            PairingControl.REVEAL_TYPED_PAYLOAD to revealTypedPayload,
            PairingControl.TYPED_PAYLOAD to typedPayload,
            PairingControl.RELAY_URL to relayUrl,
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

    /**
     * PB-SEC-12 clause 1: every control here authorises something.
     *
     * [revealTypedPayload] IS FILTERED TOO, AND THAT IS ARGUED RATHER THAN ASSUMED. It authorises
     * nothing by itself -- it opens a field -- so on `PairOnlyView`'s own reasoning about its hero
     * CTA it could be left off. It is on because the field it opens is where a payload enters this
     * screen: an overlay that stole this tap would have put a paste field in front of a user who
     * did not ask for one, which is the first move of getting somebody to paste an attacker's code.
     * The filter costs nothing and the alternative needs a longer argument than this one.
     */
    val touchFilteredActions: List<View> = listOf(
        startScan, revealTypedPayload, useTypedPayload, openSystemSettings, confirmDestination,
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
        cameraLive = false
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
            // TWO SPELLINGS, ONE SEAM (ADR-007 B140): the QR's payload announces itself with
            // the wire prefix, so anything else is read as the ten-character code, completed
            // by the relay this phone already knows. A code arriving before any relay is
            // known is refused by the facade with a routed message -- words on this screen,
            // naming the two paths that do work -- rather than a handle with no address. On a
            // fresh install that address comes from the field beside the code
            // (agents-tracker-3fkm), so the refusal is now what a person sees only when they
            // have left it empty.
            val started = if (PairingFlow.entryCarriesItsOwnRelay(payload)) app.beginPairing(payload)
            else app.beginPairingWithCode(payload, relayForTypedCode())
            handle = started
            attempt = PairingFlow.begin(payload, started.origin(), started.originIsPrivate())
            outcome.text = ""
        } catch (refused: Exception) {
            handle = null
            outcome.text = routed(refused)
        }
        render()
    }

    /**
     * The relay a typed code is completed with: the one just entered, else the one this phone
     * already knows.
     *
     * THE TWO ARE NEVER BOTH SET THROUGH THE SCREEN -- the field is offered only while
     * [PhoneRuntime.knownRelay] is empty -- so the order matters for one case only, and it is the
     * right way round for it: a person who has just typed an address is told about THAT address,
     * never about a value they cannot see.
     */
    private fun relayForTypedCode(): String =
        relayUrl.text.toString().trim().ifEmpty { runtime.knownRelay() }

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
        // THE PANEL IS REDRAWN BEFORE THE CAMERA EXISTS, and the order is the point: the frame
        // counter joining the screen is a panel change, which rebuilds the view tree, which
        // detaches every slot -- including the preview. Bound first, that rebuild would pull the
        // PreviewView's surface out from under a camera that had just been given it.
        cameraLive = true
        scanProgress.text = ""
        render()

        val live = scanner ?: QrScanner(activity).also {
            scanner = it
            scannerHost.addView(it.view, LinearLayout.LayoutParams(MATCH, MATCH))
            // THE DIAGNOSTIC IS BEHIND A LONG PRESS, on the one view whose whole purpose is
            // being aimed (agents-tracker-av7k). It is not on the panel as a control because it
            // is not part of pairing: a person following the guided steps must never meet it,
            // and the person who needs it is being told where to press.
            it.view.setOnLongClickListener { _ -> dumpOneAnalysisFrame() }
        }
        live.view.visibility = View.VISIBLE
        scannerHost.visibility = View.VISIBLE
        live.start(
            onPayload = { payload -> acceptScannedPayload(payload) },
            // WRITTEN STRAIGHT INTO THE VIEW, never through a redraw: a render() here would ask
            // the Go core for its state several times a second and rebuild the tree under the
            // running preview. The screen model still owns the words.
            onFrames = { seen -> scanProgress.text = PairingPanelScreen.scanProgress(seen) },
            onError = { failure -> scanFailed(failure) },
        )
    }

    /**
     * A camera that started binding and then failed -- no available camera, a lifecycle already
     * gone, anything CameraX itself refuses -- routed to the same outcome line every other
     * pairing failure reaches instead of crashing the app (agents-tracker-nz9h).
     */
    private fun scanFailed(failure: Exception) {
        stopScanning()
        outcome.text = routed(failure)
        render()
    }

    /**
     * PB-E2E-5's missing evidence, on demand: one analysis frame written where a person can
     * fetch it (agents-tracker-av7k).
     *
     * THE FRAME MAY CONTAIN A LIVE PAIRING SYMBOL, so where it goes is a decision and not a
     * default. `getExternalFilesDir` is this app's own directory on external storage -- no other
     * app can read it under scoped storage, and it is the one place the OWNER can reach on a
     * release-signed internal-testing build, where `run-as` is unavailable and a file under
     * `noBackupFilesDir` would be evidence nobody can collect. The transfer surface that opens is
     * shut in `res/xml/data_extraction_rules.xml`, which now excludes `domain="external"` beside
     * the private root for exactly this file.
     *
     * @return true always: the press was consumed either way, and a long press that reports
     *  "unhandled" would fall through to whatever the platform does with it next.
     */
    private fun dumpOneAnalysisFrame(): Boolean {
        scanner?.dumpNextFrame(activity.getExternalFilesDir(null) ?: activity.noBackupFilesDir)
        outcome.text = "Saving the next camera frame for diagnosis. Its path is in the log."
        render()
        return true
    }

    private fun stopScanning() {
        scanner?.stop()
        cameraLive = false
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
                manualEntryRevealed = manualEntryRevealed,
                cameraLive = cameraLive,
                // ASKED OF THE RUNTIME ON EVERY DRAW, never latched: the URL is written during
                // this screen's own confirm step, so a value cached at construction would keep
                // the field on screen for the rest of a session that had already answered it.
                relayKnown = runtime.knownRelay().isNotEmpty(),
                typedEntryCarriesItsOwnRelay = typedEntryCarriesItsOwnRelay,
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
        stepNotice.text = panel.notice
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
        // A REBUILD MUST NOT TAKE THE KEYBOARD AWAY FROM SOMEONE STILL TYPING. Every slot is
        // detached and re-added below, which drops focus and closes the soft keyboard -- and this
        // screen now has a draw that happens WHILE a field is being filled: pasting the long
        // payload withdraws the relay field mid-entry (agents-tracker-3fkm). The view is the same
        // instance across the rebuild, so asking for focus back is asking for the field the user
        // was in; a view the new panel dropped is no longer in the tree and quietly refuses.
        val focused = host.findFocus()
        (outcome.parent as? ViewGroup)?.removeView(outcome)
        host.removeAllViews()
        host.addView(pairingPanelView(activity, panel, slots, outcome))
        focused?.requestFocus()
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
        "relay_unreachable" -> PairingStep.RELAY_UNREACHABLE
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
     * PB-RUN-2's resolution, with the persisted "have we asked" bit the platform does not offer
     * -- but only once there is a camera to ask a permission ABOUT at all (agents-tracker-nz9h).
     * The manifest declares `android.hardware.camera` optional, so a handset that answers no to
     * [hasCameraHardware] is checked here, before any permission is resolved: every permission
     * question is moot once the hardware itself is absent, and answering PERMANENTLY_DENIED for
     * it would offer a Settings route that fixes nothing.
     *
     * `shouldShowRequestPermissionRationale` is false BEFORE the first ask as well as after a
     * permanent denial, so reading it alone reports PERMANENTLY_DENIED on a fresh install and
     * sends a user with nothing wrong to a Settings screen.
     */
    private fun scannerState(): ScannerState {
        if (!hasCameraHardware(activity.packageManager)) return ScannerState.NO_CAMERA
        return PairingFlow.scannerState(
            PermissionStateResolver.resolve(
                permission = AppPermission.CAMERA,
                sdkInt = Build.VERSION.SDK_INT,
                granted = activity.checkSelfPermission(Manifest.permission.CAMERA) ==
                    PackageManager.PERMISSION_GRANTED,
                hasAskedBefore = PermissionAsks.hasAsked(activity, AppPermission.CAMERA),
                showRationale = activity.shouldShowRequestPermissionRationale(
                    Manifest.permission.CAMERA,
                ),
            ),
        )
    }

    /**
     * IT IS [PermissionAsks]'S NOW AND NOT THIS FILE'S (agents-tracker-0dij). The bit, its store and
     * the backup exclusion that covers it were three lines here and nothing at all on the settings
     * screen, which hard-coded `hasAskedBefore = true` and made POST_NOTIFICATIONS unaskable for the
     * life of an install. The decision is one decision, so it is written once and both surfaces
     * spend it; what stays here is WHEN this screen asks.
     */
    private fun rememberTheAsk() {
        PermissionAsks.remember(activity, AppPermission.CAMERA)
    }

    // -----------------------------------------------------------------------

    private fun routed(failure: Exception) = ErrorRouter.route(failure.message.orEmpty()).message

    private fun show(view: View, visible: Boolean) {
        view.visibility = if (visible) View.VISIBLE else View.GONE
    }

    /**
     * Every control on this screen, drawn by the kit and carrying PB-SEC-12 clause 1's touch filter.
     *
     * IT WAS TWO FACTORIES AND `touchFilteredButton` IS GONE (agents-tracker-ksvb.4). That one
     * built a platform `Button` -- all-caps 14 sp Roboto Medium on the stock Material background --
     * for six of this screen's eight controls, beside two kit CTAs. Its KDoc called the split a
     * scope statement: "`Join this destination`, the two SAS answers and `Stop pairing` belong to
     * steps this slice did not redesign". They are redesigned now, into the three variants
     * Substrate already declares, so nothing is chosen here beyond which of them a control is.
     *
     * WHAT SURVIVED THE MERGE IS THE SECURITY CONTROL, unchanged and by construction.
     * [SecureWindow.gate] sets `filterTouchesWhenObscured`, which discards a tap that arrived while
     * another window covered the view; ADR-007 B133 removed the biometric tiers and left that
     * filter as the ONLY defence standing on the SAS answers. It is applied here, to every control,
     * exactly as `touchFilteredButton` applied it -- what changed is the view it is applied to.
     * `PhoneSurfaceControlsTest.every_button_and_switch_on_screen_filters_obscured_touches` is what
     * holds that: it walks the real hierarchy and reads `background is CtaSurface`, so a kit CTA
     * is in its subject and a control that lost the filter fails whether or not anyone updated
     * [touchFilteredActions].
     */
    private fun ctaAction(text: String, kind: CtaKind, onPress: () -> Unit): TextView =
        SecureWindow.gate(
            ctaButton(activity, text, kind).apply {
                setOnClickListener { onPress() }
                // A `TextView` ANNOUNCES ITSELF AS TEXT. The kit records the gap and cannot close
                // it -- it has no click to hang the role on -- so the role is set where the click
                // is, which is `PairOnlyView`'s arrangement for the control it owns.
                setAccessibilityDelegate(
                    object : View.AccessibilityDelegate() {
                        override fun onInitializeAccessibilityNodeInfo(
                            host: View,
                            info: AccessibilityNodeInfo,
                        ) {
                            super.onInitializeAccessibilityNodeInfo(host, info)
                            info.className = Button::class.java.name
                        }
                    },
                )
            },
        )

    /**
     * One line the screen says about its own state.
     *
     * IT WAS `label()`, AND WHAT IT PRODUCED WAS A BARE `TextView` (agents-tracker-ksvb.4). The
     * KDoc here said "the kit has no component for that ... the rest render at the theme's
     * default", which read as an absence and was not one: a `TextView` with no `TextAppearance`
     * renders at the platform's ~14 sp, larger than every body style in this app's ladder, so five
     * lines on the pairing screen -- the step body, the caveat, the outcome, the frame counter and
     * the SAS instruction -- were the largest body text on it. `§4 Notice line` specifies them now.
     *
     * @param kind ERROR only where every non-empty value is a refusal. [outcome] is the one, and
     *  it is not one on this screen alone -- see its own declaration.
     */
    private fun noticeLine(kind: NoticeKind = NoticeKind.INFO) = notice(activity, "", kind)

    private companion object {
        const val MATCH = ViewGroup.LayoutParams.MATCH_PARENT
        const val WRAP = ViewGroup.LayoutParams.WRAP_CONTENT
        const val POLL_MILLIS = 400L
        const val CAMERA_ASK = 1
    }
}

/**
 * Whether this handset has any camera at all, the fact [PairingSurface.scannerState] gates on
 * before it asks anything about the CAMERA permission (agents-tracker-nz9h).
 *
 * `FEATURE_CAMERA_ANY` IS THE AGGREGATE, not `FEATURE_CAMERA` -- the manifest already declares
 * `android.hardware.camera` optional (a rear camera specifically), and a handset with only a
 * front camera must not be told it has none.
 *
 * EXTRACTED TO A TOP-LEVEL FUNCTION so it has a seam a test can drive without a live phone core:
 * [PairingSurface] takes an `AppCompatActivity` and has no unit test of its own for the reason
 * every render comment in that class gives -- `PhoneRuntime.phone()` never resolves to `Ready`
 * on this unit-test JVM, so `scannerState`'s caller is unreachable here.
 */
internal fun hasCameraHardware(packageManager: PackageManager): Boolean =
    packageManager.hasSystemFeature(PackageManager.FEATURE_CAMERA_ANY)
