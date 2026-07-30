package dev.swarm.phone

import android.graphics.Typeface
import android.view.View
import android.view.ViewGroup
import android.widget.Button
import android.widget.EditText
import android.widget.LinearLayout
import android.widget.ScrollView
import android.widget.TextView
import androidx.appcompat.app.AppCompatActivity
import dev.swarm.phone.keys.AuthorizationLedger
import dev.swarm.phone.keys.BiometricPolicy
import dev.swarm.phone.keys.BiometricPrompts
import dev.swarm.phone.keys.GateResolution
import dev.swarm.phone.keys.GatedOperation
import dev.swarm.phone.keys.InputFreshness
import dev.swarm.phone.keys.InputGateDecision
import dev.swarm.phone.keys.InvalidationEvent
import dev.swarm.phone.keys.PerUseGate
import dev.swarm.phone.keys.PerUseRefusalReason
import dev.swarm.phone.keys.PerUseRefusalText
import dev.swarm.phone.keys.PromptOutcome
import dev.swarm.phone.runtime.ConnectivityPolicy
import dev.swarm.phone.runtime.ContentUnlockPolicy
import dev.swarm.phone.runtime.LifecycleConvergence
import dev.swarm.phone.runtime.LifecycleEvent
import dev.swarm.phone.runtime.RuntimeState
import dev.swarm.phone.runtime.SocketDisposition
import dev.swarm.phone.ui.CapabilityNotice
import dev.swarm.phone.ui.ControlLease
import dev.swarm.phone.ui.FacadeBridge
import dev.swarm.phone.ui.LaunchDraft
import dev.swarm.phone.ui.LaunchRendering
import dev.swarm.phone.ui.LaunchResult
import dev.swarm.phone.ui.LaunchScreen
import dev.swarm.phone.ui.RoutedError
import dev.swarm.phone.ui.TerminalPeek
import swarmmobile.App
import swarmmobile.LaunchSpec

/**
 * Phase B slice S18 -- the views [PhoneActivity] hosts.
 *
 * SCOPE, because this is deliberately not a finished app. PB-SEC-4 and PB-SEC-12 clause 1 had
 * no subject in this module: it declared no `<activity>`, so the secure-window flag had no
 * Window and the touch filter had no View. The ruling recorded on 2026-07-25 assigns S18 a
 * MINIMAL Activity sufficient to host the pairing and terminal-peek surfaces the requirement
 * names, and no more. What is here is a real window with real controls reaching real facade
 * verbs; what is not here is navigation, a session picker, a keyboard, or anything else
 * PB-APP-1..8 will eventually ask for.
 *
 * IT WIRES S16'S SCREEN MODELS, IT DOES NOT REIMPLEMENT THEM. Every string on screen comes from
 * [PairingFlow], [dev.swarm.phone.ui.ConnectionBanner], [dev.swarm.phone.ui.TerminalPeek] or
 * [dev.swarm.phone.ui.RoutedError], reached through [FacadeBridge], which is the one place
 * those models meet the bound facade. A second copy of any of that wording here would be a
 * screen that disagrees with every test S16 wrote.
 *
 * IT IS A SEPARATE FILE FROM THE ACTIVITY, AND THAT IS A REQUIREMENT RATHER THAN A STYLE.
 * [PhoneActivity] is exported with a LAUNCHER filter, so any app on the device can start it.
 * PB-SEC-11's last clause is that no exported component can act on the session, and
 * android/gate/s18_sec11_exported_test.go enforces it by scanning the Kotlin file named after
 * each exported component for facade verbs. The Activity therefore owns the window and nothing
 * else: every verb that touches a session is reached from here, behind a control a person
 * pressed.
 *
 * THE GRID IS TEXT AND STAYS TEXT. ADR-007 D2 puts the VT emulator on the machine; the peek
 * below displays `swarmmobile.Snapshot.Text` byte for byte in a monospace view. A renderer on
 * this side would reinterpret bytes the daemon has already declared sanitized.
 *
 * S19 ADDED THE REST OF PB-E2E-2'S SUBJECTS. The scope above was set before anyone checked what
 * the smoke required, and what it left out was four of that requirement's five in-app actions:
 * the scanner, the destination confirmation, the SAS display and its confirm control (all in
 * [PairingSurface]) and the keyboard (below). [SettingsSurface] came with them, because
 * PB-PUSH-9's "deletion on ... disable" was a facade method with no caller for want of a screen
 * to put a switch on. The panels are hosted here rather than in Activities of their own, so the
 * app still has exactly ONE window and one exported component to reason about.
 *
 * THE SCOPE RULING ABOVE NO LONGER COVERS THE LAUNCH FORM, and the paragraph is amended rather
 * than left standing, because android/unbound-verbs.tsv used to cite it BY NAME as the reason
 * `App.Launch` was deliberately unbound. ADR-007 B80 is the record of what that costs: the
 * ledger said the launch screen did not exist, the traceability table said PB-APP-6 was shipped,
 * and nothing anywhere joined the two. Section 1's binding exit criterion is a phone that
 * "pairs, observes, LAUNCHES, and types into a real session", so the fields and the control that
 * start a session are below. What is still not here is a machine pane and a session picker: the
 * peek and the session controls act on the first row of the triage inbox, and the launch form
 * needs no picker because the session it starts does not exist yet.
 */
