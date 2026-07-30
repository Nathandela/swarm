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
import dev.swarm.phone.ui.FacadeBridge
import dev.swarm.phone.ui.RoutedError
import swarmmobile.App

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
 */
class PhoneSurface(
    private val activity: AppCompatActivity,
    private val runtime: PhoneRuntime,
    /**
     * THE PROCESS'S ONE [AuthorizationLedger], passed in rather than defaulted. It is
     * `SwarmApplication.contentLock`'s, which is the only one every [InvalidationEvent] reaches;
     * a default here would be a parameter somebody eventually leaves off (ADR-007 B18(c)), and
     * what they would get is a per-use gate no screen lock can clear.
     */
    ledger: AuthorizationLedger,
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

    private val pairing = PairingSurface(activity, runtime)
    private val settings = SettingsSurface(activity, runtime)

    private val takeControl = gatedButton("Take control") { app, session -> app.takeControl(session) }

    /**
     * PER-USE (requirements 6.0), so it goes through [perUse] and not through [gatedButton].
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
    private val typed = EditText(activity).apply {
        hint = "Type into the session you hold"
        layoutParams = LinearLayout.LayoutParams(MATCH, WRAP)
    }

    /**
     * ONE CARRIAGE RETURN IS APPENDED, AND THAT IS WHAT "A LINE" MEANS AT A TERMINAL -- the key
     * a shell waits for is CR, and a control that sent the characters without it would leave the
     * user's command sitting unsubmitted with nothing on screen explaining why.
     *
     * The bytes are UTF-8 and nothing on this side interprets them. There is no VT emulator on
     * the handset (ADR-007 D2): what goes out is what was typed.
     */
    private val send = gatedButton("Send line") { app, session ->
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

    /** The session the controls act on, chosen in [renderReady] and never from an Intent. */
    private var session: String = ""

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
                    status, notice, unlockContent, pairing.root, peekTitle, peek, takeControl,
                    typed, send, kill, revoke, settings.root, outcome,
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
        listOf(takeControl, send, kill, revoke, unlockContent) +
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

        if (session.isEmpty()) {
            peekTitle.text = ""
            peek.text = ""
            setActionsEnabled(false)
            return
        }
        // leaseHeld is false because this surface never takes a lease on its own. It is a
        // PARAMETER on the bridge for exactly this reason: the lease is the outcome of a
        // take_control reply, and reading it back off a snapshot would be a guess.
        val view = bridge.terminalPeek(session, leaseHeld = false)
        peekTitle.text = view.sessionId
        peek.text = listOf(view.staleNotice, view.rendered).filter { it.isNotEmpty() }.joinToString("\n")
        setActionsEnabled(true)
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

    private fun setActionsEnabled(enabled: Boolean) {
        // Revoke stays live: it is the panic action, and a phone whose session list is empty
        // (or whose machine is unreachable) is exactly the state its owner may need it in.
        // dropPushToken persists before it speaks to the relay, so an offline revoke still
        // deletes the token that would otherwise let a machine wake a disowned handset.
        takeControl.isEnabled = enabled
        kill.isEnabled = enabled
        // The keyboard follows the same rule: with no session chosen there is nothing to type
        // into, and a live field over an empty roster invites a user to type at nothing.
        send.isEnabled = enabled
        typed.isEnabled = enabled
    }

    /**
     * A control that reaches a facade verb, with the overlay defence applied by construction
     * rather than restated at each call site. Every one of these is destructive or authorising,
     * which is exactly the set an overlay attack is worth mounting against.
     *
     * The verb's outcome goes on screen. A gated action that reports nothing is the failure
     * PB-APP-9 exists to prevent: the user presses revoke, something refuses, and the screen
     * looks identical either way.
     */
    private fun gatedButton(text: String, verb: (App, String) -> Any?): Button =
        SecureWindow.gate(
            Button(activity).apply {
                this.text = text
                setOnClickListener { invoke(verb) }
            },
        )

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

    private companion object {
        const val PADDING = 24
        const val MATCH = ViewGroup.LayoutParams.MATCH_PARENT
        const val WRAP = ViewGroup.LayoutParams.WRAP_CONTENT
    }
}
