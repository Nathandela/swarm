package dev.swarm.phone

import android.text.format.DateFormat
import android.view.View
import android.view.ViewGroup
import android.view.accessibility.AccessibilityNodeInfo
import android.widget.Button
import android.widget.EditText
import android.widget.FrameLayout
import android.widget.LinearLayout
import android.widget.TextView
import androidx.appcompat.app.AlertDialog
import androidx.appcompat.app.AppCompatActivity
import dev.swarm.phone.keys.ConnectionState
import dev.swarm.phone.runtime.ConnectivityPolicy
import dev.swarm.phone.runtime.LifecycleConvergence
import dev.swarm.phone.runtime.LifecycleEvent
import dev.swarm.phone.runtime.RuntimeState
import dev.swarm.phone.runtime.SocketDisposition
import dev.swarm.phone.ui.CapabilityNotice
import dev.swarm.phone.ui.CommandVerdict
import dev.swarm.phone.ui.ControlLease
import dev.swarm.phone.ui.FacadeBridge
import dev.swarm.phone.ui.LaunchDraft
import dev.swarm.phone.ui.LaunchRendering
import dev.swarm.phone.ui.LaunchScreen
import dev.swarm.phone.ui.PressFeedback
import dev.swarm.phone.ui.SessionDetail
import dev.swarm.phone.ui.StatusBanner
import dev.swarm.phone.ui.StopAction
import dev.swarm.phone.ui.TriageInbox
import dev.swarm.phone.ui.kit.CtaKind
import dev.swarm.phone.ui.kit.ToastHost
import dev.swarm.phone.ui.kit.ctaButton
import dev.swarm.phone.ui.kit.emptyState
import dev.swarm.phone.ui.kit.textField
import dev.swarm.phone.ui.screens.ActivityPanel
import dev.swarm.phone.ui.screens.ActivityPanelScreen
import dev.swarm.phone.ui.screens.Destination
import dev.swarm.phone.ui.screens.InboxScreen
import dev.swarm.phone.ui.screens.InboxTab
import dev.swarm.phone.ui.screens.LaunchFieldId
import dev.swarm.phone.ui.screens.LaunchPanel
import dev.swarm.phone.ui.screens.LaunchPanelScreen
import dev.swarm.phone.ui.screens.LinkPanel
import dev.swarm.phone.ui.screens.LinkPanelScreen
import dev.swarm.phone.ui.screens.MachinesPanelScreen
import dev.swarm.phone.ui.screens.PairOnlyReason
import dev.swarm.phone.ui.screens.PairOnlyScreen
import dev.swarm.phone.ui.screens.PeekPanel
import dev.swarm.phone.ui.screens.PeekPanelScreen
import dev.swarm.phone.ui.screens.Presentation
import dev.swarm.phone.ui.screens.SessionDetailPanel
import dev.swarm.phone.ui.screens.SessionDetailScreen
import dev.swarm.phone.ui.screens.TriageInboxScreen
import dev.swarm.phone.ui.screens.activityPanelView
import dev.swarm.phone.ui.screens.launchPanelView
import dev.swarm.phone.ui.screens.linkPanelView
import dev.swarm.phone.ui.screens.pairOnlyView
import dev.swarm.phone.ui.screens.peekPanelView
import dev.swarm.phone.ui.screens.phoneScaffoldView
import dev.swarm.phone.ui.screens.sessionDetailView
import dev.swarm.phone.ui.screens.statusBannerView
import dev.swarm.phone.ui.screens.triageInboxView
import java.util.Date
import swarmmobile.App
import swarmmobile.LaunchSpec
import swarmmobile.Op

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
    /**
     * Where a facade verb runs, which is NEVER the thread that pressed the control.
     *
     * Every verb below crosses JNI into a Go network call. A command verb resolves its
     * destination through `sendContext` -> `awaitConn`, which "polls for up to five seconds"
     * (mobile/commands.go:513-524) before appending to the relay, so running one inside the
     * click listener froze the app for a round trip and, on a tap issued while the link was
     * reconnecting, for about five seconds -- an ANR. `NetworkOnMainThreadException` never fires
     * for a socket Go opened, so nothing on the platform side was ever going to say so.
     *
     * It is a CONSTRUCTOR PARAMETER so a test can hand in [VerbDispatch.direct] and keep its
     * presses synchronous. The default is the shipping one.
     */
    private val dispatch: VerbDispatch = VerbDispatch.background(),
) {

    /**
     * PB-APP-9's routed startup failure, on the branch where the phone core refused construction.
     *
     * IT NO LONGER CARRIES THE LINK'S NEWS, and that move is agents-tracker-e6mi. This label used
     * to hold the connection banner, the machine's freshness verdict and the roster's stale notice
     * as well -- three sentences joined into one, on a view hosted under the inbox's sections and
     * therefore detached on every other destination. Those three are [bannerHost]'s now, above the
     * scaffold's content, where a tab change cannot reach them.
     *
     * WHAT IS LEFT IS THE ONE THING THAT IS NOT ABOUT THE LINK. A core that would not start is not
     * a machine that cannot be reached: there is no phone to ask, no roster, and no drill-down --
     * [renderUnavailable] closes all of it -- so the sentence belongs with the screen that branch
     * draws rather than in the standing banner, which reports on a link this handset does not have.
     */
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
     * around it. The inbox is redrawn only when [InboxScreen] changes -- [drawInbox] argues why
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

    // THE REDRAW IS THE MOMENT THE APP APPEARS. An unpaired phone is shown one screen and nothing
    // else ([drawPairOnly]), so a pairing that has just succeeded leaves the window still made of
    // the flow that succeeded; this is what swaps it for the app the user has now earned, when they
    // earned it rather than the next time they happen to leave and come back.
    private val pairing = PairingSurface(activity, runtime).also { it.onPaired = ::render }

    /**
     * Derivation row 1, which this app has never had: what a press's answer says WHERE THE PRESS
     * HAPPENED.
     *
     * IT IS NOT A CHILD OF [host], AND THAT IS WHAT MAKES IT SURVIVE. Both draw paths call
     * `host.removeAllViews()` and both guard on `host.childCount`, so an overlay parented there
     * would be destroyed by the next journal event to arrive -- which is the clock this surface
     * redraws on -- and would break the two guards on its way past. It hangs beside the app in
     * [windowRoot] instead, which nothing rebuilds.
     *
     * IT IS DECLARED BEFORE [settings] BECAUSE THAT PANEL IS HANDED IT. Kotlin initialises
     * properties in declaration order, so a later one would be null in the `also` block below.
     */
    private val toasts = ToastHost(activity)

    // IT IS HANDED THIS SURFACE'S DISPATCH, which is the half of the lifecycle that panel cannot
    // know: [release] detaches, and only this file is told when the screen goes away. Settings owns
    // the phone's one destructive verb now, and a revoke settling onto a window nobody is holding
    // is what the attach/detach pair exists to stop -- the default instance is never detached by
    // anyone.
    //
    // AND THE REDRAW, for the reason the line above it gives in the other direction. A revoke ends
    // the pairing, so it changes what [renderReady]'s gate answers -- and the settle inside the
    // panel can only redraw the panel, inside a window still made of the app the phone has just
    // stopped being entitled to. Nothing else would ask again: the next redraw would come from a
    // journal event, and the revoke is what stops those arriving (agents-tracker-2lz5).
    //
    // AND THE TOAST OVERLAY, for the reason it is one overlay rather than one per surface: the
    // settings panel is hosted INSIDE the tab scaffold, so a toast of its own would be drawn under
    // the tab bar and would go with the panel on the redraw a revoke causes.
    private val settings = SettingsSurface(activity, runtime, dispatch).also {
        it.onReplaced = ::render
        it.toasts = toasts
    }

    /**
     * IT REMEMBERS THE OPERATION IT ISSUED, which is what makes PB-INPUT-2's lease a fact rather
     * than a literal. The lease is not on any snapshot: it is the outcome of THIS take_control,
     * claimed by operation id, and [leaseConfirmedFor] is what asks the machine about it.
     */
    private val takeControl = ctaAction("Take control", CtaKind.MORE) { takeControlOf(session) }

    /**
     * PB-APP-3's persistent Stop, and the one control on this surface whose PRESS DOES TWO
     * DIFFERENT THINGS -- because [SessionDetail.stop] says so, not because this file chose it.
     * Without a lease the model refuses the keystroke and offers the step that would make it work,
     * which is take_control; with one it asks first and then interrupts.
     *
     * IT INTERRUPTS THROUGH `App.Interrupt` AND NOT THROUGH `sendInput(0x03)`, which is the same
     * decision one hop down: `mobile/commands.go` sends the interrupt byte itself and returns an Op
     * naming the action, so a phone that wrote the byte here would be a second implementation of
     * an interrupt and would leave the bound verb unreachable. [SessionDetail.interruptBytes] is
     * the phone-side statement of the constant and is consumed by its own unit test, not by this.
     */
    private val stop = actionButton(SLOT_LABEL, ask = ::stopQuestion) {
        // THE BRANCH IS TAKEN ON THE MAIN THREAD, and it has to be: `detailDrawn` is the panel
        // this surface last drew, written by `render` and owned by the looper. Reading it from a
        // lane would be a data race on the fact that decides which of two different things this
        // one control does.
        val target = session
        when (detailDrawn?.confirmedStopAction) {
            // The one control on this surface whose two arms are on two different PLANES, which
            // is why the plane is chosen per press rather than per control.
            StopAction.SEND_INTERRUPT -> {
                stopNotSentFor = ""
                Press(
                    SendPlane.LIVE,
                    verb = { app -> app.interrupt(target) },
                    // The one press on this surface the design wrote a confirmation for. Without
                    // it, an interrupt that reached the machine and an interrupt still crossing
                    // look identical: the outcome line is cleared for both and the button comes
                    // back enabled either way.
                    confirmation = SessionDetail.INTERRUPT_SENT,
                )
            }
            StopAction.ACQUIRE_LEASE_FIRST -> {
                stopNotSentFor = ""
                takeControlOf(target)
            }
            // NOT_SENT: input is live-only and this one is discarded rather than held (ADR-007
            // D7). IT USED TO BE `else -> null` (agents-tracker-4lta), deferring to a notice that
            // was already on screen before the press -- so the press wrote nothing, sent nothing
            // and left the screen identical, which is a dead control on a confirmed press. The
            // press is now recorded, so PB-INPUT-1's notice becomes a report of something that
            // happened, and it is said out loud where the finger was: Stop sits below the
            // transcript and the notice above it.
            StopAction.NOT_SENT -> {
                stopNotSentFor = target
                say(PressFeedback.ofUnsent(SessionDetail.NOT_SENT_NOTICE))
                null
            }
            // CONFIRM never reaches here -- it is what `stop()` answers, and this reads the answer
            // to the question it produced -- and KILL belongs to the other control entirely.
            else -> null
        }
    }

    /**
     * The escalation, and it is the SAME BUTTON that used to sit loose under the inbox.
     *
     * WHAT CHANGED IS THAT IT ASKS. `SessionDetail.killRequiresConfirmation` has been `true` since
     * S16 and reached nothing: a control that ends a session outright was one tap away for anyone
     * holding the phone. [SessionDetailPanel.killConfirmation] is the question, and it states the
     * CONSEQUENCE rather than the action precisely so it does not read like Stop's -- a
     * confirmation that read the same for both would train the user to dismiss the one that
     * matters.
     */
    private val kill = actionButton(
        SLOT_LABEL,
        ask = { detailDrawn?.killConfirmation.orEmpty() },
    ) {
        val target = session
        Press(
            SendPlane.COMMAND,
            verb = { app -> app.kill(target) },
            // AND IT REMEMBERS THE OPERATION (agents-tracker-qlf9). Without this the press took
            // the default settle, which discards the `Op`, so the id the machine keys its answer
            // by was gone before the answer existed -- and the answer to a kill is the one this
            // screen cannot infer from anything else it draws: a refused session and a killed one
            // both sit in the roster until the next event arrives.
            settle = { answer -> rememberKill(answer) },
        )
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
    private val send = actionButton("Send line") {
        // Read here, on the looper that owns the field. The lane never touches a View.
        val target = session
        val line = typed.text.toString()
        Press(
            SendPlane.LIVE,
            verb = { app -> app.sendInput(target, (line + "\r").toByteArray(Charsets.UTF_8)) },
            // The field is emptied only once the bytes are away, which is where the clear has
            // always been: a send the machine refused leaves what the user typed on screen.
            settle = { typed.text.clear() },
        )
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
    private val launch = ctaAction("Launch a session", CtaKind.APPROVE) {
        // The three fields are read on the looper that owns them, and the model's refusal is
        // resolved here too -- a draft the phone can already see is incomplete never reaches a
        // lane, let alone the wire.
        val draft = draftOnScreen()
        when (val missing = launchScreen.missingField(draft)) {
            null -> {
                launchRefusal = ""
                Press(
                    SendPlane.COMMAND,
                    verb = { app -> app.launch(specOf(draft)) },
                    settle = { answer -> submitLaunch(draft, answer) },
                )
            }
            else -> {
                launchRefusal = missing
                null
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

    /**
     * The session whose DETAIL is open, or null while the Inbox tab is showing its list.
     *
     * IT IS A SUB-STATE OF [Destination.INBOX] AND NOT A FIFTH DESTINATION, which is structural
     * rather than aesthetic: the bar draws exactly four tabs from the labels `TriageInboxScreen`
     * records and `Destination.forLabel` THROWS on a label it cannot place, so a fifth value would
     * be a destination the bar cannot express and the lookup cannot produce. It also keeps the
     * Inbox tab reading as selected while you are inside it, which is where you are.
     *
     * SWITCHING TABS PRESERVES IT. A user who checks the activity feed mid-session should come back
     * to the session they were in, and the drill-down is state inside the Inbox tab rather than a
     * place they left.
     *
     * The setter is what the SYSTEM BACK GESTURE hangs off -- see [onDrillDownChanged].
     */
    private var detail: String? = null
        set(value) {
            field = value
            onDrillDownChanged(value != null)
        }

    /**
     * Told whether the drill-down is open, so [PhoneActivity] can arm the system back gesture
     * against it and disarm it again.
     *
     * IT PUSHES AND IS NOT POLLED. The Activity draws on resume and nothing else; the drill-down
     * opens when a row is tapped, which is between resumes -- so an Activity that read this state
     * would arm its callback at the one moment it is never needed and never again.
     *
     * WHAT CROSSES IS A BOOLEAN ABOUT LOCAL SCREEN STATE AND NOTHING ELSE, which is PB-SEC-11 and
     * not style: [PhoneActivity] is exported with a LAUNCHER filter, so a back callback that could
     * reach a verb would put session-acting code on the one surface any app on the device can
     * start.
     */
    internal var onDrillDownChanged: (Boolean) -> Unit = {}

    /** What the inbox last drew, so a redraw that changes nothing rebuilds nothing. */
    private var inboxDrawn: InboxScreen? = null

    /**
     * What the drill-down last drew, and null while the Inbox tab is showing its list.
     *
     * IT ANSWERS TWO QUESTIONS AND BOTH ARE LOAD-BEARING. The first is [inboxDrawn]'s: a panel
     * that has not changed is not rebuilt, so a session printing steadily does not throw its own
     * transcript back to the top. The second is which of the two things the Inbox destination can
     * show is currently on screen -- without it, backing out to a list whose DATA is unchanged
     * would take the early return in [drawInbox] and leave the detail up.
     *
     * The two controls read it as well, because what Stop does and what Kill asks are the panel's
     * and belong to the panel the user is actually looking at.
     */
    private var detailDrawn: SessionDetailPanel? = null

    /**
     * The routed line the drill-down last drew, which is NOT derivable from [detailDrawn].
     *
     * A REFUSAL CHANGES NOTHING ABOUT THE SESSION. Press Stop, have the machine refuse it, and the
     * journal, the grid, the lease and every sentence on the panel are exactly what they were --
     * so a redraw guarded on the panel alone takes its early return and the answer never reaches
     * the screen, which is the silence [drawDetail] hands the line in to prevent.
     */
    private var detailOutcomeDrawn: String = ""

    /**
     * Which of inventory C1.4's four destinations is on screen.
     *
     * IT IS THE SURFACE'S AND NOT THE INBOX MODEL'S. `InboxScreen.tabs` carries a `selected` flag,
     * and it was written when the inbox was the only screen there was: it answers
     * `label == "Inbox"` for every list it builds. Reading it here would tell a user standing on
     * Machines that they are in the Inbox, so the selection is this field and
     * [dev.swarm.phone.ui.screens.phoneScaffoldView] takes it as a parameter.
     */
    private var destination = Destination.INBOX

    /** What the activity screen last drew, for [inboxDrawn]'s reason. */
    private var activityDrawn: ActivityPanel? = null

    /** What the machines screen's link section last drew, for [inboxDrawn]'s reason. */
    private var machinesDrawn: LinkPanel? = null

    /**
     * The destination the content host currently holds.
     *
     * It is not the same question as [inboxDrawn] or [activityDrawn]: those ask whether a screen's
     * DATA changed, and this asks whether the screen on screen is still the one the user chose. A
     * tab tapped while the data is unchanged changes only this.
     */
    private var contentShows: Destination? = null

    /** What the tab bar last drew: the tabs, and the destination they point at. */
    private var barDrawn: Pair<List<InboxTab>, Destination>? = null

    /**
     * What the scaffold's banner last said, for [inboxDrawn]'s reason.
     *
     * It is NOT keyed on the destination, and that is the whole point of the slot: the banner says
     * the same thing wherever the user is standing, so nothing about navigation invalidates it.
     */
    private var bannerDrawn: StatusBanner? = null

    /**
     * Whether the offer on the unpaired phone's screen has been taken up.
     *
     * IT IS A FACT ABOUT THIS SCREEN AND NOT ABOUT THE PAIRING STATE, which is why it is here and
     * not read from [PairingSurface]. An interrupted attempt is a state the flow restores; this is
     * whether the person in front of the phone has asked to see the flow at all, and a handset that
     * comes back to a half-finished attempt is still owed the sentence explaining why its app is
     * empty before it is shown a camera.
     */
    private var pairingStarted = false

    /**
     * What the unpaired phone's screen last drew, or null while it is not the screen on show.
     *
     * [inboxDrawn]'s reason: [render] runs on every resume, after every action and on every journal
     * event, and rebuilding this screen re-parents the pairing flow -- which on the step that
     * matters is a live camera preview.
     */
    // THE NOTICE IS PART OF THE KEY (agents-tracker-qlf9). A revoke lands the phone on this screen
    // and the sentence explaining what it left behind arrives with it; keyed on `pairingStarted`
    // alone, the early return would keep drawing the screen the phone had before the revoke.
    //
    // AND SO IS THE REASON (agents-tracker-w6o3), for the same reason one step earlier: a handset
    // reaches this screen and only then dials, so the transport's verdict routinely arrives AFTER
    // the first draw. Keyed without it, the redraw that carries "the owner removed this device"
    // would be the one the early return skips.
    private var pairOnlyDrawn: Triple<Boolean, String, PairOnlyReason>? = null

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

    /**
     * The session whose last press the MACHINE refused for want of a lease, or empty
     * (agents-tracker-agre).
     *
     * IT IS NEWER INFORMATION THAN [leaseOp]'S OUTCOME, which is the whole reason it exists.
     * `ControlLease` records the window in its own KDoc: a lease that lapsed at its horizon "still
     * reads as confirmed", because the horizon does not ride the take_control's outcome. So the
     * durable outcome says granted, the screen draws `Stop`, the machine refuses the interrupt with
     * `swarm/no-lease`, and the next press earns the same refusal -- with the remedy on screen as a
     * sentence throughout. [PressFeedback.offersTakeControl] is what says the refusal was that one;
     * this is where the screen remembers it.
     *
     * THE SESSION IS REMEMBERED BESIDE IT, for [leaseSession]'s reason exactly: the target is
     * re-derived on every draw, and a refusal against one session must not shut the keyboard on
     * another. It is cleared by [rememberLease], so the next take_control -- the very step this
     * makes available -- takes the screen's answer back off it.
     */
    private var leaseRefusedFor: String = ""

    /**
     * The kill this surface issued, so its answer can be claimed by operation id
     * (agents-tracker-qlf9).
     *
     * IT HAD NO SETTLE AT ALL. The press took [Press]'s default, which drops what the verb
     * returned, so the operation id the machine keys the outcome by was thrown away at the one
     * control whose refusal is hardest to see: the session stays in the inbox either way, and the
     * outcome line is cleared at press time, so a refused kill and a kill still crossing and a
     * kill that worked all draw the same screen.
     *
     * NO SESSION IS REMEMBERED BESIDE IT, which is the difference from [leaseSession]. A lease is
     * a standing fact about a session the surface re-derives every draw, so attributing one by
     * proximity would open the keyboard over the wrong terminal; a kill's answer is a one-shot
     * report about the operation, said once and not carried.
     */
    private var killOp: String = ""

    /**
     * The session whose Stop press resolved to NOT_SENT, or empty (agents-tracker-4lta).
     *
     * IT IS A PRESS AND NOT THE LINK. `SessionDetail.notSentNotice` was a function of `online`, so
     * a dropped connection alone put "Stop did not reach your machine and was not held for later"
     * on screen -- a report of a failure the user had not caused. This is the fact that makes it a
     * report: it is written by the Stop plan's NOT_SENT arm and read by [detailPanel].
     *
     * THE SESSION IS REMEMBERED BESIDE IT, for [leaseSession]'s reason exactly: the target is
     * re-derived every draw, and a press against one session must not put a notice on another.
     * It is cleared by a later Stop press that resolves to anything else and by leaving the
     * drill-down, so the sentence never outlives the press it reports.
     */
    private var stopNotSentFor: String = ""

    /**
     * The operations whose verdict has already been put on screen, so it is said ONCE.
     *
     * [render] runs on every journal event and the outcome stays in the core's durable map, so a
     * verdict claimed per draw would re-fire its toast at whatever rate the user's agents produce
     * events. Two fields rather than one, because a kill answered after a refused take_control
     * must not un-say the take_control.
     */
    private var killSaid: String = ""

    private var leaseSaid: String = ""

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
     * [launchHost] are composed panels rather than loose views. What is genuinely unrecomposed is
     * the startup line, the capability notice, the outcome line, and the KEYBOARD -- the field and
     * Send.
     *
     * **THE LINK'S NEWS HAS LEFT IT, AND THAT IS THE DEFECT agents-tracker-e6mi REPORTS.** The
     * connection banner, the machine's freshness verdict and the roster's stale notice were three
     * sentences joined into [status], which is a child of this column -- so PB-APP-8's whole
     * subject was legible at the bottom of one of four tabs and nowhere else, and a link that
     * dropped while the user was reading a session changed nothing they could see. They are
     * [bannerHost]'s now, above the scaffold's content and outside its scroll, and they are three
     * lines rather than one paragraph.
     *
     * **THE PAIRING PANEL HAS LEFT IT, AND THAT IS THE DEFECT agents-tracker-64rf REPORTS.** It was
     * a child of this column, which is hosted BELOW the session list on the Inbox destination -- so
     * on a fresh install the one action the phone can usefully take was a control at the bottom of
     * a scroll, under four always-drawn triage headings over an empty roster, on one of four tabs.
     * The owner installed the app on a real handset and could not find it. An unpaired phone is now
     * shown [dev.swarm.phone.ui.screens.pairOnlyView] and nothing else ([drawPairOnly]);
     * `android/gate/pairingentry_test.go` fences the panel out of this column and out of everything
     * the tab scaffold hosts, so it cannot come back to a list.
     *
     * **REVOKE HAS LEFT IT FOR THE SAME BURIAL AND IS NOW SETTINGS' "Replace this computer"**
     * ([SettingsSurface]). PB-SEC-7's panic action -- the one control whose whole value is being
     * reachable on a handset its owner no longer trusts -- was a loose button in this column, which
     * is to say at the bottom of the same scroll the pairing panel was lost in. It is one control
     * rather than two because revoking IS what replacing starts with: a revoked device is an
     * unpaired one, [PairOnlyScreen] answers `PAIR_ONLY` for it, and the next draw is the screen
     * that pairs a new computer -- so the second half needs no navigation and nothing here.
     *
     * KILL SESSION HAS LEFT IT, and inventory C2 is where it went. It was a loose button acting on
     * whichever session the surface happened to be targeting, one tap from ending it; it is now the
     * session detail's escalation, behind [SessionDetailPanel.killConfirmation], on the screen that
     * names the session it ends. PB-APP-3's Stop is beside it and is new.
     *
     * WHAT IS LEFT IS THE COMPOSER AND ONLY THE COMPOSER. The field and Send are derivation row 9's
     * bar -- its 26 dp glyphs, its recessed field and its stop control -- and that component does
     * not exist. It ships WITH PB-INPUT-1's undelivered-input ledger or not at all
     * (agents-tracker-hxv): the ledger is what stops an input path losing keystrokes with nothing
     * on screen saying so, so a composer delivered without it reintroduces exactly the defect the
     * ledger exists to prevent. Until then a field and a button stand in for it here, rather than a
     * composer-shaped affordance drawn on the detail screen promising an input path that is not
     * wired.
     *
     * THE SETTINGS PANEL HAS LEFT IT, and that is the tab bar becoming a control. C6 is a
     * DESTINATION -- inventory C1.4's fourth tab -- and it was hosted here, halfway down a column
     * under the inbox, because there was nothing to navigate with; it is now what the Settings tab
     * shows and nothing else shows it. That is the whole of what the move is.
     *
     * IT CARRIES NO PADDING OF ITS OWN ANY MORE. The 24 dp was the last thing on this surface
     * deciding a spatial value; the kit components above it carry theirs, and these views are
     * unstyled while they wait for the components that will style them.
     */
    private val unrecomposedControls = LinearLayout(activity).apply {
        orientation = LinearLayout.VERTICAL
        layoutParams = LinearLayout.LayoutParams(MATCH, WRAP)
        for (child in listOf(
            status, notice, peekHost,
            typed, send, launchHost, outcome,
        )) {
            addView(child)
        }
    }

    /**
     * The scaffold's content: the destination on screen, swapped when a tab is tapped.
     *
     * IT IS A HOST THE SURFACE OWNS rather than a view handed to the scaffold on each draw,
     * because the two change on different clocks. The bar changes when the badge count or the
     * destination does; the destination's own content changes whenever its data does -- and a
     * scaffold rebuilt for one would re-parent the other, which takes the keyboard away from
     * whoever is typing a working directory into the launch form.
     */
    private val contentHost = FrameLayout(activity).apply {
        // A glowing dot is drawn outside its own view.
        clipChildren = false
        clipToPadding = false
        layoutParams = ViewGroup.LayoutParams(MATCH, WRAP)
    }

    /**
     * agents-tracker-e6mi: where PB-APP-8's connection states, PB-APP-11's freshness verdict and
     * the roster's PB-APP-8 notice are drawn, above the destination and outside its scroll.
     *
     * IT IS A HOST THE SURFACE OWNS, for [contentHost]'s reason exactly. What the banner says
     * changes whenever the transport, the machine's clock or the journal stream does -- which is
     * to say on every event -- and the scaffold is rebuilt only when the bar changes. Handing the
     * scaffold a fresh banner per draw would rebuild the bar at the rate the agents produce
     * events, and re-parent the destination under whoever is using it.
     *
     * WHAT IT REPLACES IS A LINE ON THE INBOX. The three facts were written to [status], a child
     * of [unrecomposedControls], which `triageInboxView` hosts UNDER its four Group sections --
     * so `hostContent`'s `detachHostedViews` took them off screen on the way to Machines,
     * Activity, Settings and into every session drill-down. The link dropping changed nothing on
     * screen for a user standing anywhere else, which is the moment PB-APP-8 exists for.
     */
    private val bannerHost = FrameLayout(activity).apply {
        layoutParams = ViewGroup.LayoutParams(MATCH, WRAP)
    }

    /**
     * The APP: [dev.swarm.phone.ui.screens.phoneScaffoldView] -- the destination above the tab
     * bar. The bar is rebuilt into it when the badge or the destination changes.
     *
     * IT IS NO LONGER THE WINDOW'S ONE CHILD. It is rebuilt from nothing on every tab change and
     * on every paired/unpaired transition, and both draw paths ask `host.childCount` whether
     * anything is on screen -- so anything that must OUTLIVE a redraw cannot be in here. The toast
     * overlay is the first such thing, and [windowRoot] is where it hangs.
     */
    private val host = FrameLayout(activity).apply {
        // A glowing dot and the tab badge are drawn outside their own views.
        clipChildren = false
        clipToPadding = false
        layoutParams = ViewGroup.LayoutParams(MATCH, MATCH)
    }

    /**
     * The window: the app, and the toast overlay above it.
     *
     * THE ORDER IS THE Z-ORDER, which is the whole of what "above the tab bar" means in a
     * `FrameLayout`: [toasts] is added last, so it draws over the bar rather than behind it. It
     * takes no touches (see [ToastHost]), so the app underneath stays usable while a toast is up.
     *
     * IT IS ALSO WHAT `PhoneActivity.insetTheSystemBars` PADS, which is why the overlay is inside
     * it rather than beside it: row 1 measures `toast_bottom` from the bottom of the frame the app
     * draws in, and the gesture-nav inset is part of that frame (derivation row 19 -- an iPhone
     * constant yields to the platform's own measurement).
     */
    private val windowRoot = FrameLayout(activity).apply {
        clipChildren = false
        clipToPadding = false
        layoutParams = ViewGroup.LayoutParams(MATCH, MATCH)
        addView(host)
        addView(toasts)
    }

    val root: View = windowRoot

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
    // REVOKE IS IN HERE THROUGH [SettingsSurface] AND NOT BY NAME, which is the sentence above
    // working: the control moved to the screen that owns it and arrived back in this list with the
    // panel's own set, so the phone's one destructive action never stopped filtering obscured taps.
    val touchFilteredActions: List<View> =
        listOf(takeControl, send, stop, kill, launch) +
            pairing.touchFilteredActions + settings.touchFilteredActions

    /**
     * Draw. Called from onResume, so a phone that was unavailable when the screen opened -- a
     * handset whose Keystore or state directory refused -- redraws once it is not.
     */
    fun render() {
        // A drawn surface is one that may be handed the answer to a press. The pair with
        // `release`'s detach is what stops a command that finishes after the screen went away
        // from setting text on views nobody is holding.
        dispatch.attach()
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

        // THE THIRD THING THAT CAN OUTLIVE THIS SCREEN, and the cheapest to forget: row 1's toast
        // is a view plus a queued `Handler` callback, and `PairingSurface.release` clears its own
        // poller for exactly this reason. A message shown as the user leaves is one they did not
        // read; waiting for them with whatever is left of 3.2 seconds, over whatever the screen
        // says by then, is worse than not having said it.
        toasts.dismiss()

        // The sink goes FIRST and unconditionally. It is the only thing that can outlive this
        // screen: PhoneRuntime caches the App across Activity instances, so a listener still
        // pointed at these views would redraw a window nobody is holding -- and would keep this
        // Activity reachable for as long as the process lives.
        PhoneEvents.stopObserving()
        observing = false

        // The same reason, for the other thing that can outlive this screen. A command takes a
        // relay round trip, so its answer routinely arrives after the user has left -- and the
        // Activity may have been destroyed and rebuilt by then. A detached dispatch drops the
        // settle rather than redrawing a surface nobody is holding, while still freeing the
        // control it disabled, so a resumed screen does not come back with a dead button.
        dispatch.detach()

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
            app.stateSummary().paired
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

        // AND THE BANNER SAYS NOTHING, which is the honest answer here rather than an omission.
        // Every line it can carry is read from the phone core -- the transport's state, the
        // machine's own stamp, the journal stream's completeness -- and on this branch there is no
        // core to ask. A banner reporting "not connected" would be a claim about a machine this
        // handset is in no position to make, which is [drawContent]'s own argument for drawing no
        // inbox here.
        drawBanner(StatusBanner.NONE)
        notice.text = ""
        session = ""
        // AND NO DRILL-DOWN. The detail is read from the phone core, so a handset whose core
        // refused has nothing to fill one with -- and leaving it open would leave the back gesture
        // armed against a screen that is not there.
        detail = null
        setActionsEnabled(false)
        // NO PANEL RATHER THAN AN EMPTY ONE. A peek with no session is not a peek showing nothing
        // -- there is no session to hold a lease on, so the screen says nothing about a lease
        // rather than asserting the absence of one.
        drawPeek(null)
        setKeyboardEnabled(false)
        // There is no phone to launch through either. The FORM still draws: it is the one thing on
        // this branch a user could reasonably be reaching for, and a handset whose core refused
        // needs to be able to see what it will be asked for once it has not.
        launch.enable(false)
        drawLaunch()
        // AND NO INBOX. The roster comes from the phone core, so a handset whose core refused
        // construction has no sections to draw and no counts to state -- and a triage inbox
        // rendered over nothing would say "nothing is waiting on you", which is a claim about the
        // machine that this phone is in no position to make.
        drawContent(bridge = null, inbox = null)
        // THE BAR IS DRAWN ANYWAY, because it is the window's chrome and not the inbox's contents.
        // A phone whose core refused still has four destinations, and [chromeTabs] says what the
        // bar can honestly carry here.
        //
        // IT IS NOT THE UNPAIRED PHONE'S SCREEN, and the difference is what there is to offer.
        // [drawPairOnly] offers a pairing flow; on this branch there is no phone core to run one,
        // and [PairingSurface] draws nothing at all for exactly that reason -- so the offer would
        // lead to an empty screen. What this branch has to say is the routed startup failure on the
        // status line, which is what it says.
        drawScaffold(chromeTabs())
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

        // THE FIRST DECISION IS WHETHER THIS HANDSET IS SHOWN THE APP AT ALL (agents-tracker-64rf).
        // Nothing below this line means anything on a phone with no machine pinned to it: the
        // convergence re-establishes a connection to a machine it has not got, and every screen
        // after that reads a roster it has no connection to fill -- an inbox announcing that
        // nothing is waiting on the user, which is a claim about a machine this phone is in no
        // position to make. [PairOnlyScreen] owns the decision, including the case where the state
        // cannot be read at all.
        //
        // THE FACT IS `paired` AND NOT THE MACHINE'S NAME (agents-tracker-d0b8). Nothing clears the
        // pinned machine -- phonecore filters the durable blob on it -- so a gate that read it
        // showed the app to a handset whose owner had just revoked it, with the pairing entry point
        // on the settings screen inside.
        //
        // AND THE SCREEN IS TOLD WHY, WHICH IS A SECOND QUESTION AND NOT A SECOND GATE
        // (agents-tracker-w6o3). The verdict above is one fact assembled in Go; what the reason
        // decides is only what the screen it has already chosen SAYS. It has to be asked here
        // because the transport's own banner never gets the chance: `transportEndsPairing` folds
        // a revoked and a repair_required handset into `paired = false`, so the two sentences that
        // name a cause the user cannot otherwise discover sit behind the early return below.
        if (PairOnlyScreen.presentationOf { startup.app.stateSummary().paired } ==
            Presentation.PAIR_ONLY
        ) {
            drawPairOnly(
                PairOnlyScreen.reasonFor {
                    ConnectionState.of(startup.app.connectionState())
                },
                startup.app,
            )
            return
        }
        // AND THE REVOKE'S DIVERGENCE IS SPENT HERE AND NOWHERE ELSE (agents-tracker-qlf9). The
        // gate has just said this handset is usably paired, which is the one fact that ends the
        // warning -- a machine that may still hold a registration this phone no longer has is a
        // state a completed pairing resolves. It is NOT cleared beside the scaffold, because
        // [renderUnavailable] draws that too: a phone core that failed to build says nothing about
        // whether the revoke landed, and clearing there would drop the sentence on the way past.
        settings.unpairNotice = ""
        // AND THE OPERATION THE SENTENCE WAS ABOUT GOES WITH IT (agents-tracker-4zue). The id
        // outlives the panel that issued it on purpose; what ends it is this, the one fact that
        // resolves the divergence. Left latched, the next pairing's screens would go on asking the
        // machine about a revoke from a registration that no longer exists.
        runtime.latchRevoke("")

        converge(startup.app)
        val bridge = FacadeBridge(startup.app)
        // BEFORE ANYTHING IS DRAWN, because the session detail composes whatever is on the outcome
        // line and a verdict claimed after it would reach the screen one journal event late
        // (agents-tracker-qlf9).
        renderVerdicts(bridge)
        // PB-APP-11 rides the same line as the connection banner, and it has to: the banner is
        // the TRANSPORT's opinion, and a relay that answers every poll with an empty page while
        // withholding the machine's frames leaves it reading "Connected to your machine." with
        // nothing behind it. The freshness notice is the only thing on this screen that comes
        // from the machine's own clock.
        // PB-DS-9: the inbox is built BEFORE the status line is written, because the line now
        // carries the roster's own PB-APP-8 verdict alongside the transport's.
        val inbox = inboxScreen(bridge)
        drawContent(bridge, inbox)
        drawScaffold(inbox.tabs)

        // THREE FACTS, THREE LINES, ON A SLOT NO TAB CHANGE REACHES (agents-tracker-e6mi). This
        // was `listOfNotNull(...).joinToString(" ")` onto [status] -- one run-on paragraph, on a
        // view hosted under the inbox's sections and detached on every other destination.
        // [StatusBanner] owns the order and the emptiness rule; [bannerHost] owns the place.
        drawBanner(
            StatusBanner.of(
                connection = bridge.connectionBanner(),
                freshness = bridge.machineFreshness().notice { millis ->
                    DateFormat.getTimeFormat(activity).format(Date(millis))
                },
                // PB-APP-8 for the roster. `TriageInbox.staleNotice` decided the wording in S16
                // and reached no user until now: a list drawn from a holed journal may be missing
                // a session, an exit or a needs_input, and the one screen that must never present
                // that as live is the one a person triages from.
                staleNotice = inbox.staleNotice,
                // PB-SYNC-7's hold, shown before anyone presses anything (agents-tracker-pxz8).
                reconciled = reconciledOf(startup),
            ),
        )
        // AND THE STARTUP LINE IS CLEARED, because the other branch writes it. A core that
        // refused once and started on the next resume would otherwise leave its refusal standing
        // under a working app.
        status.text = ""
        notice.text = CapabilityNotice.of(startup.anomalies)

        // THE TARGET IS THE ROW ON SCREEN. It used to be
        // `triageInbox().sections.flatMap{}.firstOrNull()?.id` -- a session picker that discarded
        // every section, row, label and grouping the model had just built. The rows are now
        // rendered and tappable, so the session the controls act on is the one somebody chose,
        // falling back to the first row in triage order while nobody has: an unchosen target has
        // to be something, and TriageInbox already decided what a user must act on first.
        // THE DRILL-DOWN WINS WHEN IT IS OPEN, and that is not a preference. [targetOf] falls back
        // to the first row in triage order when the chosen one is no longer on screen -- which is
        // right for a list and catastrophic for a detail: Stop would interrupt whichever session
        // happens to be first while the user is reading a different one, the proximity error
        // PB-SYNC-2 exists to forbid. While a session is open, it IS the target, and [watch] below
        // follows it so the grid on screen is that session's.
        session = detail ?: targetOf(inbox)

        // Before the peek is read, and on the empty branch too: a session that has gone away
        // still leaves a watch open on the machine.
        watch(startup.app, session)

        // LAUNCH IS NOT A SESSION CONTROL, and this line is where that difference is stated: the
        // session it starts does not exist yet, so an empty roster is exactly the state a user
        // reaches for it in. Gating it on the roster would leave a freshly paired phone with no
        // way to get its first session, which is section 1's "launches" with no subject again.
        launch.enable(true)
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
        val lease = leaseVerdictFor(session, bridge)
        // THE REFUSAL OUTRANKS THE GRANT (agents-tracker-agre) -- [detailPanel]'s clause, on the
        // screen that owns the Take control button. A peek drawn as held over a machine that has
        // just refused a keystroke for want of a lease leaves the keyboard open and the one control
        // that would fix it off screen, which is `PeekPanel.offersTakeControl` reading a fact the
        // wire has already contradicted.
        val view = bridge.terminalPeek(
            session,
            leaseHeld = lease.accepted && leaseRefusedFor != session,
        )
        // AND THE REST OF THE MACHINE'S ANSWER GOES WITH IT (agents-tracker-qlf9). The peek used to
        // be handed a boolean, so a refused take_control drew the sentence written for one nobody
        // had asked for.
        drawPeek(PeekPanelScreen.of(view, lease))
        setActionsEnabled(true)
        // EVERY ONE OF THE THREE PROPERTIES IS THE MODEL'S, which is what [PeekPanel] carries:
        // `keyboardEnabled` is `leaseHeld && online`, and the second half is a separate clause --
        // a lease cannot be live while the link is down. A surface that enabled the keyboard from
        // its own lease flag would satisfy the requirement's first clause and drop the second,
        // silently, while the model that states it stayed green and unread.
        setKeyboardEnabled(view.keyboardEnabled)
    }

    /**
     * PB-SYNC-7's fail-closed hold, for [StatusBanner.of]'s fourth fact (agents-tracker-pxz8).
     *
     * `StateSummary.Reconciled` crosses the boundary and, before this line, was read by no
     * Kotlin at all -- only `.paired` (read above, through [PairOnlyScreen.presentationOf]) and
     * `.machine` were. So the hold was invisible until a mutating press ran into
     * `swarm/unreconciled`, and the screen that answer landed on read as THAT press failing
     * rather than as a state the phone was already sitting in before it was pressed.
     *
     * UNREADABLE READS AS UNRECONCILED, for PB-SYNC-7's own reason: a state this phone cannot
     * confirm is not one it should tell the user is fine. In practice this branch is moot by the
     * time it runs -- [converge] and the `paired` read above it have already proven
     * `stateSummary()` readable this draw -- but defaulting the other way here would be a second
     * place this fact can disagree with itself.
     */
    private fun reconciledOf(startup: PhoneStartup.Ready): Boolean = try {
        startup.app.stateSummary().reconciled
    } catch (unreadable: Exception) {
        false
    }

    // -----------------------------------------------------------------------
    // PB-DS-9: the triage inbox.
    // -----------------------------------------------------------------------

    /**
     * The inbox's model. It is built on every draw because the tab badge is counted from it.
     *
     * agents-tracker-3nx6: GUARDED, the way [FacadeBridge.terminalPeek] already guards `App.peek`
     * (agents-tracker-9ds). This is called unconditionally from [renderReady], on the path
     * [PhoneEvents.onEvent]'s `main.post` reaches on every journal event -- so a refusal from
     * `bridge.triageInbox()` (the machine offline, revoked, rate-limited) used to propagate
     * uncaught onto the main looper instead of reaching a screen. The fallback is [chromeTabs]'s
     * own empty roster, and never a fabricated one: there is no honest inbox to show when the
     * roster could not be read.
     */
    private fun inboxScreen(bridge: FacadeBridge): InboxScreen = try {
        TriageInboxScreen.of(
            inbox = bridge.triageInbox(),
            scope = scope,
            selectedSession = chosen.takeIf { it.isNotEmpty() },
        )
    } catch (refused: Exception) {
        outcome.text = bridge.routeFacadeError(refused.message.orEmpty()).message
        TriageInboxScreen.of(TriageInbox.from(emptyList(), journalStale = false))
    }

    /**
     * The four tabs on a branch that has no roster to count one from.
     *
     * IT SPENDS `.tabs` AND NOTHING ELSE, which is why it is not the inbox [renderUnavailable]
     * refuses to draw. The labels are inventory C1.4's chrome and say nothing about any machine;
     * the badge over an empty roster is NO BADGE AT ALL rather than a zero (derivation table 1.4),
     * so nothing here tells a user whose phone core refused that nothing is waiting on them --
     * which is the claim that branch exists to withhold.
     */
    private fun chromeTabs(): List<InboxTab> =
        TriageInboxScreen.of(TriageInbox.from(emptyList(), journalStale = false)).tabs

    /**
     * The screen an unpaired phone opens on: one offer to pair, hosting the flow once it is taken
     * up, and nothing else -- no scaffold, no tab bar, no inbox.
     *
     * IT IS HOSTED IN [host] AND NOT IN [contentHost], which is the whole of what the fix is. The
     * content host is what the tab scaffold wraps around; putting this screen there would leave an
     * unpaired phone looking at four tabs again, three of which lead to screens with nothing to
     * show it. This screen IS the window while it is up.
     *
     * "AN UNPAIRED PHONE" INCLUDES A REVOKED ONE, WHICH IT DID NOT UNTIL agents-tracker-d0b8. The
     * body below already assumed it -- it clears two early-return guards because "a phone whose
     * device was revoked lands here" -- and no revoked phone could: the gate read the pinned
     * machine, which nothing clears. Now `App.StateSummary.paired` is false for all three endings
     * (the local Replace, the owner's `swarm remote revoke`, a destroyed relay-auth key), so that
     * sentence is load-bearing rather than aspirational.
     *
     * AND THE THREE ENDINGS NO LONGER READ ALIKE (agents-tracker-w6o3). They shared one screen AND
     * one set of words -- the fresh install's -- so a phone whose owner removed it this morning
     * opened on "Pair this phone" with no statement of what had happened, and a phone whose
     * relay-auth key was destroyed was offered a control the machine refuses while it still holds
     * the registration. [PairOnlyScreen.copyFor] owns the words; this passes the reason.
     *
     * @param reason why this handset is unpaired, from [PairOnlyScreen.reasonFor].
     * @param app the facade this draw asks for the revoke's answer -- see [revokeNotice].
     */
    private fun drawPairOnly(reason: PairOnlyReason, app: App) {
        // WHAT THE REVOKE LEFT BEHIND, RE-ASKED ON THE DRAW THAT SHOWS IT (agents-tracker-4zue).
        // The settings panel is gone by the time this draws -- a revoked phone is an unpaired one
        // and this screen replaces the whole scaffold -- so the one sentence explaining why the
        // pairing about to be attempted may be refused has nowhere else to land, and this is also
        // the only surface left that can still ask what the machine said.
        val revoked = revokeNotice(app)
        val next = Triple(pairingStarted, revoked, reason)
        if (pairOnlyDrawn == next && host.childCount > 0) return
        pairOnlyDrawn = next
        // THE SCAFFOLD IS COMING DOWN, SO WHAT IT LAST DREW SAYS NOTHING ABOUT WHAT IS ON SCREEN.
        // Both are cleared rather than left, because both guard early returns: a phone whose device
        // was revoked lands here with a bar and a destination already recorded, and would then be
        // shown neither of them again when it re-pairs.
        barDrawn = null
        contentShows = null
        host.removeAllViews()
        host.addView(
            pairOnlyView(
                context = activity,
                pairing = pairing.root,
                started = pairingStarted,
                onStartPairing = {
                    pairingStarted = true
                    render()
                },
                notice = revoked,
                copy = PairOnlyScreen.copyFor(reason),
            ),
        )
    }

    /**
     * PB-SYNC-2's answer to the revoke this install issued, claimed by operation id on every draw
     * of the screen the revoke sent the phone to (agents-tracker-4zue).
     *
     * IT IS [renderKillVerdict]'S PROGRAM ON THE ONE VERB THAT COULD NOT USE IT. Those run from
     * [renderReady] after the presentation gate has said this handset is still usably paired --
     * and a revoke is precisely the command that makes the gate answer otherwise, so the pair-only
     * branch returns before [renderVerdicts] is ever reached. The answer has to be claimed here or
     * nowhere.
     *
     * WHY THE PANEL'S SETTLE COULD NOT DO IT. `SettingsSurface` resolves the verdict when its
     * press settles, which is the moment `signedCommand` has SEALED and APPENDED: the reply is a
     * relay round trip away, and `CommandVerdict.of` refuses to claim an outcome carrying no code
     * -- so that reading is UNANSWERED on every run this app has ever made, and both of
     * `PairOnlyScreen.revokeNoticeFor`'s answered arms were unreachable. The panel's own comment
     * argues there is no later draw to ask again from, which is true of that PANEL: this surface
     * redraws on every resume and every journal event, and the id outlives the panel in
     * [PhoneRuntime] for exactly that reason.
     *
     * THE PANEL'S SENTENCE IS THE FALLBACK AND NOT THE ANSWER. It is what an unanswered,
     * unreadable or never-issued revoke says -- including the routed reason a revoke that never
     * reached the wire failed, which no outcome can carry -- and a machine that has since answered
     * replaces it. With silence, where the removal was confirmed: a warning drawn over a state
     * that is fine teaches the user to ignore the one that is not.
     */
    private fun revokeNotice(app: App): String {
        val issued = runtime.revokeOperation()
        if (issued.isEmpty()) return settings.unpairNotice
        val verdict = try {
            CommandVerdict.of(
                FacadeBridge(app).launchOutcome(issued),
                issued,
                CommandVerdict.ACCEPTED_OK,
            )
        } catch (unreadable: Exception) {
            // A facade that cannot answer has not answered, and the next draw asks again.
            return settings.unpairNotice
        }
        if (!verdict.answered) return settings.unpairNotice
        // THE OTHER FACT TRAVELS WITH IT (agents-tracker-jx23). This line REPLACES the sentence
        // the panel composed, so anything that sentence carried and this call does not is dropped
        // the moment the machine answers -- and what the panel carried is whether the key material
        // at rest survived the purge, which no reply from the machine has any bearing on.
        return PairOnlyScreen.revokeNoticeFor(verdict, purgeFailure = runtime.purgeFailure())
    }

    /**
     * Draw the bar, and rebuild it only when what it says has changed.
     *
     * THE EQUALITY CHECK IS NOT AN OPTIMISATION, for the reason [inboxDrawn] records: [render]
     * runs on every resume, after every action AND on every journal event, and the scaffold holds
     * [contentHost] -- so a bar rebuilt unconditionally would re-parent the destination under
     * whoever is using it, at whatever rate their agents happen to be producing events.
     */
    private fun drawScaffold(tabs: List<InboxTab>) {
        // THE APP IS ON SCREEN, SO THE UNPAIRED PHONE'S SCREEN IS NOT -- [drawPairOnly]'s clearing,
        // in the other direction. The offer is untaken again as well: a handset revoked later comes
        // back to the sentence explaining why its app is empty, not to a camera it did not ask for.
        pairOnlyDrawn = null
        pairingStarted = false
        val next = tabs to destination
        if (next == barDrawn && host.childCount > 0) return
        barDrawn = next
        // BOTH HOSTS SURVIVE THE REBUILD, and both have to be taken out of the scaffold that is
        // about to be discarded: Android refuses an `addView` of a child that still claims a
        // parent. The banner joined the content host here rather than in `detachHostedViews`
        // because it is not hosted IN the content -- that is the whole of the fix.
        (contentHost.parent as? ViewGroup)?.removeView(contentHost)
        (bannerHost.parent as? ViewGroup)?.removeView(bannerHost)
        host.removeAllViews()
        host.addView(
            phoneScaffoldView(
                context = activity,
                content = contentHost,
                tabs = tabs,
                destination = destination,
                onSelectDestination = ::selectDestination,
                banner = bannerHost,
            ),
        )
    }

    /**
     * Draw what the app has to say about its link, redrawn only when it has changed.
     *
     * THE EQUALITY CHECK IS [drawInbox]'s AND SHARPER HERE. This runs on every resume, after every
     * action and on every journal event, and the banner sits above a destination somebody may be
     * scrolling; rebuilding it unconditionally would re-lay out the whole column at whatever rate
     * their agents produce events. [StatusBanner] is a data class of three strings, so "has
     * anything a user can read changed" is one comparison.
     *
     * THE HOST IS ALWAYS FILLED, INCLUDING WITH SILENCE. A silent banner is a tagged container
     * holding no lines rather than no container at all, which costs nothing on screen -- it has no
     * fill, no border and no padding of its own -- and keeps the slot findable, so a test can tell
     * "the phone has nothing to report" from "the slot went away again".
     *
     * WHERE THE ONE CONTROL LEADS IS DECIDED HERE (agents-tracker-agre), because navigation is the
     * surface's and the composition cannot know it. The two states that offer it -- `RELAY_UNTRUSTED`
     * and `RELAY_INSECURE` -- are terminal AND still paired, so the offer must not be `BeginPairing`:
     * `swarm remote pair` is refused while this device is registered (PB-STATE-10), and a control
     * that walked into that refusal would be PB-APP-10's failure loop one step further along. The
     * Settings destination leads with the `Pairing` section, whose one control clears the
     * registration first; [StatusBanner.PAIR_AGAIN] is named for it.
     */
    private fun drawBanner(banner: StatusBanner) {
        if (banner == bannerDrawn && bannerHost.childCount > 0) return
        bannerDrawn = banner
        bannerHost.removeAllViews()
        bannerHost.addView(
            statusBannerView(activity, banner) { selectDestination(Destination.SETTINGS) },
        )
    }

    /**
     * Draw the destination the user is on.
     *
     * @param bridge null on the branch where the phone core refused, which is the only reason
     *  three of the four destinations can have nothing to draw.
     * @param inbox null for the same reason.
     */
    private fun drawContent(bridge: FacadeBridge?, inbox: InboxScreen?) {
        when (destination) {
            Destination.INBOX -> when (val open = detailPanel(bridge)) {
                null -> drawInbox(inbox)
                else -> drawDetail(open)
            }
            Destination.MACHINES -> drawMachines(bridge)
            Destination.ACTIVITY -> drawActivity(bridge)
            Destination.SETTINGS -> drawSettings()
        }
    }

    /**
     * PB-DS-9's triage inbox, redrawn only if it has changed.
     *
     * THE EQUALITY CHECK IS NOT AN OPTIMISATION. [render] runs on every resume, after every gated
     * action AND on every journal event, so rebuilding the view hierarchy each time would throw
     * the list back to the top under whoever was scrolling it -- while an agent working steadily
     * produces events steadily. [InboxScreen] is a data class of data classes, so "has anything a
     * user can see changed" is one comparison.
     *
     * @param screen null on the branch with no roster, where what is left under this tab is the
     *  unrecomposed column alone -- the pairing panel above all, which is the one thing that might
     *  get such a handset out of that state.
     */
    private fun drawInbox(screen: InboxScreen?) {
        // `detailDrawn == null` IS THE THIRD CLAUSE AND IT IS NOT AN OPTIMISATION EITHER. Backing
        // out of a session lands here with the list's data unchanged, so without it the early
        // return fires and the drill-down stays on screen over a tab that thinks it popped.
        if (screen == inboxDrawn && detailDrawn == null && contentShows == Destination.INBOX) return
        inboxDrawn = screen
        detailDrawn = null
        hostContent(
            when (screen) {
                null -> unrecomposedControls
                else -> triageInboxView(
                    context = activity,
                    screen = screen,
                    onSelectSession = ::selectSession,
                    onSelectScope = ::selectScope,
                    below = unrecomposedControls,
                )
            },
        )
    }

    /**
     * PB-APP-3's session detail, as the model of it, or null when no session is drilled into.
     *
     * IT READS THE SAME TWO FACADE CALLS THE REST OF THE SCREEN ALREADY MAKES, which is why
     * opening a session costs no new traffic: the journal page is the whole retained log
     * [drawActivity] already reads, narrowed here to one session by `JournalRow.sessionId`, and the
     * grid is the peek [renderReady] already reads for the watched session.
     *
     * THE SNAPSHOT MAY BE EMPTY ON THE FIRST DRAW AND THAT IS HONEST. `App.Peek` is a cache the
     * MACHINE fills, and only for a session this phone has WATCHED -- [watch] follows [detail] one
     * line below this in [renderReady], so the frame arrives on a later event.
     * [SessionDetailPanel.hasSnapshot] draws no card at all until it does, which says "we have not
     * heard from this session" rather than "this session's screen is blank".
     *
     * @param bridge null on the branch where the phone core refused, where there is no detail to
     *  read. [renderUnavailable] closes the drill-down on that branch rather than leaving a user
     *  inside a screen nothing can fill.
     */
    private fun detailPanel(bridge: FacadeBridge?): SessionDetailPanel? {
        val open = detail ?: return null
        if (bridge == null) return null
        // AND A REFUSAL FOR WANT OF A LEASE ENDS IT (agents-tracker-agre). The verdict is the
        // machine's answer to a take_control that may be older than the refusal this session's last
        // press just earned; without this clause the panel goes on labelling the control `Stop`,
        // and pressing it collects `swarm/no-lease` again.
        val lease = leaseVerdictFor(open, bridge).accepted && leaseRefusedFor != open
        val grid = bridge.terminalPeek(open, leaseHeld = lease)
        val log = bridge.journal(JOURNAL_FROM_THE_START, WHOLE_JOURNAL)
        return SessionDetailScreen.of(
            SessionDetail(
                sessionId = open,
                journal = log.rows.filter { it.sessionId == open },
                snapshotText = grid.text,
                leaseHeld = lease,
                // ONLINE IS THE PEEK'S AND NOT A SECOND OPINION. `TerminalPeek.online` is the
                // transport fact `FacadeBridge` already derived, and it is the clause that decides
                // whether a confirmed Stop is sent or discarded.
                online = grid.online,
                journalStale = log.stale,
                // PB-INPUT-1's notice answers a PRESS (agents-tracker-4lta), and this is where the
                // press is read back: the Stop plan latched the session it could not send for, and
                // a notice about another session's press would be the proximity error again.
                stopNotSent = stopNotSentFor == open,
                // THE SNAPSHOT'S OWN STALENESS (agents-tracker-0qe7), which rides the read the
                // grid already came on. It was dropped here, so the card was drawn with only the
                // JOURNAL's verdict beside it -- a different fact, with a different remedy, about
                // a different stream.
                snapshotStale = grid.stale,
            ),
        )
    }

    /**
     * Draw the drill-down, redrawn only when the panel has changed under it -- [drawInbox]'s
     * reason, and sharper here: the transcript grows while the user is reading it.
     *
     * THE LABELS ARE THE PANEL'S AND ARE WRITTEN NOWHERE ELSE. The two controls are built with no
     * words on them at all (see [SLOT_LABEL]); what Stop reads changes with the lease, and a second
     * copy of either sentence in this file is the defect PB-DS-9's "copy belongs to the screen"
     * exists to prevent.
     */
    private fun drawDetail(panel: SessionDetailPanel) {
        val routed = outcome.text.toString()
        if (panel == detailDrawn && routed == detailOutcomeDrawn &&
            contentShows == Destination.INBOX
        ) {
            return
        }
        detailDrawn = panel
        detailOutcomeDrawn = routed
        stop.text = panel.stopLabel
        kill.text = panel.killLabel
        hostContent(
            sessionDetailView(
                context = activity,
                panel = panel,
                stop = stop,
                kill = kill,
                // PB-APP-9's routed line, which is a child of the column this screen replaces. It
                // is handed in rather than left behind, because Stop and Kill reach a machine from
                // here and a refusal with nowhere to land is a control that fails silently.
                outcome = routed,
                onBack = ::closeSessionDetail,
            ),
        )
    }

    /**
     * PB-APP-5's machines screen, and **it draws nothing, which is a report rather than an
     * omission.** [dev.swarm.phone.ui.screens.machinesPanelView] is built, composed from the kit
     * and covered by its own suite; what is missing is the two facts it renders, and neither can
     * be supplied from here without inventing it:
     *
     *  - **Presence.** `MachinesPanel` takes `MachinePane.presence`, which is `App.Presence` -- a
     *    BLOCKING RELAY ROUND-TRIP with no timeout. android/unbound-verbs.tsv ledgers the verb
     *    deliberately unbound in exactly these words: "this surface's render() is now driven by an
     *    event stream -- so calling it per redraw would issue a relay RPC per journal record. It
     *    needs a screen that polls it on its own cadence, off the main thread." [render] is that
     *    render(), so wiring it here is the defect the ledger describes, spelled out in advance.
     *  - **The paired device's name.** `MachinePane.pairedDeviceName` has NO bound accessor. The
     *    string exists once, in Go (`mobile/pairing.go` sends `DeviceName: "swarm phone"`), and no
     *    facade verb returns it. Typing it here would be a second copy of a Go constant rendered as
     *    though the wire had carried it, which is ADR-007 B135's defect class and the one this
     *    project has spent the most effort on.
     *
     * So the tab navigates, the bar stays, and the screen arrives with the two accessors.
     *
     * **WHAT IT DRAWS IN THE MEANTIME IS A SENTENCE, AND IT USED TO BE NOTHING.** A blank
     * destination looked like the answer [SettingsSurface.render] and [renderUnavailable] give,
     * and it is not the same case: those are ERROR BRANCHES, reached when something went wrong,
     * while this is the STEADY STATE of a primary tab -- blank on every tap, for as long as the
     * gap lasts. PB-DS-9 spends its longest argument on exactly that distinction and rules the
     * other way: an empty section is still a section, because dropping it makes "there is nothing
     * here" indistinguishable from "this failed to load". A blank primary destination reads as a
     * crash. So the tab shows the kit's `emptyState` carrying [MachinesPanelScreen.UNAVAILABLE_COPY],
     * which is the screen's own copy and says what is true of this phone without claiming anything
     * about the machine.
     *
     * **AND ABOVE THAT SENTENCE IS NOW THE HALF OF THIS SCREEN THAT NEEDS NO RELAY**
     * (agents-tracker-ah2). The two gaps above are both about verbs that must ask the relay or
     * that have no accessor at all; PB-TIME-1's clock verdict and PB-APP-8's per-channel staleness
     * are neither. `App.ClockVerdict` reads a mutex-guarded field, `App.StreamState` and
     * `App.ResyncPending` read local core state, and none of the three costs a round trip -- so
     * [dev.swarm.phone.ui.screens.LinkPanelScreen] renders what this phone knows about its own
     * link while the machine's own details stay out of reach. Both models had been fully built and
     * drawn by nothing, which is the same defect one level down from the one this tab was already
     * carrying.
     *
     * THE SENTENCE MOVES UNDER THE SECTION AND IS OTHERWISE UNCHANGED. It is the caveat about what
     * is missing, and a caveat above the facts it qualifies would be read as the whole screen.
     */
    private fun drawMachines(bridge: FacadeBridge?) {
        // PULLED PER DRAW, NEVER LATCHED. Both accessors say why in their own KDoc: on Android the
        // process is killed and rebuilt constantly, so a screen that opens after the measurement
        // was never sent the event, and one that latched the event has nothing to clear it with.
        val panel = bridge?.let { LinkPanelScreen.of(it.clockBanner(), it.streamViews()) }
        // THE EQUALITY CHECK IS [drawActivity]'s, AND IT REPLACES AN UNCONDITIONAL EARLY RETURN.
        // This tab used to draw once and never again, which was right for a constant sentence and
        // is wrong the moment the content is live: a clock corrected or a stream repaired while
        // the tab is on screen has to reach it.
        if (panel == machinesDrawn && contentShows == Destination.MACHINES) return
        machinesDrawn = panel
        val unavailable = emptyState(activity, MachinesPanelScreen.UNAVAILABLE_COPY)
        hostContent(
            when (panel) {
                null -> unavailable
                else -> linkPanelView(activity, panel, below = unavailable)
            },
        )
    }

    /**
     * PB-APP-5's activity log, redrawn only when the journal has changed under it.
     *
     * THE WHOLE RETAINED LOG IS READ, and both arguments say so rather than picking a page size
     * this screen would have to paginate. `App.ReadJournal` walks entries AFTER a cursor and
     * treats a non-positive limit as its own bound (`journalLogSize`), so [JOURNAL_FROM_THE_START]
     * and [WHOLE_JOURNAL] ask for everything the phone is holding -- which is what a feed with no
     * paging control can honestly show. [ActivityPanelScreen] puts it newest-first.
     */
    private fun drawActivity(bridge: FacadeBridge?) {
        val panel = bridge?.let {
            // agents-tracker-3nx6: GUARDED, the way [FacadeBridge.terminalPeek] already guards
            // `App.peek` (agents-tracker-9ds). Reachable from [PhoneEvents.onEvent]'s `main.post`
            // on every journal event while the Activity tab is on screen; a refusal from
            // `it.journal(...)` used to propagate uncaught onto the main looper instead of
            // reaching this tab's own fallback below.
            try {
                ActivityPanelScreen.of(it.journal(JOURNAL_FROM_THE_START, WHOLE_JOURNAL))
            } catch (refused: Exception) {
                outcome.text = it.routeFacadeError(refused.message.orEmpty()).message
                null
            }
        }
        if (panel == activityDrawn && contentShows == Destination.ACTIVITY) return
        activityDrawn = panel
        // agents-tracker-j171: SOMETHING RATHER THAN NOTHING on the branch with no panel, for
        // [drawMachines]' reason exactly -- a blank primary tab reads as a crash. `bridge == null`
        // is reachable only from [renderUnavailable], which has already written PB-APP-9's routed
        // failure onto [status]; this is the same sentence the Inbox tab already carries, not a
        // second one invented here.
        hostContent(
            panel?.let { activityPanelView(activity, it) }
                ?: emptyState(activity, status.text.toString()),
        )
    }

    /**
     * PB-APP-7's settings, which is a DESTINATION and used to be a block halfway down the inbox.
     *
     * It is hosted once and never rebuilt here: [SettingsSurface] owns what is inside its own root
     * and redraws it from [render], including emptying it on the branch where the phone core
     * refused.
     */
    private fun drawSettings() {
        if (contentShows == Destination.SETTINGS) return
        hostContent(settings.root)
    }

    /** Put [view] under the bar, and record which destination it is. */
    private fun hostContent(view: View?) {
        contentShows = destination
        detachHostedViews()
        contentHost.removeAllViews()
        view?.let { contentHost.addView(it) }
    }

    /**
     * Take the surface's own long-lived views out of whatever last held them.
     *
     * `removeAllViews` on the content host detaches the INBOX, and the controls are two levels
     * inside it -- so without this they arrive at their next `addView` still claiming a parent, and
     * Android refuses that with "the specified child already has a parent". The settings root is
     * here for the same reason now that it is a destination of its own, and the pairing root now
     * that [drawPairOnly] hosts it: it is the one view in this list that can be left parented by a
     * screen this host never held.
     */
    private fun detachHostedViews() {
        for (view in listOf(unrecomposedControls, settings.root, pairing.root)) {
            (view.parent as? ViewGroup)?.removeView(view)
        }
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

    /**
     * Open a session, which is what tapping a row now MEANS.
     *
     * IT USED TO ONLY RETARGET THE COLUMN BELOW. A tap set [chosen] and the loose controls started
     * acting on a different session, several screens further down, with nothing between the row and
     * them saying so -- a selection the user could not see they had made. It is a destination now,
     * and inventory C2 is what is on the other side of it.
     */
    private fun selectSession(id: String) {
        chosen = id
        detail = id
        render()
    }

    /**
     * Leave the drill-down: §4's chevron, and the system back gesture through [PhoneActivity].
     *
     * It clears LOCAL SCREEN STATE and nothing else, which is the boundary PB-SEC-11 draws around
     * the exported component that reaches it. [chosen] deliberately survives, so the row the user
     * came back from is still the selected one on the list they came back to.
     */
    internal fun closeSessionDetail() {
        detail = null
        // AND THE UNSENT PRESS IS FORGOTTEN WITH THE SCREEN THAT REPORTED IT. It is the answer to
        // one press on one screen; carried across a departure it would greet the user on their
        // return with a failure from before they left.
        stopNotSentFor = ""
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
     * Go to the destination a tapped tab names.
     *
     * IT KEEPS THE SESSION AND THE SCOPE. Navigating away from the inbox and back is not a change
     * of mind about which session the controls act on, and dropping the choice would make the tab
     * bar clear a selection the user still has on screen when they return. The DRILL-DOWN is kept
     * for the same reason: a user who checks the activity feed mid-session comes back to it.
     *
     * TAPPING THE TAB YOU ARE ALREADY ON POPS IT, AND THE DESIGN IS SILENT ON THAT. It is the
     * platform convention, adopted deliberately rather than derived: a tab that does nothing when
     * tapped reads as dead, and it is the only way back for a user who does not use the gesture and
     * has scrolled the chevron off the top. It is navigation behaviour rather than a fact about the
     * machine, which is the line the "never render what the wire does not carry" rule actually
     * draws.
     */
    private fun selectDestination(next: Destination) {
        if (next == destination) detail = null
        destination = next
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
        send.enable(enabled)
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
     * Ask for the lease, and REMEMBER THE OPERATION, which is what makes PB-INPUT-2's lease a fact
     * rather than a literal. The lease is not on any snapshot: it is the outcome of THIS
     * take_control, claimed by operation id, and [leaseConfirmedFor] is what asks the machine
     * about it.
     *
     * IT IS A FUNCTION AND NOT A LAMBDA ON ONE BUTTON because the peek's `[Take control]` is no
     * longer the only way in: the session detail's Stop offers the same step to an observer, in the
     * words [SessionDetailPanel.stopLabel] chooses, and a second copy of these three lines is a
     * second place for the operation id to be forgotten.
     */
    private fun takeControlOf(target: String) = Press(
        SendPlane.COMMAND,
        verb = { app -> app.takeControl(target) },
        // THE OPERATION ID IS REMEMBERED ON THE LOOPER, not beside the verb where it used to be
        // written. `leaseOp` and `leaseSession` are read by `render` on every draw, so latching
        // them from a lane would publish a lease to the drawing thread through a plain field.
        settle = { answer -> rememberLease(answer, target) },
    )

    /**
     * Latch the take_control this surface issued, so [leaseConfirmedFor] can claim its answer by
     * operation id (PB-SYNC-2) rather than resolving it by proximity.
     *
     * @param answer what the verb returned, which is a `swarmmobile.Op` for every caller here.
     *  Typed as `Any?` because [Press] carries one settle shape for six controls; a change to
     *  `App.TakeControl`'s return type therefore fails to latch rather than failing to compile,
     *  which is why the cast is written as one that cannot throw on a live handset.
     */
    private fun rememberLease(answer: Any?, target: String) {
        val issued = answer as? Op ?: return
        leaseOp = issued.operationID
        leaseSession = target
        // AND THE REFUSAL THAT SENT THE USER HERE IS SPENT (agents-tracker-agre). The step the
        // no-lease refusal offered has now been taken, so what decides the lease from here is the
        // machine's answer to THIS operation; leaving the latch up would hold the keyboard shut
        // over a lease the machine goes on to grant.
        leaseRefusedFor = ""
    }

    /** Latch the kill this surface issued, so [renderKillVerdict] can claim its answer. */
    private fun rememberKill(answer: Any?) {
        val issued = answer as? Op ?: return
        killOp = issued.operationID
    }

    /** Hand [LaunchScreen] the operation id the MACHINE keyed this launch by. See [rememberLease]. */
    private fun submitLaunch(draft: LaunchDraft, answer: Any?) {
        val issued = answer as? Op ?: return
        launchScreen.submit(draft, issued.operationID)
    }

    /**
     * What Stop asks before it acts, which is nothing at all for an observer.
     *
     * [SessionDetail.stop] resolves to CONFIRM only once the lease is held; without one the press
     * is the take_control the label offers, and a confirmation over "shall I take control" would
     * be a question about a step the user just chose by reading the button.
     */
    private fun stopQuestion(): String = detailDrawn
        ?.takeIf { it.stopAction == StopAction.CONFIRM }
        ?.stopConfirmation
        .orEmpty()

    /**
     * What the MACHINE said about the control lease for [session].
     *
     * IT ASKS ABOUT ONE OPERATION -- the take_control this surface issued -- and refuses to
     * answer about any other session, because an outcome attributed by proximity is the error
     * PB-SYNC-2's operation ids exist to prevent. A phone that has taken control of nothing, or
     * whose target moved to a different first row, holds no lease and says so.
     *
     * IT RETURNS THE VERDICT AND NOT A BOOLEAN (agents-tracker-qlf9). `ControlLease.confirmedBy`
     * answered the keyboard's question correctly and threw away the rest of the reply, so every
     * refusal reached the screen as "your machine has not confirmed control ... Take control
     * first" -- which reads as "you have not pressed the button yet" and offers as the remedy the
     * step that was just declined. [PeekPanelScreen.leaseNoticeFor] is what needs the rest.
     */
    private fun leaseVerdictFor(session: String, bridge: FacadeBridge): CommandVerdict {
        if (leaseOp.isEmpty() || session != leaseSession) return CommandVerdict.UNANSWERED
        return try {
            ControlLease.verdictOf(bridge.launchOutcome(leaseOp), leaseOp)
        } catch (unreadable: Exception) {
            // A facade that cannot answer has not confirmed anything, and fail-closed here is a
            // shut keyboard rather than a keystroke sent against a lease nobody vouched for.
            CommandVerdict.UNANSWERED
        }
    }

    /**
     * PB-APP-9 for the two session controls that used to answer with nothing: the machine's
     * verdict on the kill and on the take_control this surface issued (agents-tracker-qlf9).
     *
     * IT IS `renderLaunch`'S PROGRAM ON THE OTHER TWO VERBS. The launch form polls its outcome per
     * draw and resolves it by operation id; kill and take_control reached the same facade method
     * and read none of it. This runs BEFORE [drawContent] because the session detail draws
     * whatever is on the outcome line, and a verdict written after it lands on screen a whole
     * event later.
     *
     * SAID ONCE PER OPERATION. The outcome stays in the core's durable map, so an unlatched claim
     * would re-toast on every journal record for as long as the app is open.
     */
    private fun renderVerdicts(bridge: FacadeBridge) {
        renderKillVerdict(bridge)
        renderLeaseVerdict(bridge)
    }

    private fun renderKillVerdict(bridge: FacadeBridge) {
        if (killOp.isEmpty() || killOp == killSaid) return
        val verdict = try {
            CommandVerdict.of(bridge.launchOutcome(killOp), killOp, CommandVerdict.ACCEPTED_OK)
        } catch (unreadable: Exception) {
            // Unresolved is the honest state, and the next draw asks again.
            return
        }
        if (!verdict.answered) return
        killSaid = killOp
        // A KILL THE MACHINE CARRIED OUT SAYS NOTHING. The session leaving the roster is the
        // confirmation, and [SessionDetailScreen] is where that silence is decided rather than
        // here -- `remote-control-mock.html` wrote no toast for a kill.
        val notice = SessionDetailScreen.killNoticeFor(verdict)
        if (notice.isNotEmpty()) say(PressFeedback.ofRefusal(notice))
    }

    private fun renderLeaseVerdict(bridge: FacadeBridge) {
        if (leaseOp.isEmpty() || leaseOp == leaseSaid) return
        val verdict = leaseVerdictFor(leaseSession, bridge)
        if (!verdict.answered) return
        leaseSaid = leaseOp
        if (verdict.accepted) return
        // THE PEEK SHOWS THIS SENTENCE TOO, and that is [PressFeedback]'s rule rather than a
        // duplication: the line and the peek keep the message where it can be re-read, and the
        // toast puts it in front of the eye that was on the control -- which on this surface may
        // be the session detail's Stop, on a screen the peek is not composed into at all.
        say(PressFeedback.ofRefusal(PeekPanelScreen.leaseNoticeFor(confirmed = false, verdict)))
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
        // REVOKE IS NOT AMONG THEM AND NEVER WAS SUBJECT TO THIS, which is why it is Settings' now
        // rather than a control this function has to remember to leave alone: it is the panic
        // action, and a phone whose session list is empty -- or whose machine is unreachable -- is
        // exactly the state its owner may need it in.
        takeControl.enable(enabled)
        // The session detail's two, which cannot in fact be on screen while this is false -- an
        // open drill-down IS the target, so the roster cannot be empty under one. They are here
        // because they act on the chosen session, which is what this function is about.
        stop.enable(enabled)
        kill.enable(enabled)
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
     *
     * @param ask what to ASK before the verb runs, or the empty string for a control that acts on
     *  the press. The question is a function rather than a string because the two controls that use
     *  one are the session detail's, and what they ask is the panel on screen's.
     */
    private fun actionButton(
        text: String,
        ask: () -> String = { "" },
        plan: () -> Press?,
    ): Button = SecureWindow.gate(
        Button(activity).apply {
            this.text = text
            setOnClickListener { control -> confirmThenPress(control, ask(), plan) }
        },
    )

    /**
     * Put the question the screen wrote in front of the user, and act only if they answer it.
     *
     * IT IS A DIALOG AND NOT A PART OF ANY COMPOSITION, which is `sessionDetailView`'s own ruling:
     * a confirmation is a second window over the screen rather than a row inside it, so it is built
     * here and the screen never learns it happened.
     *
     * THE TWO BUTTON WORDS ARE THE PLATFORM'S AND THE QUESTION IS THE SCREEN'S. PB-DS-9 assigns
     * copy to the screen and [SessionDetailPanel] writes both questions; `ok` and `cancel` are
     * Android's own localised strings, so answering yes reads in the user's language rather than in
     * a third copy of "Confirm" typed at this call site.
     *
     * WHAT IT DOES NOT CARRY IS PB-SEC-12 CLAUSE 1, and that is a limit rather than an oversight.
     * The tap that OPENS the dialog is filtered; the dialog's own buttons live in a window this
     * surface does not own and `filterTouchesWhenObscured` is a property of a View in it. What the
     * confirmation buys against an overlay is different and still real: a tap the user could not
     * see now has to be followed by a second one on a window that was not there before.
     */
    private fun confirmThenPress(control: View, question: String, plan: () -> Press?) {
        if (question.isEmpty()) return press(control, plan)
        AlertDialog.Builder(activity)
            .setMessage(question)
            .setPositiveButton(android.R.string.ok) { _, _ -> press(control, plan) }
            .setNegativeButton(android.R.string.cancel, null)
            .show()
    }

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
     * ONE THING COMES WITH [ctaButton] THAT A `Button` HAD FOR FREE, and it is handled here because
     * the kit cannot: a `TextView` announces itself as text rather than as a button, and the kit has
     * no click to hang the role on (`CtaButton`'s own KDoc records the gap). The role is set below.
     *
     * THE DISABLED APPEARANCE WAS THE SECOND AND IS NOW THE KIT'S. It used to be recorded here as a
     * deliberate omission -- `Button` dims itself, this did not, and derivation row 24 had no
     * implementation -- so [launch] refused the tap while drawing full-strength phosphor green with
     * its bloom, which is a dead control that looks pressable. [ctaButton] now paints row 24's pair
     * (`--p-hair` fill, `--p-ink3` ink, no bloom) off the view's own drawable state, so every
     * `isEnabled = false` below both refuses the tap and looks refused, with nothing to remember at
     * this call site.
     */
    private fun ctaAction(
        text: String,
        kind: CtaKind,
        plan: () -> Press?,
    ): TextView = SecureWindow.gate(
        ctaButton(activity, text, kind).apply {
            setOnClickListener { control -> press(control, plan) }
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
     * What one press does, in the three phases the main thread requires.
     *
     * A press used to be one lambda run inside the click listener. It cannot be, for two
     * independent reasons that pull in opposite directions:
     *
     *  - the FACADE CALL must not run on the looper, because it is a Go network call and
     *    `awaitConn` can sit on it for five seconds ([dispatch]'s own KDoc has the rest);
     *  - the SCREEN READS AND WRITES around it must, because `typed`, the three launch fields,
     *    `detailDrawn`, `launchScreen` and `leaseOp` are all owned by the looper, and a lane
     *    that touched them would be a data race the emulator would never show.
     *
     * So a control declares what to read before it acts, what crosses to Go, and what the answer
     * changes on screen -- and only the middle one leaves the main thread.
     *
     * @param plane which of the facade's two send planes the verb resolves through. It is
     *  declared per press and not per control because [stop] is on both:
     *  `mobile/commands.go`'s command path polls `awaitConn` for a connection and its live path
     *  deliberately does not (ADR-007 D7), and the two must not share a lane.
     * @param verb the facade call, and NOTHING else. It runs on a lane. It must not touch a View.
     * @param settle what the answer changes on screen, back on the looper. It runs only if the
     *  verb returned; a refusal goes to the outcome line and the toast instead.
     * @param confirmation what a toast says when the verb RETURNS, or null where the design wrote
     *  no words for this press -- which is most of them, and which is silence rather than a
     *  sentence made up here. `remote-control-mock.html` fires a toast for seven actions and for
     *  nothing else; a confirmation authored at this seam would be copy invented in the one place
     *  PB-DS-9 keeps copy out of. It is PER PRESS and not per control for [plane]'s reason: Stop
     *  is two different actions behind one button, and only one of them is an interrupt.
     */
    private class Press(
        val plane: SendPlane,
        val verb: (App) -> Any?,
        val settle: (Any?) -> Unit = {},
        val confirmation: String? = null,
    )

    /**
     * Press [control], having planned what that means on the thread that owns the screen.
     *
     * @param plan runs HERE, on the looper: it reads the fields, applies the model's own
     *  refusals, and returns null for a press that resolves without reaching the wire -- a launch
     *  draft missing a required field, a Stop with no lease to send on. A null still redraws,
     *  because the refusal it just recorded is what the screen has to show.
     */
    private fun press(control: View, plan: () -> Press?) {
        when (val startup = runtime.phone()) {
            is PhoneStartup.Unavailable -> outcome.text = startup.error.message
            is PhoneStartup.Ready -> {
                val app = startup.app
                val planned = plan()
                if (planned != null) return dispatchPress(control, app, planned)
            }
        }
        render()
    }

    /**
     * Hand one planned press to its lane, and its answer back to the looper.
     *
     * THE OUTCOME LINE IS CLEARED FIRST. It holds the LAST command's answer, and a control that
     * is now responsive would otherwise leave that answer sitting under a press in flight, where
     * it reads as this press's. Empty is still what an unconfirmed success shows: there is no
     * in-flight state in any of the 25 rows of docs/design/substrate-components.md, so what
     * separates "still crossing" from "done" is that [VerbDispatch] holds the control disabled
     * until the answer lands (derivation row 24's pair, which the kit already paints off the
     * view's own drawable state) -- and that is DELIBERATELY UNCHANGED here.
     *
     * WHAT IS NEW IS WHERE THE ANSWER LANDS. The outcome line is a child of
     * [unrecomposedControls], which is hosted at the bottom of the Inbox tab -- so a refusal
     * produced by a control on Machines, Activity or a session detail was written to a view the
     * user could not see. Row 1's toast is shown over whatever screen is up, and the line KEEPS
     * the message it always had: a routed error frequently names a remedy ("try again once the
     * connection is back"), and a remedy that scrolls away in 3.2 seconds is worse than one that
     * sits still. [PressFeedback] is where that decision is written down and tested; this is the
     * one place it is spent.
     *
     * THE FEEDBACK IS APPLIED BEFORE [Press.settle] RATHER THAN AFTER, so that a settle which has
     * something of its own to say about the same press -- a launch answer, a field cleared -- is
     * writing over the generic answer rather than under it.
     */
    private fun dispatchPress(control: View, app: App, planned: Press) {
        outcome.text = ""
        dispatch.press(
            control,
            planned.plane,
            work = { planned.verb(app) },
            settle = { answer ->
                answer.fold(
                    onSuccess = {
                        say(PressFeedback.ofSuccess(planned.confirmation))
                        planned.settle(it)
                    },
                    // Everything the facade refuses arrives as an exception whose message carries
                    // the error class as a prefix, so it routes through the same table as every
                    // other failure rather than being shown raw.
                    onFailure = {
                        val refusal = PressFeedback.ofRefusal(
                            FacadeBridge(app).routeFacadeError(it.message.orEmpty()),
                        )
                        // PB-APP-9's remedy becomes the control it names (agents-tracker-agre).
                        // `swarm/no-lease` says the machine will not carry this session's keystrokes,
                        // which the screen's own lease fact cannot know -- see [leaseRefusedFor] --
                        // so the answer is latched here and the next draw offers take control in
                        // place of a Stop that would earn the same refusal.
                        if (refusal.offersTakeControl) leaseRefusedFor = session
                        say(refusal)
                    },
                )
                render()
            },
        )
    }

    /**
     * Put one press's answer on screen: the persistent line, and row 1's toast.
     *
     * A TOAST IS SHOWN ONLY WHEN THERE IS SOMETHING TO SAY. An empty one is a 92 dp-high box that
     * flashes over the tab bar for 3.2 seconds carrying nothing, which is what an unconfirmed
     * success would produce if this asked no question.
     */
    private fun say(feedback: PressFeedback) {
        outcome.text = feedback.line
        if (!feedback.saysNothing) toasts.show(feedback.toast)
    }

    /**
     * Enable a control unless a press of it is still crossing to the machine.
     *
     * IT IS A FUNCTION AND NOT SIX ASSIGNMENTS, and the reason is a hole this change would
     * otherwise have opened. [render] runs on every journal event, and it sets these flags -- so
     * a control disabled at press time was re-enabled by the next event to arrive, one tap into a
     * command still in flight. Two Launches is worse than the frozen UI this replaces, so the
     * in-flight mark is [dispatch]'s and every enable is asked about it here.
     */
    private fun View.enable(on: Boolean) {
        isEnabled = on && !dispatch.inFlight(this)
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

        /**
         * The activity feed's read window: every record after the beginning, and as many as the
         * core is holding. `App.ReadJournal` bounds the second itself (`limit <= 0` becomes
         * `journalLogSize`), so asking for the whole log is one value rather than a page size this
         * screen would then owe a paging control for.
         */
        const val JOURNAL_FROM_THE_START = 0L
        const val WHOLE_JOURNAL = 0L

        /**
         * What a control built as a SLOT is labelled before the screen that places it has said.
         *
         * The session detail's Stop and Kill are built once and live for the process, and both read
         * as their panel says: Stop's wording differs for an observer -- [SessionDetailPanel] picks
         * between two sentences on the lease -- and typing either of them here would be a second
         * copy of a screen's copy, which is what PB-DS-9 assigns to the screen. Neither control is
         * ever on screen without a panel to fill it, so the blank is never read by anybody.
         */
        const val SLOT_LABEL = ""
    }
}
