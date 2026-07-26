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
import dev.swarm.phone.runtime.ConnectivityPolicy
import dev.swarm.phone.runtime.LifecycleConvergence
import dev.swarm.phone.runtime.LifecycleEvent
import dev.swarm.phone.runtime.RuntimeState
import dev.swarm.phone.runtime.SocketDisposition
import dev.swarm.phone.ui.FacadeBridge
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
 * to put a switch on. The panels are hosted here rather than in Activities of their own so that
 * [SecureWindow.protect]'s one window still covers every one of them.
 */
class PhoneSurface(private val activity: AppCompatActivity, private val runtime: PhoneRuntime) {

    private val status = label(bold = true)
    private val peekTitle = label(bold = true)
    private val peek = label().apply { typeface = Typeface.MONOSPACE }
    private val outcome = label()

    private val pairing = PairingSurface(activity, runtime)
    private val settings = SettingsSurface(activity, runtime)

    private val takeControl = gatedButton("Take control") { app, session -> app.takeControl(session) }
    private val kill = gatedButton("Kill session") { app, session -> app.kill(session) }

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
    private val revoke = gatedButton("Revoke this device") { app, _ -> app.revokeThisDevice() }

    /** The session the controls act on, chosen in [renderReady] and never from an Intent. */
    private var session: String = ""

    /** The phone this surface has started, so [release] can stop the one it actually started. */
    private var connected: App? = null

    val root: View = ScrollView(activity).apply {
        addView(
            LinearLayout(activity).apply {
                orientation = LinearLayout.VERTICAL
                setPadding(PADDING, PADDING, PADDING, PADDING)
                layoutParams = ViewGroup.LayoutParams(MATCH, MATCH)
                for (child in listOf(
                    status, pairing.root, peekTitle, peek, takeControl, typed, send, kill,
                    revoke, settings.root, outcome,
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
        listOf(takeControl, send, kill, revoke) + pairing.gatedActions + settings.gatedActions

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
        val live = connected ?: return
        if (ConnectivityPolicy.ruleFor(RuntimeState.BACKGROUND).socket != SocketDisposition.CLOSED) {
            return
        }
        connected = null
        try {
            live.stop()
        } catch (refused: Exception) {
            // Stop is idempotent and the process may be going away regardless. There is no user
            // present on this path and no screen left to report to.
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
        }
    }

    private fun renderUnavailable(startup: PhoneStartup.Unavailable) {
        // PB-APP-9: the ROUTED message, never the platform's own words. A Keystore alias is not
        // a remedy, and `detail` exists for a bug report rather than for a person.
        status.text = startup.error.message
        peekTitle.text = ""
        peek.text = ""
        session = ""
        setActionsEnabled(false)
    }

    private fun renderReady(startup: PhoneStartup.Ready) {
        converge(startup.app)
        val bridge = FacadeBridge(startup.app)
        status.text = bridge.connectionBanner().text

        // No navigation on this surface, so the target is the first row of the triage inbox --
        // the order TriageInbox already decided is what a user must act on first. Inventing a
        // picker here would be building the app rather than the window.
        session = bridge.triageInbox().sections
            .flatMap { it.rows }
            .firstOrNull()
            ?.id
            .orEmpty()

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
