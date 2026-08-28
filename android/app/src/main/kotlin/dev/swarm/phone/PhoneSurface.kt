package dev.swarm.phone

import android.graphics.Rect
import android.os.SystemClock
import android.text.Editable
import android.text.TextWatcher
import android.view.Gravity
import android.view.View
import android.view.ViewGroup
import android.view.accessibility.AccessibilityNodeInfo
import android.widget.Button
import android.widget.EditText
import android.widget.FrameLayout
import android.widget.ImageView
import android.widget.LinearLayout
import android.widget.ScrollView
import android.widget.TextView
import androidx.appcompat.app.AlertDialog
import androidx.appcompat.app.AppCompatActivity
import dev.swarm.phone.keys.ConnectionState
import dev.swarm.phone.runtime.ConnectivityPolicy
import dev.swarm.phone.runtime.LifecycleConvergence
import dev.swarm.phone.runtime.LifecycleEvent
import dev.swarm.phone.runtime.RuntimeState
import dev.swarm.phone.runtime.SocketDisposition
import dev.swarm.phone.ui.ApprovalDecision
import dev.swarm.phone.ui.CapabilityNotice
import dev.swarm.phone.ui.CommandVerdict
import dev.swarm.phone.ui.ErrorRouter
import dev.swarm.phone.ui.RoutedError
import dev.swarm.phone.ui.ControlLease
import dev.swarm.phone.ui.FacadeBridge
import dev.swarm.phone.ui.LaunchDraft
import dev.swarm.phone.ui.LaunchRendering
import dev.swarm.phone.ui.LaunchScreen
import dev.swarm.phone.ui.MachineLabel
import dev.swarm.phone.ui.PressFeedback
import dev.swarm.phone.ui.SessionDetail
import dev.swarm.phone.ui.SyncStatus
import dev.swarm.phone.ui.StopAction
import dev.swarm.phone.ui.TriageInbox
import dev.swarm.phone.ui.kit.ComposerActionGlyph
import dev.swarm.phone.ui.kit.CtaKind
import dev.swarm.phone.ui.kit.Haptics
import dev.swarm.phone.ui.kit.Motion
import dev.swarm.phone.ui.kit.NoticeKind
import dev.swarm.phone.ui.kit.SendState
import dev.swarm.phone.ui.kit.KitTag
import dev.swarm.phone.ui.kit.ToastHost
import dev.swarm.phone.ui.kit.composerAction
import dev.swarm.phone.ui.kit.composerBar
import dev.swarm.phone.ui.kit.conversationHeader
import dev.swarm.phone.ui.kit.conversationMenu
import dev.swarm.phone.ui.kit.ctaButton
import dev.swarm.phone.ui.kit.decisionPill
import dev.swarm.phone.ui.kit.earlierChip
import dev.swarm.phone.ui.kit.emptyState
import dev.swarm.phone.ui.kit.navHeaderDrill
import dev.swarm.phone.ui.kit.notice
import dev.swarm.phone.ui.kit.overflowControl
import dev.swarm.phone.ui.kit.screenAir
import dev.swarm.phone.ui.kit.textField
import dev.swarm.phone.ui.screens.PendingSend
import dev.swarm.phone.ui.screens.ActivityPanel
import dev.swarm.phone.ui.screens.ActivityPanelScreen
import dev.swarm.phone.ui.screens.ApprovalSheetPanel
import dev.swarm.phone.ui.screens.ApprovalSheetScreen
import dev.swarm.phone.ui.screens.Destination
import dev.swarm.phone.ui.screens.GlobalInboxRowModel
import dev.swarm.phone.ui.screens.InboxScreen
import dev.swarm.phone.ui.screens.InboxTab
import dev.swarm.phone.ui.screens.LaunchDeliveryNotice
import dev.swarm.phone.ui.screens.LaunchFieldId
import dev.swarm.phone.ui.screens.LaunchPanel
import dev.swarm.phone.ui.screens.LaunchPanelScreen
import dev.swarm.phone.ui.screens.LaunchPresetPanel
import dev.swarm.phone.ui.screens.LaunchPresetScreen
import dev.swarm.phone.ui.screens.PresetRowModel
import dev.swarm.phone.ui.screens.MachinesDestination
import dev.swarm.phone.ui.screens.MachinesPanel
import dev.swarm.phone.ui.screens.MachinesPanelScreen
import dev.swarm.phone.ui.screens.MachinesScreen
import dev.swarm.phone.ui.screens.PairOnlyReason
import dev.swarm.phone.ui.screens.PairOnlyScreen
import dev.swarm.phone.ui.screens.Presentation
import dev.swarm.phone.ui.screens.DeepLinkAnchor
import dev.swarm.phone.ui.screens.DeepLinkLanding
import dev.swarm.phone.ui.screens.RevokeNotice
import dev.swarm.phone.ui.screens.SessionDetailOpen
import dev.swarm.phone.ui.screens.SessionDetailPanel
import dev.swarm.phone.ui.screens.TerminalFallbackBinding
import dev.swarm.phone.ui.screens.TerminalFallbackModel
import dev.swarm.phone.ui.screens.TerminalGrid
import dev.swarm.phone.ui.screens.terminalFallbackView
import dev.swarm.phone.ui.screens.SessionDetailScreen
import dev.swarm.phone.ui.screens.SheetTag
import dev.swarm.phone.ui.screens.TranscriptScreen
import dev.swarm.phone.ui.screens.TriageInboxScreen
import dev.swarm.phone.ui.screens.TranscriptRoute
import dev.swarm.phone.ui.screens.activityPanelView
import dev.swarm.phone.ui.screens.conversationScaffoldView
import dev.swarm.phone.ui.screens.diffScreen
import dev.swarm.phone.ui.screens.outputScreen
import dev.swarm.phone.ui.screens.globalInboxView
import dev.swarm.phone.ui.screens.launchPanelView
import dev.swarm.phone.ui.screens.launchPresetView
import dev.swarm.phone.ui.screens.machinesPanelView
import dev.swarm.phone.ui.screens.approvalSheetView
import dev.swarm.phone.ui.screens.pairOnlyView
import dev.swarm.phone.ui.screens.phoneScaffoldView
import dev.swarm.phone.ui.screens.sessionDetailRedraw
import dev.swarm.phone.ui.screens.sessionDetailView
import dev.swarm.phone.ui.screens.syncPillView
import dev.swarm.phone.ui.screens.syncStatusView
import dev.swarm.phone.ui.screens.triageInboxView
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
     * therefore detached on every other destination. Those three are [syncHost]'s now, above the
     * scaffold's content, where a tab change cannot reach them.
     *
     * WHAT IS LEFT IS THE ONE THING THAT IS NOT ABOUT THE LINK. A core that would not start is not
     * a machine that cannot be reached: there is no phone to ask, no roster, and no drill-down --
     * [renderUnavailable] closes all of it -- so the sentence belongs with the screen that branch
     * draws rather than in the standing banner, which reports on a link this handset does not have.
     */
    private val status = heading()

    /**
     * PB-KEY-8's non-fatal half. [dev.swarm.phone.keys.CustodyPlanner] records a capability the
     * handset did not confirm that no matrix row consumes; until this label existed the record
     * was computed on every launch and read by nobody.
     *
     * IT WAS `notice` UNTIL agents-tracker-ksvb.4. With the kit's `notice` factory imported into
     * this file a property of that name shadows it across the whole class, and the compiler reports
     * it as "Type checking has run into a recursive problem" on the declaration itself.
     */
    private val capabilityNotice = noticeLine()

    /**
     * PB-APP-9's routed line, and the ERROR variant because every value it ever holds is a refusal.
     *
     * The five writes below it are `routeFacadeError`, `startup.error.message`,
     * `PhoneRuntime.unlockContent`'s `RoutedError` and `PressFeedback.line` -- and that last one is
     * the load-bearing check: `ofSuccess` and `ofUnsent` both set `line = ""` and speak in the
     * toast instead, deliberately, so a success never reaches this view at all. `PairingSurface`'s
     * outcome line looks identical and is NOT the error variant, for the reason its own declaration
     * gives.
     */
    private val outcome = noticeLine(NoticeKind.ERROR)

    /**
     * ADR-009 (4)'s approval card, in the host the terminal peek used to occupy.
     *
     * IT IS A HOST AND NOT A PANEL, which is the peek's own reason and is sharper here. The inbox
     * is redrawn only when [InboxScreen] changes -- [drawInbox] argues why -- and an approval
     * arrives on its own clock, at the moment an agent stops and asks. Composing the card inside
     * the inbox's tree would tie one to the other: either the card would arrive late, or the list
     * would be thrown back to the top under whoever was scrolling it.
     *
     * **IT IS THE SAME SLOT IN THE SAME COLUMN, and that is the whole of what half 2 moved here.**
     * What stood in it was `peekHost` and the grid: `ADR-009-structured-chat-interaction` (3)
     * deletes it at slice I1's exit, and (4) says what a phone shows instead when a session is
     * blocked on a human -- "the phone renders the sanitized prompt region as a CARD whose buttons
     * carry the same signed `ActionApprove` op every other approval card uses". The place on screen
     * where a user used to go to read what their machine was waiting for is where the question now
     * is, with the answer beside it.
     *
     * **IT IS NO LONGER A CAPTIVE CHILD OF ONE COLUMN, AND THAT IS agents-tracker-dwwv.2.4'S WHOLE
     * MOVE.** Until this bead it was `addView`'d into [unrecomposedControls] at construction and
     * never touched again, which put the inbox list in the ONE place a pending approval could be
     * seen or answered: [detachHostedViews] takes the whole column off screen on the way into a
     * session's detail, and this host went with it. So [openApproval] -- the tap on the same
     * question inside the transcript this card answers -- had to CLOSE the detail to reach it,
     * which is the defect the audit named by line: "tapping an approval row in the transcript
     * calls openApproval ... which navigates OUT to the inbox where the sheet is parented."
     *
     * IT IS [approvalSlot]'S NOW, on [statusHost]'s own pattern: a screen tree is built before it
     * is hosted, so a detach done at hosting time runs after the `addView` that would throw "the
     * specified child already has a parent". [drawInbox] asks for the slot to keep the inbox entry
     * point working; [drawDetail] asks for the same slot so the session detail can place it right
     * under the transcript block that names it (`SessionDetailView.DetailTag.APPROVAL`) -- one
     * component, tagged [dev.swarm.phone.ui.screens.SheetTag.HOST], reparented to whichever of the
     * two screens the pending session is open on. [drawApproval] fills or empties its CHILDREN and
     * never asks which of the two currently holds it.
     */
    private val approvalHost = LinearLayout(activity).apply {
        orientation = LinearLayout.VERTICAL
        layoutParams = LinearLayout.LayoutParams(MATCH, WRAP)
        tag = SheetTag.HOST
    }

    /** PB-APP-6's form, hosted for [approvalHost]'s reason: it is redrawn when its notice changes. */
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
    //
    // AND THE SYNC PILL, which the settings nav row carries like the other two (agents-tracker-
    // nx44.2). It is handed as a PROVIDER rather than as a view because the panel redraws itself:
    // [statusSlot] detaches the one host from whichever screen held it last, and this surface is
    // not told when the panel decides to rebuild.
    // AND THE MACHINE-SWITCHER ENTRY (wave R4, bead agents-tracker-0ox9). The settings panel
    // places the row and spends the model's recorded name; where it goes is navigation state this
    // surface owns, so the callback crosses the same way the pill provider does.
    private val settings = SettingsSurface(activity, runtime, dispatch).also {
        it.onReplaced = ::render
        it.toasts = toasts
        it.statusSlot = ::statusSlot
        it.onOpenMachines = ::openMachines
    }

    // TAKE CONTROL AND RELEASE CONTROL WERE DELETED HERE (owner ruling R1, 2026-08-26).
    // composer_send and turn_interrupt take no lease at any layer, so neither button changed
    // anything on the wire: one un-greyed a field for a verb that never needed it, and the
    // other gave back something nothing had taken. `App.TakeControl` and `App.ReleaseControl`
    // return to android/unbound-verbs.tsv, where ReleaseControl already sat.

    /**
     * The interrupt, planned ONCE for the two things that press it: the composer's square while
     * the agent works and the field is empty ([send]), and the header menu's Stop row
     * ([chooseFromMenu]). Phone refit W3 (owner ruling): the full-width Stop above the bar is
     * gone, no question stands in front of the interrupt, and what the press says is one word
     * under the composer ([rememberInterrupt]).
     *
     * IT INTERRUPTS THROUGH `App.Interrupt` AND NOT BY WRITING ANY BYTE ITSELF, which since Wave
     * R6 is the SIGNED turn_interrupt op (`mobile/commands.go`, Mirror M2.4): the daemon types
     * the session adapter's own recorded cancel sequence, so a phone that composed a keystroke
     * here would be guessing a key the daemon refuses to guess. The op takes no lease (owner
     * ruling R1), and the only question left is the link's ([SessionDetail.confirmStop]).
     *
     * THE BRANCH IS TAKEN ON THE MAIN THREAD, and it has to be: `detailDrawn` is the panel this
     * surface last drew, written by `render` and owned by the looper. Reading it from a lane
     * would be a data race on the fact that decides what this press does.
     *
     * AND IT NAMES THE TURN IT WAS DRAWN AGAINST (Wave R6 review finding B7). `App.Interrupt`
     * REQUIRES a non-empty expected_turn: a Stop rendered against turn A that lands in turn B
     * types the adapter's cancel sequence into whatever the machine is doing NOW -- which in
     * playbook 8.1 is the turn the owner just started at the terminal, where the cancel key
     * clears their half-typed line. The turn is read HERE, on the looper that owns it.
     */
    private fun interruptPlan(): Press? {
        val target = session
        val turn = detailDrawn?.expectedTurn.orEmpty()
        return when (detailDrawn?.confirmedStopAction) {
            StopAction.SEND_INTERRUPT -> {
                stopNotSentFor = ""
                Press(
                    // COMMAND since Wave R6: App.Interrupt is the signed turn_interrupt op
                    // (sealSignedCommand -> sendContext -> awaitConn), not a keystroke, so its
                    // press rides the command lane like every other signed verb (s25's fence
                    // is what holds the declaration to the verb's real destination policy).
                    SendPlane.COMMAND,
                    verb = { app -> app.interrupt(target, turn) },
                    // SILENT HERE, BY DECISION (W3.4). `App.Interrupt` returns the moment the
                    // envelope is appended, so the sealing is a fact about the phone; the settle
                    // draws it under the composer, never as row 1's toast. `interrupt_unsupported`
                    // and `stale_turn` are facts only the machine has, and [renderInterruptVerdict]
                    // is where they reach the reader.
                    confirmation = "",
                    settle = { answer -> rememberInterrupt(answer) },
                )
            }
            // NOT_SENT: input is live-only and this one is discarded rather than held (ADR-007
            // D7). The press is recorded and said out loud where the finger was
            // (agents-tracker-4lta), so PB-INPUT-1's notice becomes a report of something that
            // happened rather than a sentence that was already on screen.
            StopAction.NOT_SENT -> {
                stopNotSentFor = target
                say(PressFeedback.ofUnsent(SessionDetail.NOT_SENT_NOTICE))
                null
            }
            // CONFIRM is the model's arm and never this surface's: the square resolves it
            // directly (W3.3), and `confirmStop()` answers only the two above.
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
     *
     * IT IS `.a2-no`, the treatment the full-width Stop shared until phone refit W3 took that
     * control off the composer: an action whose result cannot be taken back. What separates it
     * from the square that stops now is the question above, not the colour.
     */
    private val kill = actionButton(
        SLOT_LABEL,
        CtaKind.DENY,
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
     *
     * `mono = true` (agents-tracker-ksvb.7): it sits directly under [peekHost]'s `Mono.Code`
     * grid, and it was the one field on this surface still set in row 9's proportional
     * `Body.Message` -- a keystroke rendered in a face the terminal itself never uses.
     */
    private val typed = field("Type into the session you hold", mono = true)

    /**
     * ONE CARRIAGE RETURN IS APPENDED, AND THAT IS WHAT "A LINE" MEANS AT A TERMINAL -- the key
     * a shell waits for is CR, and a control that sent the characters without it would leave the
     * user's command sitting unsubmitted with nothing on screen explaining why.
     *
     * The bytes are UTF-8 and nothing on this side interprets them. There is no VT emulator on
     * the handset (ADR-007 D2): what goes out is what was typed.
     *
     * IT IS `.a2-ok`: sending is the composer's whole purpose, and derivation row 9's own bar draws
     * its send affordance as the primary thing on it.
     *
     * **THE SECOND CHAMPAGNE IN [unrecomposedControls] IS RESOLVED BY LEAVING** (agents-tracker-
     * ksvb.4 recorded it, agents-tracker-nx44.6 spends it). [launch] is `.a2-ok` too, and while
     * both hung in that column the Inbox destination drew two primaries at once. The repair
     * ksvb.4 named is the one that landed: this field and this button are derivation row 9's
     * composer now, on the session detail, and the launch form keeps the column to itself.
     */
    /**
     * The composer's ONE control, and the whole of phone refit W3 (owner ruling): a 40 dp square
     * that sends what is in the field, or interrupts the agent when the session is WORKING and
     * the field is EMPTY -- both read AT PRESS TIME from the live field and the last-drawn panel,
     * on the looper that owns them. A fast typist whose tap lands after their first character
     * sends; a tap on an empty field over a working agent stops. [drawComposerAction] keeps the
     * glyph and the spoken word on the same predicate on every draw and on every text change, so
     * what the finger presses is what the eye was shown.
     *
     * THE SEND HALF IS UNCHANGED (Mirror M2.4): what the machine does with a `composer_send` is
     * type the line into the session's own composer and submit it; it is a SIGNED operation that
     * resolves, so it rides the command lane, it needs no lease, and it is refused legibly rather
     * than silently when the conversation has moved on. The stop half is [interruptPlan], which
     * the header menu's Stop row presses too.
     *
     * IT IS AN `ImageView` THE KIT BUILT, WITH [pressable]'S PLUMBING: PB-SEC-12 clause 1's touch
     * filter, the lane, the accessibility role. The words it speaks are the screen's
     * ([SessionDetailScreen.COMPOSER_SEND], [SessionDetailScreen.COMPOSER_STOP]) and the glyphs
     * are the kit's ([ComposerActionGlyph]); this file names neither a drawable nor a colour.
     */
    private val send: ImageView = pressable(composerAction(activity)) {
        if (stopping()) {
            interruptPlan()
        } else {
            // Read here, on the looper that owns the field. The lane never touches a View.
            val target = session
            val line = typed.text.toString()
            val turn = detailDrawn?.expectedTurn.orEmpty()
            // The last send's report goes the moment a new one is planned: a notice about a
            // refusal the user has already answered by typing again is a report of the wrong
            // press.
            composerSendFor = target
            composerSentText = line
            composerRefusal = ""
            composerRefusalDetail = ""
            composerSendState = SendState.PENDING
            Press(
                // COMMAND, and the plane change IS the verb change: `composer_send` is a SIGNED
                // operation that resolves, so it rides the lane that polls `awaitConn` for a
                // connection, exactly like every other signed verb on this surface.
                SendPlane.COMMAND,
                verb = { app -> app.composerSend(target, turn, line) },
                // THE FACADE'S OWN refusals, and ONLY those: a phone with no link, an
                // unreconciled handset, an empty line. They are the ones that resolve without
                // the machine, so they are the ones this hook can answer -- see
                // [rememberComposerSend] for the refusals that arrive later, which is every
                // refusal the daemon authors.
                refused = { routed ->
                    composerSendState = SendState.REFUSED
                    composerRefusal = routed.state.name
                },
                // THE SETTLE LATCHES THE OPERATION AND CHANGES NOTHING ELSE (review round 2).
                //
                // IT USED TO SET `SendState.SENT` AND RUN `typed.text.clear()` HERE, under a
                // comment reading "THE FIELD IS EMPTIED ONLY ON THE MACHINE'S ACCEPTANCE" --
                // which was false. `VerbDispatch.press` settles on the FACADE CALL returning, and
                // `App.ComposerSend` returns its `Op` the instant the envelope is appended to the
                // mailbox. So the composer reported a send as delivered on LOCAL SEALING, and a
                // send the daemon went on to refuse was shown as sent with the user's words
                // already erased.
                settle = { answer -> rememberComposerSend(answer) },
            )
        }
    }

    /**
     * Whether the square STOPS rather than sends, read from the live field: the session works --
     * ONE source, [SessionDetailPanel.composerWorking], the fact the header word and the
     * placeholder read -- and nothing is typed.
     */
    private fun stopping(): Boolean = detailDrawn?.composerWorking == true && typed.text.isBlank()

    /**
     * The square's glyph and spoken word, on [stopping]'s predicate: square + "Stop" when the
     * agent works and the field is empty, arrow + "Send" otherwise. Called on every draw of the
     * composer region and on every text change, so the control never shows one thing and does
     * another.
     */
    private fun drawComposerAction() {
        val glyph = if (stopping()) ComposerActionGlyph.STOP else ComposerActionGlyph.SEND
        send.setImageLevel(glyph.ordinal)
        send.contentDescription = when (glyph) {
            ComposerActionGlyph.STOP -> SessionDetailScreen.COMPOSER_STOP
            ComposerActionGlyph.SEND -> SessionDetailScreen.COMPOSER_SEND
        }
    }

    init {
        // THE FIELD IS READ LIVE (owner ruling, W3.2): the moment there is a draft the control is
        // Send, and the moment it is cleared the control is Stop again.
        typed.addTextChangedListener(
            object : TextWatcher {
                override fun beforeTextChanged(s: CharSequence?, start: Int, count: Int, after: Int) = Unit
                override fun onTextChanged(s: CharSequence?, start: Int, before: Int, count: Int) = Unit
                override fun afterTextChanged(s: Editable?) = drawComposerAction()
            },
        )
        drawComposerAction()
    }

    /**
     * ADR-014's "load earlier" (Mirror M3.1), and the FIRST caller of `App.LoadEarlierInteractions`.
     *
     * IT PAGES BY ITEM ID AND NEVER BY CURSOR (IS-ENV-2). The id is the oldest item this phone
     * holds for the session, read off the panel this surface last drew; a daemon restart's
     * reconciliation legitimately re-delivers the same items at new cursors, so a cursor-paged
     * read would silently skip or repeat after one.
     *
     * IT IS A SLOT ON THE DETAIL rather than a control the screen builds, for [resyncControl]'s
     * reason: the verb is live-only and refuses with a routed class, and routing a refusal through
     * PB-APP-9's table is this surface's job.
     */
    private val loadEarlier = pressable(earlierChip(activity, SLOT_LABEL)) {
        val target = session
        val before = detailDrawn?.loadEarlierBeforeItem.orEmpty()
        if (before.isEmpty()) {
            null
        } else {
            backfilledAt[target] = SystemClock.elapsedRealtime()
            Press(
                SendPlane.COMMAND,
                verb = { app -> app.loadEarlierInteractions(target, before, HISTORY_PAGE) },
                // AND THE PAGE IS CLAIMED, WHICH IS WHAT FOLDS IT (review round 2). The reply
                // becomes the transcript inside `App.Outcome` and nowhere else, so a press that
                // forgot the operation id was a control that could never do anything: "Load
                // earlier" reached the machine, the machine answered, and the answer sat in the
                // reply cache while the control went on being offered.
                settle = { answer -> rememberHistoryRead(answer, target, aloud = true) },
            )
        }
    }

    /**
     * Derivation row 9's bar, holding [typed] and [send] (agents-tracker-hxv, nx44.6).
     *
     * IT IS BUILT ONCE AND RE-PARENTED, which is every slot on this surface's rule and is sharper
     * here than anywhere else: the field holds what the user has typed and not yet sent, and a bar
     * rebuilt per draw would empty it at the rate the machine produces journal events.
     */
    private val composer = composerBar(activity, typed, send)

    /**
     * The conversation header's way back: the design's own chevron, and nothing beside it.
     *
     * **IT IS LIFTED OUT OF [navHeaderDrill] RATHER THAN BUILT, AND THAT IS INTERIM.** The kit's
     * back control is `NavHeaderDrill.kt`'s private `backControl` -- the tinted `swarm_nav_back`
     * path, the 48 dp floor on both edges, row 23's focus ring, `KitTag.DRILL_BACK` -- and
     * `conversationHeader` takes the control as a SLOT the surface fills, so the surface needs a
     * factory to fill it with. There is no public one yet. The two alternatives were worse: a
     * `ctaButton` here is a champagne call-to-action where the drawing draws a chevron, and a
     * `TextView` assembled in this file would be this surface choosing an appearance, which is
     * the one thing PB-DS-9 keeps out of a call site. A REQUEST to expose `backControl(context,
     * label)` is filed; when it lands, these four lines become one call and nothing else changes.
     *
     * THE LABEL IS BLANKED AND THE WORDS BECOME THE DESCRIPTION, which is the drawing's own
     * header: `‹` alone, with the session name taking the row. `navHeaderDrill` pairs the glyph
     * with a visible label because a screen TITLE row has space for one; a conversation header
     * spends that space on the machine and the state. The words are not lost -- they are what a
     * screen reader reads, set per panel from [SessionDetailPanel.back], which is the field that
     * has always existed for exactly that ("the label a screen reader reads; the chevron is the
     * kit's").
     */
    private val back: View =
        navHeaderDrill(activity, back = SLOT_LABEL, title = "").let { row ->
            val control = row.findViewWithTag<View>(KitTag.DRILL_BACK)
            row.removeView(control)
            control.apply { setOnClickListener { closeSessionDetail() } }
        }

    /**
     * The header's trailing mark, and the 48 dp square that replaced 160 dp of stacked CTAs
     * (owner ruling R2).
     *
     * IT IS NOT [SecureWindow.gate]D AND THAT IS THE RULE RATHER THAN AN OMISSION. PB-SEC-12
     * clause 1 is about controls that ACT -- destructive or authorising -- and opening a menu
     * acts on nothing: an overlay that stole this tap would show the user a menu. The tap that
     * matters is the one on `Kill session` inside the menu, and the menu itself is gated where it
     * is built ([drawConversationMenu]).
     */
    private val overflow: TextView = overflowControl(activity).apply {
        setOnClickListener { toggleConversationMenu() }
    }

    /**
     * The drawing's one persistent affordance: *Decision needed*, above the composer, while the
     * machine is blocked on this reader.
     *
     * IT IS ADDED AND REMOVED RATHER THAN HIDDEN, which is this surface's standing rule ("a view
     * that is not on screen is a view this did not add") -- and it is the ONE child of
     * [composerRegion] that moves on the model's say-so, deliberately: the bar and its notice are
     * permanent, because the bar holds what the user has typed. Inserting at index 0 touches
     * neither. ([stoppedNotice] comes and goes on the same rule, at the end of the region.)
     *
     * **WHAT IT DOES NOT YET DO, NAMED RATHER THAN IMPLIED.** The drawing shows it only while the
     * unanswered decision is OFF SCREEN. That needs the scroll position of the block, which is a
     * listener on the scaffold's `ScrollView` and belongs with the inline decision card it points
     * at (Wave E). Until then it is drawn whenever a decision is unanswered, which is a superset:
     * it never fails to appear when it is owed, and it can appear beside the card it names. The
     * tap is the same landing `openApproval` already resolves BY ITEM ID, so a decision that
     * resolved while the reader was reading scrolls nowhere rather than to something else.
     */
    private val decisionPillControl: TextView =
        decisionPill(activity, SLOT_LABEL).apply {
            setOnClickListener { detailDrawn?.pendingDecisionId?.let(::openApproval) }
        }

    /**
     * What a composer that is on screen and CANNOT SEND says under itself.
     *
     * **THIS CLOSES D.10'S HALF OF THE OFFLINE STATE, WHICH THIS FILE WAS CLAIMING AND NOT DOING.**
     * `ComposerModel` computes two sentences for `ComposerAvailability.OFFLINE` -- "Not connected
     * to your machine" and "Messages are never held - nothing is sent when the link returns" --
     * and the second is the one fact on that sheet a reader cannot deduce from a greyed field.
     * The bar stayed (the sink exists, the link is coming back, the draft is worth typing) and
     * `composerShut` was DISCARDED on that arm, so the hint came from `placeholderFor(working)`
     * alone and both sentences were read by nothing: exactly the "computed and read by nothing"
     * defect the wave exists to close, inside the wave.
     *
     * IT IS A `notice` AND NOT A SLOT THAT COMES AND GOES, because an empty one draws no height at
     * all -- `notice` spends no padding -- so the region's permanent children never change and
     * the composer is never re-parented. The three shut states that lose the bar say the same two
     * sentences INSIDE the scroll instead, where the composer would have been
     * (`DetailTag.COMPOSER_ABSENT`); this is the fourth, which keeps its bar.
     */
    private val composerShutDetail: TextView = noticeLine()

    /**
     * "Stopped", once, under the composer (phone refit W3.4): the SEALING of the interrupt, drawn
     * where the finger was and never as a toast.
     *
     * IT IS ADDED AND REMOVED RATHER THAN HIDDEN, [decisionPillControl]'s arrangement for its
     * reason: a view that is not on screen is a view this surface did not add. [drawStopped]
     * adds it when the envelope is sealed and writes the word over the open turn
     * ([stoppedOverTurn]); [drawComposerRegion] takes it off on the first draw whose open turn is
     * a different one -- for a Stop that landed, the turn closing -- and keeps it through every
     * draw of the same turn (W3 review round: the settle renders in its own dispatch, and a
     * working agent draws at output rate). It is the sealing and not the agent's answer:
     * `interrupt_unsupported` and `stale_turn` arrive later through [renderInterruptVerdict] and
     * say so on the outcome line.
     */
    private val stoppedNotice: TextView = noticeLine().apply {
        gravity = Gravity.CENTER_HORIZONTAL
        screenAir()
    }

    /** The turn [stoppedNotice] was said over, or "" while it is off screen. */
    private var stoppedOverTurn = ""

    /**
     * What the conversation pins under the thumb: the bar that sends a line, and the notice under
     * it.
     *
     * **IT IS ONE REGION BECAUSE IT IS ONE CONTROL** (phone refit W3, owner ruling). Ruling R2 put
     * Stop with the composer and Kill in the header's menu so that neither costs vertical space
     * in the READING FLOW; W3 finishes the thought: the full-width Stop that stood above the bar
     * is gone, and stopping is what the bar's own square does while the agent works and the field
     * is empty ([send]). Stop stays reachable from the header menu for a reader with a draft in
     * the field ([chooseFromMenu]). The scaffold has one pinned slot below its scroll
     * ([conversationScaffoldView]), and this is what goes in it.
     *
     * NOTHING HERE IS REBUILT. The bar is permanent -- it holds what the user typed -- and so is
     * the notice under it; what comes and goes is the decision pill at index 0 and, after a Stop,
     * [stoppedNotice] at the end.
     */
    private val composerRegion: LinearLayout = LinearLayout(activity).apply {
        orientation = LinearLayout.VERTICAL
        layoutParams = LinearLayout.LayoutParams(MATCH, WRAP)
        addView(composer)
        addView(composerShutDetail)
        composerShutDetail.screenAir()
    }

    /**
     * PB-SYNC-1's repair, and `App.Resync`'s FIRST CALLER (agents-tracker-upbo).
     *
     * WHY IT IS HERE AND NOT ON THE LINK SECTION, which is where upbo sited it: that section drew
     * four verdicts on the Machines destination -- both deleted by agents-tracker-nx44.3 -- and the
     * gap is felt on the screen where a conversation has records missing from it. The refusal it needs somewhere to render is the
     * routed line this screen is already handed -- ErrClassRateLimited has a row in PB-APP-9's
     * table with its own remedy ("wait a moment before trying again"), so a rate-bounded press
     * lands there like every other refusal rather than needing a rule of its own.
     *
     * AND IT IS WHAT MAKES `StreamBadge.RESYNCING` REACHABLE. `App.Resync` marks `resyncAsked`
     * before it seals anything, `App.ResyncPending` reads it back, and until this control existed
     * nothing in production set it -- so the third badge value was one no user could produce. The
     * two readers of it now are `SyncStatus`'s detail sheet and the settings CONNECTION section,
     * both of which count a repair in flight as a gap that has not closed yet.
     */
    private val resyncControl = actionButton(SLOT_LABEL, CtaKind.MORE) {
        Press(
            SendPlane.COMMAND,
            // THROUGH THE ADAPTER AND NOT `app.resync("journal")`. The four channel names cross as
            // bare strings and are spelled once, in `FacadeBridge.REPAIR_CHANNELS`; a name typed
            // at a call site is a second alphabet that `android/gate/pbapp8_repairchannels_test.go`
            // refuses outright. `dispatchPress` already builds a bridge around the App this way.
            verb = { app -> FacadeBridge(app).repairTranscript() },
        )
    }

    /**
     * PB-INPUT-1's acknowledgement (agents-tracker-hxv).
     *
     * It is a SEPARATE control from the notice it clears because the verb is separate, and
     * `App.ClearUndeliveredInputs` says why: "a screen that OPENS must see the backlog, and a user
     * who DISMISSES it says so once, for every screen". It is process-wide for the same reason --
     * the ledger is -- so a user who clears it on one session clears it everywhere, which is what
     * "says so once" means.
     */
    private val acknowledge = actionButton(SLOT_LABEL, CtaKind.MORE) {
        Press(
            SendPlane.COMMAND,
            verb = { app -> FacadeBridge(app).clearUndelivered() },
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
    private val launch = actionButton("Launch a session", CtaKind.APPROVE) {
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

    /**
     * The add-computer form's two boxes (wave R4, bead agents-tracker-0ox9), on the launch form's
     * own reasoning: `App.AddMachine` takes a machine id and a display name, this surface has
     * neither, and a value supplied on the user's behalf would be a hardcoded pairing shipped in
     * production code. The hints are the SCREEN MODEL's recorded copy (PB-DS-9).
     */
    private val addMachineId = field(MachinesPanelScreen.ADD_ID_HINT)

    private val addMachineName = field(MachinesPanelScreen.ADD_NAME_HINT)

    /**
     * The add form as one re-parentable host, for [launchHost]'s reason sharpened by [composer]'s:
     * the boxes hold what the user has typed and not yet sent, and a form rebuilt per draw would
     * empty them at the rate the machine produces journal events. [machinesPanelView] takes it as
     * the NAMED `addForm` slot; [addComputerSlot] detaches it from whichever draw held it last.
     */
    private val addComputerHost = LinearLayout(activity).apply {
        orientation = LinearLayout.VERTICAL
        layoutParams = LinearLayout.LayoutParams(MATCH, WRAP)
        for (box in listOf(addMachineId, addMachineName)) {
            addView(box)
            box.screenAir()
        }
    }

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
     * IT IS A SUB-STATE OF [Destination.INBOX] AND NOT A DESTINATION OF ITS OWN, which is
     * structural rather than aesthetic: the bar draws exactly the labels `TriageInboxScreen`
     * records and `Destination.forLabel` THROWS on a label it cannot place, so an extra value
     * would be a destination the bar cannot express and the lookup cannot produce. It also keeps the
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
            // THE REMEMBERED SCROLL BELONGS TO ONE CONVERSATION, AND THIS IS THE ONE PLACE THAT
            // KNOWS WHICH ONE IS OPEN. Every departure and every switch goes through this setter
            // -- the chevron, the back gesture, re-tapping the Inbox tab, a row tap on the list,
            // and the core refusing on a later resume -- so clearing here cannot be forgotten on
            // one of them, which is the failure mode a clause added beside each of those five
            // call sites has. Left standing, the offset would open the NEXT session at the place
            // the reader left the last one, which on a shorter conversation is its end and on a
            // longer one is the middle of somebody else's work. [conversationScrollY] argues the
            // other half: what makes it survive a REBUILD is that a rebuild does not come
            // through here.
            if (value != field) conversationScrollY = null
            field = value
            pushDrillDown()
        }

    /**
     * Whether the machine switcher is open (wave R4, bead agents-tracker-0ox9).
     *
     * IT IS A SUB-STATE OF [Destination.SETTINGS] AND NOT A DESTINATION OF ITS OWN, for exactly
     * [detail]'s structural reason: the bar draws the labels `TriageInboxScreen` records and
     * `Destination.forLabel` THROWS on one it cannot place -- and the Machines TAB is deleted by
     * decision (agents-tracker-nx44.3), so the switcher lives behind the settings screen's named
     * entry ([MachinesPanelScreen.ENTRY_LABEL]) rather than bringing the tab back. Switching tabs
     * preserves it, like the drill-down; re-tapping the Settings tab pops it, like the drill-down.
     */
    private var machinesOpen = false

    /**
     * Whether the aggregate inbox is open -- one level below [machinesOpen], reached from the
     * switcher's [MachinesPanelScreen.GLOBAL_INBOX_LABEL] entry (inbox.global).
     */
    private var globalInboxOpen = false

    /** What the switcher last drew, for [inboxDrawn]'s reason. */
    private var machinesDrawn: MachinesPanel? = null

    /**
     * The machine this phone last switched to, or empty until somebody switches (round 3).
     *
     * IT IS HERE BECAUSE THE FACADE HAS NOWHERE TO PUT IT. `App.SelectMachine` records the viewed
     * pairing for the least-recently-viewed connection policy, and `MachineInfo` carries no
     * current-machine fact back: the roster's `Connected` flag only moves when the roster exceeds
     * the cap, so two successive switches inside the cap produced the SAME panel, [drawMachines]'
     * equality guard early-returned, and a successful switch was indistinguishable from a dead
     * button. This is the fact that makes the panel differ.
     *
     * WHAT IT DOES NOT CLAIM: it is this surface's memory of a selection, so a rebuilt Activity
     * starts with no row marked. That is the honest end of the trade -- an unmarked row asserts
     * nothing, while a mark restored from a fact the facade does not publish would be invented.
     */
    private var selectedMachine: String = ""

    /**
     * The minute the switcher's row ages were computed in (round 2). The panel model carries no
     * clock, so an unchanged panel would freeze "synced 4m ago" at its first draw; folding the
     * minute into the redraw guard keeps the age as fresh as the last render, which is exactly
     * the sync pill's own freshness and no more.
     */
    private var machinesAgeMinute = 0L

    /**
     * Whether the Computers screen's add block is open (W7 review): held HERE, beside the form's
     * own fields in [addComputerHost], because [drawMachines] rebuilds the view whenever the panel
     * or the minute changes and a block that only the toggle's click composed vanished under a
     * user who was typing. The view composes the block at draw from this, and [toggleAddForm]
     * is how the header's action flips it. `machinesAddFormDrawn` is the value the last draw
     * used, so a flip is a redraw like any other change.
     */
    private var addFormOpen = false
    private var machinesAddFormDrawn = false

    /** What the aggregate inbox last drew, for [inboxDrawn]'s reason. */
    private var globalInboxDrawn: List<GlobalInboxRowModel>? = null

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

    /**
     * The one place the gesture's arming predicate exists (round 2). It is the UNION of every
     * drill sub-state -- the session detail, the switcher, the aggregate inbox -- because a
     * sub-state the predicate ignores is a screen the system back gesture exits the app from,
     * and since the flag survives the exit, re-entering lands the user back in the screen the
     * gesture failed to leave. Three writers pushing their own booleans is how that drift
     * shipped the first time; every writer calls this instead.
     */
    private fun pushDrillDown() {
        onDrillDownChanged(detail != null || machinesOpen || globalInboxOpen)
    }

    /**
     * The committed back gesture, from [PhoneActivity]: pop the drill-down the user is actually
     * standing in, innermost first -- the aggregate inbox sits one level below the switcher.
     * What crosses is still local screen state and nothing else (PB-SEC-11).
     */
    internal fun closeDrillDown() {
        when {
            // THE MENU IS THE INNERMOST THING THERE IS, and it is asked first for the same reason
            // the aggregate inbox is asked before the switcher: a sub-state the predicate ignores
            // is a screen the gesture exits THROUGH. Back over an open conversation menu closes
            // the menu; it does not take away the conversation the menu was opened on top of.
            closeConversationMenu() -> Unit
            // AND THE LITERAL SCREEN IS THE NEXT ONE IN, on the menu's own argument: back over
            // R8's output or R9's diff returns to the conversation it was opened from, not out of
            // the conversation entirely. Without this arm the gesture would close the session and
            // leave the reader on the inbox, two screens from where they were.
            closeLiteralScreen() -> Unit
            globalInboxOpen -> closeGlobalInbox()
            machinesOpen -> closeMachines()
            else -> closeSessionDetail()
        }
    }

    /**
     * What the inbox last put IN FRONT OF THE USER: null whenever the inbox list is not what is on
     * screen.
     *
     * IT ANSWERS TWO QUESTIONS AND THE SECOND IS THE ONE THAT COSTS. The first is cheap -- a
     * redraw that changes nothing rebuilds nothing. The second is ADR-009 D5's: it is the screen
     * `TriageInboxScreen.promotions` compares against, so it decides which rows sweep and whether
     * the NEEDS_YOU haptic fires. That makes "last drawn" the wrong reading and "last seen" the
     * right one, and [drawContent] is where the difference is maintained.
     */
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
     * Which destination is on screen.
     *
     * IT IS THE SURFACE'S AND NOT THE INBOX MODEL'S. `InboxScreen.tabs` carries a `selected` flag,
     * and it was written when the inbox was the only screen there was: it answers
     * `label == "Inbox"` for every list it builds. Reading it here would tell a user standing on
     * Activity that they are in the Inbox, so the selection is this field and
     * [dev.swarm.phone.ui.screens.phoneScaffoldView] takes it as a parameter.
     */
    private var destination = Destination.INBOX

    /** What the activity screen last drew, for [inboxDrawn]'s reason. */
    private var activityDrawn: ActivityPanel? = null

    /**
     * The destination the content host currently holds.
     *
     * It is not the same question as [inboxDrawn] or [activityDrawn]: those ask whether a screen's
     * DATA changed, and this asks whether the screen on screen is still the one the user chose. A
     * tab tapped while the data is unchanged changes only this.
     */
    private var contentShows: Destination? = null

    /**
     * What the SCAFFOLD last drew: which composition, over what.
     *
     * IT WAS `barDrawn` AND A `Pair` OF THE TABS AND THE DESTINATION, which was the whole question
     * while there was one composition. There are two now ([drawScaffold] has the argument), and a
     * key that could not tell them apart would leave a conversation hosted inside the tab scaffold
     * for as long as the tabs and the destination happened not to change -- which is every draw
     * between opening a session and closing it.
     */
    private var scaffoldDrawn: ScaffoldKey? = null

    /**
     * Where the reader was in the conversation when its scaffold was last discarded, or null when
     * the next conversation to be drawn is being OPENED rather than returned to.
     *
     * **IT IS A NUMBER THE SURFACE CARRIES BECAUSE THE VIEW THAT HELD IT IS GONE**
     * (agents-tracker-jz0z). [ScaffoldKey] includes `literal` and `composer`, so opening an R8
     * output screen or an R9 diff and coming back rebuilds the scaffold, as does a session losing
     * its message sink -- and `conversationScaffoldView` builds a fresh `ScrollView` per call, so
     * the offset dies with the one that is discarded. [contentHost] surviving the rebuild does not
     * save it: a `FrameLayout` has no scroll position, and the comment in [drawScaffold] that said
     * otherwise stood in this file for a whole wave while the screen it described was dumping
     * readers at the top of the transcript.
     *
     * **NULL IS A STATE AND NOT AN ABSENCE**, which is the whole of H.1 (agents-tracker-tu7z):
     * a conversation with nothing to restore opens at its NEWEST message, because the transcript
     * is oldest-at-top and the alternative -- the one that shipped -- is a reader landing on the
     * first messages of a session and never being scrolled again.
     *
     * IT IS CLEARED BY [detail]'S SETTER AND WRITTEN BY [drawScaffold], which is the split that
     * makes it correct: a rebuild is not a change of conversation and does not go through the
     * setter, and a departure is, and does. Kept while the reader is off on Activity or Settings
     * with a session still open, for the reason `selectDestination` already gives about the
     * drill-down itself: checking the feed mid-session is not leaving the session.
     */
    private var conversationScrollY: Int? = null

    /**
     * The four facts that decide which root the window holds, and what is in it.
     *
     * IT IS A TYPE AND NOT A TUPLE because two of the four are booleans, and a `Triple` of a list,
     * an enum and two `Boolean`s is a key whose halves can be swapped without anything failing.
     */
    private data class ScaffoldKey(
        val tabs: List<InboxTab>,
        val destination: Destination,
        /** Whether the conversation composition is the one on screen. */
        val conversation: Boolean,
        /**
         * Whether that conversation gets a pinned composer at all
         * ([SessionDetailPanel.composerIsBar]). A session that loses its message sink -- a torn
         * record, an ended agent -- loses the bar rather than being handed a disabled one, and
         * that is a change of COMPOSITION, so it belongs in the key rather than in a redraw.
         */
        val composer: Boolean,
        /**
         * The item whose literal is open on its own screen, or "" for the conversation itself.
         *
         * IT IS IN THE KEY BECAUSE IT CHANGES WHICH ROOT THE WINDOW HOLDS, exactly like
         * [conversation]. R8's output and R9's diff are SCREENS, not sheets over the
         * conversation: the drawing gives each one a nav header and a well that scrolls both
         * ways, and neither fits inside a column whose whole point is that only the list moves.
         */
        val literal: String = "",
    )

    /**
     * What the sync chrome last said, for [inboxDrawn]'s reason.
     *
     * It is NOT keyed on the destination, and that is the whole point of the slot: the status says
     * the same thing wherever the user is standing, so nothing about navigation invalidates it.
     */
    private var syncDrawn: SyncStatus? = null

    /**
     * Whether the sync detail is open.
     *
     * IT IS THE SURFACE'S AND NOT THE COMPOSITION'S. The chrome is rebuilt on every draw -- which
     * is to say on every journal event -- so a flag owned by the view would close the sheet under
     * a user who had just opened it, at whatever rate their agents happen to be producing work.
     */
    private var syncSheetOpen = false

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
    // THE DETAIL IS IN THE KEY WITH IT (agents-tracker-ksvb.10). It is a second cell rather than
    // a longer sentence, and [RevokeNotice] carries both so a machine reply arriving on a later
    // draw cannot be skipped by an early return keyed on the head alone.
    private var pairOnlyDrawn: Triple<Boolean, RevokeNotice, PairOnlyReason>? = null

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
    private var approvalDrawn: ApprovalSheetPanel? = null

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
     * Wave R5's preset flow state (round 2; the R4 latch idiom of [leaseOp]/[killOp]):
     *
     *  - [presetOp] is the in-flight session_launch, claimed BY OPERATION ID (PB-SYNC-2), and
     *    [presetDelivery] its resolved sentence -- [LaunchPresetScreen.noticeFor] of the claimed
     *    outcome's [LaunchPresetScreen.noticeStateFor] state. The claim is ONE-SHOT (round 3):
     *    presetOp clears once a landed outcome has rendered, because a latched op re-claims its
     *    stale sentence every pass and silently overwrites the fetch verb's refusal (D3).
     *  - [presetRefreshOp] is the in-flight launch_presets read. Claiming ITS outcome is what
     *    adopts the machine's list and this phone's stamped tier into the facade cache
     *    (`mobile/app.go Outcome` -> `adoptPresets`), so [renderPresetFlow] keeps claiming it
     *    until an answer lands; a wire refusal of the read lands on the same delivery line.
     *  - [presetDrawn] is the redraw guard ([launchDrawn]'s reason: this section redraws inside
     *    the same host as the launch form's live fields).
     */
    private var presetOp: String = ""
    private var presetDelivery: String = ""

    /**
     * The FETCH PRESETS verb's own resolved refusal, or empty (round 4, review MEDIUM 3).
     *
     * A FIELD OF ITS OWN, which is the fix. Round 3 gave the fetch verb its own COPY but both
     * verbs still wrote [presetDelivery], and the launch block's write is unconditional while
     * `presetOp` is set -- cleared only once the outcome lands NON-PENDING. So for the entire
     * in-flight window of any launch, every fetch refusal was overwritten by "The launch is on
     * its way to the machine and has not resolved yet.": the fetch control refused in silence,
     * which is the D3 defect class this wave exists to keep out. Two verbs, two slots.
     */
    private var presetFetchDelivery: String = ""
    private var presetRefreshOp: String = ""
    private var presetDrawn: LaunchPresetPanel? = null

    /** The last [FacadeBridge.launchPresetFlow] snapshot, read at render and spent at draw. */
    private var presetFlow: FacadeBridge.LaunchPresetFlow? = null

    /**
     * The FETCH_PRESETS control: one signed launch_presets read. The operation id is latched so
     * [renderPresetFlow] can claim the reply -- which is the adoption moment for both the list
     * and this phone's machine-stamped tier.
     */
    private val fetchPresets = actionButton(LaunchPresetScreen.FETCH_LABEL, CtaKind.MORE) {
        // The previous fetch's refusal is taken off screen by the press that retries it: a
        // sentence must never outlive the attempt it reports (the leaseRefusedFor rule).
        presetFetchDelivery = ""
        Press(
            SendPlane.COMMAND,
            verb = { app -> app.refreshLaunchPresets() },
            settle = { answer -> (answer as? Op)?.let { presetRefreshOp = it.operationID } },
        )
    }

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
     * The approve this surface last issued, so its answer can be claimed by operation id
     * (agents-tracker-dwwv.2.4), on [killOp]'s own reasoning -- including NO SESSION REMEMBERED
     * BESIDE IT, for [killOp]'s own reason: PB-SYNC-2's operation id is already the whole of what
     * claims this answer, and what it claims is a ONE-SHOT toast report rather than a standing
     * per-session fact any screen redraws, so there is no proximity for a second field to guard.
     *
     * `App.Approve`'s `ok` means APPLIED, not RESOLVED (mirror-program.md M1.2): the answer this
     * verdict claims is never a success to say out loud -- the `approval_resolved` item arriving
     * later is the confirmation, and [ApprovalSheetScreen] is silent on accepted for exactly the
     * reason [SessionDetailScreen.killNoticeFor] is silent on a kill that worked. What this DOES
     * have to say something about is a REFUSAL: `already_applied`, `no_dialog` and the rest all
     * mean the card this phone is holding is no longer one a tap here can act on, and
     * [renderApprovalVerdict] is where that reaches the screen.
     */
    private var approveOp: String = ""

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
     * The session the last composer send was issued for, and what became of it (Mirror M2.4,
     * ADR-009 (6): "pending -> sent -> refused ... a send that cannot get through is shown
     * refused, not silently swallowed").
     *
     * THEY ARE A PRESS AND NOT A SESSION FACT, on [stopNotSentFor]'s argument exactly: the state
     * belongs to a send this surface issued, so it is remembered beside the session it was issued
     * for and a send against one session never reports on another. Cleared when a new send is
     * planned and when the drill-down closes.
     */
    private var composerSendFor: String = ""

    /**
     * The words that were actually sent, captured at the press (owner ruling R6).
     *
     * IT IS NOT THE COMPOSER'S TEXT. The draft is spent on the daemon's acceptance and the reader
     * is free to start typing the next line immediately; reading the field to draw the pending
     * bubble would show them their NEXT message attributed to the one already in flight.
     */
    private var composerSentText: String = ""

    private var composerSendState: SendState? = null

    /**
     * PB-APP-9's routed ERROR STATE for that send, as the token `ComposerModel.noticeFor` speaks.
     *
     * IT IS THE STATE AND NOT THE MESSAGE. `stale_turn` is ORDINARY -- the conversation moved on
     * between the render and the tap -- and its remedy is mild, so it has copy of its own; every
     * other refusal shares the generic wording. Routing on the token rather than on the sentence
     * is what keeps that decision in one table (`ErrorRouter`) instead of in a string match here.
     */
    private var composerRefusal: String = ""

    /** The machine's own words for that refusal, for the notice's detail cell (W2.3). */
    private var composerRefusalDetail: String = ""

    /**
     * The four Wave R6 operations this surface has issued and not yet claimed an answer for
     * (review round 2), on [killOp]'s own argument: an operation the machine ANSWERS must be
     * remembered by id, because there is no generic outcome drain on this surface and a
     * discarded id is an answer nobody can ever ask for.
     *
     * ALL FOUR WERE FIRE-AND-FORGET. `VerbDispatch.press` settles on the FACADE CALL returning,
     * and each of these verbs returns the moment its envelope is appended to the mailbox --
     * `signedCommand` for the send and the Stop, `unsignedRead` for the two M3 reads. So the
     * composer reported delivery on local sealing, the Stop's `interrupt_unsupported` never
     * arrived, and neither read ever folded: `adoptInteractionRead` runs inside `App.Outcome`
     * and nothing called it, so "Load earlier" did nothing and a clipped card never expanded.
     *
     * THEY ARE CLAIMED ON A LATER DRAW AND NOT ON THE LANE, which is [rememberLease]'s rule:
     * these fields are read by `render` on the looper, so latching them from a lane would
     * publish an operation to the drawing thread through a plain field.
     *
     * THEY SURVIVE LEAVING THE DRILL-DOWN, deliberately and unlike the composer's own press
     * facts: an op that is never claimed is an op that stays in `App.PendingOpCount` for the
     * life of the process, and a refusal the reader walked away from is still a refusal they
     * asked for. [renderVerdicts] runs on every draw of every destination, so all four resolve
     * wherever the user has gone.
     */
    private var composerOp: String = ""

    private var interruptOp: String = ""

    private var historyOp: String = ""

    /** The session [historyOp] was issued for: a page's answer must never report on another. */
    private var historyFor: String = ""

    /**
     * Whether [historyOp]'s refusal is said OUT LOUD.
     *
     * The same read is issued by two things: the reader's own "Load earlier" press, whose
     * refusal has to reach the finger that asked, and M3.2's cold-open backfill, whose refusal
     * is deliberately silent because nobody pressed anything (see [backfillOnOpen]). One latch
     * serves both, so the difference travels with it.
     */
    private var historySpeaks: Boolean = false

    private var detailOp: String = ""

    /** The item [detailOp] was issued for: a refusal is a fact about THAT card and no other. */
    private var detailFor: String = ""

    /**
     * The cards whose whole body the machine has TERMINALLY refused, by item_id (Wave R6 review
     * round 3, finding F4).
     *
     * A card offers the fetch on `truncated`+`detail`, both journalled when the item was captured
     * -- so a body the daemon's bounded store has since evicted goes on advertising itself, and
     * the refusal left it doing so forever: tap, read "no longer kept", tap again. This is the
     * phone's memory of the one answer that settles it, and it is per ITEM because that is what
     * the answer is about. It is deliberately NOT durable: the machine's retention is its own to
     * change, and a fresh visit asking once more is a cheap question with a true answer.
     */
    private val detailRefused = mutableSetOf<String>()

    /**
     * The tool cards the reader has SHUT, by item_id (Mirror M2.2's expand/collapse).
     *
     * IT IS THE SURFACE'S AND NOT THE PANEL'S because it is a fact about this reader's screen and
     * not about the session: nothing on the wire says a card is open, and a model that
     * carried it would be a model that had to be told. Cleared with the drill-down, which is what
     * "the expansion is theirs to spend" means for the life of one visit.
     *
     * IT HOLDS THE OPENS, NOT THE CLOSES (owner ruling R3). The default inverted -- a tool run
     * is closed until the reader asks -- so what a reader spends is the OPEN, and this set
     * follows the decision rather than keeping its old sense under a name that would then lie.
     */
    private val expandedCards = mutableSetOf<String>()

    /**
     * When this surface last asked the machine for a page of history, per session, on
     * `SystemClock.elapsedRealtime`'s monotonic clock (Mirror M3.2's throttle).
     *
     * THE CLOCK IS MONOTONIC AND NOT THE WALL CLOCK. PB-APP-11 forbids trusting a wall clock the
     * user or a peer can move; what a throttle needs is elapsed time, and a backfill that a clock
     * correction let fire on every open would multiply reads against the machine-to-phone append
     * ceiling.
     */
    private val backfilledAt = mutableMapOf<String, Long>()


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

    private var approveSaid: String = ""

    /** The same one-shot latch for the Stop's own verdict (review round 2). See [killSaid]. */
    private var interruptSaid: String = ""

    /**
     * The phone's start/stop verbs on the command lane (committee round 3, the onPause
     * finding). `App.Stop` joins the relay drain's five-second graceful close, so the pause
     * path may not run it on the looper; the lane's serial order is also what keeps a resume
     * from restarting AGAINST a still-draining stop. Its eager hold replaced [connected]:
     * `lifecycle.started` is non-null exactly where the old field was.
     */
    private val lifecycle = LifecycleLane<AppLifecycle>(dispatch)

    /**
     * The facade behind [lifecycle]'s seam, cached per App so the lane's identity checks are
     * about the PHONE rather than about whichever wrapper a redraw happened to build --
     * `converge` runs on every render, and a fresh wrapper each time would read as a fresh
     * phone to start.
     */
    private var lifecycleHandle: AppLifecycle? = null

    private fun lifecycleFor(app: App): AppLifecycle {
        val held = lifecycleHandle
        if (held != null && held.app === app) return held
        return AppLifecycle(app).also { lifecycleHandle = it }
    }

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
     * WHAT IS LEFT IN IT AFTER THE LAST TWO SCREENS, which is the honest list. [approvalHost] and
     * [launchHost] are composed panels rather than loose views. What is genuinely unrecomposed is
     * the startup line, the capability notice, the outcome line, and the KEYBOARD -- the field and
     * Send.
     *
     * **THE LINK'S NEWS HAS LEFT IT, AND THAT IS THE DEFECT agents-tracker-e6mi REPORTS.** The
     * connection banner, the machine's freshness verdict and the roster's stale notice were three
     * sentences joined into [status], which is a child of this column -- so PB-APP-8's whole
     * subject was legible at the bottom of one of four tabs and nowhere else, and a link that
     * dropped while the user was reading a session changed nothing they could see. They are
     * [syncHost]'s now, above the scaffold's content and outside its scroll, and they are three
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
     * **THE COMPOSER HAS LEFT IT, AND THAT IS THE DEFECT agents-tracker-nx44.6 REPORTS CLOSING.**
     * The field and Send hung here, under `triageInboxView`'s anonymous `below:` parameter -- the
     * same parameter that hid the pairing block in defect 64rf -- while the screen that PROMISES
     * live typing is the session detail, and [detachHostedViews] rips this whole column off the
     * window on the way into it. So the app said "what you type is sent live" on a screen with no
     * field on it. They are `ui/kit/Composer.kt`'s bar now, placed by `sessionDetailView`, and they
     * arrived there WITH PB-INPUT-1's undelivered-input ledger, which is agents-tracker-hxv's
     * do-not-split ruling: a composer that accepts input with no way to report losing it is a lie
     * by omission.
     *
     * THE SETTINGS PANEL HAS LEFT IT, and that is the tab bar becoming a control. C6 is a
     * DESTINATION -- inventory C1.4's fourth tab -- and it was hosted here, halfway down a column
     * under the inbox, because there was nothing to navigate with; it is now what the Settings tab
     * shows and nothing else shows it. That is the whole of what the move is.
     *
     * IT CARRIES NO PADDING OF ITS OWN ANY MORE. The 24 dp was the last thing on this surface
     * deciding a spatial value; the kit components above it carry theirs, and these views are
     * unstyled while they wait for the components that will style them.
     *
     * **AND THAT IS WHY THE THREE LINES PAY THE SCREEN'S AIR HERE** (owner ruling 2026-08-09,
     * agents-tracker-nx44.11). This column is hosted under the Inbox's sections as
     * `triageInboxView`'s `below:`, and §4's notice line carries "no margin, no padding and no
     * gravity of its own ... the air is the composing column's" -- so with the padding gone, three
     * sentences rendered against both edges of the glass on a destination whose rows sit 12 dp in.
     *
     * **PER LINE, AND NOT ON THIS COLUMN, WHICH WOULD BE THE F2 DOUBLING.** [launchHost] holds the
     * launch form and [approvalHost] the approval sheet; both are compositions whose own children
     * already spend `screenAir` (`ui/kit/ScreenColumn.kt`). A margin on this column would stack on
     * top of theirs and walk the launch fields to 24 dp -- agents-tracker-2pnu F2 exactly -- which
     * is also why `triageInboxView` must not air the slot itself: it cannot know that what arrives
     * is half bare lines and half composed screens. The air goes to the three that arrive with
     * none, where `ScreenAirSweepTest`'s window sweep is what holds it.
     *
     * **[approvalHost] IS NOT ADDED HERE ANY MORE (agents-tracker-dwwv.2.4).** It used to be a
     * fixed child, `addView`'d once alongside the four below and never touched again -- which is
     * exactly why it could never be reparented into a session's detail. [drawInbox] adds it back
     * through [approvalSlot] on every draw that shows the list, at the same position this column
     * always held it, so the inbox entry point is unchanged; what changed is that the same host
     * can now leave.
     */
    private val unrecomposedControls = LinearLayout(activity).apply {
        orientation = LinearLayout.VERTICAL
        layoutParams = LinearLayout.LayoutParams(MATCH, WRAP)
        for (child in listOf(status, capabilityNotice, launchHost, outcome)) {
            addView(child)
        }
        listOf(status, capabilityNotice, outcome).forEach { line -> line.screenAir() }
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
     * agents-tracker-e6mi, as replaced by agents-tracker-nx44.2: where the BROKEN state's opaque
     * strip and the sync detail are drawn, above the destination and outside its scroll.
     *
     * IT IS A HOST THE SURFACE OWNS, for [contentHost]'s reason exactly. What the status says
     * changes whenever the transport, the machine's clock or a repair channel does -- which is
     * to say on every event -- and the scaffold is rebuilt only when the bar changes. Handing the
     * scaffold fresh chrome per draw would rebuild the bar at the rate the agents produce
     * events, and re-parent the destination under whoever is using it.
     *
     * WHAT IT REPLACES IS A LINE ON THE INBOX. The three facts were written to [status], a child
     * of [unrecomposedControls], which `triageInboxView` hosts UNDER its four Group sections --
     * so `hostContent`'s `detachHostedViews` took them off screen on the way to Activity,
     * Settings and into every session drill-down. The link dropping changed nothing on
     * screen for a user standing anywhere else, which is the moment PB-APP-8 exists for.
     */
    private val syncHost = FrameLayout(activity).apply {
        layoutParams = ViewGroup.LayoutParams(MATCH, WRAP)
    }

    /**
     * The nav row's sync pill, hosted rather than rebuilt (agents-tracker-nx44.2).
     *
     * IT IS ONE HOST FOR THREE NAV ROWS, and that is safe for the reason exactly one destination
     * is on screen at a time. What makes it safe in PRACTICE is [statusSlot]: every composition
     * that takes this view takes it through that method, which detaches it from whichever screen
     * held it last. A screen tree is built before it is hosted, so a detach done at hosting time
     * would run after the `addView` that throws.
     *
     * WHY A HOST AT ALL. The pill changes whenever the transport, the machine's clock or a repair
     * channel does; the screens around it are rebuilt only when their own models change, and the
     * settings panel is redrawn by a different object entirely. A pill built into each screen
     * would be as stale as whichever screen was cheapest to skip.
     */
    private val statusHost = FrameLayout(activity).apply {
        layoutParams = LinearLayout.LayoutParams(WRAP, WRAP)
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
        // NAMED, so a test can ask how many compositions this window is holding rather than
        // guessing at a child index. Two roots stacked here draw on top of each other -- the one
        // underneath keeps its listeners, its accessibility focus and whatever was typed into it,
        // while the one on top looks correct -- so a swap that forgot its `removeAllViews` would
        // pass every assertion about what is ON screen. This is what makes that question askable.
        tag = PHONE_APP_HOST
        // A glowing dot and the tab badge are drawn outside their own views.
        clipChildren = false
        clipToPadding = false
        layoutParams = ViewGroup.LayoutParams(MATCH, MATCH)
    }

    /**
     * The conversation menu, and the tap that closes it, over the app and under the toast.
     *
     * **IT IS A VIEW IN THIS WINDOW AND NOT A DIALOG, AND PB-SEC-12 CLAUSE 1 IS WHY.** Kill is a
     * privileged control -- it ends a session outright -- and `filterTouchesWhenObscured` is a
     * property of a View in a window this surface OWNS. `confirmThenPress` says so about its own
     * `AlertDialog` in as many words: "the dialog's own buttons live in a window this surface does
     * not own". A menu built as a popup or a dialog would take Kill's tap through a window with no
     * touch filter on it, which is the one control an overlay attack is most worth mounting
     * against. Owner ruling R2 moved Kill; it did not relax what protects it.
     *
     * **THE HOST IS THE SCRIM AND THAT IS THE WHOLE DISMISS BEHAVIOUR.** It fills the window and
     * takes taps, so anything outside the menu closes it -- the platform convention, and the only
     * way out for a user who opened the menu by accident and does not want any of its three acts.
     * It is `INVISIBLE`-by-emptiness rather than by a flag: an empty host draws nothing, takes
     * nothing (see [drawConversationMenu]'s clearing) and is exactly as findable as a full one,
     * which is [drawSync]'s own argument for filling both its slots with silence.
     *
     * UNDER THE TOAST, WHICH IS THE ORDER IT IS ADDED IN. A toast reports what a press did; a menu
     * is where presses come from. A menu drawn over the report of its own last act would hide the
     * answer from the person who just asked the question.
     */
    private val menuHost = FrameLayout(activity).apply {
        clipChildren = false
        clipToPadding = false
        layoutParams = ViewGroup.LayoutParams(MATCH, MATCH)
        // THE SCRIM IS THE HOST ITSELF and it is armed only while there is a menu in it, because
        // a permanently clickable full-window view over the app is a window that eats every tap.
        setOnClickListener { closeConversationMenu() }
        isClickable = false
    }

    /**
     * The conversation header's own host, for [syncHost]'s reason exactly.
     *
     * WHAT IT SAYS AND WHEN THE SCAFFOLD IS BUILT ARE TWO DIFFERENT CLOCKS. The header carries the
     * session's state word, which flips at every turn boundary; the scaffold is rebuilt only when
     * the destination changes or a conversation opens or closes. A scaffold handed a fresh header
     * per draw would be rebuilt at the rate the agent produces turns, re-parenting the
     * conversation -- and the composer, with whatever the user has typed in it -- under whoever is
     * using it. So the header is drawn INTO this host by [drawConversationHeader], guarded on its
     * own three facts, and the host itself never moves.
     */
    private val headerHost = FrameLayout(activity).apply {
        layoutParams = ViewGroup.LayoutParams(MATCH, WRAP)
    }

    /**
     * The window: the app, the menu over it, and the toast overlay above both.
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
        addView(menuHost)
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
    // AND ONE CONTROL IS DELIBERATELY REACHED THROUGH A VIEW THIS LIST CANNOT HOLD. Owner ruling
    // R2 moves Kill into the conversation header's menu, so the tap that ends a session lands on a
    // menu ROW -- and the menu is built per open ([drawConversationMenu]) rather than once, because
    // which rows exist is a fact about the session. A list is the wrong shape for a view that does
    // not exist yet, so the gate is applied where the menu is built, on the block: a `ViewGroup`
    // consults `filterTouchesWhenObscured` before dispatching to its children, so one gate covers
    // every row. [kill] itself stays in this list and stays gated -- it is still the control the
    // row presses, and it is still what carries the confirmation.
    val touchFilteredActions: List<View> =
        listOf(send, kill, launch, resyncControl, acknowledge) +
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
     * It never CONSTRUCTS a phone: [lifecycle] holds a handle only once one was built and
     * started, so a pause before anything was built does not reach Keystore on the way out.
     */
    fun release() {
        pairing.release()

        // ADR-017 T4-b: the watch goes with the screen. Leaving the app is leaving the screen,
        // and a watch held across it is the machine rendering, sealing and appending full screens
        // for a phone that is not looking. The facade's own severance verb below withdraws the
        // INPUT authority; this withdraws the READ.
        reconcileTerminalWatch(bridge = null, session = null)

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

        // ADR-017 amendment T8-b: BACKGROUNDING SEVERS DIRECTLY. The severance is asked for
        // whatever the connectivity policy says about the socket, because that is the whole
        // ruling: the old answer -- that a backgrounded app loses its authority because
        // backgrounding disconnects it -- is BY CONSEQUENCE, and rests on a connectivity choice
        // a later wave could revisit, at which point a raw-input generation would quietly
        // outlive the screen that owns it. Every control lease, every terminal control
        // generation and every held byte goes, with no transport event required and none
        // assumed; ADR-007 B16's disconnect -- journal delivery withdrawn while there is still
        // a socket to withdraw it over, then Stop -- follows only where the policy closes the
        // socket. The lane keeps exactly the order the inline calls had.
        //
        // ON THE COMMAND LANE, NOT HERE (committee round 3). App.Stop joins the relay drain
        // goroutine (mobile/app.go:480), whose teardown performs a five-second graceful close
        // (internal/remote/relay/client.go:411), and this function runs inside
        // PhoneActivity.onPause on the main looper -- a silent ANR, since
        // NetworkOnMainThreadException never fires for a socket Go opened. The enqueue sits
        // after `dispatch.detach()` and SURVIVES it: `VerbDispatch.enqueue` gates only the
        // settle on attachment, never the work ([TerminalWatchLane.drop]'s recorded property),
        // so the T8-b severance still reaches the core -- posted, prompt, never dropped.
        //
        // THE TERMINAL WATCH USED TO BE WITHDRAWN HERE BESIDE IT. There is none left to
        // withdraw: ADR-009 (2) stops this app issuing one at all, so the per-session render
        // work it cost the daemon is not started rather than being cleaned up.
        lifecycle.background(
            disconnect =
                ConnectivityPolicy.ruleFor(RuntimeState.BACKGROUND).socket == SocketDisposition.CLOSED,
        )
    }

    /**
     * Start observing, which nothing did -- and it is why PB-APP-3/4/5 were non-functional in
     * the shipping app rather than merely incomplete.
     *
     * `SetEventListener`, `SubscribeJournal` and `TerminalWatch` appeared ZERO times in all
     * Kotlin (residuals §2.9). So no listener was installed, journal delivery never started, and
     * the machine was never asked to send terminal frames -- while the peek read `App.Peek`, a
     * LOCAL cache that only a watched session ever fills. It was permanently empty, and it failed
     * looking exactly like a quiet machine.
     *
     * THE THIRD VERB IS DELIBERATELY NOT CALLED HERE ANY MORE. ADR-009 (2) ends the watch rather
     * than fixing it: with no grid on any screen, a phone that asked for terminal frames would be
     * spending the machine-to-phone append budget (7) on frames nothing draws. The two remaining
     * calls are what journal delivery needs, and interaction items ride that same journal.
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
     * WHERE `watch` AND `unwatch` USED TO BE, recorded rather than left as a gap.
     *
     * They asked `App.TerminalWatch` for the session on screen and `App.TerminalUnwatch` for the one
     * before it, which is what filled the cache the peek read. `ADR-009-structured-chat-interaction`
     * (2) ends both: "no phone surface issues a watch", so no snapshot frames are appended to a
     * phone at all and the transcript inherits the whole of the machine-to-phone append budget the
     * peek used to spend (7). The two verbs stay exported and stay on the wire -- nothing is deleted
     * protocol-side -- and are ledgered unbound in `android/unbound-verbs.tsv`.
     */

    /**
     * Connect, which nothing did before S19 -- and it is why PB-E2E-2's "observes, takes control,
     * types" had no chance of working even once the controls existed. `App.Start` is what dials
     * the relay and begins draining the machine's mailbox, and no production Kotlin called it:
     * every screen read a roster the app was never connected to fill.
     *
     * THE PLAN COMES FROM [LifecycleConvergence], which had no production caller either. Its
     * COLD_START row is this moment -- the screen coming to the front, over a phone core that may
     * have been rebuilt since the last one -- and it says to re-establish exactly ONCE and only
     * when there is persisted state to resume. `Start` is idempotent, and [lifecycle]'s hold
     * is eager besides, so a redraw enqueues nothing -- which is what makes "one re-establish"
     * survive a screen that redraws on every poll.
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
        // ON THE COMMAND LANE (committee round 3), for the ORDER more than the wait: Start
        // itself spawns and returns, but a start issued HERE while release()'s stop was still
        // draining its five-second close would no-op against the dying session (App.Start
        // returns while a.sess != nil) and the queued stop would then land on nothing -- a
        // foregrounded phone, disconnected. On the lane it runs behind whatever the pause
        // enqueued. The hold is eager, so the render-per-event loop enqueues one start per
        // connect cycle rather than one per redraw; a refused start claws the hold back on the
        // settle and puts the routed refusal where the inline catch used to.
        lifecycle.foreground(lifecycleFor(app)) { refused ->
            outcome.text = FacadeBridge(app).routeFacadeError(refused.message.orEmpty()).message
        }
        // Observing is subscription state the core keeps regardless of the start crossing
        // above; it was sequenced after a synchronous start before and is order-independent
        // of it (both are local flags plus a listener install), so it stays here rather than
        // riding a settle that a pause may legitimately drop.
        observe(app)
    }

    private fun renderUnavailable(startup: PhoneStartup.Unavailable) {
        // PB-APP-9: the ROUTED message, never the platform's own words. A Keystore alias is not
        // a remedy, and `detail` exists for a bug report rather than for a person.
        status.text = startup.error.message

        // AND THE SYNC STATUS SAYS NOTHING, which is the honest answer here rather than an
        // omission. Every fact it composes is read from the phone core -- the transport's state,
        // the machine's own stamp, the repair channels' completeness -- and on this branch there is
        // no core to ask. A status reporting "broken" would be a claim about a machine this handset
        // is in no position to make, which is [drawContent]'s own argument for drawing no inbox
        // here.
        drawSync(SyncStatus.NONE)
        capabilityNotice.text = ""
        session = ""
        // AND NO DRILL-DOWN. The detail is read from the phone core, so a handset whose core
        // refused has nothing to fill one with -- and leaving it open would leave the back gesture
        // armed against a screen that is not there.
        detail = null
        setActionsEnabled(false)
        // NO CARD RATHER THAN AN EMPTY ONE. A phone whose core refused holds no approvals, so
        // there is no question to put on screen -- and a sheet drawn over nothing would be this
        // handset asserting that its machine is waiting for nobody, which it is in no position to
        // say.
        drawApproval(null)
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
        // PB-APP-11 is ranked ABOVE the transport in [SyncStatus], and it has to be: the
        // transport's opinion is that the socket is up, and a relay that answers every poll with
        // an empty page while withholding the machine's frames leaves it reading "Connected to
        // your machine." with nothing behind it. The machine's own clock is the only evidence on
        // this screen a relay cannot forge newer.
        val inbox = inboxScreen(bridge)
        drawContent(bridge, inbox)
        drawScaffold(inbox.tabs)

        // ONE RANKED STATUS, ON A SLOT NO TAB CHANGE REACHES (agents-tracker-nx44.2). This was
        // four stacked sentences, and before that one run-on paragraph on a view hosted under the
        // inbox's sections and detached on every other destination. [SyncStatus] owns the rank and
        // the silence; [statusHost] and [syncHost] own the two places.
        drawSync(
            SyncStatus.of(
                connection = bridge.connectionBanner(),
                // THE MACHINE'S OWN STAMP, AND NO FORMATTER. `MachineFreshness.notice` used to
                // render a bare clock time through the user's locale; agents-tracker-2pnu F5
                // retired that, and it spends the same elapsed-duration model this draw does,
                // which is the whole of agents-tracker-nx44.2's second half.
                freshness = bridge.machineFreshness(),
                nowUnixMs = System.currentTimeMillis(),
                // PB-APP-8 per channel, which is what the roster's single stale mark was a
                // summary of. The sheet names the ones with holes in them; the settings CONNECTION
                // section reads the same four and says only how many there are.
                streams = bridge.streamViews(),
                // PB-SYNC-7's hold, shown before anyone presses anything (agents-tracker-pxz8).
                reconciled = reconciledOf(startup),
            ),
        )
        // AND THE STARTUP LINE IS CLEARED, because the other branch writes it. A core that
        // refused once and started on the next resume would otherwise leave its refusal standing
        // under a working app.
        status.text = ""
        capabilityNotice.text = CapabilityNotice.of(startup.anomalies)

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
        // PB-SYNC-2 exists to forbid. While a session is open, it IS the target.
        session = detail ?: targetOf(inbox)

        // LAUNCH IS NOT A SESSION CONTROL, and this line is where that difference is stated: the
        // session it starts does not exist yet, so an empty roster is exactly the state a user
        // reaches for it in. Gating it on the roster would leave a freshly paired phone with no
        // way to get its first session, which is section 1's "launches" with no subject again.
        launch.enable(true)
        renderLaunch(bridge)
        renderPresetFlow(bridge)
        drawLaunch()

        if (session.isEmpty()) {
            drawApproval(null)
            setActionsEnabled(false)
            setKeyboardEnabled(false)
            return
        }
        // PB-INPUT-2: the lease is what the MACHINE answered this screen's own take_control with,
        // claimed by operation id. It was the literal `false` until ADR-007 B83(3), which told
        // every user they held nothing while Send stayed live from a different fact entirely.
        val lease = bridge.sessionLease(session)
        // ADR-009 (4)'s card, for the question this session is actually blocked on. It is read
        // BESIDE the roster and not off it: `InboxRow.lit` is the display group, and an unresolved
        // approval_request is the machine waiting for an answer -- two different facts that a sheet
        // must not confuse (IS-SS-1).
        drawApproval(approvalPanel(bridge, inbox))
        setActionsEnabled(true)
        // THE MODEL'S, NOT THIS SURFACE'S. `keyboardEnabled` is the link, and a surface that
        // decided the keyboard from its own flag would be a second copy of a policy the model
        // already states -- which is how the lease clause survived here unread for a wave.
        setKeyboardEnabled(lease.keyboardEnabled)
    }

    /**
     * The approval card on screen, or null when this phone holds no question for [session].
     *
     * THE ROW IS FOR THE CONTEXT LINE AND NOTHING ELSE. Which session is asking comes from the
     * ITEM; who is asking -- the project and the agent -- is the roster's, and the two can
     * legitimately disagree, because a pending approval survives a reconnect and a process death
     * (IS-LIFE-3) while a roster is whatever the last read returned. `ApprovalSheetScreen.of` takes
     * a null row for exactly that case and falls back to the session's own id.
     */
    private fun approvalPanel(bridge: FacadeBridge, inbox: InboxScreen?): ApprovalSheetPanel? {
        val item = bridge.pendingApproval(session) ?: return null
        val row = inbox?.sections?.flatMap { it.rows }?.firstOrNull { it.id == session }
        return ApprovalSheetScreen.of(item, row)
    }

    /**
     * PB-SYNC-7's fail-closed hold, for [SyncStatus.of]'s fourth fact (agents-tracker-pxz8).
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
     * agents-tracker-3nx6: GUARDED, the way [FacadeBridge.pendingApproval] guards its own read
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
            // WHAT THE MACHINE CALLS ITSELF, for the scope chips (agents-tracker-ksvb.1). This
            // phone is paired to exactly one machine -- `App.MachineName` answers for that one and
            // there is no verb that enumerates machines -- so the map has at most one entry, keyed
            // by the endpoint id the roster namespaces sessions under. The chips still FILTER on
            // the id; an entry only changes the word on the chip, and a machine the phone holds no
            // name for keeps rendering its id.
            machineNames = bridge.machineNames(),
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
        scaffoldDrawn = null
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
                revokedNotice = revoked.notice,
                revokedDetail = revoked.detail,
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
     * -- so in practice that reading is UNANSWERED, and both of
     * `PairOnlyScreen.revokeNoticeFor`'s answered arms were unreachable. The panel's own comment
     * argues there is no later draw to ask again from, which is true of that PANEL: this surface
     * redraws on every resume and every journal event, and the id outlives the panel in
     * [PhoneRuntime] for exactly that reason.
     *
     * "IN PRACTICE" IS THE HONEST WORD AND NOT A HEDGE. The reply arrives on a different goroutine
     * -- `mobile/relay.go`'s drain runs accept -> onReply -> resolve with no ordering against the
     * dispatch lane or this looper -- so a machine that answered first would resolve ACCEPTED in
     * the settle, and nothing in the code forbids it. What keeps it out of the field is that a
     * revoke which succeeds severs the path its own reply comes back on. This function is correct
     * either way, and that is the property to rely on rather than the timing: it composes whatever
     * verdict it is handed, so an answered one becomes the answer and an unanswered one becomes
     * the fallback below.
     *
     * THE PANEL'S SENTENCE IS THE FALLBACK AND NOT THE ANSWER. It is what an unanswered,
     * unreadable or never-issued revoke says -- including the routed reason a revoke that never
     * reached the wire failed, which no outcome can carry -- and a machine that has since answered
     * replaces it. With silence, where the removal was confirmed: a warning drawn over a state
     * that is fine teaches the user to ignore the one that is not.
     *
     * AND ON THIS VERB THE FALLBACK IS WHAT ACTUALLY RENDERS (agents-tracker-j45x). The purge in
     * the press's `finally` erases the answer this would claim: `dropContentMaterial` nils
     * `State.OpOutcomes`, the router's reply cache is rebuilt from those same empty outcomes, and
     * the content key is destroyed -- so an outcome received before the purge is gone and one
     * received after cannot be opened by a disowned phone. `launchOutcome` therefore answers
     * nothing for a revoke, and the answered branch below does not fire in production today.
     *
     * THAT IS RECORDED RATHER THAN REMOVED, and the shipped sentence is the honest one either way:
     * a successful revoke severs the path its own reply comes back on, so "your machine has not
     * confirmed it" is the designed terminal state and not a gap in this function. What the branch
     * costs is a `when` arm; what deleting it would cost is the only correct behaviour available
     * if the outcome ever survives the purge. j45x holds the decision.
     */
    private fun revokeNotice(app: App): RevokeNotice {
        val issued = runtime.revokeOperation()
        if (issued.isEmpty()) return RevokeNotice(settings.unpairNotice, detail = "")
        val verdict = try {
            CommandVerdict.of(
                FacadeBridge(app).launchOutcome(issued),
                issued,
                CommandVerdict.ACCEPTED_OK,
            )
        } catch (unreadable: Exception) {
            // A facade that cannot answer has not answered, and the next draw asks again.
            CommandVerdict.UNANSWERED
        }
        // THE OTHER FACT TRAVELS WITH IT (agents-tracker-jx23). This REPLACES the sentence the
        // panel composed, so anything that sentence carried and this call does not is dropped the
        // moment the machine answers -- and what the panel carried is whether the key material at
        // rest survived the purge, which no reply from the machine has any bearing on.
        val composed = PairOnlyScreen.revokeNoticeFor(verdict, purgeFailure = runtime.purgeFailure())
        // AND THE PANEL MAY HAVE SAID NOTHING AT ALL (agents-tracker-xeex). `VerbDispatch.press`
        // ends in `if (attached) settle(answer)` and `release()` detaches on every pause, so a
        // revoke whose round trip outlives the user's attention loses the whole settle -- while
        // the purge in the same press's `finally` destroyed both key tiers regardless. Returning
        // that empty string drew the screen a FRESH INSTALL gets, on a handset that had just
        // unpaired and purged itself.
        //
        // WHAT MAKES THE FALLBACK POSSIBLE is that both latches are written where nothing can drop
        // them: the operation id on the command lane inside the work lambda, and the purge answer
        // by `PhoneRuntime.purgeKeys` itself. So the two facts the settle would have reported are
        // still derivable here, from this side alone.
        //
        // THE PANEL'S SENTENCE STILL WINS WHERE IT EXISTS, because it can say one thing this
        // cannot: the routed reason a revoke that never reached the wire failed, which no outcome
        // carries and no verdict can express.
        //
        // AND THE MACHINE'S OWN WORDS ARE A SECOND CELL NOW (agents-tracker-ksvb.10), which is
        // correct on both arms rather than only on one: a verdict that ANSWERED is the arm whose
        // sentence is `composed`, and an unanswered one has no reply to quote, so
        // `revokeDetailFor` answers "" for exactly the case the panel's fallback wins.
        return RevokeNotice(
            notice = if (verdict.answered) composed else settings.unpairNotice.ifEmpty { composed },
            detail = PairOnlyScreen.revokeDetailFor(verdict),
        )
    }

    /**
     * Put the right ROOT in the window, and rebuild it only when which one, or what is in it, has
     * changed.
     *
     * **THERE ARE TWO ROOTS NOW AND THIS IS THE ONE PLACE THAT CHOOSES** (chat-surface-plan §5).
     * The tab scaffold hosts a destination above one shared bar; the conversation scaffold hosts a
     * header, a list that owns its own scroll, and a pinned composer, with no bar and the
     * connection strip kept. They are separate compositions rather than one with a flag because
     * what differs is the ARRANGEMENT -- which parts scroll -- and a scaffold that took a boolean
     * would be two screens sharing a body and disagreeing about what is inside its `ScrollView`.
     *
     * THE EQUALITY CHECK IS NOT AN OPTIMISATION, for the reason [inboxDrawn] records: [render]
     * runs on every resume, after every action AND on every journal event, and the scaffold holds
     * [contentHost] -- so a root rebuilt unconditionally would re-parent the destination under
     * whoever is using it, at whatever rate their agents happen to be producing events. On the
     * conversation it would also re-parent the composer, with whatever the reader has typed and
     * not yet sent still in it.
     */
    private fun drawScaffold(tabs: List<InboxTab>) {
        // THE APP IS ON SCREEN, SO THE UNPAIRED PHONE'S SCREEN IS NOT -- [drawPairOnly]'s clearing,
        // in the other direction. The offer is untaken again as well: a handset revoked later comes
        // back to the sentence explaining why its app is empty, not to a camera it did not ask for.
        pairOnlyDrawn = null
        pairingStarted = false
        val conversation = conversationOnScreen
        // BUILT BEFORE THE KEY, because building it is what decides whether there is one. A route
        // whose block has left the conversation -- a page of history prepended past the retention
        // bound, an item the machine replaced -- resolves to nothing, and the honest answer is to
        // fall back to the conversation rather than to host an empty screen.
        val literal = literalRouteItem.takeIf { conversation && it.isNotEmpty() }?.let(::literalScreenOrNull)
        if (literal == null) literalRouteItem = ""
        val next = ScaffoldKey(
            tabs = tabs,
            destination = destination,
            conversation = conversation,
            composer = conversation && detailDrawn?.composerIsBar == true,
            literal = literalRouteItem,
        )
        if (next == scaffoldDrawn && host.childCount > 0) return
        // WHERE THE READER WAS, READ OFF THE SCROLL THAT IS ABOUT TO BE DISCARDED
        // (agents-tracker-jz0z). It is taken HERE, past the early return and before
        // [scaffoldDrawn] is overwritten, because both facts it needs are only true at this
        // point: that a rebuild is actually happening, and which composition is being torn down.
        //
        // TWO GUARDS, AND EACH ANSWERS A CASE THAT WOULD OTHERWISE PUT A READER SOMEWHERE THEY
        // HAVE NEVER BEEN. The first is that the composition being discarded was the
        // CONVERSATION: on a tab destination [contentHost] hangs inside `phoneScaffoldView`'s
        // scroll instead, and reading that would carry how far down the Activity journal someone
        // had scrolled into the transcript they return to. The second is that the drill-down is
        // still open: [closeSessionDetail] clears the memory through [detail]'s setter and then
        // renders, so a read here would resurrect the offset one line after it was deliberately
        // dropped, and the next session opened would land at the previous one's position.
        //
        // A NULL PARENT NEEDS NO GUARD OF ITS OWN and that is not an accident: while an R8 or R9
        // screen is up the conversation's hosts are detached and [contentHost] has no parent at
        // all, so the return journey reads nothing and the offset taken on the way IN is still
        // the one that gets restored.
        if (scaffoldDrawn?.conversation == true && detail != null) {
            (contentHost.parent as? ScrollView)?.let { conversationScrollY = it.scrollY }
        }
        scaffoldDrawn = next
        // EVERY LONG-LIVED HOST SURVIVES THE REBUILD, and each has to be taken out of the scaffold
        // that is about to be discarded: Android refuses an `addView` of a child that still claims
        // a parent. The status chrome joined the content host here rather than in
        // `detachHostedViews` because it is not hosted IN the content -- that is the whole of the
        // fix. The nav row's pill is the other way round ([statusSlot]): it IS inside a
        // destination, so it detaches at composition time instead.
        //
        // THE HEADER AND THE COMPOSER REGION JOINED THEM WITH THE SECOND COMPOSITION. Both are
        // handed to `conversationScaffoldView` and neither is inside [contentHost], so
        // `removeAllViews` on that host cannot reach either -- and the composer holds what the
        // user has typed, which is the one thing on this screen that must survive every rebuild
        // the app performs.
        for (held in listOf(contentHost, syncHost, headerHost, composerRegion)) {
            (held.parent as? ViewGroup)?.removeView(held)
        }
        host.removeAllViews()
        host.addView(
            if (literal != null) {
                // **THE THIRD ROOT** (owner rulings R8 and R9). It replaces the conversation
                // rather than covering it, which is what "on its own screen" means and what the
                // two screens are built for: `outputScreen` carries its own nav header and back,
                // so there is no second way out to keep in step.
                //
                // **THIS COMMENT USED TO CLAIM THE SCROLL SURVIVED BY ITSELF, AND IT DID NOT**
                // (agents-tracker-jz0z). What stood here was "the conversation's own hosts are
                // detached above and re-attached by the next draw with their scroll intact --
                // [contentHost] keeps its child throughout, so coming back lands the reader where
                // they left rather than at the top of the transcript." [contentHost] is a
                // `FrameLayout`; it has no scroll position to keep. The offset lived on the
                // `ScrollView` `conversationScaffoldView` had just built and this branch had just
                // thrown away, so every trip into a tool's output returned the reader to the
                // OLDEST message in the session -- while a comment justifying the design said the
                // opposite, in as many words, for a whole wave. It is written down rather than
                // quietly deleted because a load-bearing false claim is the failure class that
                // cost this repo a P0 in the pairing-entry wave, and the correction is worth more
                // than the tidiness.
                //
                // WHAT ACTUALLY CARRIES IT IS [conversationScrollY]: the offset is read off the
                // scroll above, before this root replaces it, and handed back to
                // `conversationScaffoldView` on the return journey.
                literal
            } else if (conversation) {
                // **THE SECOND TOP-LEVEL COMPOSITION** (chat-surface-plan §5): a conversation is
                // hosted by a root of its own rather than swapped in as `content` under the tab
                // scaffold. It is not a second Activity and not a navigation library -- this
                // Activity already hosts exactly one view and now swaps between two, and the back
                // handling gains one case, the same shape it already has for three sub-states.
                //
                // WHAT THE OTHER COMPOSITION CANNOT DO, which is the whole reason for the branch:
                // `phoneScaffoldView` wraps whatever it is handed in ONE ScrollView, so a
                // session's notices, its conversation and its controls scrolled as a single
                // document -- which is why the owner's screenshot of one session needed two
                // screenshots, and why the composer, being the last child of that document, could
                // only be reached by scrolling past the entire transcript.
                conversationScaffoldView(
                    context = activity,
                    header = headerHost,
                    content = contentHost,
                    // NULL IS A STATE AND NOT AN ABSENCE. A session with no message sink draws no
                    // bar at all rather than a disabled one (ADR-017), and the sentence saying why
                    // is drawn by the column inside the scroll, where the reader is looking. The
                    // predicate is the panel's own, so the two cannot disagree.
                    composer = composerRegion.takeIf { next.composer },
                    // KEPT, AND IT IS ONE DECISION PER PART RATHER THAN ONE DECISION (plan B.2).
                    // The bar goes because a conversation is a place you go INTO; the strip stays
                    // because a warning that belongs to one destination is a warning the others do
                    // not have -- and dropping it here would make the one screen where a person is
                    // TYPING the one screen that cannot tell them the link is gone.
                    status = syncHost,
                    // WHERE THE READER LANDS, WHICH ONLY THIS FUNCTION CAN ANSWER. The scaffold is
                    // built fresh for an OPENING and for a RETURN alike and cannot tell them
                    // apart; [conversationScrollY] is null for the first, so the conversation
                    // opens at its newest message (agents-tracker-tu7z), and holds the offset read
                    // above for the second (agents-tracker-jz0z).
                    scrollY = conversationScrollY,
                )
            } else {
                phoneScaffoldView(
                    context = activity,
                    content = contentHost,
                    tabs = tabs,
                    destination = destination,
                    onSelectDestination = ::selectDestination,
                    status = syncHost,
                )
            },
        )
    }

    /**
     * Whether the CONVERSATION is what the window is showing, which is a different question from
     * whether a session is open.
     *
     * IT IS DERIVED AND NOT LATCHED, deliberately: three fields already answer it between them and
     * a fourth boolean would be a fourth thing to keep in step -- which is exactly how
     * [pushDrillDown]'s predicate came to be written three times before it was written once.
     * [detail] alone is NOT the answer, because a drill-down is state inside the Inbox tab and
     * survives a trip to Activity or Settings ([selectDestination] keeps it on purpose); what the
     * scaffold needs to know is what is on the glass right now.
     *
     * THE FALLBACK IS EXCLUDED BY THE THIRD CLAUSE. ADR-017 T1 routes a `terminal_fallback`
     * session to the sanitized terminal, which is not a conversation and keeps the tab bar it
     * always had: it is a destination's content, not a place you go into.
     */
    private val conversationOnScreen: Boolean
        get() = contentShows == Destination.INBOX && detailDrawn != null && fallbackDrawn == null

    /**
     * The item whose literal is open on its own screen, or "" while the conversation itself is.
     *
     * IT IS AN ITEM ID AND NOT A ROUTE OBJECT, which is [openApproval]'s own rule: the id is what
     * survives a redraw, and the route it names is re-resolved against the conversation ON EVERY
     * DRAW. So a tool whose body the machine replaced shows the new body, and one whose block left
     * the conversation entirely closes the screen instead of leaving a reader inside a page that
     * no longer describes anything.
     */
    private var literalRouteItem: String = ""

    /**
     * R8 and R9's destination: open the literal this block routes to, on its own screen.
     *
     * **ONE HANDLER FOR TWO CALLBACKS, because the block already knows which it is.**
     * `transcriptView` offers `onOutput` and `onDiff` separately -- correctly, since it draws two
     * different affordances -- but what a route OPENS is decided by `TranscriptBlock.route`, and a
     * surface that kept its own second copy of that decision would be the drift PB-DS-9 keeps out
     * of call sites. Both callbacks point here.
     *
     * IT IS NOT A `press`, because nothing crosses the wire: the whole literal is already on this
     * phone, carried on the route ([TranscriptRoute.Output] holds the tool's own body and
     * [TranscriptRoute.Diff] the file's own diff). What would cross is IS-CAP-2's fetch for a
     * CLIPPED body, and that is [fetchDetail], one affordance over, with its own refusal.
     */
    private fun openLiteral(itemId: String) {
        literalRouteItem = itemId
        // The menu cannot survive into a screen it did not open; nor can a preview of a gesture
        // aimed at the conversation underneath.
        closeConversationMenu()
        Motion.clearPredictiveBack(contentHost)
        render()
    }

    /** Leave the literal and come back to the conversation, at the place it was left. */
    private fun closeLiteralScreen(): Boolean {
        if (literalRouteItem.isEmpty()) return false
        literalRouteItem = ""
        render()
        return true
    }

    /**
     * The screen for [literalRouteItem], or null when there is nothing honest to draw.
     *
     * **NULL IS THE ANSWER FOR [TranscriptRoute.None] AND IT IS LOAD-BEARING.** `TranscriptView`
     * refuses to draw an offer for a block that routes nowhere and calls an offer onto an empty
     * page "the dead-chevron defect wearing a route"; a host that answered a route it could not
     * fill would undo that from the other side, on a screen the reader has already committed to.
     * Both halves of the pair now refuse the same case.
     *
     * THE DIFF'S TITLE IS THE CHANGE'S OWN PATH and never the sentence the screen wrote about it:
     * `FileChangeChip.path` is the wire's own field, already carrying `<old> → <new>` for a
     * rename, so what titles the screen is what the machine said changed.
     */
    private fun literalScreenOrNull(itemId: String): View? {
        val block = detailDrawn?.transcript?.blocks?.firstOrNull { it.itemId == itemId } ?: return null
        return when (val route = block.route) {
            // A LAMBDA AND NOT A REFERENCE, because [closeLiteralScreen] answers whether there
            // WAS a screen to close -- which is what the back gesture reads -- and a screen's
            // `onBack` wants no answer at all.
            is TranscriptRoute.Output ->
                outputScreen(activity, block.line, route.text) { closeLiteralScreen() }
            is TranscriptRoute.Diff ->
                diffScreen(activity, block.fileChange?.path.orEmpty(), route.text) { closeLiteralScreen() }
            else -> null
        }
    }

    /**
     * Draw what the app has to say about its link, redrawn only when it has changed.
     *
     * THE EQUALITY CHECK IS [drawInbox]'s AND SHARPER HERE. This runs on every resume, after every
     * action and on every journal event, and the chrome sits above a destination somebody may be
     * scrolling; rebuilding it unconditionally would re-lay out the whole column at whatever rate
     * their agents produce events. [SyncStatus] is a data class, so "has anything a user can read
     * changed" is one comparison.
     *
     * BOTH HOSTS ARE ALWAYS FILLED, INCLUDING WITH SILENCE. A live phone leaves an empty tagged
     * container in each, which costs nothing on screen -- neither has a fill, a border or padding
     * of its own -- and keeps both slots findable, so a test can tell "the phone has nothing to
     * report" from "the slot went away again".
     */
    private fun drawSync(status: SyncStatus) {
        if (status == syncDrawn && syncHost.childCount > 0) return
        syncDrawn = status
        statusHost.removeAllViews()
        syncPillView(activity, status, ::toggleSyncSheet)?.let { statusHost.addView(it) }
        syncHost.removeAllViews()
        syncHost.addView(
            syncStatusView(
                context = activity,
                status = status,
                open = syncSheetOpen,
                onOpen = ::toggleSyncSheet,
                onRepair = ::repairSync,
            ),
        )
    }

    /**
     * Open or close the sync detail.
     *
     * IT REDRAWS THROUGH [drawSync] RATHER THAN CALLING [render], because the state that changed is
     * not one the phone core knows about: a full render would re-read the roster, the transcript
     * and the journal to answer a disclosure toggle. Clearing [syncDrawn] first is what gets past
     * that method's own equality check, which compares the STATUS and cannot see this flag.
     */
    private fun toggleSyncSheet() {
        syncSheetOpen = !syncSheetOpen
        val status = syncDrawn ?: return
        syncDrawn = null
        drawSync(status)
    }

    /**
     * The sync detail's one control, and where it leads.
     *
     * THE MODEL DECIDED WHICH REMEDY IS OWED AND THIS SPENDS IT. [SyncStatus.PAIR_AGAIN] is the
     * offer for a machine that has stopped answering this device: `swarm remote pair` is refused
     * while a registration stands (PB-STATE-10), so the destination is Settings, whose leading
     * section clears it -- the same reasoning, and the same word, the retired banner's control
     * carried. Everything else is a hole in the transport's read position, which [resyncControl]'s
     * verb rewinds for all four channels at once.
     *
     * IT PRESSES THE EXISTING CONTROL RATHER THAN REPEATING ITS VERB. `resyncControl` carries the
     * press plumbing PB-SEC-12 clause 1 requires -- the touch filter, the dispatch, the routed
     * refusal onto the outcome line -- and a second call site typing `app.resync(...)` would have
     * none of it.
     */
    private fun repairSync() {
        when (syncDrawn?.detail?.repair) {
            SyncStatus.PAIR_AGAIN -> selectDestination(Destination.SETTINGS)
            SyncStatus.REPAIR -> resyncControl.performClick()
            else -> Unit
        }
    }

    /**
     * The sync pill's host, detached from whatever held it last.
     *
     * A SCREEN TREE IS BUILT BEFORE IT IS HOSTED, which is why this is a method and not a field
     * read. `hostContent` detaches the surface's long-lived views, but by then the composition has
     * already called `addView` on this one -- and Android refuses a child that still has a parent.
     */
    private fun statusSlot(): View {
        (statusHost.parent as? ViewGroup)?.removeView(statusHost)
        return statusHost
    }

    /**
     * [approvalHost]'s host, detached from whatever held it last (agents-tracker-dwwv.2.4).
     *
     * [statusSlot]'s reason, spent on the second view this app moves between two screens: a
     * screen tree is built before it is hosted, so the detach has to happen here, at request
     * time, rather than inside whichever composition asks for it. [drawInbox] calls this to put
     * the host back under the inbox list; [drawDetail] calls it to put the SAME host inside the
     * session detail, right under the transcript block that answers to it.
     */
    private fun approvalSlot(): View {
        (approvalHost.parent as? ViewGroup)?.removeView(approvalHost)
        return approvalHost
    }

    /**
     * Draw the destination the user is on, and forget the inbox when it is not what they see.
     *
     * **THE CLEAR AT THE BOTTOM IS THE OTHER HALF OF [inboxDrawn]'s MEANING.** That field is the
     * screen `TriageInboxScreen.promotions` compares against, and the whole claim its KDoc makes
     * is that a promotion happened "in front of the user" -- so the memo has to be what the user
     * SAW, not what was last drawn. Those are the same thing only while the inbox list is on
     * screen. Without this line the memo froze for as long as the user was on Activity or
     * Settings, or inside a drill-down, and every session that started asking during that
     * window was announced when they came back: a NEEDS_YOU two-pulse and a slab sweep for
     * transitions nobody was there for.
     *
     * NULL IS THE RIGHT VALUE TO FORGET WITH, and `promotions` already defines it: `previous ==
     * null` returns the empty set, because nothing can have transitioned in front of a user who
     * has not been shown anything. Coming back to the inbox therefore announces nothing, which is
     * correct -- what waits for them is carried by the lit slab, which is a state and not an event.
     *
     * THE INBOX ARM RETURNS RATHER THAN FALLING THROUGH, and that is not a style choice. Clearing
     * the memo on the inbox's own draw would forget the screen the user is looking at right now,
     * so the next redraw would treat every waiting session as newly promoted -- the same defect,
     * louder. `android/gate/o4_sweepmemo_test.go` asserts both halves and perturbs each.
     *
     * @param bridge null on the branch where the phone core refused, which is the only reason
     *  a destination can have nothing to draw.
     * @param inbox null for the same reason.
     */
    private fun drawContent(bridge: FacadeBridge?, inbox: InboxScreen?) {
        // ADR-017 T1: THREE DESTINATIONS, AND THE MACHINE PICKS -- asked BEFORE anything about
        // the chat screen is decided. A session the daemon routed to `terminal_fallback` gets the
        // sanitized terminal and never the chat screen, and -- the direction that matters -- a
        // healthy structured session can never reach the fallback from here, because
        // [FacadeBridge.terminalFallback] answers null for every destination but that one. It is
        // not a branch a user can take: neither screen offers the other, which is T2 rule 4's "no
        // route" made a property of this function rather than of a conditional somewhere else.
        val fallback = if (destination == Destination.INBOX) {
            detail?.let { open -> bridge?.terminalFallback(open) }
        } else {
            null
        }
        // ADR-017 T4-b's OTHER HALF, and it is reconciled HERE because here is the one place that
        // knows what is on the glass. A watch is a lease with a horizon: it must be renewed while
        // the screen is up and CLOSED when the screen goes away. Before this, `watch()` was called
        // from inside the fallback drawer on every redraw -- one sealed unsigned append per state
        // change -- and `unwatch()`/`renew()` had no call site in the app at all, so the machine
        // kept rendering, sealing and appending full screens for a screen the user had left.
        reconcileTerminalWatch(bridge, if (fallback == null) null else detail)
        when (destination) {
            Destination.INBOX -> {
                if (fallback != null) {
                    drawTerminalFallback(bridge!!, detail!!, fallback)
                    return
                }
                when (val open = detailPanel(bridge)) {
                    null -> {
                        drawInbox(inbox)
                        return
                    }
                    else -> drawDetail(open)
                }
            }
            Destination.ACTIVITY -> drawActivity(bridge)
            // The machine switcher and the aggregate inbox are SUB-STATES of the Settings
            // destination ([machinesOpen]'s KDoc has the argument), drawn only where there is a
            // phone to read them from: the branch with no core has no machine roster, and what it
            // shows on this tab is the routed failure the settings panel already carries.
            Destination.SETTINGS -> when {
                globalInboxOpen && bridge != null -> drawGlobalInbox(bridge)
                machinesOpen && bridge != null -> drawMachines(bridge)
                else -> drawSettings()
            }
        }
        inboxDrawn = null
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
        // ADR-009 D5's sweep, computed BEFORE the previous screen is forgotten. [inboxDrawn] is
        // "what the inbox last drew", which is exactly the state a promotion is a transition from
        // -- not what the phone core last reported. A session the user has never seen has not
        // transitioned in front of them, and neither has one this scope was hiding.
        val promoted = screen?.let { TriageInboxScreen.promotions(inboxDrawn, it) }.orEmpty()
        // THE SAME EVENT THE SWEEP FIRES ON, TOLD TO THE HAND (migration plan O6.2, `needs-you
        // two-pulse`). A promotion is a session that has just started asking, in front of a user
        // who is already holding the phone -- the one moment in this app where something arrives
        // rather than being asked for. It is fired ONCE per draw and not once per session: two
        // rows promoted by one journal event are one interruption, and D5's "at most one sweep
        // animating per viewport" is the same ruling one sense over.
        if (promoted.isNotEmpty()) Haptics.play(activity, Haptics.Signal.NEEDS_YOU)
        inboxDrawn = screen
        detailDrawn = null
        fallbackDrawn = null
        // THE APPROVAL HOST COMES BACK HERE, AND ONLY HERE ON THIS BRANCH (agents-tracker-
        // dwwv.2.4). It is [approvalSlot]'s and no longer [unrecomposedControls]'s fixed child, so
        // every draw that shows the list re-claims it -- at the position between the capability
        // notice and the launch form this column has always held it, found by the neighbour
        // rather than by an index that would go stale the day a line above it is added or removed.
        unrecomposedControls.addView(
            approvalSlot(),
            unrecomposedControls.indexOfChild(launchHost).let {
                if (it < 0) unrecomposedControls.childCount else it
            },
        )
        hostContent(
            when (screen) {
                null -> unrecomposedControls
                else -> triageInboxView(
                    context = activity,
                    screen = screen,
                    onSelectSession = ::selectSession,
                    onSelectScope = ::selectScope,
                    promoted = promoted,
                    below = unrecomposedControls,
                    status = statusSlot(),
                )
            },
        )
    }

    /**
     * PB-APP-3's session detail, as the model of it, or null when no session is drilled into.
     *
     * IT READS ONE FACADE CALL THE REST OF THE SCREEN ALREADY MAKES, which is why opening a session
     * costs no new traffic: the journal page is the whole retained log [drawActivity] already reads,
     * narrowed here to one session by `JournalRow.sessionId`.
     *
     * IT USED TO READ A SECOND ONE -- the daemon-rendered grid, off `App.Peek`, for the session
     * [watch] had asked the machine to render. Both are gone with the terminal well
     * (`ADR-009-structured-chat-interaction.md` (2)/(3)): no phone surface issues a `terminal_watch`,
     * so no snapshot frames are appended at all and the transcript inherits the whole of the
     * machine-to-phone budget the peek used to spend.
     *
     * @param bridge null on the branch where the phone core refused, where there is no detail to
     *  read. [renderUnavailable] closes the drill-down on that branch rather than leaving a user
     *  inside a screen nothing can fill.
     */
    private fun detailPanel(bridge: FacadeBridge?): SessionDetailPanel? {
        val open = detail ?: return null
        if (bridge == null) return null
        val control = bridge.sessionLease(open)
        // THE ROSTER ROW, GUARDED, FOR THE HEADER'S DOT (chat-surface-plan §5). It is one read and
        // it is guarded for `FacadeBridge.sessionTitle`'s own recorded reason: `App.Session`
        // REFUSES an id the cached roster does not hold, and a drill-down on a session that has
        // just left the roster is an ordinary race rather than a failure. An empty Group is what a
        // refusal costs, and `SessionDetailScreen.of` is where that is turned into something the
        // dot can draw -- never here, so there is one place answerable for the substitution.
        //
        // THE GROUP IS READ AND NEVER DERIVED, which is `TriageInbox`'s standing rule: the mark in
        // the header is the mark on the row the reader tapped to get here, or the header is
        // claiming a state of its own.
        val row = try {
            bridge.sessionRow(open)
        } catch (unknown: Exception) {
            null
        }
        // AT MOST ONE ENTRY AND STILL A MAP (`FacadeBridge.machineNames`): pairing is to one
        // machine, but the roster is namespaced per machine, so a lookup that assumed one would
        // label another machine's sessions with this machine's name. A miss renders the endpoint
        // id, which is a fact and is what shipped before any name crossed the wire.
        val machineNames = bridge.machineNames()
        // THE CONVERSATION, AND IT IS ONE READ RATHER THAN TWO. `App.ReadTranscript` is per session,
        // so there is no roster-wide page to filter down here -- and `TranscriptPageView.stale` is
        // the JOURNAL's own stale mark, read off the handle that carried the items, because an
        // interaction item IS a journal record (IS-LAYER-1/-4). Reading from cursor zero on every
        // draw is deliberate: the core holds at most `MaxItemsPerSession` per session, and an item
        // updated in place keeps its FIRST record's cursor (IS-LAYER-3), so paging past the tail
        // would miss exactly the updates the event fired for.
        val chat = bridge.transcript(open, JOURNAL_FROM_THE_START, WHOLE_JOURNAL)
        // M3.2's COLD OPEN, decided by the model and thrown once. `SessionDetailOpen.plan` says a
        // session this phone holds NO items for backfills on open -- a week-old session must show
        // its history rather than an empty well and a Repair button -- and that a re-open inside
        // the throttle window asks for nothing, because flipping in and out of a screen must not
        // multiply reads against the machine-to-phone append ceiling.
        backfillOnOpen(open, chat.items.size)
        return SessionDetailScreen.of(
            SessionDetail(
                sessionId = open,
                // THE SESSION'S OWN NAME for the nav header (agents-tracker-ksvb.1). `open` stays
                // the identity every control on this screen acts on; only what is READ changes,
                // and an unreadable roster leaves this empty so the id renders as it did before.
                title = bridge.sessionTitle(open),
                group = row?.group.orEmpty(),
                // WHAT TO CALL THE MACHINE, decided by `MachineLabel` and by nothing here: the
                // hostname it published, or the endpoint id where it published none. The half of
                // the session id before the first "/" IS that endpoint id -- `mobile/app.go`
                // derives a session's display title by cutting there and throwing this half away,
                // so reading it is a parse of an identifier and not a derivation of state.
                //
                // A REQUEST IS FILED to lift the parse into `MachineLabel`, where the naming
                // decision already lives: `TriageInboxScreen.machineOf` is the same three
                // characters, private, one package over, and two copies of a rule is how the two
                // drift. Until it lands this call site is the second copy and says so.
                machineLabel = open.substringBefore('/', "").let { endpoint ->
                    if (endpoint.isEmpty()) "" else MachineLabel.of(machineNames[endpoint].orEmpty(), endpoint)
                },
                // ONLINE IS THE LEASE MODEL'S AND NOT A SECOND OPINION. `SessionLease.online` is the
                // transport fact `FacadeBridge` already derived, and it is the clause that decides
                // whether a confirmed Stop is sent or discarded.
                online = control.online,
                journalStale = chat.stale,
                // PB-INPUT-1's notice answers a PRESS (agents-tracker-4lta), and this is where the
                // press is read back: the Stop plan latched the session it could not send for, and
                // a notice about another session's press would be the proximity error again.
                stopNotSent = stopNotSentFor == open,
                // ADR-009 (6)'s per-send state, and PB-APP-9's routed class for it -- both read
                // back per session, so a send against one never reports on another.
                composerState = composerSendState.takeIf { composerSendFor == open },
                composerRefusal = if (composerSendFor == open) composerRefusal else "",
                composerRefusalDetail = if (composerSendFor == open) composerRefusalDetail else "",
            ),
            TranscriptScreen.of(
                chat.items,
                // M2.2's collapse, which is this reader's and not the wire's.
                expanded = expandedCards,
                // AND THE OFFERS THE MACHINE HAS ALREADY SETTLED (round 3, finding F4): a card
                // whose whole body it answered `unavailable` for keeps advertising the fetch off
                // its capture-time fields, and re-offering a fetch that can never succeed is the
                // app inviting a tap it knows the answer to.
                withoutDetail = detailRefused,
                // ADR-014 §2's honest floor: once the machine has said nothing older is retained,
                // the control goes rather than staying to come back empty.
                atFloor = historyFloorFor(bridge, open),
                // AND THIS PHONE'S OWN END OF THE CONVERSATION, which is a different sentence
                // from the machine's floor and is kept apart from it: the last page could not
                // be held whole, so there IS more and this handset cannot show it.
                atCapacity = bridge.historyAtCapacity(open),
                // THE ANSWER THIS PHONE HAS SENT AND NOT SEEN RESOLVED ([answeringItemId] has the
                // argument, including what is not yet reachable). A SET because the model takes
                // one; at most one answer is ever in flight from this surface, because the sheet
                // disables the control it was pressed on for as long as the work is crossing.
                answering = answeringItemId.takeIf { it.isNotEmpty() }?.let(::setOf).orEmpty(),
                // OWNER RULING R6: the message this phone has sent and not yet seen come back.
                //
                // WITHOUT IT THE MESSAGE IS NOWHERE. The draft is spent on the daemon's acceptance
                // and the transcript will not carry the line until the agent echoes it, so between
                // those two moments the reader has pressed send and has nothing on screen -- and
                // if the echo never lands, what they typed is gone with no evidence it existed.
                //
                // SCOPED TO THE SESSION IT WAS SENT TO, like the two composer fields above: a
                // pending bubble is a fact about ONE conversation and must not follow the reader
                // into another one.
                pendingSend = pendingSendFor(open),
            ),
            // PB-INPUT-2 REACHES THE USER HERE NOW, and that is the peek's deletion landing rather
            // than a new fact: the sentence and the Take control button were that screen's, and this
            // is the screen a session is read on. The verdict travels with the lease for
            // agents-tracker-qlf9's reason -- a refusal and a severance are not "you have not
            // pressed the button yet", and only the machine's own reply says which one this is.
            control,
            // ADR-017 T2 rule 3: the MACHINE's capability record, read and never inferred. It
            // replaces the `transcript.structureTorn` derivation the panel used to make -- a
            // torn transcript and a provider that never had structured chat are different
            // states with different explanations, and only the record knows which one this is.
            capabilities = bridge.sessionCapabilities(open),
            // PB-INPUT-1's ledger, NARROWED HERE AND NOT IN THE FACADE. `App.UndeliveredInputs` is
            // process-wide -- a dropped link loses keystrokes for every session at once -- and a
            // screen open on one session that reported another's loss would be the proximity error
            // PB-SYNC-2 forbids, applied to input.
            undelivered = bridge.undelivered().forSession(open),
        )
    }

    /**
     * ADR-014's floor for one session, guarded and cached-free: `App.HistoryFloor` is an O(1) read
     * of a map the reply already filled, so asking it per draw costs nothing on the wire.
     */
    private fun historyFloorFor(bridge: FacadeBridge, session: String): Boolean =
        bridge.historyFloor(session)

    /**
     * M3.2: ask the machine for this session's history ONCE on a cold open, throttled.
     *
     * THE DECISION IS [SessionDetailOpen]'S AND THE THROW IS HERE, which is this surface's
     * standing split: a model decides, a surface acts. Both halves of the model's rule matter --
     * a session with items already on the phone backfills on the reader's own "load earlier" and
     * never on open, and a re-open inside the window asks for nothing, because flipping in and out
     * of a screen must not multiply reads against the 8/s machine-to-phone append ceiling.
     *
     * IT IS FIRE-AND-FORGET AND ITS REFUSAL IS SILENT, which is the one difference from every
     * press on this surface and is deliberate: nobody pressed anything. A cold open that cannot
     * reach the machine shows what the phone holds -- which is what the empty state already says
     * -- and the reader's own "load earlier" is the control that reports a refusal out loud.
     */
    private fun backfillOnOpen(session: String, itemCount: Int) {
        val now = SystemClock.elapsedRealtime()
        if (!SessionDetailOpen.plan(itemCount, backfilledAt[session] ?: NEVER_BACKFILLED, now)) {
            return
        }
        backfilledAt[session] = now
        // AND HERE M3.2 STOPS, HONESTLY AND ON PURPOSE. `SessionDetailOpen.plan` answers true for
        // a session this phone holds NO items for, and that is exactly the session the facade
        // cannot page: `App.LoadEarlierInteractions` REQUIRES a non-empty `before_item` (ADR-014
        // pages strictly BEFORE a named item, by id and never by cursor, IS-ENV-2), and there is
        // no anchorless "newest page" read on this boundary. A phone holding nothing has no id to
        // name, so there is nothing to ask.
        //
        // WHAT A USER THEREFORE CANNOT DO: open a session this phone has never held an item for
        // and see its history. What they get is the transcript's own empty state, which says the
        // conversation has not reached this phone rather than that the agent said nothing, and
        // PB-SYNC-1's Repair control beside it. This is disclosed in docs/verification/r6-chat.md
        // rather than papered over, and it is one facade verb away from being closed.
        //
        // The throttle instant is still recorded above, deliberately: the moment an anchorless
        // read exists, this call site is correct without any other change, and until then a
        // re-open must not re-run the decision at the rate a user flips between screens.
        val before = detailDrawn?.loadEarlierBeforeItem.orEmpty()
        if (before.isEmpty()) return
        val app = (runtime.phone() as? PhoneStartup.Ready)?.app ?: return
        // `enqueue` AND NOT `press`, which is agents-tracker-xla6's own split: `press` disables
        // the control it is handed for as long as the work is crossing, and nobody tapped
        // anything here -- handing it a view would grey out part of a screen the reader is
        // reading. The refusal is deliberately silent for the same reason (see the KDoc).
        dispatch.enqueue(
            SendPlane.COMMAND,
            key = BACKFILL_KEY + session,
            work = { app.loadEarlierInteractions(session, before, HISTORY_PAGE) },
            // The answer is claimed like the pressed one -- the fold happens in `App.Outcome`
            // and a cold open that never claimed its own read showed the same empty screen it
            // was fired to fill -- but SILENTLY: `aloud = false` keeps this path's refusal
            // unsaid, which is the decision in this function's KDoc and not a new one.
            settle = { answer -> rememberHistoryRead(answer, session, aloud = false) },
        )
    }

    /**
     * M2.2's expand/collapse, which is a screen fact and reaches no wire at all.
     *
     * IT REDRAWS THROUGH [render] LIKE EVERY OTHER STATE CHANGE, so the collapse arrives through
     * `sessionDetailRedraw`'s incremental patch -- one row rebound, the rest of the conversation
     * left exactly where the reader's finger last saw it.
     */
    private fun toggleToolCard(itemId: String) {
        if (!expandedCards.remove(itemId)) expandedCards.add(itemId)
        render()
    }

    /**
     * M3.3/IS-CAP-2: ask the machine for the whole of a clipped card.
     *
     * IT IS A PRESS AND NOT A BACKGROUND READ, because it has an answer the reader is waiting for
     * and it can be refused: outside the daemon's retention window the reply is the coded
     * `unavailable` refusal carrying NO records at all (IS-CAP-3 -- never a partial body presented
     * as whole), and that refusal has to reach the person who tapped. `dispatchPress` is what puts
     * it on PB-APP-9's routed line and in row 1's toast.
     */
    private fun fetchDetail(control: View, itemId: String) {
        val target = session
        press(control) {
            Press(
                SendPlane.COMMAND,
                verb = { app -> app.loadInteractionDetail(target, itemId) },
                // AND THE BODY IS CLAIMED, which is the whole of what this press does (review
                // round 2): `App.Outcome` is where the reply replaces the clipped body, so a
                // press that discarded the operation id expanded nothing while reporting
                // success -- and the `unavailable` refusal IS-CAP-3 exists for never arrived
                // either, because a refusal is an answer and answers are claimed by id.
                settle = { answer -> rememberDetailRead(answer, itemId) },
            )
        }
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
    /**
     * ADR-017 T1/T4 -- the capability-routed terminal fallback, drawn for a session the MACHINE
     * routed here and for no other.
     *
     * THE WATCH IS ASKED FOR BY THE SCREEN'S OWN BINDING, never by this surface: the app has
     * exactly one place that may issue one, and `android/gate/r8_fallback_ui_test.go` names it by
     * path. What this function owns is what every other destination's drawer owns -- when the
     * screen is on the glass, and what it is handed.
     *
     * IT IS READ-ONLY IN THIS ROUND. No generation is opened here, so the banner is absent and the
     * body draws T6-b's read-only sentence; entering control is R8b's own slice and is disclosed
     * as such rather than half-wired.
     */
    private fun drawTerminalFallback(bridge: FacadeBridge, session: String, model: TerminalFallbackModel) {
        val grid = bridge.terminalRows(session)
        // THE REDRAW GUARD, for [drawDetail]'s reason one screen over. [drawContent] re-enters on
        // every state change -- every journal event, every gated action, every resume -- and this
        // drawer had none, so a working session rebuilt the whole view hierarchy at output rate
        // and threw the grid back to the top under whoever was reading it.
        val drawn = FallbackDraw(session, model, grid)
        if (drawn == fallbackDrawn && contentShows == Destination.INBOX) return
        fallbackDrawn = drawn
        detailDrawn = null
        hostContent(
            terminalFallbackView(
                context = activity,
                model = model,
                rows = grid.rows,
                gridRows = grid.gridRows,
                snapshotAge = grid.ageMs,
                streamStale = grid.streamStale,
                onBack = { closeSessionDetail() },
            ),
        )
    }

    /** What [drawTerminalFallback] last put on the glass, for its redraw guard. */
    private data class FallbackDraw(
        val session: String,
        val model: TerminalFallbackModel,
        val grid: TerminalGrid,
    )

    private var fallbackDrawn: FallbackDraw? = null

    /**
     * The session whose terminal watch this surface currently holds, with the binding that holds
     * it -- on [VerbDispatch]'s COMMAND LANE, never this thread (agents-tracker-jx1x). ADR-017
     * T4-b: a watch is opened ONCE, renewed while its screen is up, and closed when it is not --
     * so the machine never renders for a screen nobody is looking at.
     *
     * The verbs are relay appends behind awaitConn's five-second poll, exactly like every other
     * command this surface issues, and they used to run INLINE here -- a silent ANR, because
     * NetworkOnMainThreadException never fires for a socket Go opened. [TerminalWatchLane] owns
     * the ordering (a replaced watch is unwatched BEFORE its successor is watched, on the one
     * serial command thread) and the refusal path (a refused watch clears the hold on the
     * settle, so this surface re-watches instead of renewing into nothing).
     */
    private val watchLane = TerminalWatchLane<TerminalFallbackBinding>(dispatch)

    /**
     * [renewHeldWatch]'s single-flight key ([VerbDispatch.enqueue]'s keyed fence). Renewals ride
     * the redraw AND the 20-second tick; while one is crossing -- awaitConn can park it for five
     * seconds -- a second says nothing the machine has not already heard, so it is dropped
     * rather than queued behind the first.
     */
    private val watchRenewalCrossing = Any()

    /**
     * The clock that keeps a held watch alive while its screen is on the glass, and the interval
     * it runs at.
     *
     * **WHY A CLOCK AT ALL, when the comment below says a timer is what T6-c bans.** The renewal
     * used to ride the redraw alone, and the redraw is not guaranteed: [render] has exactly one
     * lifecycle caller (`PhoneActivity.onResume`) plus `PhoneEvents.observe { render() }`, so an
     * IDLE fallback screen watching an IDLE session produces no redraw at all -- for minutes.
     * The machine's horizon is sixty seconds. It therefore reaps a watch the user is looking at,
     * the peek ends from the gateway side, and the last grid stays on the glass.
     *
     * **WHY IT IS NOT THE THING T6-c BANS.** That ruling is about `terminal_control_keepalive`:
     * a background emitter holding RAW INPUT AUTHORITY open with no screen displaying it. A
     * watch grants NO INPUT AUTHORITY (T4), this tick is started only where a fallback is on the
     * glass, it is cancelled in [release], and it RENEWS rather than watching -- and a renewal
     * never starts a watch, so it can never acquire a peek the capability gate did not open. A
     * timer the composition owns and tears down IS the composition; the banned one is the timer
     * that outlives it.
     */
    private val watchRenewal = android.os.Handler(android.os.Looper.getMainLooper())

    /**
     * How often the held watch is renewed. A THIRD of the machine's horizon, so two consecutive
     * missed ticks -- a descheduled main thread, a slow relay append -- still leave one inside
     * the wall. Renewing at the wall itself would make every jittered tick a reap.
     */
    private val watchRenewalMillis = 20_000L

    /**
     * Bring the held watch into line with what is on the glass.
     *
     * A DIFFERENT SESSION, OR NONE, CLOSES THE OLD WATCH FIRST. The same session RENEWS, which is
     * the machine's only evidence that anyone is still looking; the renewal is deliberately driven
     * by the redraw rather than by a timer, because a timer is exactly the background emitter
     * T6-c bans -- a renewal that outlives the composition is a machine rendering for nobody.
     *
     * Both verbs ride [watchLane] -- the command lane, agents-tracker-jx1x -- because they are
     * relay appends: run here they blocked this thread for awaitConn's poll plus the append, and
     * a refusal on the way out of a screen has no user to report to and must not take the draw
     * down with it. The lane wraps both, and its settle is where a refused watch clears the hold.
     */
    private fun reconcileTerminalWatch(bridge: FacadeBridge?, session: String?) {
        val current = watchLane.heldSession
        if (current != null && current == session && !heldWatchLapsed()) {
            renewHeldWatch()
            return
        }
        watchLane.drop()
        watchRenewal.removeCallbacksAndMessages(null)
        if (session == null || bridge == null) return
        // NULL IS THE CAPABILITY REFUSAL, and it is now a refusal this surface cannot route
        // around: the binding's constructor is private and its one factory reads the machine's
        // routing decision, so a session the machine did not send here yields no handle to watch
        // with (closing review, finding 2).
        val binding = bridge.terminalFallbackBinding(session) ?: return
        // THE HOLD IS EAGER: state is written at enqueue time, so the redraw that follows renews
        // instead of re-watching -- one append, not one per redraw. A watch the machine refuses
        // clears the hold on the settle, and the tick below then finds nothing held and stops.
        watchLane.hold(session, binding)
        scheduleWatchRenewal()
    }

    /**
     * Renew the held watch once, and queue the next renewal.
     *
     * It is the whole of the tick's body, so there is one place that decides what a clock may do
     * on this plane: **renew, and nothing else**. It never calls `watch()` -- a renewal that
     * could open one would be a way to acquire a peek with no user on the screen -- and it never
     * touches the control plane, whose keepalive is bound to the foreground composition by a
     * different and stricter rule (T6-c).
     */
    private fun scheduleWatchRenewal() {
        watchRenewal.removeCallbacksAndMessages(null)
        watchRenewal.postDelayed({
            if (watchLane.heldSession == null) return@postDelayed
            renewHeldWatch()
            scheduleWatchRenewal()
        }, watchRenewalMillis)
    }

    /**
     * Tell the machine somebody is still looking. A refusal has no user to report to: the horizon
     * lapses on its own if this never lands, the machine reaps the watch, and -- since round 3 --
     * blanks the phone's copy rather than leaving a dead grid the screen would call fresh.
     *
     * ON THE COMMAND LANE, like the watch it renews (agents-tracker-jx1x): a renewal is a relay
     * append behind awaitConn, and this function used to run it on the main thread from a
     * 20-second main-looper tick. Keyed single-flight, so a renewal parked in awaitConn is not
     * joined by the queue of its own successors.
     */
    private fun renewHeldWatch() {
        val held = watchLane.held ?: return
        dispatch.enqueue(
            plane = SendPlane.COMMAND,
            key = watchRenewalCrossing,
            work = { held.renew() },
            settle = {
                // Deliberately silent; the machine's own horizon is the backstop.
            },
        )
    }

    /**
     * Whether the watch this surface holds has LAPSED, so [reconcileTerminalWatch] must fall
     * through to a full re-watch instead of renewing.
     *
     * THE DEFECT (closing review, finding 6). The same-session branch above renewed
     * UNCONDITIONALLY, and `TerminalWatcher.Renew` is a documented no-op for a session with no
     * live watch. One lapsed horizon -- sixty seconds of machine-side wall against a twenty-second
     * tick, so three missed ticks on a descheduled UI thread, or a short offline window -- ended
     * the stream PERMANENTLY for that screen, and this surface renewed into nothing forever while
     * the user read a grid that stopped updating.
     *
     * The evidence is the SCREEN'S OWN AGE, which is the machine's clock and not this one's, and a
     * refusal answers "not lapsed": a binding that cannot be read is not evidence of a reap, and
     * re-watching on a transient failure would spend an append per redraw.
     *
     * AND IT IS THE WHOLE GRID THAT IS ASKED, not the age alone (closing round 2). The machine
     * BLANKS the phone's copy when it reaps a watch, and that blank carries no render time -- so
     * an age-only question answered NOT LAPSED for the very frame that says the watch is over, and
     * this surface renewed into nothing while the user looked at a blank terminal. A snapshot that
     * has not arrived at all is a REFUSAL from the facade, not a blank, so the arrival window
     * still reads as "not lapsed" and no redraw tears down a peek that is still opening.
     */
    private fun heldWatchLapsed(): Boolean {
        val binding = watchLane.held ?: return false
        return try {
            TerminalFallbackBinding.watchLapsed(binding.grid())
        } catch (unreadable: Exception) {
            false
        }
    }

    /**
     * Draw one conversation: the fixed header, the pinned composer region, and the column.
     *
     * `internal` FOR ONE READER (phone refit W3.2): `PhoneSurfaceControlsTest`, on a JVM whose
     * `PhoneRuntime` answers Unavailable and so can never open a drill-down, builds a surface of
     * its own and draws a real `SessionDetailScreen.of(...)` panel through this -- the production
     * path -- to see what the composer's square says over a working agent. The body is exactly
     * what [drawContent] calls.
     */
    internal fun drawDetail(panel: SessionDetailPanel) {
        val routed = outcome.text.toString()
        if (panel == detailDrawn && routed == detailOutcomeDrawn &&
            contentShows == Destination.INBOX
        ) {
            return
        }
        // THE NARROW PATCH agents-tracker-ksvb.3 ESTABLISHED, now over the transcript -- which is
        // what a working agent changes on every item, the way the grid used to. A rebuild at output
        // rate re-created every block the user was reading AND re-parented the two controls, one of
        // which may be under a finger. `sessionDetailRedraw` accepts a difference in the
        // conversation alone; the two clauses beside it are this function's own guard, unchanged --
        // a routed line that moved is not on the panel, and a destination change means the
        // drill-down is not the view in the host. `openApproval` rides along because the recomposed
        // blocks carry the tap that answers a card.
        if (routed == detailOutcomeDrawn && contentShows == Destination.INBOX &&
            sessionDetailRedraw(
                contentHost, detailDrawn, panel, ::openApproval, ::toggleToolCard, ::fetchDetail,
                // THE SAME PAIR THE COMPOSITION BELOW IS GIVEN, and `transcriptBlockViewCount`
                // says why it must be: two of the transcript's offers exist only when there is a
                // host for them, so a patch counting without them would splice one block's row
                // into the middle of another.
                onOutput = ::openLiteral, onDiff = ::openLiteral,
                // R4's answer rides the patch path too: a decision rebuilt without it loses its
                // buttons the moment the agent writes one more line.
                onDecision = ::answerDecision,
            )
        ) {
            detailDrawn = panel
            // M2.5's placeholder is applied to the FIELD and not composed, which is why it rides
            // the patch: the bar is a slot this surface owns and a redraw never rebuilds it.
            drawComposerRegion(panel)
            // AND THE HEADER IS DRAWN ON THE PATCH PATH TOO, which is what the whitelist bought.
            // `sessionDetailRedraw` accepts a difference in the subtitle and the dot's Group
            // BECAUSE this call is what spends them -- admitting them without redrawing here would
            // leave the state word frozen at whatever it said when the column was last rebuilt,
            // which on a long-lived conversation is for the rest of the session. Its own guard
            // makes this free whenever nothing the reader can see has moved.
            drawConversationHeader(panel)
            return
        }
        detailDrawn = panel
        detailOutcomeDrawn = routed
        fallbackDrawn = null
        drawComposerRegion(panel)
        loadEarlier.text = panel.loadEarlierLabel
        kill.text = panel.killLabel
        resyncControl.text = panel.resyncLabel
        acknowledge.text = panel.acknowledgeLabel
        drawConversationHeader(panel)
        hostContent(
            sessionDetailView(
                context = activity,
                panel = panel,
                resync = resyncControl,
                acknowledge = acknowledge,
                // THE SHEET, IN PLACE (agents-tracker-dwwv.2.4). The same host the inbox list
                // places, re-parented here: answering the question this screen's own transcript
                // just showed no longer means leaving it. See [approvalHost]'s own KDoc.
                approval = approvalSlot(),
                // PB-APP-9's routed line, which is a child of the column this screen replaces. It
                // is handed in rather than left behind, because Stop and Kill reach a machine from
                // here and a refusal with nowhere to land is a control that fails silently.
                outcome = routed,
                onApproval = ::openApproval,
                // ADR-014's page, M2.2's collapse and IS-CAP-2's fetch: the control is a slot this
                // surface owns (the verb and its refusal are both this surface's), and the two
                // taps are callbacks for `onApproval`'s reason -- the ROWS are the conversation's
                // controls, and only this surface may reach a facade verb from one.
                loadEarlier = loadEarlier,
                onToolTap = ::toggleToolCard,
                onDetail = ::fetchDetail,
                // R8 AND R9 ARRIVING AT A HOST. `outputScreen` and `diffScreen` were built,
                // covered and reachable from nothing; these two lines are what a reader taps to
                // get to them, and `transcriptView` draws no offer at all without them.
                onOutput = ::openLiteral,
                onDiff = ::openLiteral,
                // OWNER RULING R4, and the reason this is a callback rather than a control this
                // surface builds: the choices are the CLI's own `decisions[]`, one to eight labels
                // in the order the wire sent them, and IS-APR-4 keeps the verdict machine-side.
                // The screen draws them; only this surface may reach `App.Approve` from one.
                onDecision = ::answerDecision,
            ),
        )
    }

    /**
     * The conversation's fixed header, rebuilt only when what it says has changed.
     *
     * THE GUARD IS [drawSync]'S AND IT IS LOAD-BEARING FOR A SECOND REASON HERE. The first is that
     * one: [render] runs on every resume, after every action and on every journal event, and a
     * header rebuilt unconditionally would re-lay out the top of a screen somebody is reading at
     * whatever rate their agent produces work. The second is that both of this header's controls
     * are SLOTS this surface owns -- a rebuild re-parents them, and the overflow is a 48 dp target
     * a thumb may already be travelling towards. Three facts is the whole of what it draws, so
     * "has anything a reader can see changed" is one comparison.
     *
     * IT IS DRAWN HERE AND NOT PATCHED BY `sessionDetailRedraw`, which is the other half of that
     * function's D.2 decision. The header is not in the column the patch walks; it is redrawn on
     * its own clock, above the scroll and outside it, exactly as the sync chrome already is -- and
     * that is precisely what lets its two fields ride along in the patch's whitelist instead of
     * forcing a full rebuild of the conversation at every turn boundary.
     */
    private fun drawConversationHeader(panel: SessionDetailPanel) {
        val next = Triple(panel.title, panel.headerSubtitle, panel.headerGroup)
        if (next == headerDrawn && headerHost.childCount > 0) return
        headerDrawn = next
        // THE WORDS A SCREEN READER GETS ARE THE PANEL'S, applied to the slots rather than
        // composed into them: `overflowControl` and the kit's back control both refuse to author
        // copy (PB-DS-9), and this is the call site that owes it.
        back.contentDescription = panel.back
        overflow.contentDescription = panel.menu
        (back.parent as? ViewGroup)?.removeView(back)
        (overflow.parent as? ViewGroup)?.removeView(overflow)
        headerHost.removeAllViews()
        headerHost.addView(
            conversationHeader(
                context = activity,
                title = panel.title,
                subtitle = panel.headerSubtitle,
                group = panel.headerGroup,
                back = back,
                menu = overflow,
            ),
        )
    }

    /** What the conversation header last drew: the name, the subtitle, and the dot's Group. */
    private var headerDrawn: Triple<String, String, String>? = null

    /**
     * The pinned region under the conversation: the pill, the bar, what a bar that cannot send
     * says under itself, and the square's own glyph and word -- and the sealing's one word,
     * taken off once the turn it was said over is gone (phone refit W3.4, review round).
     *
     * **THE FIELD'S HINT IS THE SHUT SENTENCE WHERE THERE IS ONE.** `composerPlaceholder` answers
     * "Message" or "Add feedback..." -- the two states of a composer that CAN send -- and for a
     * bar that is on screen and cannot, the honest words are the ones `ComposerModel` already
     * computed for that exact state. Offline is the only such bar (the other three shut states
     * lose the bar entirely and say so inside the scroll), and before this the reader saw a
     * composer visually identical to a live one.
     *
     * IT IS CALLED ON BOTH DRAW PATHS for the header's reason: what it spends is derived from the
     * transcript, which is the one thing the patch lets through, so a region left un-drawn on the
     * patch path would freeze at whatever it said when the column was last rebuilt.
     *
     * NOTHING HERE IS REBUILT. The bar and its notice are permanent -- the composer holds what the
     * user typed -- so what changes is a hint, a string, the square's glyph, one insertion at
     * index 0 and one removal at the end.
     */
    private fun drawComposerRegion(panel: SessionDetailPanel) {
        // "Stopped" was said over one turn; it comes off when that turn is no longer the open
        // one -- closed, or another opened -- and not before, because this runs in the very
        // dispatch that sealed it and again on every item a working agent writes.
        if (stoppedNotice.parent != null && panel.transcript.latestTurnId != stoppedOverTurn) {
            composerRegion.removeView(stoppedNotice)
            stoppedOverTurn = ""
        }
        val shut = panel.composerShut.takeIf { panel.composerIsBar }
        typed.hint = shut?.placeholder ?: panel.composerPlaceholder
        composerShutDetail.text = shut?.detail.orEmpty()
        decisionPillControl.text = panel.decisionPillLabel
        val wanted = panel.pendingDecisionId.isNotEmpty()
        val shown = decisionPillControl.parent != null
        if (wanted && !shown) {
            composerRegion.addView(decisionPillControl, 0)
            // CENTRED HERE, WHICH IS THE SCREEN'S HALF OF THE FENCE: `decisionPill` sets no
            // gravity of its own and says so, because where a pill sits is arrangement and
            // arrangement belongs to whoever placed it.
            (decisionPillControl.layoutParams as? LinearLayout.LayoutParams)
                ?.gravity = Gravity.CENTER_HORIZONTAL
        } else if (!wanted && shown) {
            composerRegion.removeView(decisionPillControl)
        }
        // The square follows the panel it was just drawn against (W3.2): `detailDrawn` is set
        // before this runs on both draw paths.
        drawComposerAction()
    }

    /**
     * Whether the header's menu is open.
     *
     * IT IS THE SURFACE'S AND NOT THE COMPOSITION'S, which is [syncSheetOpen]'s reason exactly:
     * the conversation is redrawn on every journal event, so a flag owned by a view would close
     * the menu under a user who had just opened it, at whatever rate their agent happens to be
     * producing work.
     */
    private var conversationMenuOpen = false

    /** The header's overflow: open the menu, or close the one that is up. */
    private fun toggleConversationMenu() {
        conversationMenuOpen = !conversationMenuOpen
        drawConversationMenu()
    }

    /**
     * Close the menu, if one is open, and say whether there was one.
     *
     * THE ANSWER IS WHAT [closeDrillDown] READS. Back pops the innermost thing the user is
     * standing in, and while a menu is up that is the menu -- a back gesture that left the
     * conversation instead would take away the screen the user opened the menu on top of.
     */
    private fun closeConversationMenu(): Boolean {
        if (!conversationMenuOpen) return false
        conversationMenuOpen = false
        drawConversationMenu()
        return true
    }

    /**
     * Put the menu on screen, or take it off.
     *
     * **IT IS BUILT PER OPEN AND NOT ONCE**, which is [drawApproval]'s ruling for its buttons and
     * holds here for the same reason: a menu row holds nothing a user typed, and WHICH rows exist
     * is a fact about the session that changes under it -- a fully loaded conversation has nothing
     * older to fetch. A menu built once would draw a row it then had to refuse on tap, which is
     * the dead-affordance defect `navHeaderDrill` already has a nullable parameter to avoid.
     *
     * **[SecureWindow.gate] IS ON THE MENU AND NOT ONLY ON [kill]** (PB-SEC-12 clause 1). The tap
     * that ends a session is now the tap on a ROW, and the filter is a property of the view that
     * receives the touch: a `ViewGroup` consults it before dispatching to its children, so gating
     * the block covers every row in it. [touchFilteredActions] cannot hold this view -- it does
     * not exist until the menu is opened -- which is why the gate is applied at the one place that
     * builds it rather than being remembered about in a list.
     *
     * **THE CONFIRMATION IS UNCHANGED AND IS NOT REPEATED HERE.** Choosing `Kill session` presses
     * the very control that already carries it, so the question, its wording and its consequence
     * are exactly what shipped ([kill]'s `ask`). A second question authored at this seam would be
     * two ceremonies for one act, and a menu that killed directly would be a menu that deleted the
     * one that mattered.
     */
    private fun drawConversationMenu() {
        menuHost.removeAllViews()
        menuHost.isClickable = conversationMenuOpen
        val panel = detailDrawn.takeIf { conversationMenuOpen } ?: return
        // WHICH ROWS EXIST IS THE MODEL'S AND NOT THIS FILE'S (PB-DS-9): it is copy and
        // arrangement, both assigned to the screen, and it is the one part of this menu a JVM test
        // can reach -- `SessionDetailScreen.menuChoicesFor` carries the argument for every row it
        // offers and for the two it refuses.
        val choices = SessionDetailScreen.menuChoicesFor(panel)
        val menu = SecureWindow.gate(conversationMenu(activity, choices, ::chooseFromMenu)).apply {
            // THE BLOCK ABSORBS ITS OWN TAPS. Without this, a finger landing on the menu's inset
            // rather than on one of its rows falls through to the scrim behind it and closes the
            // menu -- a near-miss on `Load earlier messages` dismissing the thing the user was
            // aiming at, which is the same class of defect the 48 dp row floor is there to stop.
            isClickable = true
        }
        // ANCHORED UNDER THE CONTROL THAT OPENED IT, by where that control actually is on screen
        // rather than by a height this file would have to know. The header is laid out by the time
        // a finger can reach its overflow, so its own bottom edge is the honest anchor -- and a
        // margin measured from a constant would drift the day the header gains a part.
        val anchor = IntArray(2).also { headerHost.getLocationInWindow(it) }
        val window = IntArray(2).also { windowRoot.getLocationInWindow(it) }
        menuHost.addView(
            menu,
            FrameLayout.LayoutParams(WRAP, WRAP, Gravity.TOP or Gravity.END).apply {
                topMargin = anchor[1] - window[1] + headerHost.height
            },
        )
    }

    /**
     * What a menu row MEANS, which is the only thing that leaves `conversationMenu`.
     *
     * EVERY ARM PRESSES A CONTROL THIS SURFACE ALREADY OWNS rather than repeating its verb, which
     * is [repairSync]'s own arrangement and its reason: those controls carry the press plumbing
     * PB-SEC-12 clause 1 and PB-APP-9 require -- the touch filter, the lane, the confirmation, the
     * routed refusal onto the outcome line -- and a second call site typing `app.kill(...)` would
     * have none of it.
     *
     * THE MENU CLOSES FIRST, AND ON EVERY ARM. A confirmation opening over a menu that is still up
     * would put two decisions on screen at once, and the one underneath is the one the user has
     * already made.
     */
    private fun chooseFromMenu(choice: String) {
        closeConversationMenu()
        when (choice) {
            SessionDetailScreen.MENU_LOAD_EARLIER -> loadEarlier.performClick()
            // STOP PRESSES THE PLAN AND NOT THE SQUARE'S CLICK (phone refit W3.1): the square
            // reads the live field and SENDS when there is a draft, and a menu Stop must
            // interrupt whatever is typed. It goes through the same [press] the square uses,
            // against the same control, so it keeps every piece of that plumbing.
            SessionDetailScreen.MENU_STOP -> press(send, ::interruptPlan)
            SessionDetailScreen.MENU_KILL -> kill.performClick()
        }
    }

    /**
     * Where an approval block in the conversation goes when it is tapped: to the card that answers
     * it, which is [approvalHost] -- composed on THIS SAME SCREEN now, directly under the
     * transcript (`SessionDetailView.DetailTag.APPROVAL`).
     *
     * IT NO LONGER NAVIGATES, AND THAT IS agents-tracker-dwwv.2.4's WHOLE FIX. It used to call
     * [closeSessionDetail], because the sheet was composed in exactly one place -- under the
     * inbox list -- and leaving the drill-down was the only way to reach it: "tapping an approval
     * row in the transcript calls openApproval ... which navigates OUT to the inbox where the
     * sheet is parented", the defect as the audit that opened this bead found it. Answering an
     * approval must never throw a reader out of the conversation they were just reading.
     *
     * IT SCROLLS RATHER THAN DOING NOTHING, which is [dev.swarm.phone.ui.screens.TranscriptTag]'s
     * own words about the block this responds to: "the transcript's job is to say that a decision
     * is waiting and to get the reader to it; the sheet is where it is taken." With the sheet on
     * this same screen, "getting the reader to it" is a scroll and not a departure --
     * `requestRectangleOnScreen` is the platform's own "bring this view into view", asked of
     * whichever ancestor scrolls (`PhoneScaffoldView`'s `ScrollView`); it is a no-op wherever
     * there is none, which is the honest answer when the sheet is already on screen.
     *
     * @param itemId the block's `item_id`, which IS the `interaction_id` (IS-APR-1). It is COMPARED
     *  rather than spent: the card is `pendingApproval(session)` and the tapped block belongs to
     *  that same session, so the only thing this id can still tell the surface is whether the
     *  question is the one being asked. A block whose approval resolved while the user was reading
     *  (IS-LIFE-2) therefore scrolls nowhere, instead of to a card about something else.
     */
    private fun openApproval(itemId: String) {
        // THE LANDING IS RESOLVED BY ITEM ID AND SAYS SO WHEN IT MISSES (Mirror M3's deep link,
        // as far as PB-SEC-11 permits one).
        //
        // THE NOTIFICATION HALF OF M3'S DEEP LINK IS PARKED AND CANNOT BE BUILT HERE.
        // `PhoneActivity` reads NOTHING off its Intent -- its own KDoc states it in the
        // imperative and `PhoneActivityWindowTest.a_crafted_launch_intent_selects_nothing`
        // enforces it by comparing a hostile intent's render against a plain launch's, byte for
        // byte -- and `WakeNotifications` deliberately carries no destination, using
        // NEW_TASK|CLEAR_TASK instead of an extra for exactly that reason. So a notification tap
        // cannot land on an item, and building one would be the shape that test exists to catch.
        // What IS reachable is the IN-APP landing: this tap, on a block of the conversation. `DeepLinkAnchor` answers Found or NotRetained over the
        // conversation this screen is drawing, and NotRetained is a NAMED state rather than a
        // silent nothing: a tap that lands on the wrong thing is worse than one that says so, and
        // a tap that does nothing at all is the dead-chevron defect (agents-tracker-2yb).
        val landing = DeepLinkAnchor.resolveById(
            detailDrawn?.transcript?.blocks.orEmpty().map { it.itemId },
            itemId,
        )
        if (landing is DeepLinkLanding.NotRetained || approvalDrawn?.itemId != itemId) {
            say(PressFeedback.ofUnsent(ANCHOR_NOT_RETAINED))
            return
        }
        approvalHost.requestRectangleOnScreen(Rect(0, 0, approvalHost.width, approvalHost.height))
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
            // agents-tracker-3nx6: GUARDED, the way [FacadeBridge.pendingApproval] guards
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
        // agents-tracker-j171: SOMETHING RATHER THAN NOTHING on the branch with no panel, because
        // a blank primary tab reads as a crash -- PB-DS-9's empty-section argument applied to a
        // whole destination. (The deleted Machines tab carried the same reasoning and is where
        // this comment used to point.) `bridge == null` is reachable only from [renderUnavailable],
        // which has already written PB-APP-9's routed failure onto [status]; this is the same
        // sentence the Inbox tab already carries, not a second one invented here.
        hostContent(
            panel?.let {
                // W7.4: a row is a way into its session, by the inbox row's own rule.
                activityPanelView(activity, it, status = statusSlot(), onSelectSession = ::selectSession)
            } ?: emptyState(activity, status.text.toString()),
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
        // The two machines memos are part of the guard, for `drawInbox`'s third-clause reason:
        // backing out of the switcher lands here with the destination unchanged, so without them
        // the early return fires and the switcher stays on screen over a tab that thinks it
        // popped.
        if (contentShows == Destination.SETTINGS &&
            machinesDrawn == null && globalInboxDrawn == null
        ) {
            return
        }
        machinesDrawn = null
        globalInboxDrawn = null
        hostContent(settings.root)
    }

    /**
     * Wave R4's machine switcher (bead agents-tracker-0ox9), drawn ONLY where the first-run
     * resolver says the world it lives in exists -- see [machinesPanel]. Redrawn only when the
     * panel has changed, for [drawInbox]'s reason.
     */
    private fun drawMachines(bridge: FacadeBridge) {
        val panel = machinesPanel(bridge)
        if (panel == null) {
            // The resolver answered PAIR_ONLY, or the roster could not be read -- and either
            // way [machinesPanel] has already SAID so (round 2): the sub-state is dropped
            // rather than left pointing at a world the resolver never answered, and the
            // gesture is disarmed with it.
            machinesOpen = false
            globalInboxOpen = false
            pushDrillDown()
            drawSettings()
            return
        }
        // THE CLOCK IS PART OF THE GUARD (round 2): the row ages are computed from `now`, so an
        // unchanged panel redrawn only on data change would freeze them at first draw.
        val now = System.currentTimeMillis()
        val minute = now / 60_000L
        if (panel == machinesDrawn && minute == machinesAgeMinute && globalInboxDrawn == null &&
            addFormOpen == machinesAddFormDrawn && contentShows == Destination.SETTINGS
        ) {
            return
        }
        machinesDrawn = panel
        machinesAgeMinute = minute
        machinesAddFormDrawn = addFormOpen
        globalInboxDrawn = null
        hostContent(
            machinesPanelView(
                context = activity,
                panel = panel,
                onAddComputer = ::addComputer,
                onSwitchComputer = ::switchComputer,
                onForgetComputer = ::forgetComputer,
                onOpenGlobalInbox = ::openGlobalInbox,
                onBack = ::closeMachines,
                addForm = addComputerSlot(),
                addFormOpen = addFormOpen,
                onToggleAddForm = ::toggleAddForm,
                nowUnixMs = now,
            ),
        )
    }

    /** The Computers header's Add action: flip the surface's state; the view shows the press itself. */
    private fun toggleAddForm() {
        addFormOpen = !addFormOpen
    }

    /**
     * The switcher's panel, THROUGH THE FIRST-RUN RESOLVER -- never hand-fed. The destination is
     * `MachinesScreen.destinationFor`'s answer over the roster the facade just returned: zero
     * machines is the pair-only world and composes NO panel, one or more is the machines world.
     * A branch here that conjured a MACHINES world the resolver never answered is the hand-fed
     * defect shape this call exists to refuse.
     */
    private fun machinesPanel(bridge: FacadeBridge): MachinesPanel? = try {
        val snapshot = bridge.machines()
        when (MachinesScreen.destinationFor(snapshot.rows.size)) {
            MachinesDestination.PAIR_ONLY -> {
                // The resolver's answer is an answer, not an absence (round 2): the user
                // tapped the entry, the panel is not composed, and the recorded sentence says
                // why over the screen they land back on.
                say(PressFeedback.ofUnsent(MachinesPanelScreen.PAIR_FIRST))
                null
            }
            MachinesDestination.MACHINES ->
                // The selection rides the panel (round 3): it is what the row's mark is drawn
                // from AND what makes two panels differ across a switch, which is what stops
                // [drawMachines]' equality guard from swallowing the redraw.
                MachinesPanelScreen.of(
                    snapshot.rows,
                    cap = snapshot.cap,
                    selected = selectedMachine,
                )
        }
    } catch (refused: Exception) {
        // A refusal is a state, never a silent no-op (round 2). `outcome` alone is a child of
        // unrecomposedControls at the bottom of the Inbox tab -- invisible from Settings -- so
        // the routed answer goes through [say], which writes the line AND fires row 1's toast
        // over the screen the user is standing on. Reached once per entry attempt: the null
        // return drops [machinesOpen], so journal-event renders do not repeat it.
        say(PressFeedback.ofRefusal(bridge.routeFacadeError(refused.message.orEmpty())))
        null
    }

    /**
     * The aggregate inbox (inbox.global), one level below the switcher. Redrawn only when its
     * rows have changed, for [drawInbox]'s reason.
     */
    private fun drawGlobalInbox(bridge: FacadeBridge) {
        val rows = try {
            bridge.globalInbox()
        } catch (refused: Exception) {
            // [machinesPanel]'s catch has the argument (round 2): the routed answer must be
            // visible from the screen the user is on, and `say` is what makes that true.
            say(PressFeedback.ofRefusal(bridge.routeFacadeError(refused.message.orEmpty())))
            null
        }
        if (rows == null) {
            globalInboxOpen = false
            pushDrillDown()
            drawMachines(bridge)
            return
        }
        if (rows == globalInboxDrawn && contentShows == Destination.SETTINGS) return
        globalInboxDrawn = rows
        machinesDrawn = null
        hostContent(globalInboxView(activity, rows, onBack = ::closeGlobalInbox))
    }

    /**
     * The add form's host, detached from whatever held it last -- [statusSlot]'s reason, spent on
     * the third view this app moves between draws: a screen tree is built before it is hosted, so
     * the detach has to happen here, at request time.
     */
    private fun addComputerSlot(): View {
        (addComputerHost.parent as? ViewGroup)?.removeView(addComputerHost)
        return addComputerHost
    }

    /** The settings entry was tapped: open the switcher (wave R4), and arm the back gesture. */
    private fun openMachines() {
        machinesOpen = true
        globalInboxOpen = false
        pushDrillDown()
        render()
    }

    /**
     * The switcher's chevron, and the system back gesture through [closeDrillDown]: back to the
     * settings screen that named the entry. The preview is undone before the next screen lands
     * in the same host -- [closeSessionDetail]'s own reasoning, verbatim: a committed gesture
     * leaves [contentHost] at 90% and transparent.
     */
    private fun closeMachines() {
        machinesOpen = false
        globalInboxOpen = false
        pushDrillDown()
        Motion.clearPredictiveBack(contentHost)
        render()
    }

    /** The switcher's aggregate entry: open the global inbox (inbox.global). */
    private fun openGlobalInbox() {
        globalInboxOpen = true
        pushDrillDown()
        render()
    }

    /** The aggregate inbox's chevron and back gesture: back to the switcher it was opened from. */
    private fun closeGlobalInbox() {
        globalInboxOpen = false
        pushDrillDown()
        Motion.clearPredictiveBack(contentHost)
        render()
    }

    /**
     * Playbook 4.1 step 4: the developer chooses Add computer. `App.AddMachine` registers the
     * pairing BESIDE the existing ones and runs MM6's transactional migration on first use --
     * both facade-side already; what this surface owes it is the two values a user typed and
     * nothing supplied on their behalf. The model's own refusal resolves a blank id here, before
     * a lane is spent on a request the facade must refuse (the launch form's discipline).
     *
     * IT ASKS FIRST, AND IT ASKS ABOUT THE RIGHT THING (round 3). The blast radius is not the new
     * row: the verb below stops the drain, and `App.Stop` is `suspendInput` -- every buffered
     * keystroke resolved UNDELIVERED, every input lease severed, the link really dropped. That is
     * strictly more destructive than forgetting one pairing, which has asked since round 2, and
     * it asked nothing. [MachinesPanelScreen.ADD_CONFIRM] names it; the dialog is
     * [forgetComputer]'s shape for [confirmThenPress]'s recorded reasons.
     *
     * AND A SECOND TAP IS REFUSED OUT LOUD. The controls here are rebuilt per draw, so
     * `VerbDispatch.press`'s per-control fence cannot hold this one; the lane's keyed fence does,
     * and its refusal is spoken rather than swallowed -- running stop/add/start twice would
     * disconnect the phone and destroy unsent input a second time.
     */
    private fun addComputer() {
        val id = addMachineId.text.toString().trim()
        val name = addMachineName.text.toString().trim()
        if (id.isEmpty()) {
            say(PressFeedback.ofUnsent(MachinesPanelScreen.ADD_ID_MISSING))
            return
        }
        AlertDialog.Builder(activity)
            .setMessage(MachinesPanelScreen.ADD_CONFIRM)
            .setPositiveButton(android.R.string.ok) { _, _ ->
                val sent = machineVerb(
                    key = ADD_MACHINE_KEY,
                    settle = {
                        addMachineId.text.clear()
                        addMachineName.text.clear()
                    },
                ) { app ->
                    // THE SURFACE SATISFIES THE FACADE'S PRECONDITION INSTEAD OF FORWARDING ITS
                    // REFUSAL (round 2). `App.AddMachine` is refused while `a.sess != nil` -- the
                    // MM6 migration must not race a live drain -- and `a.sess` is non-nil on
                    // every foregrounded phone, so the raw verb could never succeed and its
                    // ErrClassInvalidRequest routed to a "report a bug" toast blaming the app for
                    // a precondition only this surface can meet. Stop and Start are idempotent
                    // and safe by their own KDoc; the whole sequence runs on this lane, off the
                    // looper, and the restart is in a finally so a refused add does not leave the
                    // phone disconnected.
                    app.stop()
                    try {
                        app.addMachine(id, name)
                    } finally {
                        app.start()
                    }
                }
                if (!sent) say(PressFeedback.ofUnsent(MachinesPanelScreen.ADD_IN_FLIGHT))
            }
            .setNegativeButton(android.R.string.cancel, null)
            .show()
    }

    /**
     * A row was tapped: record the switch, BY MACHINE ID (MM4). The view is the only input the
     * deterministic least-recently-viewed connection policy takes; the broken-pairing refusal
     * cannot reach here from the panel -- a broken row takes no tap -- and if the facade refuses
     * anyway, the routed answer lands on screen like every other (MM8: a state, never a crash and
     * never a silent no-op).
     *
     * SUCCESS IS ANSWERED TOO (round 3), which it was not: the verb settled with [machineVerb]'s
     * default no-op, so a switch that WORKED said nothing and marked nothing, and the redraw
     * guard early-returned over a byte-identical panel. Both halves of the answer are here -- the
     * row's mark, through [selectedMachine], and the spoken confirmation, which states in the
     * same breath what the switch did NOT do (mobile/machines.go:19-21: the live relay session
     * has not moved). Round 2 routed every refusal through [say] and left the success mute.
     */
    private fun switchComputer(machineId: String) {
        val name = machinesDrawn?.rows?.firstOrNull { it.machineId == machineId }
            ?.displayName.orEmpty().ifEmpty { machineId }
        machineVerb(
            settle = {
                selectedMachine = machineId
                say(PressFeedback.ofSuccess(MachinesPanelScreen.switchedTo(name)))
            },
        ) { app -> app.selectMachine(machineId) }
    }

    /**
     * Playbook 4.9: the PHONE-side removal of exactly one pairing, distinct from revoking a phone
     * from a computer. The facade's own refusals -- the active pairing, the last pairing -- route
     * to the screen as states.
     *
     * IT ASKS FIRST (round 2). What this destroys -- the pairing's keys, namespace and caches --
     * does not come back, it hangs on a denyChip in a row's trailing slot beside a row that is
     * itself a tap target, and `kill`, which ends ONE session, has asked since S24: mrq5's own
     * argument, unspent here until now. The dialog is [confirmThenPress]'s shape (a second
     * window, never a row in the composition; the platform's own OK/Cancel words) without its
     * [Press], because the machines verbs settle through [machineVerb]'s lane rather than a
     * per-control dispatch. The QUESTION is the model's recorded copy (PB-DS-9).
     */
    private fun forgetComputer(machineId: String) {
        AlertDialog.Builder(activity)
            .setMessage(MachinesPanelScreen.FORGET_CONFIRM)
            .setPositiveButton(android.R.string.ok) { _, _ ->
                machineVerb { app -> app.forgetMachine(machineId) }
            }
            .setNegativeButton(android.R.string.cancel, null)
            .show()
    }

    /**
     * One machines verb, run OFF the looper and answered back on it.
     *
     * IT IS [dispatch]'s `enqueue` AND NOT `press`, because the controls here are built per draw
     * from the row set -- an in-flight mark keyed on a view the next redraw replaces would fence
     * nothing. What survives is the half that matters: the facade call leaves the main thread
     * (`AddMachine` runs MM6's migration, which is file I/O), and the refusal is ROUTED rather
     * than thrown -- PB-APP-9's table, the outcome line and row 1's toast, exactly like every
     * other press ([dispatchPress]'s program without the control).
     *
     * @param key the lane's single-flight key, or null for a verb that must never be dropped
     *  ([VerbDispatch.enqueue]'s own argument). Add computer carries one: it stops the drain, so
     *  a double tap would disconnect the phone and destroy unsent input twice.
     * @param settle what a RETURNED verb changes on screen, on the looper; a refusal goes to the
     *  routed line instead.
     * @return whether the verb was ACCEPTED, i.e. whether anything was sent at all. False means a
     *  verb under the same [key] is still crossing -- or, unreachable from a drawn switcher
     *  because the panel is composed only where the bridge exists, that the phone core is not
     *  ready. A caller that passes no key is never refused and may ignore this.
     */
    private fun machineVerb(
        key: Any? = null,
        settle: () -> Unit = {},
        verb: (App) -> Unit,
    ): Boolean {
        val startup = runtime.phone()
        if (startup !is PhoneStartup.Ready) return false
        return dispatch.enqueue(
            SendPlane.COMMAND,
            key = key,
            work = { verb(startup.app) },
            settle = { answer ->
                answer.fold(
                    onSuccess = { settle() },
                    onFailure = {
                        say(
                            PressFeedback.ofRefusal(
                                FacadeBridge(startup.app)
                                    .routeFacadeError(it.message.orEmpty()),
                            ),
                        )
                    },
                )
                render()
            },
        )
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
        for (view in listOf(unrecomposedControls, settings.root, pairing.root, statusHost)) {
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
        // [closeSessionDetail]'s argument, on the other end of the same journey: a menu opened
        // over one session must not survive into another, where its rows would act on a session
        // the user did not open it on -- and the same for a literal screen, whose item id belongs
        // to a conversation the reader has just left.
        closeConversationMenu()
        literalRouteItem = ""
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
        // AND SO DOES THE LITERAL SCREEN, which is a place INSIDE this conversation: carried
        // across a departure it would put the reader back on a tool's output the next time they
        // opened any session, with no conversation behind it to have come from.
        literalRouteItem = ""
        // AND THE MENU GOES WITH THE HEADER IT HANGS OFF. It is opened from a control that is
        // about to leave the window, and a block left over the inbox would be three acts on a
        // session the user is no longer looking at -- the proximity error PB-SYNC-2 forbids,
        // wearing a menu.
        closeConversationMenu()
        // AND THE UNSENT PRESS IS FORGOTTEN WITH THE SCREEN THAT REPORTED IT. It is the answer to
        // one press on one screen; carried across a departure it would greet the user on their
        // return with a failure from before they left.
        stopNotSentFor = ""
        // AND SO ARE THE COMPOSER'S OWN PRESS FACTS, on the same argument one clause over: the
        // send state and its routed refusal report on a press made on THIS visit, and the tool
        // cards the reader collapsed are a fact about the screen they were reading rather than
        // about the session. Carried across a departure they would greet the user on their return
        // with a refusal from before they left, over a conversation they had rearranged.
        composerSendFor = ""
        composerSentText = ""
        composerSendState = null
        composerRefusal = ""
        composerRefusalDetail = ""
        // AND THE ANSWER IN FLIGHT IS FORGOTTEN WITH THE SCREEN, on the same argument: a lock is a
        // fact about a press made on THIS visit, and one carried across a departure would greet
        // the reader on their return with a decision frozen behind an answer they can no longer
        // see the outcome of.
        answeringItemId = ""
        expandedCards.clear()
        // THE PREVIEW IS UNDONE BEFORE THE NEXT SCREEN IS DRAWN INTO THE SAME HOST. A committed
        // gesture leaves [contentHost] at 90% and fully transparent, and the inbox is hosted in
        // that same view -- so without this the user's back gesture succeeds and lands them on an
        // invisible list. It is here rather than only in the Activity because this is the one
        // function every departure runs through, chevron and gesture alike.
        Motion.clearPredictiveBack(contentHost)
        render()
    }

    /**
     * One frame of the system back gesture, previewed on the drill-down (migration plan O6.3).
     *
     * IT SCALES [contentHost] AND NOT [root], and the difference is the whole reason the drill-down
     * is the subject: the tab bar and the sync chrome are chrome that the gesture is not leaving,
     * so a preview that shrank the window would tell the user they were about to exit the app --
     * which is what back does on the inbox, and is exactly the thing this callback exists to
     * prevent them confusing.
     *
     * **THE CONVERSATION HAS NO TAB BAR AND THE SUBJECT IS STILL [contentHost]**, which is a
     * decision rather than an oversight. What the reader is leaving is the CONVERSATION; the
     * header names it and the composer types into it, and both are the frame around it in exactly
     * the sense the bar is the frame around a destination. Shrinking all three would leave a
     * one-line strip standing over a receding window, which reads as leaving the app -- the
     * confusion the paragraph above exists to prevent, arriving by the other door. The platform's
     * own choreography would pivot and translate as well; neither is here, for the reason
     * `Motion.predictiveBack` records: this app's nav is one `FrameLayout` whose content is
     * replaced, so there is no incoming view to fade up.
     *
     * WHAT CROSSES FROM [PhoneActivity] IS A FLOAT, which is PB-SEC-11 and not style. That class is
     * exported with a LAUNCHER filter, so the gesture handler over there may touch local screen
     * state and nothing else; the view work lives here, one call away, exactly as
     * [closeSessionDetail]'s does.
     */
    internal fun previewBack(progress: Float) {
        Motion.predictiveBack(activity, previewSubject(), progress)
    }

    /** The gesture was abandoned: put the drill-down back exactly as it was. */
    internal fun cancelBackPreview() {
        Motion.clearPredictiveBack(previewSubject())
    }

    /**
     * What the back gesture is previewed ON, which is not always [contentHost].
     *
     * R8'S OUTPUT AND R9'S DIFF ARE THE WHOLE ROOT, not a destination inside a scaffold: while one
     * is up the conversation's hosts are detached, so a preview aimed at [contentHost] would move
     * a view that is not on screen and the gesture would have no answer at all. What is being left
     * there IS the window's one child, so that is the subject -- and it is correct rather than
     * alarming for this screen's reason: back from a literal returns to the conversation, and the
     * conversation is what will be underneath.
     */
    private fun previewSubject(): View =
        if (literalRouteItem.isNotEmpty()) host.getChildAt(0) ?: contentHost else contentHost

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
        if (next == destination) {
            detail = null
            // The Settings tab's own sub-states pop by the same platform convention (wave R4,
            // bead agents-tracker-0ox9): re-tapping the tab is the way back for a user standing
            // in the switcher or the aggregate inbox, chevrons aside.
            machinesOpen = false
            globalInboxOpen = false
            pushDrillDown()
        }
        destination = next
        render()
    }

    /**
     * ADR-009 (4)'s approval card, composed rather than shown and hidden.
     *
     * **IT IS `drawPeek` WITH THE GRID TAKEN OUT AND THE QUESTION PUT IN.** What stood here drew
     * `peekPanelView` into `peekHost`; the ADR deletes that screen at slice I1's exit (3), and this
     * is the surface it names in its place. What is unchanged is the shape: one host, a diff
     * against what was last drawn, and no visibility writes -- "a view that is not on screen is a
     * view this did not add". That was the fix for `renderLease`, which stated what was on screen a
     * second time, three lines from the composition that put it there, and could contradict it.
     *
     * THE BUTTONS ARE BUILT PER DRAW, AND THE DATA FORCES IT. Every other control on this surface
     * is a field built once and re-parented, because it holds what the user typed or must survive a
     * redraw. A decision button holds nothing, and how many there are and what they are called is
     * `decisions[]` -- 1..8 entries in the CLI's own vocabulary (§5's `MaxDecisions`) -- so there is
     * nothing to preserve when one card replaces another.
     *
     * @param panel null when this phone holds no unresolved approval for the session on screen.
     *  Nothing is composed, because with no question there is nothing to answer.
     */
    private fun drawApproval(panel: ApprovalSheetPanel?) {
        if (panel == approvalDrawn) return
        approvalDrawn = panel
        approvalHost.removeAllViews()
        if (panel == null) return
        approvalHost.addView(
            approvalSheetView(
                context = activity,
                panel = panel,
                actionFor = { decision -> approvalAction(panel, decision) },
            ),
        )
    }

    /**
     * One offered decision, as the control that answers it.
     *
     * IT IS `CtaKind.MORE` FOR EVERY DECISION, AND THAT IS IS-APR-4 RATHER THAN A TASTE. The
     * verdict -- `allow` | `deny` | `other` -- is classified by the adapter at capture and read by
     * the DAEMON to resolve §3.6's allowed/denied. It is deliberately NOT a field on the item:
     * "the card labels its buttons from `decisions[].label` and no phone surface switches on
     * polarity". `.a2-ok` or `.a2-no` on a label this side cannot classify would be a grant or a
     * refusal asserted by the paint, over a vocabulary this side does not own.
     *
     * THE PRESS SENDS THE ID AND NOTHING ELSE (IS-APR-2). `App.Approve` reads the binding tuple --
     * the agent instance, the content hash, the daemon-authoritative expiry -- off the card this
     * phone is already holding, and refuses one it does not hold. A screen able to PASS those
     * values is a screen able to compute them, and the failure would be a perfectly valid command
     * the daemon rejects as stale.
     *
     * IT IS ON THE COMMAND PLANE because an approval is signed: `mobile/commands.go` polls
     * `awaitConn` for a connection on that path and deliberately does not on the live one
     * (ADR-007 D7), and the two must not share a lane.
     *
     * IT ADDS NO CONFIRMATION DIALOG. The sheet IS the confirmation -- D4.4's heaviest surface,
     * "reserved for the moment of decision" -- and a second dialog over a button the user pressed
     * after reading the question would ask them to confirm the thing they just read.
     *
     * IT REMEMBERS THE OPERATION NOW (agents-tracker-dwwv.2.4), which is what [takeControlOf] and
     * [kill] already do and this control never did. `App.Approve`'s `ok` stopped meaning RESOLVED
     * under mirror-program.md M1.2: it means the daemon TYPED the dialog's keys, and the card does
     * NOT leave the screen on its own -- `pendingApproval(session)` still names it, unresolved,
     * until the `approval_resolved` item the daemon emits by OBSERVING the dialog leave finally
     * arrives, which [TranscriptScreen] already renders. So a toast on acceptance would report a
     * resolution nobody has seen yet; what this settle claims the operation id FOR is the one
     * answer that is a fact right away -- a REFUSAL, `already_applied` foremost among them, which
     * is exactly the safety net a second tap during that window needs. [renderApprovalVerdict] is
     * where the claim is read back.
     */
    /**
     * Owner ruling R4's answer, reaching the machine from the card in the stream.
     *
     * **IT IS [approvalAction]'S VERB WITHOUT [approvalAction]'S BUTTON.** The sheet builds its own
     * controls, so it could wrap them in [pressable] and inherit the whole dispatch. The inline
     * card's choices are built by `transcriptView` out of the CLI's own `decisions[]`, which this
     * surface never sees and must not author -- so what crosses is a callback, and the verb has to
     * live here: `App.Approve` on the COMMAND plane, an operation id [renderApprovalVerdict] reads
     * back, and refusals routed through PB-APP-9. None of those are a screen's to own.
     *
     * THE PRESSED VIEW IS PASSED IN rather than substituted, on `onDetail`'s precedent. [press]
     * marks a control in flight and plays its haptic against it; naming any other control would be
     * a lie about which one the finger landed on, and the transcript rebuilds these buttons on
     * every redraw so this surface has no lasting handle to name instead.
     *
     * **SINGLE-FLIGHT IS THE MODEL'S HERE, NOT THE DISPATCH'S**, and that is the one real
     * difference from the sheet. [pressable]'s guard is per CONTROL, and a control rebuilt by the
     * next patch is a different object with no memory of being pressed. So the lock that matters
     * is [answeringItemId] -> `TranscriptScreen.of(answering = ...)` -> `block.locked`, which is a
     * fact about the QUESTION and survives every redraw of the button. Set before the press for
     * [interruptPlan]'s reason exactly: read on the looper that owns the screen, never from a lane.
     */
    /**
     * Owner ruling R6's bubble, or null when this phone is holding nothing for [session].
     *
     * IT IS BUILT FROM WHAT WAS ACTUALLY SENT, never from the field: [composerSendFor] records the
     * session the send was addressed to and [composerSentText] the words that went, both captured
     * at the press. Reading the composer here instead would draw whatever the reader has since
     * started typing, attributed to a message they already sent.
     *
     * THE OPERATION ID IS THE JOIN. [composerOp] is the id the send was issued under, and the
     * daemon stamps the echo with it (`stampComposerEchoLocked`), so the transcript can tell this
     * copy from the record's own item without comparing words -- which would collapse two
     * identical sends into one.
     *
     * A REFUSED SEND STILL DRAWS. The words stay with the reason beside them, because nothing is
     * silently swallowed and nothing is queued: a message that cannot go is refused visibly and
     * kept where they can send it again.
     */
    private fun pendingSendFor(session: String): PendingSend? {
        if (composerSendFor != session || composerOp.isEmpty()) return null
        return when (composerSendState) {
            SendState.PENDING, SendState.SENT ->
                PendingSend(composerOp, composerSentText)
            SendState.REFUSED, SendState.STALE_TURN ->
                PendingSend(composerOp, composerSentText, refused = true)
            null -> null
        }
    }

    private fun answerDecision(pressed: View, itemId: String, decision: ApprovalDecision) {
        val target = session
        answeringItemId = itemId
        press(pressed) {
            Press(
                SendPlane.COMMAND,
                verb = { app -> app.approve(target, itemId, decision.id) },
                settle = { answer -> rememberApproval(answer) },
            )
        }
    }

    private fun approvalAction(panel: ApprovalSheetPanel, decision: ApprovalDecision): View =
        actionButton(decision.label, CtaKind.MORE) {
            // THE ANSWER IS IN FLIGHT FROM THIS INSTANT, and the item it answers is what the
            // transcript needs to lock. Read here, on the looper that owns the screen, for
            // [interruptPlan]'s reason exactly: a lane must never touch this.
            answeringItemId = panel.itemId
            Press(
                SendPlane.COMMAND,
                verb = { app -> app.approve(panel.sessionId, panel.itemId, decision.id) },
                settle = { answer -> rememberApproval(answer) },
            )
        }

    /**
     * Latch the approve this surface issued, so [renderApprovalVerdict] can claim its answer by
     * operation id (PB-SYNC-2) rather than resolving it by proximity. [rememberKill]'s own
     * reasoning, spent on the third verb that never had a settle at all.
     */
    private fun rememberApproval(answer: Any?) {
        val issued = answer as? Op ?: return
        approveOp = issued.operationID
    }

    /**
     * The decision this phone has ANSWERED and not yet seen resolved, or "" when none is in
     * flight.
     *
     * WHAT IT BUYS is the drawing's rule that every choice locks while one answer is crossing, so
     * nothing reflows under a descending thumb and no second answer is sent to a question that
     * already has one. `TranscriptScreen.of(answering = ...)` turns it into
     * `TranscriptBlock.locked`.
     *
     * **IT IS A REAL PRODUCER AND ITS CONSUMER IS HALF-BUILT, WHICH IS WORTH SAYING OUT LOUD.**
     * The choices a reader presses today are the SHEET's ([approvalHost]), composed under the
     * transcript rather than inline at the item -- so `locked` is computed correctly and there are
     * no inline choices for it to grey out yet. The inline decision card is Wave E's. When it
     * lands, this latch is already the fact it needs; until then the lock is correct and
     * invisible, which is a state named here rather than discovered later.
     *
     * IT IS CLEARED BY THE MACHINE'S ANSWER and by leaving the screen -- never by a timer. An
     * answer that never resolves leaves the question locked, which is the safe direction: the
     * alternative is a second answer to a question the machine may already have taken.
     */
    private var answeringItemId: String = ""

    /**
     * The keyboard's two controls, which were never part of the peek.
     *
     * They are row 9's composer: the field, and the one control that sends or stops (phone refit
     * W3). Offline shuts both -- `SessionLease.keyboardEnabled` is the link -- so an offline Stop
     * reaches [interruptPlan]'s NOT_SENT arm from the header menu's row alone, where it is
     * reported rather than sealed.
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
        val presets = presetPanelOnScreen()
        val panel = launchPanelOnScreen()
        if (panel == launchDrawn && presets == presetDrawn) return
        launchDrawn = panel
        presetDrawn = presets
        launchHost.removeAllViews()
        // Wave R5 round 2: the preset flow is the REMOTE launch UX (playbook 4.3) and sits
        // first; the free-form form below it remains the PB-APP-6 surface this slice does not
        // retire. The fetch control is a long-lived view this surface owns ([launch]'s reason),
        // detached here so the redraw can re-home it.
        presets?.let { p ->
            (fetchPresets.parent as? ViewGroup)?.removeView(fetchPresets)
            launchHost.addView(
                launchPresetView(
                    context = activity,
                    panel = p,
                    fetch = fetchPresets,
                    onSelect = ::confirmPresetLaunch,
                ),
            )
        }
        launchHost.addView(
            launchPanelView(
                context = activity,
                panel = panel,
                fieldFor = ::launchField,
                submit = launch,
            ),
        )
    }

    /**
     * The preset flow as it stands: the last [FacadeBridge.launchPresetFlow] snapshot resolved
     * to its panel, or null before any bridge has answered (the failed-core branch draws no
     * section rather than a section made of guesses).
     */
    private fun presetPanelOnScreen(): LaunchPresetPanel? = presetFlow?.let { flow ->
        LaunchPresetPanel(
            availability = flow.availability,
            availabilityNotice = LaunchPresetScreen.noticeFor(flow.availability),
            rows = flow.rows,
            deliveryNotice = presetDelivery,
            fetchNotice = presetFetchDelivery,
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
    private fun rememberDetailRead(answer: Any?, itemId: String) {
        val issued = answer as? Op ?: return
        detailOp = issued.operationID
        detailFor = itemId
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
        // THE MACHINE'S WORDS TRAVEL AS THE TOAST'S SUFFIX (agents-tracker-ksvb.10). This verb has
        // no panel cell of its own -- the kill's answer reaches the user through the outcome line
        // and row 1's toast and nowhere else -- and row 1 gives that toast a separate MONO cell
        // beside its message, which is the register the demotion asks for.
        if (notice.isNotEmpty()) {
            say(PressFeedback.ofRefusal(notice, SessionDetailScreen.killDetailFor(verdict)))
        }
    }

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
        renderApprovalVerdict(bridge)
        // Wave R6's four, on the same program and for the same reason (review round 2): every
        // one of them has an ANSWER, and none of them was claimed.
        renderComposerVerdict(bridge)
        renderInterruptVerdict(bridge)
        renderReadVerdicts(bridge)
    }

    /**
     * ADR-009 (6)'s per-send lifecycle, claimed BY OPERATION ID (Mirror M2.4, review round 2).
     *
     * THE DECISION IS [SessionDetailScreen.composerVerdictFor]'S AND NOT THIS FUNCTION'S, which
     * is this surface's standing split -- a model decides, a surface acts -- and it matters more
     * here than anywhere: what a settle does to a draft cannot be unit-tested on this side of
     * the AAR, and a composer that cleared the field on local sealing is what that blindness
     * produced last round.
     *
     * THE CLAIM IS ONE-SHOT because `App.Outcome` answers from a durable map: a latched
     * operation re-claimed per draw would re-toast at whatever rate the user's agents produce
     * journal events (`renderPresetFlow`'s own recorded defect).
     */
    private fun renderComposerVerdict(bridge: FacadeBridge) {
        if (composerOp.isEmpty()) return
        val verdict = try {
            SessionDetailScreen.composerVerdictFor(bridge.launchOutcome(composerOp), composerOp)
        } catch (unreadable: Exception) {
            // Unresolved is the honest state, and the next draw asks again.
            return
        }
        if (!verdict.answered) return
        composerOp = ""
        composerSendState = verdict.state
        composerRefusal = verdict.refusal
        // THE MACHINE'S WORDS RIDE WITH THE REFUSAL (W2.3). The composer notice is a refusal's
        // single surface: the say() that stood here wrote the outcome line and a toast carrying
        // the same sentence, so one refused send said it three times. A refused Stop is not a
        // composer refusal and keeps its say() (renderInterruptVerdict).
        composerRefusalDetail = verdict.detail
        // THE DRAFT IS SPENT ONLY HERE, and only on the answer that says it was delivered.
        if (verdict.clearsDraft) typed.text.clear()
    }

    /** The Stop's own answer, claimed the way [renderKillVerdict] claims the kill's. */
    private fun renderInterruptVerdict(bridge: FacadeBridge) {
        if (interruptOp.isEmpty() || interruptOp == interruptSaid) return
        val verdict = try {
            CommandVerdict.of(bridge.launchOutcome(interruptOp), interruptOp, CommandVerdict.ACCEPTED_OK)
        } catch (unreadable: Exception) {
            return
        }
        if (!verdict.answered) return
        interruptSaid = interruptOp
        // SILENT ON ACCEPTED: the turn's own terminal item is the confirmation, and the press
        // already said Stopped under the composer (SessionDetail.INTERRUPT_SENT).
        val notice = SessionDetailScreen.interruptNoticeFor(verdict)
        if (notice.isNotEmpty()) {
            say(PressFeedback.ofRefusal(notice, SessionDetailScreen.interruptDetailFor(verdict)))
        }
    }

    /**
     * The two M3 reads, whose answer is not a sentence but the TRANSCRIPT.
     *
     * CLAIMING IS THE ADOPTION MOMENT, exactly as it is for the preset refresh: `App.Outcome`
     * folds a claimed `interaction_history` page into the live `ItemStore` and a claimed
     * `interaction_detail` body over the clipped card (`adoptInteractionRead`). Until this
     * claimed them, both replies sat in the reply cache and no screen could ever show them --
     * which is why [renderVerdicts] runs BEFORE the content is drawn: a page claimed after it
     * would reach the screen one journal event late.
     *
     * A REFUSED READ IS SAID OUT LOUD ONLY WHERE SOMEBODY PRESSED SOMETHING ([historySpeaks]).
     * The detail read is always a press, so it always speaks.
     */
    private fun renderReadVerdicts(bridge: FacadeBridge) {
        if (historyOp.isNotEmpty()) {
            val outcome = try {
                bridge.launchOutcome(historyOp)
            } catch (unreadable: Exception) {
                null
            }
            if (outcome != null && outcome.operationId == historyOp && outcome.code.isNotBlank()) {
                val speaks = historySpeaks
                val target = historyFor
                historyOp = ""
                historyFor = ""
                historySpeaks = false
                when {
                    outcome.code != READ_HISTORY_OK -> {
                        // THE MACHINE'S OWN WORDS, in the detail cell (round 3, finding F4). This
                        // used to hand the wire code to `ErrorRouter.routeMachineCode` and pass
                        // the result to the ONE-ARG `ofRefusal`, which sets no detail -- so the
                        // daemon's sentence was discarded and codes with no row in the routing
                        // table (deliberately: `unavailable`, `invalid_field`) were rendered as
                        // `ErrorState.UNKNOWN`'s "Something failed in a way the app does not
                        // recognise". Every other machine-answering verb on this surface says a
                        // verb-specific sentence and shows what the machine said; these two are
                        // no longer the exception.
                        val verdict = CommandVerdict.of(outcome, outcome.operationId, READ_HISTORY_OK)
                        if (speaks) {
                            say(
                                PressFeedback.ofRefusal(
                                    SessionDetailScreen.historyReadNoticeFor(verdict),
                                    SessionDetailScreen.historyReadDetailFor(verdict),
                                ),
                            )
                        }
                    }
                    // THE PAGE ARRIVED AND THIS PHONE COULD NOT HOLD IT (ADR-014 A8). The control
                    // is dropped by the panel on the same fact; this is the sentence that stops
                    // that from reading as "you have reached the beginning" -- said ONCE, at the
                    // moment it becomes true, where the finger that asked was.
                    speaks && bridge.historyAtCapacity(target) ->
                        say(PressFeedback.ofRefusal(SessionDetailScreen.historyCapacityNotice(), ""))
                }
            }
        }
        if (detailOp.isNotEmpty()) {
            val outcome = try {
                bridge.launchOutcome(detailOp)
            } catch (unreadable: Exception) {
                null
            }
            if (outcome != null && outcome.operationId == detailOp && outcome.code.isNotBlank()) {
                val card = detailFor
                detailOp = ""
                detailFor = ""
                if (outcome.code != READ_DETAIL_OK) {
                    // IS-CAP-3's `unavailable` among them: the machine no longer retains the
                    // whole of this card. The sentence is the SCREEN's, verb-specific, with the
                    // machine's own words beside it -- see the history arm above for the routing
                    // this replaced and why.
                    val verdict = CommandVerdict.of(outcome, outcome.operationId, READ_DETAIL_OK)
                    say(
                        PressFeedback.ofRefusal(
                            SessionDetailScreen.detailReadNoticeFor(outcome.code, verdict),
                            SessionDetailScreen.detailReadDetailFor(verdict),
                        ),
                    )
                    // AND THE OFFER GOES WITH IT when the refusal is terminal (round 3, F4). The
                    // card advertises the fetch from fields journalled at CAPTURE time, so an
                    // evicted body goes on offering itself: the reader taps, reads that it is
                    // gone, and is invited to tap again. This is the one place the phone learns
                    // otherwise, and it is remembered per item because it is true of that item.
                    if (card.isNotEmpty() && SessionDetailScreen.detailReadIsTerminal(outcome.code)) {
                        detailRefused.add(card)
                    }
                }
            }
        }
    }

    /** Latch the composer_send this surface issued. See [rememberLease] for the `Any?`. */
    private fun rememberComposerSend(answer: Any?) {
        val issued = answer as? Op ?: return
        composerOp = issued.operationID
    }

    /**
     * Latch the turn_interrupt this surface issued, and say ONCE, under the composer, that it
     * went (phone refit W3.4).
     *
     * THE WORD IS THE SEALING'S AND NOT THE AGENT'S: `App.Interrupt` returns the moment the
     * envelope is appended, and [SessionDetail.INTERRUPT_SENT]'s own KDoc keeps that argument.
     * It is drawn in place by [drawStopped] -- [stoppedNotice] joins [composerRegion] and stays
     * while the turn it was said over is the open one -- and never as row 1's toast; the
     * machine's refusal, when there is one, arrives through [renderInterruptVerdict].
     */
    private fun rememberInterrupt(answer: Any?) {
        val issued = answer as? Op ?: return
        interruptOp = issued.operationID
        interruptSaid = ""
        drawStopped()
    }

    /**
     * "Stopped", under the composer, over the turn the panel shows open (W3 review round,
     * 2026-08-28).
     *
     * IT RECORDS THE TURN BESIDE THE WORD, and [drawComposerRegion] takes the word off only when
     * the drawn panel's open turn is a different one -- closed, or another opened. It used to come
     * off on the NEXT region draw, which was the defect the review found: this settle runs inside
     * the dispatch that then calls `render()`, and a working agent redraws at output rate, so the
     * word was taken off before it reached a frame (and when the outcome line was non-empty at
     * press time, by the full draw path in the same dispatch).
     *
     * `internal` FOR ONE READER, [drawDetail]'s reason: `PhoneSurfaceControlsTest` cannot press
     * its way here -- `press` stops at the runtime gate on every JVM and `Op` is the AAR's -- so
     * it calls this half directly over a drawn panel and redraws. That [rememberInterrupt] is
     * what calls it in production is `w34_onebutton_test.go`'s to read.
     */
    internal fun drawStopped() {
        stoppedOverTurn = detailDrawn?.transcript?.latestTurnId.orEmpty()
        stoppedNotice.text = SessionDetail.INTERRUPT_SENT
        if (stoppedNotice.parent == null) composerRegion.addView(stoppedNotice)
    }

    /** Latch the history read this surface issued, and whether its refusal speaks. */
    private fun rememberHistoryRead(answer: Any?, target: String, aloud: Boolean) {
        val issued = answer as? Op ?: return
        historyOp = issued.operationID
        historyFor = target
        historySpeaks = aloud
    }


    /**
     * PB-APP-9 for the fourth verb this table now covers (agents-tracker-dwwv.2.4):
     * [approveOp]'s own answer, claimed the same way [killOp]'s is.
     *
     * SILENT ON ACCEPTED, WHICH IS `ApprovalSheetScreen.refusalNoticeFor`'s OWN CLAUSE AND NOT A
     * SECOND ONE HERE. `ok` means APPLIED under M1.2, not resolved -- the `approval_resolved`
     * item is the confirmation, and it is [TranscriptScreen]'s to render, already, the moment it
     * arrives. What this claims a REFUSAL for is the one thing the daemon can say about a tap
     * that is a fact right away: the card the phone is holding is no longer one it can act on
     * (`already_applied` foremost -- the exact race a second tap during the applied-but-not-yet-
     * observed window would otherwise risk), and [ApprovalSheetScreen.refusalNoticeFor] is where
     * the calm, honest sentence for that is written.
     */
    private fun renderApprovalVerdict(bridge: FacadeBridge) {
        if (approveOp.isEmpty() || approveOp == approveSaid) return
        val verdict = try {
            CommandVerdict.of(bridge.launchOutcome(approveOp), approveOp, CommandVerdict.ACCEPTED_OK)
        } catch (unreadable: Exception) {
            // Unresolved is the honest state, and the next draw asks again.
            return
        }
        if (!verdict.answered) return
        approveSaid = approveOp
        // THE LOCK ENDS WHERE THE ANSWER DOES, on the same fact and not one draw later: the
        // machine has spoken about this approval, so the choices are no longer waiting on a reply
        // this phone is holding.
        answeringItemId = ""
        val notice = ApprovalSheetScreen.refusalNoticeFor(verdict)
        if (notice.isNotEmpty()) {
            say(PressFeedback.ofRefusal(notice, ApprovalSheetScreen.refusalDetailFor(verdict)))
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

    /**
     * Wave R5's preset flow, per draw (round 2): read the snapshot the section renders from, and
     * claim the two in-flight operations BY ID (PB-SYNC-2).
     *
     * CLAIMING THE REFRESH IS THE ADOPTION MOMENT: `App.Outcome` folds a claimed launch_presets
     * reply into the preset cache and the machine-stamped tier (`adoptPresets`), so until this
     * claims it the snapshot below still renders the pre-refresh world. A wire refusal of the
     * read lands on the same delivery line as the confirm's -- a fetch that was refused out
     * loud, never a list that silently stays empty.
     */
    private fun renderPresetFlow(bridge: FacadeBridge) {
        if (presetRefreshOp.isNotEmpty()) {
            val refreshed = try {
                bridge.launchOutcome(presetRefreshOp)
            } catch (unreadable: Exception) {
                null // unresolved is the honest state; keep claiming on the next draw
            }
            when {
                refreshed == null || refreshed.code.isEmpty() -> {} // not landed yet
                refreshed.code == "launch_presets" -> { // adopted (reply op == success)
                    presetRefreshOp = ""
                    presetFetchDelivery = "" // a fetch that worked leaves no refusal behind it
                }
                else -> {
                    // ROUND 3 (review D3 MAJOR): the fetch verb's OWN copy -- a refused READ
                    // must not claim a launch was refused. ROUND 4 (review MEDIUM 3): written to
                    // the fetch verb's OWN slot, so the launch block below cannot overwrite it
                    // for the duration of a pending launch.
                    presetFetchDelivery = LaunchPresetScreen.fetchNoticeFor(
                        LaunchPresetScreen.noticeStateFor(refreshed.code),
                        refreshed.message,
                    )
                    presetRefreshOp = ""
                }
            }
        }
        if (presetOp.isNotEmpty()) {
            val outcome = try {
                bridge.launchOutcome(presetOp)
            } catch (unreadable: Exception) {
                null
            }
            if (outcome != null) {
                val state = LaunchPresetScreen.noticeStateFor(outcome.code)
                presetDelivery = LaunchPresetScreen.noticeFor(
                    state,
                    if (state == LaunchDeliveryNotice.REFUSED) outcome.message else "",
                )
                // ROUND 3 (review D3 MAJOR): the claim is ONE-SHOT once the outcome has
                // landed. `App.Outcome` is a persistent map, so a latched presetOp re-claimed
                // the resolved launch on EVERY pass and its stale sentence unconditionally
                // overwrote whatever the fetch verb had just written on this shared line --
                // a machine refusal of FETCH PRESETS was silently swallowed forever.
                if (state != LaunchDeliveryNotice.PENDING) presetOp = ""
            }
        }
        presetFlow = try {
            bridge.launchPresetFlow()
        } catch (unreadable: Exception) {
            null // a core that cannot answer draws no preset section rather than an invented one
        }
    }

    /**
     * A preset row was tapped (SELECT_PRESET): the ADR-007 D8 explicit confirm, as a sheet over
     * EXACTLY the tapped row. The five playbook facts plus the echoed revision come from
     * [LaunchPresetScreen.confirmationFor]; the prompt box is the phone's ONE free-text
     * contribution; CONFIRM_LAUNCH issues the signed session_launch AT THE DISPLAYED REVISION
     * (echo, never derive), and CANCEL_LAUNCH is a named control, not a gesture.
     */
    private fun confirmPresetLaunch(row: PresetRowModel) {
        val prompt = field(LaunchPresetScreen.PROMPT_HINT)
        val sheet = LaunchPresetScreen.confirmationFor(
            preset = row,
            machineName = presetFlow?.machineLabel.orEmpty(),
            worktreeIsolation = row.worktree,
            promptPresent = false, // presence is decided at confirm time from the box below
        )
        val facts = listOf(
            sheet.machineName,
            sheet.provider,
            sheet.workspacePath,
            sheet.worktreeBehavior,
            "Preset revision " + sheet.presetRevision,
        ).filter { it.isNotEmpty() }.joinToString("\n")
        val body = LinearLayout(activity).apply {
            orientation = LinearLayout.VERTICAL
            addView(notice(activity, facts).screenAir())
            addView(prompt.screenAir())
        }
        AlertDialog.Builder(activity)
            .setTitle(LaunchPresetScreen.HEADING)
            .setView(body)
            .setPositiveButton(LaunchPresetScreen.CONFIRM_LABEL) { _, _ ->
                press(launchHost) {
                    presetDelivery = ""
                    Press(
                        SendPlane.COMMAND,
                        verb = { app ->
                            app.sessionLaunch(row.id, row.revision, prompt.text.toString())
                        },
                        settle = { answer -> (answer as? Op)?.let { presetOp = it.operationID } },
                    )
                }
            }
            .setNegativeButton(LaunchPresetScreen.CANCEL_LABEL, null)
            .show()
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
        // Kill, which cannot in fact be on screen while this is false -- an open drill-down IS
        // the target, so the roster cannot be empty under one. It is here because it acts on the
        // chosen session, which is what this function is about. The composer's square is the
        // keyboard's ([setKeyboardEnabled]), since phone refit W3 made it the one control.
        kill.enable(enabled)
        // THE REPAIR AND THE ACKNOWLEDGEMENT ARE NOT IN THIS FUNCTION and the omission is
        // deliberate: neither acts on a session. `App.Resync` mends the transport's own read
        // position and `App.ClearUndeliveredInputs` clears a process-wide ledger, so gating them
        // on the roster having a row would disable exactly the two controls a phone with an empty
        // roster may need -- which is [launch]'s own argument one control over.
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
     * AND NOT TWO EITHER, WHICH IS WHERE `ctaAction` WENT (agents-tracker-ksvb.4). This file
     * carried a second factory beside it whose KDoc argued "TWO FACTORIES AND NOT ONE, and the
     * split is the SCREEN and not the verb": a control composed into a recomposed panel took the
     * shape the design gives that site, and "the four controls still sitting in the unrecomposed
     * remainder have no design source at all". Three of those four -- Stop, Kill and Send line --
     * are mapped now, to `.a2-no`, `.a2-no` and `.a2-ok`, so the split had nothing left on the
     * other side of it and a platform `Button` was the last thing keeping them apart. What the
     * merged factory kept from BOTH is the touch filter and the accessibility role; what it lost
     * is all-caps 14 sp Roboto Medium on the stock Material background, beside champagne CTAs.
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
        kind: CtaKind,
        ask: () -> String = { "" },
        plan: () -> Press?,
    ): TextView = pressable(ctaButton(activity, text, kind), ask, plan)

    /**
     * [actionButton]'s body, over a control the KIT chose rather than over a CTA.
     *
     * IT EXISTS BECAUSE THE DESIGN GAVE ONE VERB A DIFFERENT SHAPE. "Load earlier" is a PILL at the
     * head of the list in the drawing, not a full-width button in the reading path, and
     * `earlierChip` is what draws one -- but everything [actionButton] wraps a `ctaButton` in is
     * about the PRESS and not about the appearance: PB-SEC-12 clause 1's touch filter, the
     * confirmation, the lane, and the accessibility role a bare `TextView` cannot announce for
     * itself. Splitting the two is what stops a chip that reaches a facade verb quietly shipping
     * with none of it.
     *
     * AND OVER ANY VIEW, NOT ONLY A `TextView` (phone refit W3.2): the composer's square is an
     * `ImageView`, and nothing in this body reads text -- the delegate that announces `Button`
     * is a View's.
     */
    private fun <V : View> pressable(
        control: V,
        ask: () -> String = { "" },
        plan: () -> Press?,
    ): V = SecureWindow.gate(
        control.apply {
            // `pressed` AND NOT `control`: the outer parameter is this same view, and a lambda
            // parameter shadowing it would read as two things at the seam where one of them
            // decides what a press does.
            setOnClickListener { pressed -> confirmThenPress(pressed, ask(), plan) }
            // A `TextView` ANNOUNCES ITSELF AS TEXT. The kit records the gap and cannot close it --
            // it has no click to hang the role on (`CtaButton`'s own KDoc) -- so the role is set
            // where the click is.
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
     *  declared per press and not per control because the full-width Stop used to be on both:
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
        /**
         * What this press's REFUSAL changes on screen, back on the looper, beyond PB-APP-9's
         * routed line and row 1's toast -- which every press already gets and which are not
         * replaced here.
         *
         * IT IS THE MIRROR OF [settle] AND WAS MISSING (Mirror M2.4). A refusal used to be
         * uniform, so a control could only report success in its own words; the composer needs
         * both halves, because ADR-009 (6) makes the per-send state VISIBLE and `stale_turn` is
         * an ordinary outcome with its own gentle copy rather than a fault. The parameter is the
         * routed classification and not the message, so the decision stays in `ErrorRouter`'s
         * table instead of becoming a string match at a call site.
         */
        val refused: (RoutedError) -> Unit = {},
    )

    /**
     * Press [control], having planned what that means on the thread that owns the screen.
     *
     * @param plan runs HERE, on the looper: it reads the fields, applies the model's own
     *  refusals, and returns null for a press that resolves without reaching the wire -- a launch
     *  draft missing a required field, a Stop with no lease to send on. A null still redraws,
     *  because the refusal it just recorded is what the screen has to show.
     *
     * THE HAPTIC ANSWER IS GIVEN HERE AND THAT IS THE ONLY PLACE IT COULD BE (migration plan O6.2:
     * "fired locally on tap, never on server ack"). This function is exactly the seam where the
     * phone has decided what a press means and has not yet asked the machine anything: the plan
     * either produced a verb, which is `SENT`, or refused it on the handset, which is `FAILED`.
     * One line further in ([dispatchPress]'s settle) the answer belongs to the relay and can be
     * five seconds old -- feedback that late is not feedback, it is a second event -- and one line
     * back, in the click listener, the press has not been resolved yet, so a signal there would
     * report `SENT` for a launch form with an empty field.
     */
    private fun press(control: View, plan: () -> Press?) {
        when (val startup = runtime.phone()) {
            // The phone core would not build, so nothing was sent and nothing will be. It is the
            // same fact as a model refusal from the hand's point of view: the press stopped here.
            is PhoneStartup.Unavailable -> {
                outcome.text = startup.error.message
                Haptics.play(control.context, Haptics.Signal.FAILED)
            }
            is PhoneStartup.Ready -> {
                val app = startup.app
                val planned = plan()
                if (planned != null) {
                    Haptics.play(control.context, Haptics.Signal.SENT)
                    return dispatchPress(control, app, planned)
                }
                Haptics.play(control.context, Haptics.Signal.FAILED)
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
     * produced by a control on Activity, Settings or a session detail was written to a view the
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
                        val routed = FacadeBridge(app).routeFacadeError(it.message.orEmpty())
                        val refusal = PressFeedback.ofRefusal(routed)
                        // R1: this used to latch `swarm/no-lease` so the next draw could offer
                        // take control in place of a Stop that would earn the same refusal.
                        // There is no such control now, and the ops this app sends need no
                        // lease -- so the refusal is reported and nothing is latched.
                        say(refusal)
                        planned.refused(routed)
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
        // ROW 1'S SUFFIX IS THE MACHINE'S OWN CELL (agents-tracker-ksvb.10), and it is passed as
        // null rather than "" where there is none: `toastText` treats an empty suffix as absent
        // anyway, and a caller that spelled the absence as a blank string would still be appending
        // the mock's separator space to a sentence with nothing after it.
        if (!feedback.saysNothing) toasts.show(feedback.toast, feedback.detail.ifEmpty { null })
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
     * PB-DS-11: a heading takes a TEXT APPEARANCE, never a typeface. The same two lines were in all
     * three surface files, which is what "no visual constant may enter the app except through the
     * theme" is a fence against.
     *
     * ITS ONE CALLER IS [status], and the boolean is gone with the other branch
     * (agents-tracker-ksvb.4). `label(heading = false)` returned a bare `TextView` and its two
     * callers are [capabilityNotice] and [outcome], which are `§4 Notice line`s now -- so what is
     * left here
     * is a heading, and a factory with one shape does not need a flag to say which one.
     */
    private fun heading() = TextView(activity).apply {
        setTextAppearance(R.style.TextAppearance_Swarm_Title_Row)
        layoutParams = LinearLayout.LayoutParams(MATCH, WRAP)
    }

    /**
     * One line this surface says about its own state, from the kit (agents-tracker-ksvb.4).
     *
     * It is `noticeLine` and not `notice` because a property of that name -- which is what
     * [capabilityNotice] was called -- shadows the imported factory across the whole class, and the
     * compiler reports it as "Type checking has run into a recursive problem" on the declaration.
     */
    private fun noticeLine(kind: NoticeKind = NoticeKind.INFO) = notice(activity, "", kind)

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
     *
     * @param mono agents-tracker-ksvb.7: [typed] alone passes true -- it is the field that types
     *  under `Mono.Code` grid content, and the terminal is the one place a proportional face made
     *  a keystroke hard to compare against what comes back. The launch fields below never pass it.
     */
    private fun field(hint: String, mono: Boolean = false): EditText = textField(activity, hint, mono)

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
     * asserted by nothing. PB-DS-9 assigns copy to the screen: they are [SessionDetailScreen]'s and
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
         * ADR-014's page size for one "load earlier" (Mirror M3.1).
         *
         * IT IS A COUNT OF ITEMS AND NOT A SCREENFUL. The daemon rounds a page DOWN to an item
         * boundary, so what arrives is whole messages; what this number bounds is how much of the
         * machine's retained history one tap asks for. The facade refuses anything above its own
         * frame bound, so a larger number here would be a press that can only ever be refused.
         */
        const val HISTORY_PAGE = 50L

        /**
         * What an ACCEPTED M3 read replies with (review round 2).
         *
         * A read that worked answers with its own op name rather than `ok`: `handleInteraction*`
         * writes `Control{Op: OpInteractionHistory/OpInteractionDetail}` and the facade reports
         * the reply's op as the outcome code where there is no error code. This is
         * `renderPresetFlow`'s `"launch_presets"` arm, twice, and it is spelled here rather than
         * inline so the two reads cannot drift apart from each other.
         */
        const val READ_HISTORY_OK = "interaction_history"

        const val READ_DETAIL_OK = "interaction_detail"

        /**
         * The backfill instant for a session this surface has never backfilled.
         *
         * IT IS NOT ZERO. `SessionDetailOpen.plan` compares `now - last >= throttle` on a
         * MONOTONIC clock whose zero is boot, so a session opened in the first thirty seconds of
         * an uptime would read as "backfilled at boot, still inside the window" and would show an
         * empty conversation with no read issued. A large negative instant is outside every
         * window by construction.
         */
        const val NEVER_BACKFILLED = Long.MIN_VALUE / 2

        /**
         * The single-flight key prefix for a session's cold-open backfill, per session.
         *
         * PER SESSION AND NOT GLOBAL: two sessions opened in quick succession are two different
         * reads and neither should drop the other, which is what one shared key would do.
         */
        const val BACKFILL_KEY = "backfill:"

        /**
         * What the screen says when a tapped block names an item the conversation no longer holds
         * (Mirror M3's landing).
         *
         * IT IS SAID RATHER THAN SWALLOWED. The tap used to `return` in silence when the id did
         * not match the card on screen -- which is exactly what a resolved approval looks like
         * (IS-LIFE-2) and exactly what a retention-trimmed one looks like -- so the reader pressed
         * a row and the screen did nothing at all.
         */
        const val ANCHOR_NOT_RETAINED =
            "That message is no longer in the history this phone is holding."

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

        /**
         * Add computer's single-flight key on the COMMAND lane (round 3).
         *
         * A CONSTANT AND NOT THE MACHINE ID: what must not run twice is the stop/add/start
         * SEQUENCE, whatever pairing it is registering, because it is the disconnect and the
         * abandoned input that a second run repeats -- keying on the typed id would let two
         * different ids sever the leases twice.
         */
        const val ADD_MACHINE_KEY = "machines.add"
    }
}

/**
 * The app host: whichever top-level composition the window is currently holding.
 *
 * INTERNAL AND OUTSIDE THE COMPANION, because the one caller beyond this file is a test in the
 * same module and the companion is private. It is a TAG rather than an id for `ScaffoldTag`'s own
 * reason: this app allocates no view ids, and a tag is what every other part of this window is
 * found by.
 */
internal const val PHONE_APP_HOST = "phone.app.host"
