package dev.swarm.phone

import android.app.Activity
import android.graphics.Typeface
import android.view.View
import android.view.ViewGroup
import android.widget.Button
import android.widget.LinearLayout
import android.widget.ScrollView
import android.widget.TextView
import dev.swarm.phone.ui.FacadeBridge
import dev.swarm.phone.ui.PairingFlow
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
 */
class PhoneSurface(private val activity: Activity, private val runtime: PhoneRuntime) {

    private val status = label(bold = true)
    private val pairing = label()
    private val peekTitle = label(bold = true)
    private val peek = label().apply { typeface = Typeface.MONOSPACE }
    private val outcome = label()

    private val takeControl = gatedButton("Take control") { app, session -> app.takeControl(session) }
    private val kill = gatedButton("Kill session") { app, session -> app.kill(session) }

    /**
     * PB-SEC-7's panic action, and the reason it is on this minimal surface at all: it is the
     * one control whose whole value is being reachable on a handset its owner no longer trusts.
     * It revokes THIS device. The kill switch is owner-tier only and this app can never set it,
     * so revoke is the phone-side response to a lost device (mobile/screen_coverage.tsv).
     */
    private val revoke = gatedButton("Revoke this device") { app, _ -> app.revokeThisDevice() }

    /** The session the controls act on, chosen in [renderReady] and never from an Intent. */
    private var session: String = ""

    val root: View = ScrollView(activity).apply {
        addView(
            LinearLayout(activity).apply {
                orientation = LinearLayout.VERTICAL
                setPadding(PADDING, PADDING, PADDING, PADDING)
                layoutParams = ViewGroup.LayoutParams(MATCH, MATCH)
                for (child in listOf(status, pairing, peekTitle, peek, takeControl, kill, revoke, outcome)) {
                    addView(child)
                }
            },
        )
    }

    /**
     * The controls PB-SEC-12 clause 1 is about, exposed so the assertion has a named subject
     * rather than having to guess which views in a hierarchy are the gated ones.
     */
    val gatedActions: List<View> = listOf(takeControl, kill, revoke)

    /**
     * Draw. Called from onResume, so a phone that was unavailable when the screen opened -- a
     * locked handset, or a key the user has not authenticated for yet -- redraws once it is not.
     */
    fun render() {
        when (val startup = runtime.phone()) {
            is PhoneStartup.Unavailable -> renderUnavailable(startup)
            is PhoneStartup.Ready -> renderReady(FacadeBridge(startup.app))
        }
    }

    private fun renderUnavailable(startup: PhoneStartup.Unavailable) {
        // PB-APP-9: the ROUTED message, never the platform's own words. A Keystore alias is not
        // a remedy, and `detail` exists for a bug report rather than for a person.
        status.text = startup.error.message
        pairing.text = ""
        peekTitle.text = ""
        peek.text = ""
        session = ""
        setActionsEnabled(false)
    }

    private fun renderReady(bridge: FacadeBridge) {
        status.text = bridge.connectionBanner().text

        // PB-PAIR-4 resumes the persisted step. Nothing persists one yet, so this resolves to
        // SCAN and says so in PairingFlow's own words. The surface exists to carry the window
        // protections and for S19's smoke to drive; it does not run a pairing on its own.
        pairing.text = PairingFlow.messageFor(PairingFlow.restore(null).step)

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
