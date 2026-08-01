package dev.swarm.phone

import android.text.format.DateFormat
import android.view.View
import android.view.ViewGroup
import android.view.accessibility.AccessibilityNodeInfo
import android.widget.Button
import android.widget.EditText
import android.widget.FrameLayout
import android.widget.LinearLayout
import android.widget.ScrollView
import android.widget.TextView
import androidx.appcompat.app.AppCompatActivity
import dev.swarm.phone.runtime.ConnectivityPolicy
import dev.swarm.phone.runtime.LifecycleConvergence
import dev.swarm.phone.runtime.LifecycleEvent
import dev.swarm.phone.runtime.RuntimeState
import dev.swarm.phone.runtime.SocketDisposition
import dev.swarm.phone.ui.CapabilityNotice
import dev.swarm.phone.ui.ControlLease
import dev.swarm.phone.ui.FacadeBridge
import dev.swarm.phone.ui.LaunchDraft
import dev.swarm.phone.ui.LaunchRendering
import dev.swarm.phone.ui.LaunchScreen
import dev.swarm.phone.ui.kit.CtaKind
import dev.swarm.phone.ui.kit.ctaButton
import dev.swarm.phone.ui.kit.textField
import dev.swarm.phone.ui.screens.InboxScreen
import dev.swarm.phone.ui.screens.LaunchFieldId
import dev.swarm.phone.ui.screens.LaunchPanel
import dev.swarm.phone.ui.screens.LaunchPanelScreen
import dev.swarm.phone.ui.screens.PeekPanel
import dev.swarm.phone.ui.screens.PeekPanelScreen
import dev.swarm.phone.ui.screens.TriageInboxScreen
import dev.swarm.phone.ui.screens.launchPanelView
import dev.swarm.phone.ui.screens.peekPanelView
import dev.swarm.phone.ui.screens.triageInboxView
import java.util.Date
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
 * S24 REPLACED THE TOP OF THAT SCOPE WITH A REAL SCREEN (PB-DS-6, PB-DS-9). This surface was one
 * flat `LinearLayout` of twenty unstyled views under a single 24 dp padding, and it consumed
 * `TriageInbox` -- four Groups, sections, empty states, the whole triage design -- as
 * `.flatMap{}.firstOrNull()?.id`, a session picker that discarded every section, row, label and
 * grouping the model had just built. The root of the window is now
 * [dev.swarm.phone.ui.screens.triageInboxView], composed entirely from `ui/kit`, and the
 * remaining controls hang below it as [unrecomposedControls] until they have components of their
 * own. The session picker exists: rows are tappable and the scope bar narrows the list.
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
 * THERE IS NO LOCAL AUTHENTICATION ON ANY CONTROL BELOW (ADR-007 B133). The trust boundary is
 * the WIRE between phone and computer; the phone and whoever is holding it are trusted the way
 * the Mac's owner-uid user has always been. So take_control, input, kill, launch and revoke are
 * plain buttons reaching plain verbs, and the accepted residual is recorded in B133: a stolen
 * unlocked phone gives its holder full control of agents on the machine, and the only surviving
 * mitigation is `swarm remote off` / device revoke issued FROM THE COMPUTER.
 *
 * WHAT SURVIVES ON THESE CONTROLS IS [SecureWindow.gate], which is a DIFFERENT protection
 * against a different attack: PB-SEC-12 clause 1's touch filter discards a tap that arrived
 * while another window covered the view. It matters MORE now than it did, because there is no
 * longer a second checkpoint behind revoke or take-control.
 *
 * THE SCOPE RULING ABOVE NO LONGER COVERS THE LAUNCH FORM, and the paragraph is amended rather
 * than left standing, because android/unbound-verbs.tsv used to cite it BY NAME as the reason
 * `App.Launch` was deliberately unbound. ADR-007 B80 is the record of what that costs: the
 * ledger said the launch screen did not exist, the traceability table said PB-APP-6 was shipped,
 * and nothing anywhere joined the two. Section 1's binding exit criterion is a phone that
 * "pairs, observes, LAUNCHES, and types into a real session", so the fields and the control that
 * start a session are below. What is still not here is a machine pane; the session picker
 * arrived with S24's inbox, and the launch form needs none because the session it starts does not
 * exist yet.
 */