class PhoneSurface(
    private val activity: AppCompatActivity,
    private val runtime: PhoneRuntime,
    /**
     * THE PROCESS'S ONE [AuthorizationLedger], passed in rather than defaulted. It is
     * `SwarmApplication.contentLock`'s, which is the only one every [InvalidationEvent] reaches;
     * a default here would be a parameter somebody eventually leaves off (ADR-007 B18(c)), and
     * what they would get is a per-use gate no screen lock can clear.
     *
     * It is HELD rather than only passed on, because requirements 6.0's timed tier is decided
     * from it too: the moment the tier was last authorized is a fact every [InvalidationEvent]
     * must be able to destroy, and a timestamp kept in this class would survive a screen lock
     * that emptied the ledger.
     */
    private val ledger: AuthorizationLedger,
) {

    /**
     * PB-SEC-2's PER-USE tier, and the reason this constructor gained a parameter.
     *
     * ADR-007 B51: `KeystoreSpecs.forOperation` was referenced from `src/test/` alone, the ledger
     * had no production caller, and there was no `BiometricPrompt` in the app -- so revoke and
     * kill were gated by the content KEK's 60-second TIMED window, exactly like typing. This is
     * where that stops being true, because this is the only place those verbs are reached from.
     */
    private val prompts = BiometricPrompts(activity)

    private val perUse = PerUseGate(
        prompt = prompts,
        ciphers = runtime.perUseCiphers(),
        ledger = ledger,
    )

    private val status = label(bold = true)

    /**
     * PB-KEY-8's non-fatal half. [dev.swarm.phone.keys.CustodyPlanner] records a capability the
     * handset did not confirm that no matrix row consumes; until this label existed the record
     * was computed on every launch and read by nobody.
     */
    private val notice = label()
    private val peekTitle = label(bold = true)
    private val peek = label().apply { typeface = Typeface.MONOSPACE }
    private val outcome = label()

    /**
     * PB-INPUT-2's "VISIBLY confirmed", which is the requirement's own word and had no subject:
     * the surface showed the same Take control button and the same live keyboard whether the
     * machine had granted a lease or not, so a user could not tell until a keystroke vanished.
     * The wording is chosen from [dev.swarm.phone.ui.TerminalPeek]'s verdict, never from the
     * press.
     */
    private val lease = label()

    /**
     * The machine's answer to the launch this screen issued, on a line of its own.
     *
     * IT IS NOT [outcome]. That line is overwritten by every gated action, and a launch outcome
     * arrives on a LATER draw -- the machine answers asynchronously and PB-SYNC-2 claims the
     * answer by operation id -- so the two would erase each other.
     */
    private val launchStatus = label()

    private val pairing = PairingSurface(activity, runtime)
    private val settings = SettingsSurface(activity, runtime)

    /**
     * TIMED (requirements 6.0), so it goes through [timedButton]: 60 seconds, and a crossing
     * pauses and re-authorizes rather than continuing.
     *
     * IT REMEMBERS THE OPERATION IT ISSUED, which is what makes PB-INPUT-2's lease a fact rather
     * than a literal. The lease is not on any snapshot: it is the outcome of THIS take_control,
     * claimed by operation id, and [leaseConfirmedFor] is what asks the machine about it.
     */
    private val takeControl = timedButton("Take control", GatedOperation.TAKE_CONTROL) { app, session ->
        app.takeControl(session).also { issued ->
            leaseOp = issued.operationID
            leaseSession = session
        }
    }

    /**
     * PER-USE (requirements 6.0), so it goes through [perUse] and not through [timedButton].
     * Ending someone's session is one prompt away from a phone in a stranger's hand.
     */
    private val kill = perUseButton("Kill session", GatedOperation.KILL) { app, session ->
        app.kill(session)
    }

    /**
     * PB-E2E-2's "types", and PB-INPUT-3's precondition beside it: the machine refuses input
     * without a confirmed lease, so this control sits below Take control and the refusal it
     * produces without one routes through PB-APP-9 like every other.
     */
    private val typed = field("Type into the session you hold")

    /**
     * ONE CARRIAGE RETURN IS APPENDED, AND THAT IS WHAT "A LINE" MEANS AT A TERMINAL -- the key
     * a shell waits for is CR, and a control that sent the characters without it would leave the
     * user's command sitting unsubmitted with nothing on screen explaining why.
     *
     * The bytes are UTF-8 and nothing on this side interprets them. There is no VT emulator on
     * the handset (ADR-007 D2): what goes out is what was typed.
     */
    private val send = timedButton("Send line", GatedOperation.INPUT) { app, session ->
        val line = typed.text.toString()
        app.sendInput(session, (line + "\r").toByteArray(Charsets.UTF_8))
        typed.text.clear()
    }

    /**
     * PB-SEC-7's panic action, and the reason it is on this minimal surface at all: it is the
     * one control whose whole value is being reachable on a handset its owner no longer trusts.
     * It revokes THIS device. The kill switch is owner-tier only and this app can never set it,
     * so revoke is the phone-side response to a lost device (mobile/screen_coverage.tsv).
     */
    private val revoke = perUseButton("Revoke this device", GatedOperation.REVOKE) { app, _ ->
        app.revokeThisDevice()
    }

    /**
     * PB-APP-6's two REQUIRED fields, and the third the spec carries.
     *
     * `LaunchScreen.submit` refuses a draft without a non-blank agent and a non-blank working
     * directory, because the daemon has no default for either. A form without them could only
     * launch by inventing values, which is a hardcoded launch spec shipped in production code --
     * the same defect class `leaseHeld = false` was.
     *
     * THE PROMPT IS A FIELD RATHER THAN AN EMPTY STRING for the same reason. [LaunchDraft] models
     * three fields; passing `""` for the third would be a literal standing in for something
     * nobody was asked.
     */
    private val launchAgent = field("Which agent to start")

    private val launchCwd = field("Working directory on your machine")

    private val launchPrompt = field("First message for the agent, if any")

    /**
     * PER-USE (requirements 6.0), so it goes through [perUseButton] like kill and revoke, and
     * android/gate/s20_pbsec2_peruse_test.go has carried `app.launch(` in its list since before
     * this screen existed precisely so the gate became mandatory the day it landed. Starting work
     * on someone's machine is one prompt away from a phone in a stranger's hand.
     *
     * THE VERB IS CALLED FROM THIS LAMBDA AND NOT FROM A HELPER, which is a constraint and not a
     * style: that gate attributes a call site to the nearest enclosing member DECLARATION, so a
     * launch issued inside a private function of its own would read as ungated. A local `val`
     * here would do the same. Everything the call needs is therefore a parameter or a call.
     *
     * THE DRAFT IS REFUSED BEFORE IT IS SENT, by the model's own bar. A launch missing a required
     * field is refused at the machine too, but only after burning a durable command seq and a
     * signature on a request the phone could see was incomplete.
     */
    private val launch = perUseButton("Launch a session", GatedOperation.LAUNCH) { app, _ ->
        draftOnScreen().let { draft ->
            when (val missing = launchScreen.missingField(draft)) {
                null -> launchScreen.submit(draft, app.launch(specOf(draft)).operationID)
                else -> launchStatus.text = missing
            }
        }
    }

    /**
     * ADR-007 B44's missing exit, as a control.
     *
     * WHY IT HAS TO EXIST. B44 made a screen lock return the content tier to locked, and
     * [renderReady] asks [PhoneRuntime.unlockContent] on the way back -- with a comment
     * asserting the Keystore "will answer", which is false after a credential unlock (mandatory
     * post-reboot), after the biometric idle timeout, after repeated failures, and always on a
     * handset with no enrolled Class-3 biometric. Until this button existed the refusal was a
     * sentence on screen and nothing else: a gate whose exit is unbuilt.
     *
     * IT IS HIDDEN UNLESS IT WOULD HELP. [dev.swarm.phone.runtime.ContentUnlockPolicy] decides
     * from the ROUTED remedy, so a destroyed key or an unsupported handset gets no button to
     * press forever -- PB-APP-10's failure loop, reached through the remedy.
     */
    private val unlockContent = SecureWindow.gate(
        Button(activity).apply {
            text = "Unlock your sessions"
            visibility = View.GONE
            setOnClickListener { promptForContent() }
        },
    )

    /**
     * PB-APP-6's screen model, which decided what a launch screen shows and was consulted by
     * nothing. It holds the operation id the MACHINE keyed the launch by, so the answer is
     * claimed by that id (PB-SYNC-2) rather than resolved by proximity.
     */
    private val launchScreen = LaunchScreen()

    /** The session the controls act on, chosen in [renderReady] and never from an Intent. */
    private var session: String = ""

    /**
     * The take_control this surface issued, and the session it was issued for.
     *
     * BOTH, because the target is re-derived from the triage inbox on every draw: a lease
     * confirmed for the session that used to be first must not read as a lease on the one that
     * is first now. An operation id with no session beside it is a lease attributed by
     * proximity, which is the thing PB-SYNC-2 exists to forbid.
     */
    private var leaseOp: String = ""

    private var leaseSession: String = ""

    /** The phone this surface has started, so [release] can stop the one it actually started. */
    private var connected: App? = null

    /**
     * The session the MACHINE has been asked to render frames for, which is not the same fact as
     * [session]. `terminalWatch` is a request that costs the daemon per-session render work, so
     * what is open has to be tracked in order to be closed.
     */
    private var watching: String = ""

    /** True once this surface installed its listener and started journal delivery. */
    private var observing = false

    val root: View = ScrollView(activity).apply {
        addView(
            LinearLayout(activity).apply {
                orientation = LinearLayout.VERTICAL
                setPadding(PADDING, PADDING, PADDING, PADDING)
                layoutParams = ViewGroup.LayoutParams(MATCH, MATCH)
                for (child in listOf(
                    status, notice, unlockContent, pairing.root, peekTitle, peek, lease,
                    takeControl, typed, send, kill, launchAgent, launchCwd, launchPrompt,
                    launch, launchStatus, revoke, settings.root, outcome,
                )) {
                    addView(child)
                }
            },
        )
    }

    /**
     * The controls PB-SEC-12 clause 1 is about, exposed so the assertion has a named subject
     * rather than having to guess which views in a hierarchy are the gated ones.
     *
     * EVERY PANEL'S CONTROLS ARE IN IT. A per-screen list is how the screen added last gets
     * missed, with nothing failing -- so a new panel contributes its own gated set here rather
     * than being remembered about.
     */
    val gatedActions: List<View> =
        listOf(takeControl, send, kill, launch, revoke, unlockContent) +
            pairing.gatedActions + settings.gatedActions

    /**
     * Draw. Called from onResume, so a phone that was unavailable when the screen opened -- a
     * locked handset, or a key the user has not authenticated for yet -- redraws once it is not.
     */
    fun render() {
        pairing.render()
        settings.render()
        when (val startup = runtime.phone()) {
            is PhoneStartup.Unavailable -> renderUnavailable(startup)
            is PhoneStartup.Ready -> renderReady(startup)
        }
    }

    /**
     * Release what the surface holds while the screen is not in front of anyone: the camera, and
     * the relay socket.
     *
     * ADR-007 B16 and PB-RUN-3 say the socket is CLOSED in every background state and the phone
     * is reached by a push wake instead. The disposition is read from [ConnectivityPolicy] rather
     * than restated here, because that object and android/connectivity-policy.tsv are asserted
     * equal and a third copy of the rule is a third thing to get wrong.
     *
     * It never CONSTRUCTS a phone: [connected] is only set once one was built and started, so a
     * pause before anything was built does not reach Keystore on the way out.
     */
    fun release() {
        pairing.release()

        // The sink goes FIRST and unconditionally. It is the only thing that can outlive this
        // screen: PhoneRuntime caches the App across Activity instances, so a listener still
        // pointed at these views would redraw a window nobody is holding -- and would keep this
        // Activity reachable for as long as the process lives.
        PhoneEvents.stopObserving()
        observing = false

        val live = connected ?: return
        if (ConnectivityPolicy.ruleFor(RuntimeState.BACKGROUND).socket != SocketDisposition.CLOSED) {
            return
        }
        connected = null

        // ADR-007 B16: backgrounding DISCONNECTS. Both of these are requests the machine is
        // still serving on the phone's behalf -- per-session terminal render work, and journal
        // delivery into a queue nothing is draining -- so they are withdrawn before the socket
        // goes, while there is still a socket to withdraw them over.
        unwatch(live)
        try {
            live.unsubscribeJournal()
        } catch (refused: Exception) {
            // The socket is closing either way, and journal delivery is a phone-side flag the
            // next Start re-establishes. There is no user present on this path.
        }
        try {
            live.stop()
        } catch (refused: Exception) {
            // Stop is idempotent and the process may be going away regardless. There is no user
            // present on this path and no screen left to report to.
        }
    }

    /**
     * Start observing, which nothing did -- and it is why PB-APP-3/4/5 were non-functional in
     * the shipping app rather than merely incomplete.
     *
     * `SetEventListener`, `SubscribeJournal` and `TerminalWatch` appeared ZERO times in all
     * Kotlin (residuals §2.9). So no listener was installed, journal delivery never started, and
     * the machine was never asked to send terminal frames -- while [FacadeBridge.terminalPeek]
     * read `App.Peek`, a LOCAL cache that only a watched session ever fills. The peek was
     * permanently empty, and it failed looking exactly like a quiet machine.
     *
     * IT IS IDEMPOTENT AND GUARDED, because [render] runs on every resume and after every gated
     * action. Installing the same listener twice is harmless; re-subscribing on every button
     * press is pointless traffic through JNI.
     */
    private fun observe(app: App) {
        if (observing) return
        try {
            app.setEventListener(PhoneEvents)
            app.subscribeJournal()
        } catch (refused: Exception) {
            outcome.text = FacadeBridge(app).routeFacadeError(refused.message.orEmpty()).message
            return
        }
        // Installed only once the facade accepted both: a sink armed over a listener that was
        // never installed is a screen waiting for events that cannot arrive.
        PhoneEvents.observe { render() }
        observing = true
    }

    /**
     * Ask the machine to render [next], and stop it rendering whatever came before.
     *
     * THE PEEK IS NOT A PULL. `App.Peek` reads `Router().Snapshots()`, a cache the machine fills
     * by pushing terminal frames -- and it pushes them only for a session the phone has WATCHED
     * (PB-APP-4). Without this call the peek is empty forever and says so in the words of a
     * session with nothing on screen.
     */
    private fun watch(app: App, next: String) {
        if (next == watching) return
        unwatch(app)
        if (next.isEmpty()) return
        try {
            app.terminalWatch(next)
            watching = next
        } catch (refused: Exception) {
            outcome.text = FacadeBridge(app).routeFacadeError(refused.message.orEmpty()).message
        }
    }

    /**
     * Close the open peek. Without it the peek plane leaks per-session server render work for
     * every session the user ever looked at, which is `App.TerminalUnwatch`'s own reason for
     * existing.
     */
    private fun unwatch(app: App) {
        val open = watching
        watching = ""
        if (open.isEmpty()) return
        try {
            app.terminalUnwatch(open)
        } catch (refused: Exception) {
            // Recorded as closed regardless: this runs on the way to the background and on a
            // session that has gone away, and a phone that kept retrying a stale unwatch would
            // spend a reconnect on a session nobody is looking at.
        }
    }

    /**
     * Connect, which nothing did before S19 -- and it is why PB-E2E-2's "observes, takes control,
     * types" had no chance of working even once the controls existed. `App.Start` is what dials
     * the relay and begins draining the machine's mailbox, and no production Kotlin called it:
     * every screen read a roster the app was never connected to fill.
     *
     * THE PLAN COMES FROM [LifecycleConvergence], which had no production caller either. Its
     * COLD_START row is this moment -- the screen coming to the front, over a phone core that may
     * have been rebuilt since the last one -- and it says to re-establish exactly ONCE and only
     * when there is persisted state to resume. `Start` is idempotent, so a redraw calls it and
     * nothing happens, which is what makes "one re-establish" survive a screen that redraws on
     * every poll.
     */
    private fun converge(app: App) {
        val paired = try {
            app.stateSummary().machine.isNotEmpty()
        } catch (unreadable: Exception) {
            return
        }
        val plan = LifecycleConvergence.planFor(
            LifecycleEvent.COLD_START,
            hasPersistedState = paired,
        )
        if (!plan.reestablishConnection) return
        if (ConnectivityPolicy.ruleFor(RuntimeState.FOREGROUND).socket != SocketDisposition.CONNECTED) {
            return
        }
        try {
            app.start()
            connected = app
        } catch (refused: Exception) {
            outcome.text = FacadeBridge(app).routeFacadeError(refused.message.orEmpty()).message
            return
        }
        observe(app)
    }

    private fun renderUnavailable(startup: PhoneStartup.Unavailable) {
        // PB-APP-9: the ROUTED message, never the platform's own words. A Keystore alias is not
        // a remedy, and `detail` exists for a bug report rather than for a person.
        status.text = startup.error.message

        // THE SHARPEST CASE B44 LEFT OPEN, and the reason the offer is made here too rather than
        // only on the ready path. `PhoneRuntime.construct` opens the sealed store under the
        // content KEK, so a handset whose window has lapsed cannot build a phone AT ALL: every
        // screen is downstream of `runtime.phone()`, and what the user got was "authenticate"
        // with nothing anywhere to authenticate with. The refusal is not cached
        // (`PhoneRuntime.phone` remembers only successes), so a prompt followed by a redraw is
        // the whole recovery.
        showContentUnlock(startup.error)
        notice.text = ""
        peekTitle.text = ""
        peek.text = ""
        session = ""
        setActionsEnabled(false)
        renderLease(null)
        // There is no phone to launch through either, and a live Launch control over one would
        // prompt for a fingerprint to reach a facade that is not there.
        launch.isEnabled = false
    }

    private fun renderReady(startup: PhoneStartup.Ready) {
        // PB-KEY-7's "require a fresh unwrap before restoring content", asked at the moment the
        // screen comes back in front of someone -- which is the moment the device was unlocked to
        // get here, and therefore the moment the Keystore-backed content KEK will answer.
        //
        // It is a REQUEST and its refusal is a state, not an error to swallow: a handset the user
        // has not authenticated on refuses, and PB-APP-9's routed message is what says so. A
        // phone whose content custody is live answers without consulting Keystore at all, so this
        // costs nothing on every other redraw.
        //
        // AND ITS REFUSAL NOW HAS A WAY FORWARD (ADR-007 B44/B59). The comment above used to end
        // "and therefore the moment the Keystore-backed content KEK will answer", which is false
        // in four states -- after a credential unlock, after the biometric idle timeout, after
        // repeated failures, and always with no Class-3 biometric enrolled. When the routed
        // remedy says a fresh authentication fixes it, the control below offers one.
        val contentRefusal = runtime.unlockContent()
        contentRefusal?.let { outcome.text = it.message }
        showContentUnlock(contentRefusal)
        converge(startup.app)
        val bridge = FacadeBridge(startup.app)
        status.text = bridge.connectionBanner().text
        notice.text = CapabilityNotice.of(startup.anomalies)

        // No navigation on this surface, so the target is the first row of the triage inbox --
        // the order TriageInbox already decided is what a user must act on first. Inventing a
        // picker here would be building the app rather than the window.
        session = bridge.triageInbox().sections
            .flatMap { it.rows }
            .firstOrNull()
            ?.id
            .orEmpty()

        // Before the peek is read, and on the empty branch too: a session that has gone away
        // still leaves a watch open on the machine.
        watch(startup.app, session)

        // LAUNCH IS NOT A SESSION CONTROL, and this line is where that difference is stated: the
        // session it starts does not exist yet, so an empty roster is exactly the state a user
        // reaches for it in. Gating it on the roster would leave a freshly paired phone with no
        // way to get its first session, which is section 1's "launches" with no subject again.
        launch.isEnabled = true
        renderLaunch(bridge)

        if (session.isEmpty()) {
            peekTitle.text = ""
            peek.text = ""
            setActionsEnabled(false)
            renderLease(null)
            return
        }
        // PB-INPUT-2: the lease is what the MACHINE answered this screen's own take_control with,
        // claimed by operation id. It was the literal `false` until ADR-007 B83(3), which told
        // every user they held nothing while Send stayed live from a different fact entirely.
        val view = bridge.terminalPeek(session, leaseHeld = leaseConfirmedFor(session, bridge))
        peekTitle.text = view.sessionId
        peek.text = listOf(view.staleNotice, view.rendered).filter { it.isNotEmpty() }.joinToString("\n")
        setActionsEnabled(true)
        renderLease(view)
    }

    /**
     * PB-INPUT-2, drawn: the keyboard follows the model's verdict and the screen SAYS which of
     * the two states the user is in.
     *
     * EVERY ONE OF THE THREE PROPERTIES IS THE MODEL'S. `keyboardEnabled` is `leaseHeld &&
     * online`, and the second half is a separate clause -- a lease cannot be live while the link
     * is down -- so a surface that enabled the keyboard from its own lease flag would satisfy the
     * requirement's first clause and drop the second, silently, while the model that states it
     * stayed green and unread. That is what this file did until now.
     *
     * @param view null when there is no session to hold a lease ON -- an unavailable phone or an
     *  empty roster. The keyboard shuts and the screen says nothing about a lease rather than
     *  asserting the absence of one, because with no session there is no question.
     */
    private fun renderLease(view: TerminalPeek?) {
        if (view == null) {
            lease.text = ""
            takeControl.visibility = View.VISIBLE
            send.isEnabled = false
            typed.isEnabled = false
            return
        }
        lease.text = if (view.showsRelease) LEASE_CONFIRMED else LEASE_NOT_CONFIRMED
        // The control is offered exactly while it is the step to take. There is no Release beside
        // it: `App.ReleaseControl` is still ledgered unbound in android/unbound-verbs.tsv, and a
        // screen that hid the way in without offering a way out would be worse than one that
        // never hid it.
        takeControl.visibility = if (view.showsTakeControl) View.VISIBLE else View.GONE
        send.isEnabled = view.keyboardEnabled
        typed.isEnabled = view.keyboardEnabled
    }

    /**
     * Whether the MACHINE has confirmed a control lease for [session].
     *
     * IT ASKS ABOUT ONE OPERATION -- the take_control this surface issued -- and refuses to
     * answer about any other session, because an outcome attributed by proximity is the error
     * PB-SYNC-2's operation ids exist to prevent. A phone that has taken control of nothing, or
     * whose target moved to a different first row, holds no lease and says so.
     */
    private fun leaseConfirmedFor(session: String, bridge: FacadeBridge): Boolean {
        if (leaseOp.isEmpty() || session != leaseSession) return false
        return try {
            ControlLease.confirmedBy(bridge.launchOutcome(leaseOp))
        } catch (unreadable: Exception) {
            // A facade that cannot answer has not confirmed anything, and fail-closed here is a
            // shut keyboard rather than a keystroke sent against a lease nobody vouched for.
            false
        }
    }

    /**
     * PB-APP-6's second clause: the machine's answer to the launch this screen issued, in the
     * words the machine sent, because the user's next step depends on which refusal it was.
     *
     * It draws nothing until a launch has been issued. [LaunchScreen] holds the operation id and
     * resolves an outcome for anybody else's operation as PENDING, so a screen that has launched
     * nothing is never told something happened.
     */
    private fun renderLaunch(bridge: FacadeBridge) {
        val issued = launchScreen.inFlight ?: return
        val answer = try {
            bridge.launchOutcome(issued.operationId)
        } catch (unreadable: Exception) {
            // Unresolved is the honest state, and the line already says so from the last draw.
            return
        }
        launchStatus.text = launchNotice(launchScreen.resolve(answer))
    }

    /**
     * The rendering in a sentence. The `when` is exhaustive so a result added later has to state
     * its own wording rather than inheriting one, and `retryable` is the model's own distinction
     * between a refusal that waiting fixes and one it does not.
     */
    private fun launchNotice(rendering: LaunchRendering): String = when (rendering.result) {
        LaunchResult.PENDING -> LAUNCH_PENDING
        LaunchResult.LAUNCHED -> LAUNCH_ACCEPTED
        LaunchResult.REJECTED_BY_POLICY,
        LaunchResult.REFUSED_TRANSIENTLY,
        LaunchResult.REFUSED,
        -> if (rendering.retryable) rendering.reason + LAUNCH_RETRYABLE else rendering.reason
    }

    /** What the user typed into the launch form, with nothing supplied on their behalf. */
    private fun draftOnScreen() = LaunchDraft(
        agent = launchAgent.text.toString().trim(),
        cwd = launchCwd.text.toString().trim(),
        prompt = launchPrompt.text.toString(),
    )

    /**
     * The draft as the facade takes it.
     *
     * COLS AND ROWS ARE LEFT AT ZERO DELIBERATELY, and `swarmmobile.LaunchSpec`'s own doc is why:
     * "the Android launch sheet has no terminal view to measure before the session exists, and a
     * refused launch is a worse answer than a conventional grid the user can resize". The peek
     * here is a monospace TextView as wide as the phone, which is not the new session's grid.
     */
    private fun specOf(draft: LaunchDraft) = LaunchSpec().apply {
        agent = draft.agent
        cwd = draft.cwd
        prompt = draft.prompt
    }

    /**
     * Show the unlock control exactly when it would help, and say what to do when it would not.
     *
     * BOTH HALVES ARE THE SAME DEFECT CLASS. A refusal a prompt can fix with no prompt offered is
     * a dead end; a prompt offered for a refusal no prompt can fix is a button the user presses
     * forever. [ContentUnlockPolicy] decides from the ROUTED remedy so the decision is made once,
     * in `ErrorRouter`'s table, rather than restated here where it would drift.
     */
    private fun showContentUnlock(refusal: RoutedError?) {
        if (!ContentUnlockPolicy.offersUnlock(refusal)) {
            unlockContent.visibility = View.GONE
            return
        }
        val availability = prompts.availability()
        unlockContent.visibility = View.VISIBLE
        unlockContent.isEnabled = ContentUnlockPolicy.canPrompt(availability)
        // A handset that cannot prompt gets the advice INSTEAD of a live button, rather than a
        // disabled control with no explanation beside it.
        if (!unlockContent.isEnabled) outcome.text = ContentUnlockPolicy.adviceFor(availability)
    }

    /**
     * The controls that act on the CHOSEN SESSION, raised once the triage inbox yielded a row.
     *
     * The keyboard is not among them any more and that is PB-INPUT-2: a session on screen was
     * never the condition for typing into it, a CONFIRMED LEASE is, and [renderLease] is where
     * that is decided. Launch is not among them either -- it starts a session rather than acting
     * on one.
     */
    private fun setActionsEnabled(enabled: Boolean) {
        // Revoke stays live: it is the panic action, and a phone whose session list is empty
        // (or whose machine is unreachable) is exactly the state its owner may need it in.
        // dropPushToken persists before it speaks to the relay, so an offline revoke still
        // deletes the token that would otherwise let a machine wake a disowned handset.
        takeControl.isEnabled = enabled
        kill.isEnabled = enabled
    }

    /**
     * A control for an operation requirements 6.0 puts in the TIMED tier -- input and
     * take_control, at 60 seconds -- with the overlay defence applied by construction rather than
     * restated at each call site. Every control on this surface is destructive or authorising,
     * which is exactly the set an overlay attack is worth mounting against.
     *
     * The verb's outcome goes on screen. A gated action that reports nothing is the failure
     * PB-APP-9 exists to prevent: the user presses a control, something refuses, and the screen
     * looks identical either way.
     *
     * IT IS A SEPARATE FACTORY FROM [perUseButton] SO THE TIER IS VISIBLE AT THE CALL SITE, and
     * it replaced a plain `gatedButton` that named no tier at all. 6.0 has two rows and this
     * module now has two factories; a control that belongs to neither has to say which it is.
     */
    private fun timedButton(
        text: String,
        operation: GatedOperation,
        verb: (App, String) -> Any?,
    ): Button = SecureWindow.gate(
        Button(activity).apply {
            this.text = text
            setOnClickListener { withFreshTimedAuthorization(operation) { invoke(verb) } }
        },
    )

    /**
     * Requirements 6.0's RENEWAL clause, which had no production caller at all (ADR-007 B83(4)):
     * "a typing session crossing the 60 s freshness window must pause input and re-authorize, not
     * silently continue or silently drop; the lease itself is not ended by freshness expiry".
     *
     * WHAT WAS ACTUALLY UNENFORCED, because the neighbouring path covers less than it looks like
     * it does. take_control, kill, launch and revoke are signed, and signing unseals the content
     * tier per operation through a KEK carrying `setUserAuthenticationParameters(60,
     * AUTH_BIOMETRIC_STRONG)` -- so the PLATFORM refuses those past the window. KEYSTROKES ARE
     * NOT COVERED BY THAT: `App.resolveSend` consults the content KEK only when the in-memory
     * epoch content key is zero, which happens only after a purge, so a foregrounded app answers
     * from memory and never makes the round trip. `ContentLock` installs no foreground timeout
     * either, by its own admission and on its own merits. An unlocked, continuously foregrounded
     * session therefore held shell-input authority for as long as it stayed in front of the user.
     *
     * AND WHERE THE PLATFORM DOES REFUSE, IT REFUSES. It does not pause a running typing session
     * and ask for a fingerprint, which is the clause 6.0 actually writes and what [InputFreshness]
     * exists to decide.
     *
     * NOTHING HERE CLAIMS THE PLATFORM'S HALF. That a timed Keystore key refuses past its window
     * is PB-E2E-5, DEFERRED (ADR-007 B31), and is established nowhere in this repository.
     */
    private fun withFreshTimedAuthorization(operation: GatedOperation, act: () -> Unit) {
        if (freshnessOf(operation) == InputGateDecision.PROCEED) {
            act()
            return
        }
        // PAUSED, which is neither of the two things 6.0 forbids. Not silently continued: the
        // verb is not reached. Not silently dropped: what the user typed stays in the field, they
        // are told why, and the action runs once the prompt below succeeds. And the LEASE IS NOT
        // ENDED -- nothing on this path releases it, which is
        // `InputFreshness.freshnessExpiryEndsLease` being false at the one place it could be
        // contradicted.
        outcome.text = PAUSED_FOR_FRESHNESS
        reauthorizeTimedTier(act)
    }

    /**
     * 6.0's verdict for [operation], from the moment the timed tier was last authorized.
     *
     * NEVER AUTHORIZED IS ITS OWN BRANCH because [InputFreshness.decide] cannot say it from two
     * longs: a missing grant is not an old one, and feeding it a zero would make the verdict a
     * property of the epoch. It answers the same way regardless -- an operation the user has not
     * authenticated for is one they are asked to authenticate for -- but it answers deliberately.
     */
    private fun freshnessOf(operation: GatedOperation): InputGateDecision {
        val granted = ledger.grantedAt(operation) ?: return InputGateDecision.PAUSE_AND_REAUTHORIZE
        return InputFreshness.decide(granted, System.currentTimeMillis())
    }

    /**
     * The timed tier's prompt, and the action it was holding.
     *
     * IT IS THE CONTENT PROMPT AND NOT A SECOND ONE. The timed tier IS the content KEK's window
     * (`BiometricPolicy.specFor`), so what re-opens it is a fresh authentication of the right
     * class -- exactly what [BiometricPrompts.confirmForContent] asks for, and the reason that
     * prompt carries no CryptoObject.
     */
    private fun reauthorizeTimedTier(act: () -> Unit) {
        val availability = prompts.availability()
        if (!ContentUnlockPolicy.canPrompt(availability)) {
            outcome.text = ContentUnlockPolicy.adviceFor(availability)
            return
        }
        prompts.confirmForContent { promptOutcome ->
            if (BiometricPolicy.resolve(promptOutcome) != GateResolution.AUTHORIZED) {
                outcome.text = PerUseRefusalText.messageFor(refusalFor(promptOutcome))
                return@confirmForContent
            }
            grantTimedTier(System.currentTimeMillis())
            act()
        }
    }

    /**
     * Record what the prompt just proved, for BOTH timed operations.
     *
     * BOTH, and it is declared rather than assumed: `BiometricPolicy.sharesAuthorizationWith`
     * says the timed operations share an authorization, because they share one Keystore entry and
     * one window by construction. A grant recorded against only the operation in hand would ask
     * for a second fingerprint to type into the session the first one just took control of.
     *
     * IT GOES THROUGH beginPrompt/endPrompt rather than writing a grant directly, because that
     * pair is the only way an authorization is made -- ADR-007 B63 spent a fix making the ledger
     * refuse everything else -- and because it is a true statement: the prompt above is this
     * tier's prompt. A begin the ledger refuses leaves the grant unmade and the user is asked
     * again, which is the fail-closed direction.
     */
    private fun grantTimedTier(atMillis: Long) {
        for (operation in TIMED_TIER) {
            if (ledger.beginPrompt(operation) != GateResolution.PROMPT_STARTED) continue
            ledger.endPrompt(operation, PromptOutcome.SUCCEEDED, atMillis)
        }
    }

    /**
     * A control for an operation requirements 6.0 puts in the PER-USE tier: the verb runs only
     * after [PerUseGate] has obtained a fresh, CryptoObject-carried authorization for THIS use of
     * THIS operation.
     *
     * IT IS A SEPARATE FACTORY FROM [gatedButton] SO THE DIFFERENCE IS VISIBLE AT THE CALL SITE,
     * and android/gate/s20_pbsec2_peruse_test.go requires every production call of a per-use
     * facade verb to be declared through this one. Sharing a factory with a flag would put the
     * whole of PB-SEC-2's second tier behind a boolean somebody eventually copies wrong -- and a
     * per-use tier that silently behaves as the timed one is exactly the downgrade ADR-007 B51
     * found shipped.
     */
    private fun perUseButton(
        text: String,
        operation: GatedOperation,
        verb: (App, String) -> Any?,
    ): Button = SecureWindow.gate(
        Button(activity).apply {
            this.text = text
            setOnClickListener {
                perUse.authorize(
                    operation,
                    onRefused = { refusal ->
                        // PB-APP-9: the refusal reaches the user. A gated action that reports
                        // nothing leaves the screen looking identical whether it ran or not,
                        // which for "revoke this device" is the worst possible ambiguity.
                        outcome.text = refusal.message
                        render()
                    },
                ) { invoke(verb) }
            }
        },
    )

    /**
     * The content tier's re-authentication, and the retry that follows it.
     *
     * IT IS A RETRY AND NOT AN ASSUMPTION. A successful prompt is a statement about the user;
     * whether the content KEK now answers is the Keystore's to say, and [PhoneRuntime
     * .unlockContent] is asked again so that a refusal which survives the prompt is reported
     * rather than papered over.
     */
    private fun promptForContent() {
        val availability = prompts.availability()
        if (!ContentUnlockPolicy.canPrompt(availability)) {
            outcome.text = ContentUnlockPolicy.adviceFor(availability)
            return
        }
        prompts.confirmForContent { promptOutcome ->
            if (BiometricPolicy.resolve(promptOutcome) != GateResolution.AUTHORIZED) {
                outcome.text = PerUseRefusalText.messageFor(refusalFor(promptOutcome))
                return@confirmForContent
            }
            // The same authentication that re-opens the content tier re-opens 6.0's timed window:
            // it is one window and one Keystore entry. Recording it here is what keeps the
            // freshness check from asking for a second finger immediately after this one.
            grantTimedTier(System.currentTimeMillis())
            render()
        }
    }

    /**
     * A failed content prompt, in the per-use gate's own words. The two prompts fail for the same
     * platform reasons and a second wording table would drift from this one on the first edit.
     */
    private fun refusalFor(promptOutcome: PromptOutcome): PerUseRefusalReason = when (promptOutcome) {
        PromptOutcome.CANCELLED -> PerUseRefusalReason.CANCELLED
        PromptOutcome.FAILED -> PerUseRefusalReason.FAILED
        PromptOutcome.LOCKED_OUT -> PerUseRefusalReason.LOCKED_OUT
        PromptOutcome.LOCKED_OUT_PERMANENT -> PerUseRefusalReason.LOCKED_OUT_PERMANENT
        // Unreachable: SUCCEEDED resolves to AUTHORIZED and the caller returned above.
        PromptOutcome.SUCCEEDED -> PerUseRefusalReason.KEY_NOT_RELEASED
    }

    private fun invoke(verb: (App, String) -> Any?) {
        when (val startup = runtime.phone()) {
            is PhoneStartup.Unavailable -> outcome.text = startup.error.message
            is PhoneStartup.Ready -> outcome.text = try {
                verb(startup.app, session)
                ""
            } catch (failure: Exception) {
                // Everything the facade refuses arrives as an exception whose message carries
                // the error class as a prefix, so it routes through the same table as every
                // other failure rather than being shown raw.
                FacadeBridge(startup.app).routeFacadeError(failure.message.orEmpty()).message
            }
        }
        render()
    }

    private fun label(bold: Boolean = false) = TextView(activity).apply {
        if (bold) setTypeface(typeface, Typeface.BOLD)
        layoutParams = LinearLayout.LayoutParams(MATCH, WRAP)
    }

    /**
     * A text field, by the words that say what belongs in it. The hint IS the label on this
     * surface -- there are no XML layouts here -- so a field added without one is a box a user
     * cannot identify, and android/app/src/test/.../PhoneLaunchSurfaceTest reads exactly these.
     */
    private fun field(hint: String) = EditText(activity).apply {
        this.hint = hint
        layoutParams = LinearLayout.LayoutParams(MATCH, WRAP)
    }

    private companion object {

        /**
         * Requirements 6.0's timed row, DERIVED from the policy rather than listed here. A
         * restated list is a second copy of the tier assignment, and the two would disagree on
         * the first operation anybody added.
         */
        val TIMED_TIER = GatedOperation.entries.filter { !BiometricPolicy.specFor(it).requiresCryptoObject }

        /**
         * What the user is told when the window has lapsed. It says the session is not lost,
         * because it is not: 6.0 ends the freshness, not the lease, and a message that read like
         * a disconnection would send the user looking for a reconnect they do not need.
         */
        const val PAUSED_FOR_FRESHNESS =
            "It is over a minute since you unlocked, so this is paused rather than sent. Your " +
                "session is still yours -- unlock to continue."

        /**
         * PB-INPUT-2's "visibly", in two sentences. The second says what to do about it, because
         * a shut keyboard with no reason beside it is the invisible suppression the requirement
         * is against.
         */
        const val LEASE_CONFIRMED =
            "Your machine has confirmed you have control of this session, so what you type is " +
                "sent live."

        const val LEASE_NOT_CONFIRMED =
            "Your machine has not confirmed control of this session, so the keyboard stays shut " +
                "-- anything typed would be dropped without a word. Take control first."

        /** PB-SYNC-2: an unresolved launch is neither a success nor a failure. */
        const val LAUNCH_PENDING = "Waiting for your machine to answer the launch."

        const val LAUNCH_ACCEPTED = "Your machine started the session."

        const val LAUNCH_RETRYABLE = " This one is worth trying again shortly."

        const val PADDING = 24
        const val MATCH = ViewGroup.LayoutParams.MATCH_PARENT
        const val WRAP = ViewGroup.LayoutParams.WRAP_CONTENT
    }
}