class PhoneSurface(
    private val activity: AppCompatActivity,
    private val runtime: PhoneRuntime,
) {

    private val status = label(heading = true)

    /**
     * PB-KEY-8's non-fatal half. [dev.swarm.phone.keys.CustodyPlanner] records a capability the
     * handset did not confirm that no matrix row consumes; until this label existed the record
     * was computed on every launch and read by nobody.
     */
    private val notice = label()
    private val outcome = label()

    /**
     * PB-DS-9: the terminal peek, rebuilt into a host of its own.
     *
     * IT IS A HOST AND NOT A PANEL because the peek changes on a different clock from everything
     * around it. The inbox is redrawn only when [InboxScreen] changes -- [renderInbox] argues why
     * -- and a machine printing steadily changes the snapshot on every journal event. Composing
     * the peek inside the inbox's tree would tie one to the other: either the peek would stop
     * updating, or the list would be thrown back to the top under whoever was scrolling it.
     *
     * WHAT THE PEEK USED TO BE, so the size of the change is on the record: a heading `TextView`
     * holding the session id, a mono well, and a lease sentence -- three loose children of the
     * flat column below, with `renderLease` setting a visibility and two enabled flags over them.
     * It is now [PeekPanel] and one composition (inventory C3, derivation §4 and row 22).
     */
    private val peekHost = LinearLayout(activity).apply {
        orientation = LinearLayout.VERTICAL
        layoutParams = LinearLayout.LayoutParams(MATCH, WRAP)
    }

    /** PB-APP-6's form, hosted for [peekHost]'s reason: it is redrawn when its notice changes. */
    private val launchHost = LinearLayout(activity).apply {
        orientation = LinearLayout.VERTICAL
        layoutParams = LinearLayout.LayoutParams(MATCH, WRAP)
    }

    private val pairing = PairingSurface(activity, runtime)
    private val settings = SettingsSurface(activity, runtime)

    /**
     * IT REMEMBERS THE OPERATION IT ISSUED, which is what makes PB-INPUT-2's lease a fact rather
     * than a literal. The lease is not on any snapshot: it is the outcome of THIS take_control,
     * claimed by operation id, and [leaseConfirmedFor] is what asks the machine about it.
     */
    private val takeControl = ctaAction("Take control", CtaKind.MORE) { app, session ->
        app.takeControl(session).also { issued ->
            leaseOp = issued.operationID
            leaseSession = session
        }
    }

    private val kill = actionButton("Kill session") { app, session ->
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
    private val send = actionButton("Send line") { app, session ->
        val line = typed.text.toString()
        app.sendInput(session, (line + "\r").toByteArray(Charsets.UTF_8))
        typed.text.clear()
    }

    /**
     * PB-SEC-7's panic action, and the reason it is on this minimal surface at all: it is the
     * one control whose whole value is being reachable on a handset its owner no longer trusts.
     * It revokes THIS device. The kill switch is owner-tier only and this app can never set it,
     * so revoke is the phone-side response to a lost device (mobile/screen_coverage.tsv).
     *
     * IT IS NOW THE ONLY SURVIVING MITIGATION ON THIS SIDE, which is what ADR-007 B133 accepts
     * and what makes the purge below mandatory rather than tidy: with no local authentication
     * anywhere, a handset the owner no longer trusts is answered by revoking it from the machine
     * or by this control, and nothing else.
     *
     * THE KEYS GO WITH THE REGISTRATION (B133 decision 3). A revoke that left the epoch keys
     * live would leave a device its owner has disowned still holding the material to open the
     * session content it already received. Both tiers go and neither comes back without pairing
     * again, which is the point.
     *
     * THE PURGE IS IN A `finally`, AND THAT IS THE PANIC ACTION'S SEMANTICS RATHER THAN
     * defensive style. `App.RevokeThisDevice` drops the push token durably and then issues a
     * signed command; the command can refuse, and the whole situation this control exists for is
     * one where the phone may not reach its machine. A purge that ran only on success would
     * leave the live keys on exactly the handset the user was trying to disarm.
     */
    private val revoke = actionButton("Revoke this device") { app, _ ->
        try {
            app.revokeThisDevice()
        } finally {
            runtime.purgeKeys()
        }
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
    private val launchAgent = field(LaunchFieldId.AGENT)

    private val launchCwd = field(LaunchFieldId.CWD)

    private val launchPrompt = field(LaunchFieldId.PROMPT)

    /**
     * THE DRAFT IS REFUSED BEFORE IT IS SENT, by the model's own bar. A launch missing a required
     * field is refused at the machine too, but only after burning a durable command seq and a
     * signature on a request the phone could see was incomplete.
     */
    private val launch = ctaAction("Launch a session", CtaKind.APPROVE) { app, _ ->
        draftOnScreen().let { draft ->
            when (val missing = launchScreen.missingField(draft)) {
                null -> {
                    launchRefusal = ""
                    launchScreen.submit(draft, app.launch(specOf(draft)).operationID)
                }
                else -> launchRefusal = missing
            }
        }
    }

    /**
     * PB-APP-6's screen model, which decided what a launch screen shows and was consulted by
     * nothing. It holds the operation id the MACHINE keyed the launch by, so the answer is
     * claimed by that id (PB-SYNC-2) rather than resolved by proximity.
     */
    private val launchScreen = LaunchScreen()

    /** The session the controls act on, chosen in [renderReady] and never from an Intent. */
    private var session: String = ""

    /**
     * The session the USER tapped, which is not the same fact as [session].
     *
     * [session] is what the controls act on and always resolves to something while the roster has
     * anything in it; this is a choice, and it is empty until somebody makes one. Keeping them
     * apart is what lets a tapped session that has since gone away fall back to the first row
     * without the screen claiming the user chose that.
     */
    private var chosen: String = ""

    /** The machine the scope bar has been narrowed to, or null for all of them. */
    private var scope: String? = null

    /** What the inbox last drew, so a redraw that changes nothing rebuilds nothing. */
    private var inboxDrawn: InboxScreen? = null

    /**
     * What the peek and the launch form last drew, for [inboxDrawn]'s reason and one more.
     *
     * THE LAUNCH FORM'S IS THE ONE THAT MATTERS TO A PERSON. The three fields are views this
     * surface owns and hands to the composition; rebuilding the panel re-parents them, and a
     * re-parented `EditText` loses focus. [render] runs on every resume, after every action AND on
     * every journal event, so a form rebuilt unconditionally would take the keyboard away from
     * somebody halfway through typing a working directory, at whatever rate their agents happen to
     * be producing events. [LaunchPanel] is a data class, so "has anything on it changed" is one
     * comparison, and the only thing that ever changes is the notice.
     */
    private var peekDrawn: PeekPanel? = null

    private var launchDrawn: LaunchPanel? = null

    /**
     * The machine's answer to the launch this screen issued, or null while it has issued none.
     *
     * NULL IS NOT PENDING, which is [LaunchPanelScreen.of]'s own distinction: a form nobody has
     * submitted is not waiting for anything, and saying it is would be a status about an operation
     * that does not exist.
     */
    private var launchAnswer: LaunchRendering? = null

    /**
     * The form's OWN refusal -- a draft missing a required field, which never reached a machine.
     *
     * IT TAKES THE SAME LINE AS THE MACHINE'S ANSWER AND THE TWO CANNOT COLLIDE: a draft refused
     * here was never sent, so there is no operation for an answer to arrive about, and a draft
     * that was sent cleared this on its way out. There is one line because there is one thing to
     * say -- what happened to the launch you asked for -- and [LaunchPanel.notice] is that line.
     */
    private var launchRefusal: String = ""

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

    /**
     * The controls S24 has NOT recomposed, kept together so what is left to do is one object
     * rather than eighteen loose children.
     *
     * THIS USED TO BE THE WHOLE APP: one flat `LinearLayout` holding twenty unstyled views under a
     * single 24 dp padding, which was the entire spatial output of the product. PB-DS-9 replaces
     * it with real screens and puts the triage inbox first, so what is here now is the remainder.
     *
     * WHAT IS LEFT IN IT AFTER THE LAST TWO SCREENS, which is the honest list. [peekHost] and
     * [launchHost] are composed panels rather than loose views; the pairing panel and the settings
     * panel compose themselves. What is genuinely unrecomposed is the status line, the capability
     * notice, the outcome line, and the four SESSION CONTROLS -- the keyboard, Send, Kill session
     * and the routed-error line. Those four are inventory C2's composer (derivation row 9's bar,
     * its 26 dp glyphs and its stop control) and C2 is not built: the session-detail screen, its
     * transcript, its tool cards and its quick-reply chips have no factory and no model. A field
     * and two buttons standing in for that bar are the remainder, and they are here rather than
     * pretending to be a screen.
     *
     * IT CARRIES NO PADDING OF ITS OWN ANY MORE. The 24 dp was the last thing on this surface
     * deciding a spatial value; the kit components above it carry theirs, and these views are
     * unstyled while they wait for the components that will style them.
     */
    private val unrecomposedControls = LinearLayout(activity).apply {
        orientation = LinearLayout.VERTICAL
        layoutParams = LinearLayout.LayoutParams(MATCH, WRAP)
        for (child in listOf(
            status, notice, pairing.root, peekHost,
            typed, send, kill, launchHost, revoke, settings.root, outcome,
        )) {
            addView(child)
        }
    }

    /** Carries [unrecomposedControls] on the one branch that has no inbox to scroll them. */
    private val controlsScroll = ScrollView(activity).apply {
        isVerticalScrollBarEnabled = false
        layoutParams = ViewGroup.LayoutParams(MATCH, MATCH)
    }

    /**
     * The window's one child. The inbox is rebuilt into it whenever what it shows changes; on a
     * phone that cannot start at all it holds the controls alone, because there is no roster to
     * draw an inbox from.
     */
    private val host = FrameLayout(activity).apply {
        // A glowing dot and the tab badge are drawn outside their own views.
        clipChildren = false
        clipToPadding = false
        layoutParams = ViewGroup.LayoutParams(MATCH, MATCH)
    }

    val root: View = host

    /**
     * The controls PB-SEC-12 clause 1 is about, exposed so the assertion has a named subject
     * rather than having to guess which views in a hierarchy carry the touch filter.
     *
     * IT IS NAMED FOR THE TOUCH FILTER AND NOT FOR A GATE (ADR-007 B133). It was `gatedActions`,
     * and after the biometric gate left the product that name would have read as a protection
     * this list no longer has anything to do with -- while what it actually carries, PB-SEC-12
     * clause 1, SURVIVES and matters more: there is no second checkpoint behind revoke or
     * take-control any more.
     *
     * EVERY PANEL'S CONTROLS ARE IN IT. A per-screen list is how the screen added last gets
     * missed, with nothing failing -- so a new panel contributes its own set here rather than
     * being remembered about.
     */
    val touchFilteredActions: List<View> =
        listOf(takeControl, send, kill, launch, revoke) +
            pairing.touchFilteredActions + settings.touchFilteredActions

    /**
     * Draw. Called from onResume, so a phone that was unavailable when the screen opened -- a
     * handset whose Keystore or state directory refused -- redraws once it is not.
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

        notice.text = ""
        session = ""
        setActionsEnabled(false)
        // NO PANEL RATHER THAN AN EMPTY ONE. A peek with no session is not a peek showing nothing
        // -- there is no session to hold a lease on, so the screen says nothing about a lease
        // rather than asserting the absence of one.
        drawPeek(null)
        setKeyboardEnabled(false)
        // There is no phone to launch through either. The FORM still draws: it is the one thing on
        // this branch a user could reasonably be reaching for, and a handset whose core refused
        // needs to be able to see what it will be asked for once it has not.
        launch.isEnabled = false
        drawLaunch()
        // AND NO INBOX. The roster comes from the phone core, so a handset whose core refused
        // construction has no sections to draw and no counts to state -- and a triage inbox
        // rendered over nothing would say "nothing is waiting on you", which is a claim about the
        // machine that this phone is in no position to make.
        hostControlsAlone()
    }

    private fun renderReady(startup: PhoneStartup.Ready) {
        // PB-KEY-7's "require a fresh unwrap before restoring content", asked at the moment the
        // screen comes back in front of someone.
        //
        // WHAT IT ASKS IS NOW KEY AVAILABILITY, NOT AUTHENTICATION (ADR-007 B133 decision 2).
        // The content KEK carries `setUserAuthenticationRequired(false)`, so the unwrap no longer
        // refuses over a user who has not authenticated; what it can still refuse over is a
        // destroyed key, a missing entry or a platform fault, and each of those is a state
        // PB-APP-9 renders rather than an error to swallow. A phone whose content custody is
        // already live answers without consulting Keystore at all, so this costs nothing on
        // every other redraw.
        runtime.unlockContent()?.let { outcome.text = it.message }
        converge(startup.app)
        val bridge = FacadeBridge(startup.app)
        // PB-APP-11 rides the same line as the connection banner, and it has to: the banner is
        // the TRANSPORT's opinion, and a relay that answers every poll with an empty page while
        // withholding the machine's frames leaves it reading "Connected to your machine." with
        // nothing behind it. The freshness notice is the only thing on this screen that comes
        // from the machine's own clock.
        // PB-DS-9: the inbox is drawn BEFORE the status line is written, because the line now
        // carries the roster's own PB-APP-8 verdict alongside the transport's.
        val inbox = renderInbox(bridge)

        status.text = listOfNotNull(
            bridge.connectionBanner().text,
            bridge.machineFreshness().notice { millis ->
                DateFormat.getTimeFormat(status.context).format(Date(millis))
            },
            // PB-APP-8 for the roster. `TriageInbox.staleNotice` decided the wording in S16 and
            // reached no user until now: a list drawn from a holed journal may be missing a
            // session, an exit or a needs_input, and the one screen that must never present that
            // as live is the one a person triages from.
            inbox.staleNotice,
        ).filter { it.isNotEmpty() }.joinToString(" ")
        notice.text = CapabilityNotice.of(startup.anomalies)

        // THE TARGET IS THE ROW ON SCREEN. It used to be
        // `triageInbox().sections.flatMap{}.firstOrNull()?.id` -- a session picker that discarded
        // every section, row, label and grouping the model had just built. The rows are now
        // rendered and tappable, so the session the controls act on is the one somebody chose,
        // falling back to the first row in triage order while nobody has: an unchosen target has
        // to be something, and TriageInbox already decided what a user must act on first.
        session = targetOf(inbox)

        // Before the peek is read, and on the empty branch too: a session that has gone away
        // still leaves a watch open on the machine.
        watch(startup.app, session)

        // LAUNCH IS NOT A SESSION CONTROL, and this line is where that difference is stated: the
        // session it starts does not exist yet, so an empty roster is exactly the state a user
        // reaches for it in. Gating it on the roster would leave a freshly paired phone with no
        // way to get its first session, which is section 1's "launches" with no subject again.
        launch.isEnabled = true
        renderLaunch(bridge)
        drawLaunch()

        if (session.isEmpty()) {
            drawPeek(null)
            setActionsEnabled(false)
            setKeyboardEnabled(false)
            return
        }
        // PB-INPUT-2: the lease is what the MACHINE answered this screen's own take_control with,
        // claimed by operation id. It was the literal `false` until ADR-007 B83(3), which told
        // every user they held nothing while Send stayed live from a different fact entirely.
        val view = bridge.terminalPeek(session, leaseHeld = leaseConfirmedFor(session, bridge))
        drawPeek(PeekPanelScreen.of(view))
        setActionsEnabled(true)
        // EVERY ONE OF THE THREE PROPERTIES IS THE MODEL'S, which is what [PeekPanel] carries:
        // `keyboardEnabled` is `leaseHeld && online`, and the second half is a separate clause --
        // a lease cannot be live while the link is down. A surface that enabled the keyboard from
        // its own lease flag would satisfy the requirement's first clause and drop the second,
        // silently, while the model that states it stayed green and unread.
        setKeyboardEnabled(view.keyboardEnabled)
    }

    // -----------------------------------------------------------------------
    // PB-DS-9: the triage inbox.
    // -----------------------------------------------------------------------

    /**
     * Build the inbox's model, and redraw it only if it has changed.
     *
     * THE EQUALITY CHECK IS NOT AN OPTIMISATION. [render] runs on every resume, after every gated
     * action AND on every journal event, so rebuilding the view hierarchy each time would throw
     * the list back to the top under whoever was scrolling it -- while an agent working steadily
     * produces events steadily. [InboxScreen] is a data class of data classes, so "has anything a
     * user can see changed" is one comparison.
     */
    private fun renderInbox(bridge: FacadeBridge): InboxScreen {
        val next = TriageInboxScreen.of(
            inbox = bridge.triageInbox(),
            scope = scope,
            selectedSession = chosen.takeIf { it.isNotEmpty() },
        )
        if (next == inboxDrawn && host.childCount > 0) return next
        inboxDrawn = next
        detachControls()
        host.removeAllViews()
        host.addView(
            triageInboxView(
                context = activity,
                screen = next,
                onSelectSession = ::selectSession,
                onSelectScope = ::selectScope,
                below = unrecomposedControls,
            ),
        )
        return next
    }

    /**
     * The window with no inbox in it, for a phone that could not start.
     *
     * IT SCROLLS, and that is not cosmetic: this is the state a handset reaches when its Keystore
     * or state directory refuses, and the pairing panel -- the one thing that might get it out of
     * that state -- is halfway down the column. When the inbox is drawn the scroll is the inbox's
     * own; on this branch there is no inbox to lend one.
     */
    private fun hostControlsAlone() {
        inboxDrawn = null
        if (unrecomposedControls.parent === controlsScroll && controlsScroll.parent === host) return
        detachControls()
        controlsScroll.removeAllViews()
        controlsScroll.addView(unrecomposedControls)
        host.removeAllViews()
        host.addView(controlsScroll)
    }

    /**
     * Take the controls out of whatever last held them.
     *
     * `removeAllViews` on the host detaches the INBOX, and the controls are two levels inside it
     * -- so without this they arrive at their next `addView` still claiming a parent, and Android
     * refuses that with "the specified child already has a parent".
     */
    private fun detachControls() {
        (unrecomposedControls.parent as? ViewGroup)?.removeView(unrecomposedControls)
    }

    /**
     * The session the controls act on: the one somebody tapped, or the first row in triage order.
     *
     * A CHOSEN SESSION THAT IS NOT ON SCREEN IS NOT A TARGET. It may have exited, or the scope may
     * have moved off its machine, and acting on a session the user can no longer see is the
     * proximity error PB-SYNC-2 exists to forbid one level up. The fall-back is the rule this
     * surface has always used, which [TriageInbox.TRIAGE_ORDER] already decided.
     */
    private fun targetOf(screen: InboxScreen): String {
        val rows = screen.sections.flatMap { it.rows }
        return (rows.firstOrNull { it.selected } ?: rows.firstOrNull())?.id.orEmpty()
    }

    private fun selectSession(id: String) {
        chosen = id
        render()
    }

    private fun selectScope(machine: String?) {
        scope = machine
        // A scope change can move the target off screen, so the choice is dropped with it rather
        // than left pointing at a session this scope does not show.
        chosen = ""
        render()
    }

    /**
     * PB-DS-9: the terminal peek, composed rather than shown and hidden.
     *
     * **THE VISIBILITY WRITES ARE GONE AND THAT IS THE POINT OF THIS FUNCTION.** What stood here
     * was `renderLease`, which set `takeControl.visibility` from `showsTakeControl` and blanked
     * two `TextView`s on the null branch -- a second, contradictable statement of what is on the
     * screen, made three lines away from the composition that put it there. A view that is not on
     * screen is now a view this did not add. `android/gate/s24_screens_test.go` fences the screen
     * package against the pattern; the surface is where the last of it lived.
     *
     * The Take control button is offered exactly while it is the step to take -- there is no
     * Release beside it, because `App.ReleaseControl` is still ledgered unbound in
     * `android/unbound-verbs.tsv` and a screen that hid the way in without offering a way out
     * would be worse than one that never hid it. That condition is [PeekPanel.offersTakeControl]
     * and the composition reads it.
     *
     * @param panel null when there is no session to hold a lease ON -- an unavailable phone or an
     *  empty roster. Nothing is composed, because with no session there is no question.
     */
    private fun drawPeek(panel: PeekPanel?) {
        if (panel == peekDrawn) return
        peekDrawn = panel
        peekHost.removeAllViews()
        if (panel == null) return
        peekHost.addView(peekPanelView(activity, panel, takeControl))
    }

    /**
     * The keyboard's two controls, which are NOT part of the peek.
     *
     * They are inventory C2's composer -- derivation row 9 specifies a translucent bar with a
     * recessed field, a voice glyph and a stop control -- and C2 is unbuilt, so what is here is a
     * field and a button standing in for it. They stay in the unrecomposed remainder rather than
     * being composed into a panel that does not specify them.
     */
    private fun setKeyboardEnabled(enabled: Boolean) {
        send.isEnabled = enabled
        typed.isEnabled = enabled
    }

    /**
     * PB-APP-6's form, composed. It draws on both branches -- see [renderUnavailable].
     *
     * IT REBUILDS ONLY WHEN THE PANEL CHANGES, and [launchDrawn] carries why: the three fields are
     * views this surface owns, re-parenting an `EditText` takes the keyboard away from it, and
     * this runs on every journal event.
     */
    private fun drawLaunch() {
        val panel = launchPanelOnScreen()
        if (panel == launchDrawn) return
        launchDrawn = panel
        launchHost.removeAllViews()
        launchHost.addView(
            launchPanelView(
                context = activity,
                panel = panel,
                fieldFor = ::launchField,
                submit = launch,
            ),
        )
    }

    /** The form as it stands: the machine's answer, or this form's own refusal to send one. */
    private fun launchPanelOnScreen(): LaunchPanel {
        val panel = LaunchPanelScreen.of(launchAnswer)
        return if (launchRefusal.isEmpty()) panel else panel.copy(notice = launchRefusal)
    }

    /**
     * The box for one field.
     *
     * The `when` is exhaustive over [LaunchFieldId], so a fourth field cannot be added to the
     * model and silently reach a form with three boxes in it.
     */
    private fun launchField(id: LaunchFieldId): EditText = when (id) {
        LaunchFieldId.AGENT -> launchAgent
        LaunchFieldId.CWD -> launchCwd
        LaunchFieldId.PROMPT -> launchPrompt
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
        // THE SENTENCE IS THE MODEL'S NOW. This file carried a private `when` over LaunchResult
        // and three `const val`s beside it, which nothing could reach and nothing tested -- so the
        // one branch that matters, a refusal the machine says is worth retrying against one it
        // does not, had no test. It is `LaunchPanelScreen.noticeFor`, and `LaunchPanelScreenTest`
        // is where both branches are asserted.
        launchAnswer = launchScreen.resolve(answer)
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
     * here is the kit's mono well as wide as the phone, which is not the new session's grid.
     */
    private fun specOf(draft: LaunchDraft) = LaunchSpec().apply {
        agent = draft.agent
        cwd = draft.cwd
        prompt = draft.prompt
    }

    /**
     * The controls that act on the CHOSEN SESSION, raised once the triage inbox yielded a row.
     *
     * The keyboard is not among them any more and that is PB-INPUT-2: a session on screen was
     * never the condition for typing into it, a CONFIRMED LEASE is, and [setKeyboardEnabled] is
     * where the model's verdict lands. Launch is not among them either -- it starts a session
     * rather than acting on one.
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
     * A control that reaches a facade verb, with the overlay defence applied by construction
     * rather than restated at each call site.
     *
     * THERE IS ONE FACTORY AND NOT THREE (ADR-007 B133). This file carried `timedButton` and
     * `perUseButton` for PB-SEC-2's two authentication tiers, and before them a `gatedButton`;
     * PB-SEC-2 is VOID and both tiers left the product, so a control that named one would be
     * claiming a checkpoint that no longer exists anywhere behind it.
     *
     * WHAT [SecureWindow.gate] APPLIES IS NOT A GATE IN THAT SENSE, and it stays: PB-SEC-12
     * clause 1's touch filter makes the framework discard a tap that arrived while another
     * window covered this view. Every control built here is destructive or authorising, which
     * is exactly the set an overlay attack is worth mounting against -- and with no second
     * checkpoint behind revoke or take-control it is the only thing standing against one.
     *
     * The verb's outcome goes on screen. An action that reports nothing is the failure PB-APP-9
     * exists to prevent: the user presses a control, something refuses, and the screen looks
     * identical either way.
     */
    private fun actionButton(
        text: String,
        verb: (App, String) -> Any?,
    ): Button = SecureWindow.gate(
        Button(activity).apply {
            this.text = text
            setOnClickListener { invoke(verb) }
        },
    )

    /**
     * The same control, as the design draws one.
     *
     * TWO FACTORIES AND NOT ONE, and the split is the SCREEN and not the verb. A control composed
     * into a recomposed panel takes the shape the design gives that site -- derivation row 22 says
     * the peek's `[Take control]` is `.a2-more`, and the launch form's submit is the primary
     * action, so it is `.a2-ok` with its `--p-cta-fx` bloom. The four controls still sitting in
     * the unrecomposed remainder have no design source at all: inventory C2 is unbuilt, and
     * painting `.a2-ok` on a Kill session button because it happens to be a button would be
     * choosing a variant for a site the design has not specified.
     *
     * TWO THINGS COME WITH [ctaButton] THAT A `Button` HAD FOR FREE, and both are handled here
     * because the kit cannot: a `TextView` announces itself as text rather than as a button, and
     * the kit has no click to hang the role on (`CtaButton`'s own KDoc records the gap). The role
     * is set below. The other is the DISABLED APPEARANCE: `Button` dims itself and this does not,
     * because derivation row 24 -- the disabled/stale CTA, `--p-hair` fill and `--p-ink3` ink --
     * has no factory. `isEnabled` still refuses the tap; what is lost is that it looks refused.
     * Recorded rather than approximated with an alpha nobody derived.
     */
    private fun ctaAction(
        text: String,
        kind: CtaKind,
        verb: (App, String) -> Any?,
    ): TextView = SecureWindow.gate(
        ctaButton(activity, text, kind).apply {
            setOnClickListener { invoke(verb) }
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

    /**
     * PB-DS-11: a heading takes a TEXT APPEARANCE, never a typeface. See [SettingsSurface.label];
     * the same two lines were in all three surface files, which is what "no visual constant may
     * enter the app except through the theme" is a fence against.
     */
    private fun label(heading: Boolean = false) = TextView(activity).apply {
        if (heading) setTextAppearance(R.style.TextAppearance_Swarm_Title_Row)
        layoutParams = LinearLayout.LayoutParams(MATCH, WRAP)
    }

    /**
     * A text field, by the words that say what belongs in it. The hint IS the label on this
     * surface -- there are no XML layouts here -- so a field added without one is a box a user
     * cannot identify, and android/app/src/test/.../PhoneLaunchSurfaceTest reads exactly these.
     *
     * IT IS THE KIT'S NOW. It was a bare `EditText` with a hint and nothing else -- the platform
     * default underline on the platform default background -- and derivation row 9 specifies a
     * recessed `--p-well` field with the card radius, `Body.Message` ink and, deliberately, an
     * `--p-ink2` placeholder rather than `--p-ink3`: the hint is this surface's only label, so it
     * is text a user is actively trying to read and the 3.50:1 tertiary fails the body floor.
     */
    private fun field(hint: String): EditText = textField(activity, hint)

    /**
     * A launch field, by the words the SCREEN MODEL says belong in it.
     *
     * The three hints were `String` literals here and the same three strings in
     * [LaunchPanelScreen], which carried them over verbatim -- two copies of one piece of copy,
     * with nothing joining them. PB-DS-9 assigns copy to the screen, so the screen is asked.
     */
    private fun field(id: LaunchFieldId): EditText = field(LaunchPanelScreen.hintFor(id))

    /**
     * WHAT USED TO BE HERE WAS THE COPY OF FIVE SCREENS. This companion held PB-INPUT-2's two
     * lease sentences and the launch form's three notices -- the words a user reads, in the file
     * that also owns the transport, the lifecycle and six panels, reachable by nothing and
     * asserted by nothing. PB-DS-9 assigns copy to the screen: they are [PeekPanelScreen]'s and
     * [LaunchPanelScreen]'s now, unchanged to the character, and each has a test that says which
     * sentence goes with which state.
     */
    private companion object {
        const val MATCH = ViewGroup.LayoutParams.MATCH_PARENT
        const val WRAP = ViewGroup.LayoutParams.WRAP_CONTENT
    }
}
