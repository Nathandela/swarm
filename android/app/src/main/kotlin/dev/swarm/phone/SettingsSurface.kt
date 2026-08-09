package dev.swarm.phone

import android.app.Activity
import android.app.NotificationManager
import android.content.ActivityNotFoundException
import android.content.Intent
import android.content.pm.PackageManager
import android.os.Build
import android.provider.Settings
import android.view.View
import android.view.ViewGroup
import android.view.accessibility.AccessibilityNodeInfo
import android.widget.Button
import android.widget.CompoundButton
import android.widget.LinearLayout
import android.widget.TextView
import androidx.appcompat.app.AlertDialog
import androidx.appcompat.widget.SwitchCompat
import androidx.core.app.NotificationManagerCompat
import dev.swarm.phone.push.PushTokens
import dev.swarm.phone.push.WakeNotifications
import dev.swarm.phone.runtime.AppPermission
import dev.swarm.phone.runtime.NotificationDelivery
import dev.swarm.phone.runtime.NotificationDeliveryResolver
import dev.swarm.phone.runtime.PermissionAsks
import dev.swarm.phone.runtime.PermissionState
import dev.swarm.phone.runtime.PermissionStateResolver
import dev.swarm.phone.ui.CommandVerdict
import dev.swarm.phone.ui.FacadeBridge
import dev.swarm.phone.ui.MachineLabel
import dev.swarm.phone.ui.PressFeedback
import dev.swarm.phone.ui.PushCategory
import dev.swarm.phone.ui.PushSync
import dev.swarm.phone.ui.PushToggle
import dev.swarm.phone.ui.SettingsScreen
import dev.swarm.phone.ui.kit.CtaKind
import dev.swarm.phone.ui.kit.NoticeKind
import dev.swarm.phone.ui.kit.ToastHost
import dev.swarm.phone.ui.kit.ctaButton
import dev.swarm.phone.ui.kit.denyChip
import dev.swarm.phone.ui.kit.notice
import dev.swarm.phone.ui.screens.ConnectionSection
import dev.swarm.phone.ui.screens.PairOnlyScreen
import dev.swarm.phone.ui.screens.PairedMachineRow
import dev.swarm.phone.ui.screens.SettingsPanel
import dev.swarm.phone.ui.screens.SettingsPanelScreen
import dev.swarm.phone.ui.screens.SettingsRow
import dev.swarm.phone.ui.screens.settingsPanelView
import swarmmobile.App
import swarmmobile.Op
import swarmmobile.PushPreference

/**
 * Phase B slice S19 -- PB-APP-7's two switches, and the caller PB-PUSH-9's "deletion on
 * revoke/DISABLE" did not have.
 *
 * WHY IT IS HERE AND NOT ONLY IN [PushTokens]. `PushTokens.disable` shipped in S17 with a
 * comment saying, correctly, that it had no caller and that the settings screen was where it
 * belonged -- and the settings screen was a data class with no Activity to host it. So the
 * DISABLE half of the requirement was a method nothing called, which is the exact shape
 * PB-PUSH-9 warns about in its own text ("a facade method can exist while no Android code ever
 * calls it"). Revoke was never affected: `App.RevokeThisDevice` deletes the token in Go.
 *
 * WHAT COUNTS AS "DISABLE" IS DERIVED, NOT INVENTED. [SettingsScreen] models two categories and
 * no master switch, so a third control would be a preference the shipped model has no field for
 * and the machine has never heard of. Both categories off IS the disable: the FCM token exists
 * to deliver those two things, so with both off it is a provider-visible identifier for a phone
 * that has asked for nothing. Turning either back on re-registers, because
 * `PushTokens.requestInitialToken` is the same call a fresh launch makes and a deleted token is
 * not re-issued by itself.
 *
 * THE ORDER IS THE GO VERB'S AND IT MATTERS. `App.DeletePushToken` clears durable state BEFORE
 * it speaks to the relay, and durable state is what the reconnect path reconciles the relay to
 * -- so a switch flipped while backgrounded (the normal state under ADR-007 B16) is delivered on
 * the next authenticated reconnect rather than lost. Nothing here retries anything.
 *
 * THERE ARE TWO SWITCHES AND THERE IS NO THIRD (PB-APP-7, narrowed by ADR-007 B133). The screen
 * used to model a biometric-gate preference alongside them, never rendered, passed as `false` at
 * the one call site that read it. Its subject has left the product, so the field, its setter and
 * the freshness table it fed are gone rather than left as a preference over nothing.
 *
 * IT ALSO OWNS THE REVOKE NOW (agents-tracker-64rf). `Revoke this device` was a `PhoneSurface`
 * button in the unrecomposed remainder below the inbox -- the same burial the pairing entry point
 * was found in, on the same screen, for the same reason -- and it is the same action the paired
 * machine row offers as `Replace this computer`: swarm v1 is single-device, so replacing IS
 * revoke-then-pair. The control lives here because this is the surface whose screen shows it, and
 * because a control's touch filter, its facade verb and its in-flight identity cannot be split
 * from each other.
 */
class SettingsSurface(
    private val activity: Activity,
    private val runtime: PhoneRuntime,
    /**
     * Where the revoke behind [replace] runs, which is never the thread that drew it.
     *
     * IT IS A PARAMETER SO THE SCREEN'S DISPATCH CAN BE SHARED. `revokeThisDevice` resolves
     * through `sendContext`, whose `awaitConn` polls for up to five seconds
     * (android/gate/s25_mainthread_test.go derives that from the Go side rather than being told
     * it), so the call cannot run on the looper. The default is its own instance over the
     * process-wide lanes and is correct on every axis but one: `attach`/`detach` are the pair that
     * stops a command settling onto a screen nobody is holding, and only `PhoneSurface` knows when
     * that happens. Handed this surface its dispatch, the answer to a revoke is dropped with every
     * other one.
     */
    private val dispatch: VerbDispatch = VerbDispatch.background(),
) {

    /**
     * The error line, which is NOT part of inventory C6 and is hosted under the panel rather than
     * inside it. C6 draws sections of rows; a routed facade refusal is the same class of thing as
     * `PhoneSurface`'s outcome line and belongs on the same seam.
     *
     * IT IS `§4 Notice line`'S ERROR VARIANT NOW (agents-tracker-ksvb.4). It was built by a local
     * `label()` whose KDoc said "IT CARRIES NO TEXT APPEARANCE, and that is a statement about what
     * is missing rather than a regression ... it renders at the theme's default until there is
     * [a factory]". The theme's default for a `TextView` is the PLATFORM's ~14 sp, above every body
     * style in this app's ladder, so the one line on the settings screen that reports a failure was
     * also the largest line on it -- which reads as emphasis nobody chose. `label()` had no other
     * caller and is gone with it.
     *
     * ERROR AND NOT INFO, because every value this holds is a verdict: `startup.error.message`, and
     * `PressFeedback.line`, which `ofSuccess` and `ofUnsent` both leave empty on purpose.
     */
    private val outcome = notice(activity, "", NoticeKind.ERROR)

    private val needsInput = touchFilteredSwitch(PushToggle.FIRST)
    private val finished = touchFilteredSwitch(PushToggle.SECOND)

    /**
     * agents-tracker-64rf's `Replace this computer`, which IS the revoke.
     *
     * IT IS ONE VERB AND NOT TWO. swarm v1 is single-device -- `internal/skeleton/pairing.go`'s
     * `BeginPairing` refuses a second pairing while a device is registered and names
     * revoke-then-pair as the remedy -- so replacing is `App.RevokeThisDevice` and then the phone
     * simply is unpaired, which is the state that already has a screen of its own with one offer on
     * it. There is nothing to navigate to and no second step to build.
     *
     * IT IS BUILT HERE AND NOT BY THE SCREEN, for the three reasons the toggles are: PB-SEC-12
     * clause 1's touch filter is applied at CONSTRUCTION and a control rebuilt on every draw would
     * be a different view from the one [touchFilteredActions] names; the verb crosses to Go; and
     * `VerbDispatch` marks the control itself while a press is in flight, which needs an identity
     * that outlives the redraw.
     *
     * IT CARRIES NO WORDS AT CONSTRUCTION. Its label is [PairedMachineRow.replaceLabel] and it is
     * written on every draw by [replaceFor] -- a string typed here would be the second copy of copy
     * the model already owns, which is the drift PB-DS-9 assigns copy to one place to prevent.
     *
     * AND ITS PRESS ASKS RATHER THAN REVOKES (agents-tracker-mrq5). It called [onReplace] straight
     * from this listener, so one tap on a chip in a row's trailing slot ended the pairing and
     * destroyed both key tiers, on a screen people open to change a notification toggle. What it
     * runs now is [confirmThenReplace].
     */
    private val replace: TextView = actionChip().apply {
        setOnClickListener { confirmThenReplace() }
    }

    /**
     * The view that takes the CONFIRMING tap (agents-tracker-mrq5).
     *
     * IT IS A CONTROL THIS SURFACE BUILDS, AND THAT IS THE WHOLE REASON IT EXISTS RATHER THAN
     * `AlertDialog`'s OWN POSITIVE BUTTON. `PhoneSurface.confirmThenPress` records the gap in its
     * own KDoc: the tap that OPENS a platform dialog is filtered, and the dialog's buttons live in
     * a window that surface does not own, so `filterTouchesWhenObscured` -- a property of a View
     * instance -- reaches none of them. A confirmation built out of them would move the destructive
     * tap from a defended view to an undefended one, which is the overlay defence being spent on
     * the harmless press. Since ADR-007 B133 there is nothing else behind revoke.
     *
     * SO IT IS BUILT AT CONSTRUCTION, gated once, and named in [touchFilteredActions] -- the same
     * three reasons [replace] and the switches are, and the dialog is handed this instance rather
     * than a chip made for it.
     *
     * INTERNAL, so `SettingsSurfaceReplaceTest` has a subject. That test is the runtime half of the
     * join: the source fence in android/gate/mrq5_replaceconfirm_test.go says this view is the one
     * the dialog receives, and the Kotlin test says this view carries the filter.
     *
     * ITS LISTENER IS THE ONE THING [confirmThenReplace] WRITES RATHER THAN THIS DECLARATION, so
     * that the question, the control that answers it and what answering does are one function a
     * reader can check against itself. `setOnClickListener` replaces, so nothing stacks -- and what
     * has to survive the redraw is the view, which does.
     */
    internal val confirmReplace: TextView = actionChip()

    /**
     * agents-tracker-0dij's way out of a PERMANENTLY_DENIED notification permission.
     *
     * IT IS THE ONE STATE WITH NOTHING ELSE TO OFFER. On DENIED the platform still raises its
     * dialog and the switch's own tap is what asks; on PERMANENTLY_DENIED it will not ask again,
     * ever, and until this control existed the screen said "turn them on in system settings" with
     * no way to get there -- advice, on a screen whose two switches were dead.
     *
     * IT GOES TO THE NOTIFICATION SCREEN AND NOT THE APP INFO PAGE.
     * `ACTION_APPLICATION_DETAILS_SETTINGS` is what the pairing screen sends the camera to, because
     * the camera permission has no screen of its own; notifications do, and
     * `EXTRA_APP_PACKAGE` is what names this app on it. The difference is a control that fixes the
     * problem against one that puts the user somewhere the problem can be fixed.
     *
     * IT IS BUILT HERE, gated once, and named in [touchFilteredActions] -- the same three reasons
     * [replace] and the switches are. INTERNAL, so `SettingsSurfaceNotificationsTest` has a subject:
     * the intent behind it is the one thing in this whole flow a JVM test can execute, because it
     * reaches the platform rather than the phone core.
     *
     * ITS WORDS ARE THE MODEL'S and are written on every draw by [redirectFor], like the replace
     * chip's: a string typed here would be a second copy of copy [SettingsScreen] already owns.
     */
    internal val openNotificationSettings: TextView = SecureWindow.gate(
        ctaButton(activity, "", CtaKind.MORE).apply {
            announceAsButton()
            setOnClickListener {
                leaveFor(
                    Intent(Settings.ACTION_APP_NOTIFICATION_SETTINGS)
                        .putExtra(Settings.EXTRA_APP_PACKAGE, activity.packageName),
                )
            }
        },
    )

    /**
     * agents-tracker-2yfn's way out of a blocked wake CHANNEL.
     *
     * IT IS A SECOND CONTROL BESIDE [openNotificationSettings] AND NOT THE SAME ONE RE-LABELLED,
     * because the two open different system screens and the Intent is fixed in a listener installed
     * at construction -- which is what makes both of them the same view every draw, and what
     * PB-SEC-12 clause 1's filter is applied to. The other leads to this app's notification LIST, on
     * which the wake category is one row among however many; this leads to that row's own page,
     * which is the only place a blocked category can be turned back on.
     *
     * `EXTRA_CHANNEL_ID` IS NOT OPTIONAL. `ACTION_CHANNEL_NOTIFICATION_SETTINGS` needs both extras:
     * the package names the app, and the channel id names the page. Without the second the intent
     * has nothing to show and resolves to nothing at all.
     *
     * Built here, gated once, named in [touchFilteredActions], INTERNAL so
     * `SettingsSurfaceNotificationsTest` has a subject, and carrying no words at construction -- the
     * same five decisions [openNotificationSettings] records, for the same reasons.
     */
    internal val openChannelSettings: TextView = SecureWindow.gate(
        ctaButton(activity, "", CtaKind.MORE).apply {
            announceAsButton()
            setOnClickListener {
                leaveFor(
                    Intent(Settings.ACTION_CHANNEL_NOTIFICATION_SETTINGS)
                        .putExtra(Settings.EXTRA_APP_PACKAGE, activity.packageName)
                        .putExtra(Settings.EXTRA_CHANNEL_ID, WakeNotifications.CHANNEL_ID),
                )
            }
        },
    )

    /** What the panel last drew, so a redraw that changes nothing rebuilds nothing. */
    private var drawn: SettingsPanel? = null

    /**
     * What the screen last drew. Held so a switch press has something to derive the next value
     * from -- [SettingsScreen.setAlerts] returns a NEW screen with `pendingSync` raised, which
     * is the whole point of the model: a toggle flipped offline is a local value the machine has
     * not acknowledged, and showing it as settled would tell the user notifications are off
     * while they keep arriving.
     */
    private var screen: SettingsScreen? = null

    /** The `push_prefs` operation the machine has not answered yet, if any (PB-SYNC-2). */
    private var pendingOp: String? = null

    /** True while a redraw is writing the switches, so the listener does not re-issue a command. */
    private var drawing = false

    /**
     * The panel is rebuilt into this whenever what it shows changes.
     *
     * IT WAS A FLAT COLUMN OF SIX UNSTYLED VIEWS -- a bold "Notifications" heading, two bare
     * switches and three loose lines of text -- built once in the constructor and mutated in
     * place. PB-DS-9 replaces that with the screen inventory C6 records, composed from the kit by
     * [settingsPanelView]; what this holds now is whatever that composition currently is.
     */
    private val host = LinearLayout(activity).apply {
        orientation = LinearLayout.VERTICAL
        layoutParams = ViewGroup.LayoutParams(MATCH, WRAP)
    }

    val root: View = host

    /**
     * PB-SEC-12 clause 1: a switch that stops notifications is worth an overlay, and so is a chip
     * that revokes this device. [replace] is the one authorising control on this screen and, with
     * ADR-007 B133's removal of phone-side authentication, the filter is the only thing left
     * standing between it and a tap the user could not see.
     *
     * IT NAMES TWO CHIPS AND NOT ONE (agents-tracker-mrq5). [replace] opens the question and
     * [confirmReplace] answers it, and the second is the one that reaches the verb -- so a list
     * holding only the first would be filtering the harmless press. `PhoneSurface.confirmThenPress`
     * records why a platform dialog cannot close this gap: its buttons are in a window this surface
     * does not own, and the filter is a property of a View in one.
     *
     * AND THE REDIRECT IS THE FOURTH (agents-tracker-0dij). It authorises nothing, but it LEAVES
     * THE APP -- an overlay that swapped the tap under it would be launching an Activity the user
     * did not choose -- and the list is what `PhoneActivity.touchFilteredViews()` publishes for
     * `PhoneActivityWindowTest` to walk. A control outside it is one no fence in this module has
     * ever looked at.
     */
    val touchFilteredActions: List<View> =
        listOf(
            needsInput,
            finished,
            replace,
            confirmReplace,
            openNotificationSettings,
            // AND THE CHANNEL REDIRECT IS THE FIFTH (agents-tracker-2yfn), for the fourth's reason
            // exactly: it authorises nothing and it LEAVES THE APP.
            openChannelSettings,
        )

    /**
     * Called once a `Replace this computer` press has settled, so the window that hosts this panel
     * can redraw itself (agents-tracker-2lz5).
     *
     * WHY THE PANEL CANNOT JUST REDRAW. The press changes the answer to a question asked one level
     * up: `PhoneSurface.renderReady` decides whether this handset is shown the app at all, and a
     * revoke makes that answer PAIR_ONLY. [render] is the settings panel and nothing above it, so
     * a settle that ended there left the screen in a four-tab scaffold whose gate was never
     * re-asked -- and the next redraw would come from a journal event, which is exactly what the
     * revoke just stopped arriving.
     *
     * IT IS A CALLBACK RATHER THAN A CALL INTO THE SURFACE, which is the same shape the mirror
     * event already uses: `PhoneSurface` assigns `pairing.onPaired = ::render` so that a pairing
     * SUCCESS swaps the window at the moment it happens. This is the other direction of the same
     * transition, and the surface owns both wirings -- a panel reaching up into its host would be
     * the coupling that taking [dispatch] as a parameter exists to avoid.
     *
     * NULL IS A PANEL NOBODY HOSTS, which is the default instance's true condition rather than an
     * error to report: it is never attached, so there is no window to redraw.
     */
    var onReplaced: (() -> Unit)? = null

    /**
     * Where this panel's answers are SAID, which is a view it does not own (derivation row 1).
     *
     * IT IS THE HOST'S AND NOT THIS PANEL'S, for the same reason [onReplaced] is a callback: this
     * screen is drawn INSIDE the tab scaffold, so a toast built here would be laid out under the
     * tab bar and would go with the panel on the redraw a revoke causes. `PhoneSurface` hangs one
     * overlay beside the app and hands it to whoever needs it.
     *
     * NULL IS A PANEL NOBODY HOSTS -- the default instance's true condition, as [onReplaced]
     * records. Its refusals still reach [outcome]; what they lose is the toast, because there is
     * no window to float one over.
     */
    internal var toasts: ToastHost? = null

    /**
     * What the revoke left this phone unable to confirm, or empty where it left nothing
     * (agents-tracker-qlf9).
     *
     * IT IS A PROPERTY THIS PANEL WRITES AND THE HOST READS, rather than a second callback beside
     * [onReplaced]. The revoke ENDS this panel: a revoked phone is an unpaired phone and
     * `PhoneSurface.drawPairOnly` replaces the whole scaffold, so [outcome] and this surface's
     * toast host both leave the screen with it -- and the sentence they would have carried is the
     * one a user needs LATER, at the pairing that `swarm remote pair` may refuse. So the host
     * takes it and puts it on the screen the user actually lands on, and clears it when the app
     * comes back.
     *
     * IT IS NOT WHAT THE VERB THREW. A revoke can fail in two ways -- refused by the machine, or
     * never sent at all -- and both leave this handset purged while the registration stands. Both
     * are composed by [dev.swarm.phone.ui.screens.PairOnlyScreen], which owns the copy.
     */
    internal var unpairNotice: String = ""

    /**
     * The sync pill for this panel's nav row, or null while nothing owns one
     * (agents-tracker-nx44.2).
     *
     * IT IS A PROVIDER AND NOT A VIEW. `PhoneSurface` owns ONE pill host across three nav rows --
     * exactly one destination is on screen at a time -- and the provider is what detaches it from
     * whichever screen held it last. A field holding the view would hand this panel a child that
     * still has a parent, which Android refuses.
     *
     * IT IS ALSO WHY [draw]'S EQUALITY CHECK DOES NOT HAVE TO KNOW ABOUT THE PILL: the host is
     * stable and its CONTENTS change, so a status that moves while this panel is unchanged reaches
     * the screen without redrawing a column of switches under the finger on one.
     */
    internal var statusSlot: () -> View? = { null }

    fun render() {
        when (val startup = runtime.phone()) {
            is PhoneStartup.Unavailable -> {
                // agents-tracker-j171: THE ROUTED FAILURE, rather than an empty settings screen. A
                // panel of switches over a phone that could not start would offer preferences
                // nothing can carry to a machine -- but `PhoneSurface`'s own copy of this sentence
                // only reaches the Inbox tab (`status` is a child of `unrecomposedControls`, hosted
                // under the inbox's sections and detached on every other destination), so a phone
                // whose core refused construction showed this tab completely blank whenever the
                // user had navigated here.
                screen = null
                drawn = null
                detachControls()
                host.removeAllViews()
                outcome.text = startup.error.message
                outcome.visibility = View.VISIBLE
                host.addView(outcome)
            }

            is PhoneStartup.Ready ->
                draw(read(startup.app), machineOf(startup.app), connectionOf(startup.app))
        }
    }

    /**
     * What the screen shows now.
     *
     * A CHANGE THE MACHINE HAS NOT ACKNOWLEDGED IS HELD, not re-read. `PushPreference` answers
     * what is PERSISTED LOCALLY, which a `setPushPreference` updates immediately, so re-reading
     * it would clear [SettingsScreen.pendingSync] on the very next draw and show an unconfirmed
     * change as settled. The acknowledgement is a separate fact and it is claimed BY OPERATION
     * ID (PB-SYNC-2), never by proximity.
     */
    private fun read(app: App): SettingsScreen {
        val bridge = FacadeBridge(app)
        val held = screen
        val base = if (held != null && claimable(held, pendingOp)) {
            settleWithTheMachine(bridge, held)
        } else {
            settingsOr(held) { bridge.pushSettings() }
        }
        return base
            .withNotificationPermission(notificationPermissionNow())
            .withNotificationDelivery(deliveryNow())
    }

    /**
     * Whether the machine's answer to what this panel is holding is still one it can claim
     * (agents-tracker-n9w7).
     *
     * IT IS THE WHOLE QUESTION AND [read] USED TO ASK HALF OF IT. `pendingSync` says a change is
     * unacknowledged; the rest says whether anyone can still bring an answer for it -- either
     * because the verb is still crossing, or because [pendingOp] holds the id that could claim
     * one, and PB-SYNC-2 claims outcomes BY ID and never by proximity.
     *
     * THE CROSSING HALF IS NOT OPTIONAL AND ITS ABSENCE WAS A BLOCKER (agents-tracker-k6w2). This
     * asked `pendingSync && pendingOp != null`, and [pendingOp] is assigned in the SETTLE while
     * `pendingSync` is raised by [onToggled]'s draw BEFORE the verb leaves -- so for the whole
     * round trip the pair was (raised, null). `PhoneSurface.render` calls this surface's render
     * unconditionally and runs on every journal event, so a single event mid-flight answered "no"
     * and sent the draw to the durable re-read: the pending notice vanished while the change was
     * unconfirmed, both switches came back live -- agents-tracker-ix2v undone by its own sibling
     * -- and because the re-read screen's `pendingSync` is false, the machine's later refusal
     * could never be claimed at all (agents-tracker-os37, resurrected), while a stale [pendingOp]
     * left behind could be claimed by a LATER flip's outcome.
     *
     * `VerbDispatch.inFlight` IS THE RIGHT FACT AND NOT MERELY AN EARLIER ONE. It is true from
     * press-issue time, and it is DROP-AWARE: `press` frees the control BEFORE its `if (attached)`
     * check, so a settle a pause threw away still clears the mark. That is what lets one predicate
     * answer both this issue and [n9w7]'s -- crossing means hold, and cleared with no operation
     * means the answer is gone and the durable value is the truth.
     *
     * THE HALF THAT WAS MISSING IS REACHED BY BACKGROUNDING THE APP. [onToggled] draws the wanted
     * screen before the write leaves -- that is what raises `pendingSync` while the machine is
     * being asked -- and assigns [pendingOp] in the settle. `VerbDispatch` DROPS a settle whose
     * dispatch has been detached, which `PhoneSurface.release` does on every pause and argues for
     * in its own comment: an answer routinely outlives the screen it was meant for. So a flip
     * followed by a pause inside the command's round trip left `pendingSync` raised with
     * [pendingOp] null, [settleWithTheMachine] returning early for ever, the pending notice
     * permanent -- and, since agents-tracker-ix2v holds both switches while it stands, a settings
     * screen with two dead controls and no way back short of process death.
     *
     * WHAT REPLACES THE WAIT IS THE DURABLE VALUE, which is why this is safe to give up on.
     * `App.SetPushPreference` persists BEFORE it sends, so the facade holds what the user chose if
     * the command was issued at all and what the machine last confirmed if it never was. Either
     * way it is the truth this phone can still see, and it is exactly what a fresh process would
     * draw -- the same degradation `PhoneRuntime`'s revoke latch already accepts for a process
     * that died mid-command.
     *
     * INTERNAL so `SettingsSurfaceReadTest` has a subject: [read] is unreachable on the JVM, and a
     * guard nothing can execute is a guard nobody has checked.
     */
    internal fun claimable(held: SettingsScreen, operation: String?): Boolean =
        held.pendingSync && (dispatch.inFlight(host) || operation != null)

    /**
     * The preference the facade holds, or the last screen this panel drew where it cannot be read
     * (agents-tracker-doza).
     *
     * IT IS THE SAME COMPOSITION [machineOf] ALREADY MAKES, on the call beside it. This one was
     * unguarded: [read] runs from [render], which runs from `PhoneActivity.onResume` and from
     * every journal event, so a facade that refuses -- a core that has been closed, a state blob
     * that will not decode -- was an uncaught exception on the looper. The app died on a screen
     * the user merely opened, with no tap anywhere in it.
     *
     * THE FALLBACK IS THE LAST SCREEN AND NOT A DEFAULT, which is [SettingsScreen]'s own rule
     * ("it renders what was PERSISTED, never a default") applied to the error path: switches that
     * flipped themselves off because a read failed would silently re-enable nothing and silently
     * disable everything, which is the process-death defect that rule exists to prevent. With
     * nothing held -- the first draw after a process death -- the honest answer is that no
     * preference is known, which is the empty screen rather than an invented one.
     *
     * IT SAYS NOTHING, unlike every other refusal on this surface, and that is deliberate: this
     * runs on EVERY render, so a toast here would fire repeatedly for one condition, over a panel
     * that is still showing the user's settings correctly.
     *
     * INTERNAL AND TAKING THE READ AS A LAMBDA so `SettingsSurfaceReadTest` has a subject:
     * `PhoneRuntime.phone()` answers Unavailable on every JVM run, so [read] itself is never
     * entered there and a guard nothing can execute is a guard nobody has checked.
     */
    internal fun settingsOr(held: SettingsScreen?, read: () -> SettingsScreen): SettingsScreen =
        try {
            read()
        } catch (unreadable: Exception) {
            held ?: SettingsScreen(alerts = false, mentions = false)
        }

    /**
     * PB-RUN-2's state for POST_NOTIFICATIONS, asked of the platform on every draw -- and the one
     * place the persisted ask bit is corrected (agents-tracker-qyb3).
     *
     * THE BIT USED TO BE THE LITERAL `true` (agents-tracker-0dij). The comment beside it claimed
     * "the permission is requested on the notification path" and no such path existed -- the
     * app's only `requestPermissions` call was the camera's -- so on API 33+ every ungranted
     * phone resolved PERMANENTLY_DENIED five seconds after install: both switches disabled, and a
     * notice sending the owner to system settings where nothing was wrong. The ask is
     * [onToggled]'s now, and the bit is what tells a first run from a permanent refusal.
     *
     * AND THE BIT IS CLEARED ON A GRANT, which is the half agents-tracker-qyb3 adds.
     * `PermissionAsks.remember` was write-once, and `shouldShowRequestPermissionRationale` is
     * false after a grant exactly as it is after a permanent refusal -- so a phone that granted
     * the permission and later revoked it in system settings resolved PERMANENTLY_DENIED for the
     * life of the install: two dead switches and a redirect, over a platform that would still
     * have prompted. This is the one moment the app KNOWS, so it is where the record of the ask
     * is retired. It is not cleared on any other state: a denial is precisely what the bit was
     * written for.
     *
     * IT IS SPLIT OUT OF [read] FOR [deliveryNow]'S REASON, which is that the GATHER is what goes
     * wrong. A resolver handed a constant decides nothing and every unit test over it stays
     * green, which is 0dij's whole history; [read] cannot carry the assertion because
     * `PhoneRuntime.phone()` answers Unavailable on every JVM run, and this touches only the
     * Activity and the persisted bit, both of which Robolectric models.
     */
    internal fun notificationPermissionNow(): PermissionState {
        val state = PermissionStateResolver.resolve(
            permission = AppPermission.POST_NOTIFICATIONS,
            sdkInt = Build.VERSION.SDK_INT,
            granted = activity.checkSelfPermission(
                AppPermission.POST_NOTIFICATIONS.manifestName,
            ) == PackageManager.PERMISSION_GRANTED,
            hasAskedBefore = PermissionAsks.hasAsked(activity, AppPermission.POST_NOTIFICATIONS),
            showRationale = activity.shouldShowRequestPermissionRationale(
                AppPermission.POST_NOTIFICATIONS.manifestName,
            ),
        )
        if (state == PermissionState.GRANTED) {
            PermissionAsks.forget(activity, AppPermission.POST_NOTIFICATIONS)
        }
        return state
    }

    /**
     * Whether the framework will actually show a wake, asked of the platform on every draw
     * (agents-tracker-2yfn).
     *
     * THE PERMISSION CHECK ABOVE CANNOT SEE THIS. A user who long-presses a wake and blocks
     * `Agent updates` leaves POST_NOTIFICATIONS GRANTED -- blocking a channel does not revoke a
     * permission -- so `checkSelfPermission` goes on answering GRANTED while every wake is dropped
     * before this app is started. Push is the sole path to a backgrounded phone (ADR-007 B16), so
     * without this call nothing in the product would ever say so.
     *
     * IT IS SPLIT OUT OF [read] BECAUSE THE GATHER IS WHAT GOES WRONG. agents-tracker-0dij was a
     * correct resolver handed a LITERAL (`hasAskedBefore = true`), green across every unit test
     * while the app resolved every fresh install to PERMANENTLY_DENIED -- so the two arguments below
     * have an assertion of their own in `SettingsSurfaceNotificationsTest`. [read] cannot carry it:
     * `PhoneRuntime.phone()` answers Unavailable on every JVM run, so [read] is never entered there,
     * while this touches only NotificationManager, which Robolectric models.
     *
     * NULL IS A CHANNEL THAT DOES NOT EXIST and is passed on as such rather than defaulted. See
     * [NotificationDeliveryResolver.resolve]: it is not a block, and reading it as one would report
     * a fault on a phone where nothing is wrong.
     *
     * RE-ASKED EVERY DRAW, so `PhoneActivity.onResume` is the whole of the re-check after a trip to
     * the system settings -- the same free ride the permission resolve takes, and the reason neither
     * needs a callback.
     */
    internal fun deliveryNow(): NotificationDelivery = NotificationDeliveryResolver.resolve(
        notificationsEnabled = NotificationManagerCompat.from(activity).areNotificationsEnabled(),
        channelImportance = activity.getSystemService(NotificationManager::class.java)
            .getNotificationChannel(WakeNotifications.CHANNEL_ID)
            ?.importance,
    )

    /**
     * The machine this phone is pinned to, or null when it is pinned to none.
     *
     * NULL IS ALSO WHAT AN UNREADABLE STATE ANSWERS, and that is the same composition
     * `PairingSurface` already makes rather than a disagreement with it: there, `isPinned` answers
     * false when `stateSummary` throws, so an unreadable state is not a pairing and `machineOf`'s
     * `""` never reaches the screen. Here the two questions are one call, and the honest answer to
     * "is there a pairing to offer a replace control over" when nothing can be read is no.
     *
     * EMPTY IS NOT NULL AND IS NOT SYNTHESISED HERE. A pairing whose name this phone cannot read
     * is [PairedMachineRowScreen]'s `Paired`; a machine with no name is not a machine.
     *
     * A REVOKED PAIRING IS NO PAIRING (agents-tracker-d0b8). The pinned machine survives a revoke
     * by design -- `phonecore` filters the durable blob on it -- so the name alone would go on
     * offering a `Replace this computer` control over a registration that has already ended.
     */
    private fun machineOf(app: App): String? = endpointOf(app)?.let { id ->
        // THE MACHINE'S OWN NAME WHERE IT PUBLISHED ONE (agents-tracker-ksvb.1). This row and
        // the destructive `Replace <machine>?` question under it are the two places a person
        // is asked to recognise their computer, and `ep-` plus four bytes of a hash is not
        // something anyone recognises. `MachineLabel.of` keeps the id as the fallback, so a
        // machine that published no hostname reads exactly as it did before.
        //
        // WHAT IS PINNED IS STILL THE ID. The endpoint id is what decides whether there IS a
        // pairing to offer a replace over; the name only changes the word. A name read where the
        // id was empty would put a Replace control over no pairing at all.
        MachineLabel.of(machineNameOf(app), id)
    }

    /**
     * The endpoint id this phone is pinned to, or null when it is pinned to none.
     *
     * EXTRACTED FROM [machineOf] BY agents-tracker-nx44.3 rather than copied: the CONNECTION
     * section needs the ID as well as the label -- derivation row 11 gives the machine two cells,
     * a name a person chose and an id the software derived -- and two `stateSummary` reads deciding
     * independently whether there is a pairing at all is how one of them ends up drawing a section
     * about a machine the other says is not there. The guard is unchanged and so is its reason: an
     * unreadable state is not a pairing, which is the same composition `PairingSurface.isPinned`
     * already makes.
     */
    private fun endpointOf(app: App): String? = try {
        app.stateSummary().takeIf { it.paired }?.machine?.takeIf { it.isNotEmpty() }
    } catch (unreadable: Exception) {
        null
    }

    /**
     * The CONNECTION section (agents-tracker-nx44.3), or null on a phone with nothing to say about
     * a link -- no pairing, or a state this phone cannot read.
     *
     * EVERY READ HERE IS LOCAL AND THAT IS WHAT MAKES IT SAFE FROM [render], which `PhoneSurface`
     * calls on every journal event. `App.MachinePresence` is an O(1) read of a cache the relay
     * goroutine fills on its own cadence (never `App.Presence`, which is a blocking round-trip
     * android/unbound-verbs.tsv bars a render from); `App.MachineFreshness`, `App.StreamState`,
     * `App.ResyncPending` and `App.ClockVerdict` read core state. [draw]'s equality check is what
     * keeps the redraw itself off the switches.
     *
     * GUARDED WHOLE, for [machineOf]'s reason: a facade that refuses has told this section
     * nothing, and a section drawn from nothing would print a presence word nobody supplied.
     */
    private fun connectionOf(app: App): ConnectionSection? {
        val endpoint = endpointOf(app) ?: return null
        return try {
            val bridge = FacadeBridge(app)
            SettingsPanelScreen.connectionOf(
                // THE TWO FACTS, UNLABELLED. Which of them a person reads and whether the id gets
                // a cell of its own is derivation row 11's question, and `connectionOf` answers it
                // -- in a function the unit suite can run, which a surface holding a `swarmmobile
                // .App` is not.
                machineId = endpoint,
                machineName = machineNameOf(app),
                presence = bridge.machinePresence(),
                freshness = bridge.machineFreshness(),
                streams = bridge.streamViews(),
                clock = bridge.clockBanner(),
                // ROW 12'S STATE (agents-tracker-2pnu F5). It is READ ONLY by design and the
                // section draws the panel only where it is engaged -- see `connectionOf`.
                killSwitchEngaged = bridge.killSwitchEngaged(),
                // THIS PHONE'S CLOCK, FOR ONE ELAPSED DURATION (agents-tracker-2pnu F5). It
                // replaced `formatTime = ::clockTime`: the presence line printed a wall-clock
                // time with no date on it, which at 09:00 the next morning reads the same as a
                // machine heard from three minutes ago. It is read per draw and never latched,
                // for `clockBanner`'s reason one line up.
                nowUnixMs = System.currentTimeMillis(),
            )
        } catch (unreadable: Exception) {
            null
        }
    }

    /**
     * `App.MachineName`, guarded on its own so an unreadable name cannot take the PAIRING down
     * with it: [machineOf]'s answer is what decides whether the Replace control exists at all, and
     * that question is the endpoint id's. Empty falls back to the id.
     */
    private fun machineNameOf(app: App): String = try {
        app.machineName()
    } catch (unreadable: Exception) {
        ""
    }

    /**
     * The machine's answer to the `push_prefs` this panel issued, applied to the screen it was
     * issued from (agents-tracker-os37).
     *
     * IT USED TO ASK ONLY WHETHER AN ANSWER EXISTED. `machineAnswered` read
     * `outcome.code.isNotEmpty()`, threw the code and the message away, and called
     * [SettingsScreen.acknowledged] on ANY answer -- so a REFUSED command cleared the pending
     * notice and left the switch reading settled while the machine went on sending what the user
     * had turned off. That is the failure [SettingsScreen.setAlerts]'s own KDoc says `pendingSync`
     * exists to prevent, arriving through the one path that was supposed to end it.
     *
     * THE REFUSAL IS SAID TWICE, which is `PhoneSurface.dispatchPress`'s decision and [PressFeedback]'s
     * subject: the line keeps the machine's words where they can be re-read, and the toast puts them
     * where the switch the user just moved is. Unlike a press, nobody is waiting on this frame -- the
     * answer lands on a later render -- so a message that appeared only in a line under the panel
     * could easily be a message nobody ever sees.
     *
     * AN UNREADABLE OUTCOME LEAVES THE OPERATION PENDING rather than resolving it. A facade that
     * throws has told this screen nothing, and "nothing" is not an acceptance.
     */
    private fun settleWithTheMachine(bridge: FacadeBridge, held: SettingsScreen): SettingsScreen {
        val id = pendingOp ?: return held
        val answer = try {
            bridge.launchOutcome(id)
        } catch (unreadable: Exception) {
            return held
        }
        return when (SettingsScreen.syncAnswer(answer, id)) {
            PushSync.PENDING -> held
            PushSync.ACCEPTED -> {
                pendingOp = null
                held.acknowledged()
            }

            PushSync.REFUSED -> {
                pendingOp = null
                // THE SCREEN'S SENTENCE AND THE MACHINE'S WORDS, IN THAT ORDER
                // (agents-tracker-ksvb.10). This site had no sentence of its own: the notice WAS
                // `outcome.message`, so `push_prefs: no durable preference custody configured` was
                // the whole of what a user who moved a switch was told. The words are now row 1's
                // mono suffix, which is where a wire string belongs.
                say(
                    PressFeedback.ofRefusal(
                        SettingsScreen.refusalNotice(),
                        SettingsScreen.refusalDetail(answer),
                    ),
                )
                // THE TOKEN GOES BACK WITH THE SWITCHES (agents-tracker-b6iu). The tap reconciled
                // it optimistically against the screen the machine has now rejected, so the
                // deletion (or registration) it made stands for a preference that is no longer in
                // effect: a category shown ON over a deleted token is a phone no wake can reach
                // (ADR-007 B16), and both shown OFF over a live token is the identifier
                // PB-PUSH-9's deletion-on-disable exists to remove. The restored screen is the
                // preference in effect, so it is what the token is reconciled against.
                val restored = held.refused()
                reconcileTheToken(restored)
                restored
            }
        }
    }

    /**
     * The facade, or null with the reason already on screen (agents-tracker-po3x).
     *
     * WHY IT IS ONE FUNCTION AND NOT A GUARD AT EACH SITE. Both call sites run AFTER the user has
     * acted -- one with a destructive confirmation already answered, one with the switch already
     * moved under the finger -- and both used to spell the guard
     * `(runtime.phone() as? PhoneStartup.Ready)?.app ?: return`, which does not handle the other
     * case so much as elide it. `PhoneStartup` is sealed with exactly two, and the second carries
     * the routed error PB-APP-9 wrote for this condition; `PhoneSurface.press` already shows it on
     * its own outcome line, so there is nothing to invent here.
     *
     * THE WINDOW IS NARROW AND IT IS REAL. `PhoneRuntime` builds the core lazily and FAILABLY and
     * does not cache a refusal, which is exactly why `PhoneActivity.onResume` retries it -- so the
     * answer to `phone()` genuinely differs between the draw and the tap.
     *
     * WHAT IT DOES NOT DO IS RESTORE ANY CONTROL, because what needs restoring differs: the replace
     * chip was never disabled (the press never reached [dispatch]), and the switch has moved. The
     * caller knows which; see [restore].
     */
    private fun readyApp(): App? = when (val startup = runtime.phone()) {
        is PhoneStartup.Ready -> startup.app
        is PhoneStartup.Unavailable -> {
            say(PressFeedback.ofRefusal(startup.error.message))
            null
        }
    }

    /**
     * Put one answer on screen: the persistent line, and derivation row 1's toast.
     *
     * IT IS `PhoneSurface.say`'S PROGRAM ON THIS PANEL'S OWN SEAM, and the toast is skipped where
     * there is nothing to say -- an empty one is a 92 dp box that flashes over the tab bar for
     * 3.2 seconds carrying nothing. [outcome]'s visibility is set here as well as in [draw],
     * because the paths that say something without redrawing would otherwise write into a view
     * that is still GONE.
     */
    private fun say(feedback: PressFeedback) {
        outcome.text = feedback.line
        outcome.visibility = if (feedback.line.isEmpty()) View.GONE else View.VISIBLE
        // The suffix is row 1's mono cell and carries the machine's own words -- `PhoneSurface.say`
        // has the argument in full (agents-tracker-ksvb.10).
        if (!feedback.saysNothing) toasts?.show(feedback.toast, feedback.detail.ifEmpty { null })
    }

    /**
     * Draw the screen, and rebuild the view hierarchy only when what it shows has changed.
     *
     * THE EQUALITY CHECK IS NOT AN OPTIMISATION, for the reason `PhoneSurface.drawInbox`
     * records: [render] runs on every resume and after every switch press, and rebuilding on each
     * one would take the switch out from under the finger that just moved it. [SettingsPanel] is
     * a data class of data classes, so "has anything a user can see changed" is one comparison.
     */
    private fun draw(next: SettingsScreen, machine: String?, connection: ConnectionSection?) {
        screen = next
        val panel = SettingsPanelScreen.of(next, machine, connection)
        outcome.visibility = if (outcome.text.isEmpty()) View.GONE else View.VISIBLE
        if (panel == drawn && host.childCount > 0) return
        drawn = panel
        detachControls()
        host.removeAllViews()
        host.addView(
            settingsPanelView(
                context = activity,
                panel = panel,
                rowFor = ::controlFor,
                replaceFor = ::replaceFor,
                redirectFor = ::redirectFor,
                deliveryRedirectFor = ::deliveryRedirectFor,
                below = outcome,
                status = statusSlot(),
            ),
        )
    }

    /**
     * The trailing control for one row: the switch this surface owns, carrying the row's state.
     *
     * IT IS THE SAME SwitchCompat EVERY DRAW rather than a fresh one, and that is what makes
     * [touchFilteredActions] a true statement: PB-SEC-12 clause 1's overlay defence is applied at
     * construction, and a control rebuilt on each draw would be a different view from the one the
     * assertion holds. It is also what keeps the listener from being re-registered.
     *
     * THE SWITCH CARRIES NO TEXT. Its words are the row's -- [SettingsPanel] holds the recorded
     * copy and [settingsPanelView] renders it -- so a label on the control too would say
     * everything twice, once in the screen's copy and once in a string typed here.
     */
    private fun controlFor(row: SettingsRow): View {
        val control = when (row.toggle) {
            PushToggle.FIRST -> needsInput
            PushToggle.SECOND -> finished
        }
        // The panel it was last in has been discarded, but a discarded parent is still a parent
        // and Android refuses "the specified child already has a parent".
        (control.parent as? ViewGroup)?.removeView(control)
        drawing = true
        control.isChecked = row.checked
        drawing = false
        control.isEnabled = row.enabled
        return control
    }

    /**
     * The paired machine row's trailing control: the revoke chip this surface owns, wearing the
     * row's own words.
     *
     * THE SAME INSTANCE EVERY DRAW, for [controlFor]'s three reasons -- the touch filter, the
     * listener, and [dispatch]'s in-flight mark, which is keyed by the control's identity and
     * would be lost by a chip rebuilt under it.
     */
    private fun replaceFor(row: PairedMachineRow): View {
        (replace.parent as? ViewGroup)?.removeView(replace)
        replace.text = row.replaceLabel
        return replace
    }

    /** The redirect control, wearing the label the model decided to offer. See [replaceFor]. */
    private fun redirectFor(label: String): View {
        (openNotificationSettings.parent as? ViewGroup)?.removeView(openNotificationSettings)
        openNotificationSettings.text = label
        return openNotificationSettings
    }

    /** The channel redirect, wearing the label the model decided to offer. See [redirectFor]. */
    private fun deliveryRedirectFor(label: String): View {
        (openChannelSettings.parent as? ViewGroup)?.removeView(openChannelSettings)
        openChannelSettings.text = label
        return openChannelSettings
    }

    private fun detachControls() {
        for (control in listOf(
            needsInput,
            finished,
            replace,
            openNotificationSettings,
            openChannelSettings,
            outcome,
        )) {
            (control.parent as? ViewGroup)?.removeView(control)
        }
    }

    /**
     * Put the question the row wrote in front of the user, and revoke only if they answer it
     * (agents-tracker-mrq5).
     *
     * IT IS A DIALOG AND NOT A ROW, which is `PhoneSurface.confirmThenPress`'s own ruling for the
     * two questions that already existed: a confirmation is a second window over the screen rather
     * than something composed into it, so it is built here and [settingsPanelView] never learns it
     * happened.
     *
     * THE CONFIRMING CONTROL IS THIS SURFACE'S AND THE CANCEL IS THE PLATFORM'S, and the asymmetry
     * is the whole point. `confirmThenPress` records what a platform dialog cannot carry: its
     * buttons live in a window that surface does not own, so PB-SEC-12 clause 1's touch filter --
     * a property of a View instance -- reaches neither of them, and a confirmation built out of
     * them moves the destructive tap onto the one undefended view in the flow. So the YES is
     * [confirmReplace], gated at construction and named in [touchFilteredActions]; the NO is
     * Android's own localised `cancel`, because nothing happens when it is pressed and an overlay
     * has nothing to steal.
     *
     * NO ROW MEANS NO QUESTION AND NO PRESS. [drawn] is what is on screen, and the machine section
     * is absent exactly when this phone is pinned to nothing -- a chip that is not being shown
     * cannot be tapped, so this is the state after a revoke rather than a case to handle.
     */
    private fun confirmThenReplace() {
        val row = drawn?.machineSection?.row ?: return
        // The chip was inside the LAST confirmation, and a dismissed dialog is still a parent.
        (confirmReplace.parent as? ViewGroup)?.removeView(confirmReplace)
        // The words are the row's, here as in [replaceFor]: what the user is agreeing to is what
        // the control they pressed said it would do.
        confirmReplace.text = row.replaceLabel
        val asked = AlertDialog.Builder(activity)
            .setMessage(row.replaceConfirmation)
            .setView(confirmReplace)
            .setNegativeButton(android.R.string.cancel, null)
            .show()
        confirmReplace.setOnClickListener {
            asked.dismiss()
            // THE CONTROL THE PRESS IS MARKED ON IS THE ROW'S CHIP AND NOT THIS ONE.
            // `VerbDispatch.press` disables the control until the answer lands, so that a control
            // which has been tapped does not look untapped -- and this chip is leaving the screen
            // with the dialog. [replace] is the one still on it.
            onReplace(replace)
        }
    }

    /**
     * Replace this computer: revoke this device, off the looper, and let the next draw be whatever
     * an unpaired phone is.
     *
     * IT IS NO LONGER WHAT THE CHIP'S PRESS RUNS (agents-tracker-mrq5). [confirmThenReplace] is,
     * and this is what answering it does.
     *
     * THE VERB CANNOT RUN ON THE MAIN THREAD. `revokeThisDevice` seals a signed command and
     * resolves through `sendContext`, whose `awaitConn` polls for up to five seconds -- a tap
     * issued while the link is reconnecting would freeze the UI long enough for Android to offer to
     * kill the app, which is agents-tracker-7j4b exactly. Nothing in the platform would say so:
     * Go opens its sockets below the JVM.
     *
     * THE PURGE IS IN A `finally`, WHICH IS THE PANIC ACTION'S SEMANTICS RATHER THAN DEFENSIVE
     * STYLE. The command can refuse, and the situation this control exists for is one where the
     * phone may not reach its machine at all. A purge that ran only on success would leave the live
     * keys on the very handset whose registration its owner has just disowned -- so both key tiers
     * go with the registration, and neither comes back without pairing again (ADR-007 B133
     * decision 3).
     *
     * IT NAVIGATES NOWHERE, AND THE TWO FACTS THAT MAKES TRUE WERE BOTH MISSING. This comment used
     * to assert them -- "a revoked phone is an unpaired phone, and `PhoneSurface.renderReady` gates
     * on `PairOnlyScreen.presentationOf` before anything else, so the next draw is the screen an
     * unpaired phone gets" -- and neither half held. The gate read the pinned machine, which
     * nothing clears, so a revoked phone answered FULL_APP forever (agents-tracker-d0b8); and the
     * settle called an unqualified `render()`, which binds to [render] and redraws this panel
     * alone, so the gate was not re-asked by the press that changes its answer
     * (agents-tracker-2lz5). Between them the phone was left in the app shell with both key tiers
     * destroyed and the pairing entry point on a screen it would never be shown -- unrecoverable
     * short of clearing the app's data, which is strictly worse than the burial 64rf fixed.
     *
     * BOTH ARE NOW LOAD-BEARING RATHER THAN ASSERTED. `App.StateSummary.paired` is a fact the
     * revoke's purge clears durably, and [onReplaced] is the whole-window redraw that re-asks the
     * gate in the same frame. A "now go and pair" step would still be a second thing to keep in
     * agreement with that gate; what changed is that the gate now agrees with the press.
     */
    private fun onReplace(control: View) {
        // AND THE ANSWER TO A DEGRADED RUNTIME IS SAID, NOT SWALLOWED (agents-tracker-po3x). This
        // was `(runtime.phone() as? PhoneStartup.Ready)?.app ?: return`, reached with the
        // destructive dialog already confirmed: a phone core that failed to build between the draw
        // and the confirmation ended the flow in silence, on the one screen in this app where a
        // user has just been asked whether they are sure.
        val app = readyApp() ?: return
        // The line holds the LAST answer, and leaving it under a press in flight reads as this
        // press's -- `PhoneSurface.dispatchPress` clears it in the same place and for the reason.
        outcome.text = ""
        dispatch.press(
            control,
            SendPlane.COMMAND,
            work = {
                try {
                    // THE ID IS LATCHED HERE AND NOT IN THE SETTLE (agents-tracker-xeex, moving
                    // agents-tracker-0rle's write). `VerbDispatch.press` ends in
                    // `if (attached) settle(answer)` and `PhoneSurface.release` detaches on every
                    // pause, while this lambda runs on the lane whatever is attached. A revoke
                    // whose round trip outlives the user's attention therefore loses its whole
                    // settle -- the latch AND the sentence composed beside it -- with the purge
                    // below already done: a phone that has unpaired and purged itself, drawing
                    // the pair-only screen with no id to claim an answer by and nothing written
                    // on it. This press is the likeliest of all of them to be dropped: the revoke
                    // severs the connection its own reply would come back on, `sendContext` can
                    // wait five seconds before the append, and people put the phone down after
                    // confirming a destructive dialog.
                    //
                    // `latchRevoke` is `@Synchronized`, so what the lane writes here is visible
                    // to the looper that reads it through the same monitor rather than through
                    // this dispatch's handoff.
                    app.revokeThisDevice().also { runtime.latchRevoke(it?.operationID.orEmpty()) }
                } finally {
                    // Off the looper with the verb: PhoneRuntime is `@Synchronized` throughout and
                    // a purge is Keystore work of its own.
                    runtime.purgeKeys()
                }
            },
            settle = { answer ->
                // THE MACHINE'S VERDICT IS CLAIMED, AND WHAT IT CANNOT SAY IS SAID INSTEAD
                // (agents-tracker-qlf9). This used to read `answer.onFailure { ... }` and nothing
                // else, so the only revoke that reported anything was one that never left the
                // handset. A revoke the machine REFUSED -- a kill switch, a device it no longer
                // authorises -- returned an `Op` like any success and was reported as one, while
                // the purge above had already destroyed both key tiers: a phone locally unpaired
                // and still registered, which is exactly the state `swarm remote pair` fail-fasts
                // on (PB-STATE-10), with the reason on no screen in the product.
                //
                // AND THE ORDINARY CASE IS THE UNCONFIRMED ONE, which is why this is a NOTICE
                // rather than only an error path. `signedCommand` seals, appends and returns; the
                // reply lands later if it lands at all -- `App.RevokeThisDevice`'s own doc records
                // that a successful revoke DESTROYS the path its reply would come back on. So the
                // honest report at this moment is that the machine has not confirmed it, and
                // [PairOnlyScreen] carries that sentence onto the screen this press lands on.
                // WHAT THE PURGE ANSWERED, WHICH THIS SETTLE USED TO DROP (agents-tracker-jx23).
                // The `finally` above destroys both key tiers whether or not the command reached
                // the machine, and `App.PurgeKeys` reports an error to say the material AT REST
                // survived -- a full disk, a read-only data directory -- while the memory half
                // happened regardless. A phone in that state looks unpaired and is still holding
                // what its owner just disowned, and this is the last frame that could say so.
                //
                // IT IS READ FROM THE RUNTIME RATHER THAN CARRIED OUT OF `work`, and the reason is
                // agreement rather than convenience: `PhoneSurface.revokeNotice` RECOMPOSES this
                // sentence from `runtime.purgeFailure()` on every draw once the machine answers,
                // so a value captured here would be a second reading of the same fact that could
                // differ from the one the screen ends up showing. `purgeKeys` is called from
                // nowhere else, and the call that set the latch is the one this settle is the
                // answer to.
                val purgeFailure = runtime.purgeFailure()
                unpairNotice = answer.fold(
                    // THE SENTENCE COMPOSED HERE IS THE FALLBACK AND NOT THE ANSWER
                    // (agents-tracker-0rle, the write half of agents-tracker-4zue). It is written
                    // at the moment `signedCommand` sealed and appended, which is a relay round
                    // trip before the machine can be expected to have answered, so in practice
                    // [revokeVerdict] resolves UNANSWERED and what it composes is the fallback.
                    // The screen this press lands on re-reads the outcome on every draw and
                    // replaces it, by the id [work] latched: the panel that issued the revoke is
                    // destroyed by it, so `PhoneRuntime` keeps the id for the same reason it keeps
                    // the relay coordinate, and `PhoneSurface.renderReady` clears it when the gate
                    // says this handset is usably paired again.
                    //
                    // "IN PRACTICE" AND NOT "BY CONSTRUCTION", which is what this comment used to
                    // claim. The reply arrives on the relay drain goroutine -- accept -> onReply
                    // -> resolve -- with nothing ordering it against this lane or the looper, so a
                    // machine that answered before the settle reached the looper WOULD resolve
                    // here. Nothing depends on which way it goes: [PairOnlyScreen.revokeNoticeFor]
                    // takes the verdict it is given, so an answered one composes the answer and an
                    // unanswered one composes the fallback the draw will replace. An invariant
                    // asserted out of a race is the kind that gets fenced later and then holds
                    // somebody to a promise the drain never made.
                    onSuccess = { issued ->
                        PairOnlyScreen.revokeNoticeFor(
                            revokeVerdict(app, issued),
                            purgeFailure = purgeFailure,
                        )
                    },
                    // Every facade refusal arrives as an exception whose message carries the error
                    // class as a prefix, so it routes through the table rather than being shown raw.
                    // AND THIS ARM CARRIES IT TOO. The two facts are independent and the worst
                    // case is both at once: a revoke that never left the handset (offline, a
                    // facade refusal) over a purge that could not finish at rest. An arm that
                    // dropped it would be jx23's own silence, one branch over.
                    onFailure = { refused ->
                        PairOnlyScreen.revokeUnsentNotice(
                            FacadeBridge(app).routeFacadeError(refused.message.orEmpty()).message,
                            purgeFailure = purgeFailure,
                        )
                    },
                )
                // AND IT IS SAID TWICE, which is `PhoneSurface.dispatchPress`'s decision applied
                // to the one press this panel owns: the line keeps the remedy, and the toast puts
                // it where the chip that was pressed is. A refused revoke is the worst message in
                // this app to leave in a view somebody has to scroll to -- the situation the
                // control exists for is one where the phone may not reach its machine at all.
                if (unpairNotice.isNotEmpty()) say(PressFeedback.ofRefusal(unpairNotice))
                // THE WHOLE WINDOW, not this panel. The purge ran in the `finally` above whether
                // or not the command reached the machine, so the presentation gate's answer has
                // already changed and something has to ask it again -- see [onReplaced]. It
                // redraws this surface on its way past, so there is no second `render()` here.
                onReplaced?.invoke() ?: render()
            },
        )
    }

    /**
     * PB-SYNC-2's answer for the revoke this panel issued, claimed by operation id.
     *
     * IT IS READ ONCE AND NOT POLLED, which is the difference from [settleWithTheMachine] and is
     * forced rather than chosen: a push preference settles onto a panel that is still on screen,
     * and this one ends the screen. There is no later draw of this surface to ask again from --
     * the phone is unpaired the moment the purge above finishes.
     *
     * AN UNREADABLE OR UNRESOLVED ANSWER IS [CommandVerdict.UNANSWERED], and that is not "fine":
     * [PairOnlyScreen.revokeNoticeFor] renders it as the divergence it is. Silence here would be
     * the screen asserting a removal nobody confirmed.
     *
     * @param answer what the verb returned, which is a `swarmmobile.Op`. Typed as `Any?` because
     *  `VerbDispatch.press` is generic over the work's result; a change to the return type
     *  therefore fails to claim rather than failing to compile, which is why the cast cannot throw.
     */
    private fun revokeVerdict(app: App, answer: Any?): CommandVerdict {
        val issued = (answer as? Op)?.operationID.orEmpty()
        return try {
            CommandVerdict.of(
                FacadeBridge(app).launchOutcome(issued),
                issued,
                CommandVerdict.ACCEPTED_OK,
            )
        } catch (unreadable: Exception) {
            CommandVerdict.UNANSWERED
        }
    }

    /**
     * A switch has been moved. What that means is decided before anything is written.
     *
     * THE TAP IS THE ASK, ON THE ONE STATE WHERE THERE IS STILL SOMETHING TO ASK
     * (agents-tracker-0dij). This app has no other place that requests POST_NOTIFICATIONS, and the
     * switches are live under DENIED for exactly this reason -- Android delivers no touch to a
     * disabled control, so a screen that greyed them out could never obtain the permission. It is
     * `PairingSurface.beginScanning`'s shape verbatim: remember the ask, raise the dialog, and let
     * the redraw come from the resume that follows it (nothing in this app overrides
     * `onRequestPermissionsResult` -- see [askForNotifications]).
     *
     * AND THE PREFERENCE IS NOT WRITTEN ON THAT TAP, which is the same flow's shape too: one tap
     * does one thing, and the switch goes back where the machine has it until the user turns it on
     * again over a permission that now exists. Persisting alongside the ask would put a
     * "saved, waiting for your machine" notice on screen underneath a system dialog, about a
     * preference whose only effect is a notification the phone still cannot display.
     *
     * AND THE WRITE ITSELF NEVER RUNS HERE (agents-tracker-h39k). `setPushPreference` seals a
     * SIGNED push_prefs command: it resolves through `sendContext`, whose `awaitConn` polls for up
     * to five seconds (mobile/relay.go:149-168), and then waits on the relay append. Called from
     * this listener it froze the looper for a round trip and, on a link that was reconnecting, for
     * long enough that Android offers to kill the app -- which is agents-tracker-7j4b, on the
     * screen [onReplace] four members up already cites 7j4b to avoid. Nothing in the platform
     * would have said so: Go opens its sockets below the JVM. The listener does what a listener
     * may -- read the model, apply its refusals, decide what the tap means -- and hands the verb
     * to [dispatch]. android/gate/s25_mainthread_test.go reads this file for it now.
     *
     * THE WANTED SCREEN IS DRAWN BEFORE THE VERB LEAVES, and that is a decision rather than an
     * ordering accident. The tap has to be visible immediately -- the switch has already moved
     * under the finger -- and `pendingSync` is exactly the honest thing to show while the machine
     * is being asked: "saved on this phone, waiting for your machine". It is also what holds BOTH
     * switches while one operation is in flight (agents-tracker-ix2v), which is why the failure
     * arm has to put the panel back and not only the control.
     */
    private fun onToggled(toggle: PushToggle, value: Boolean) {
        if (drawing) return
        val current = screen ?: return
        if (current.tapAsksForPermission(value)) {
            askForNotifications()
            restore(toggle, current)
            return
        }
        val next = when (current.toggleCategory(toggle)) {
            PushCategory.NEEDS_INPUT -> current.setAlerts(value)
            PushCategory.FINISHED -> current.setMentions(value)
        }
        // THE SWITCH IS PUT BACK AND THE REASON IS SAID (agents-tracker-po3x). This was
        // `?: return`, and the switch had already moved -- `SwitchCompat` changes its own position
        // on touch and this listener runs afterwards -- so a runtime that degraded between the draw
        // and the tap left a control showing a preference nothing recorded, with nothing on screen
        // about it. [readyApp] says what is wrong; [restore] undoes what the finger did.
        val app = readyApp() ?: return restore(toggle, current)
        // The line holds the LAST answer, and leaving it under a press in flight reads as this
        // press's -- [onReplace] clears it in the same place and for the same reason.
        outcome.text = ""
        draw(next, machineOf(app), connectionOf(app))
        dispatch.press(
            // THE CONTROL IS THE PANEL AND NOT THE SWITCH, which is the one way this press differs
            // from [onReplace]'s. `VerbDispatch.press` disables the control it is given and
            // RE-ENABLES it when the answer lands -- and what holds these two switches is the
            // model (`pendingSync`, agents-tracker-ix2v), which is still raised at that moment.
            // Handing it a switch would therefore un-hold that switch alone the instant the
            // command was accepted, leaving one live control over an unanswered operation. The
            // verb also carries BOTH categories, so the press is the pair's rather than either
            // one's; the panel root is the view that is both of them.
            host,
            SendPlane.COMMAND,
            work = {
                app.setPushPreference(
                    PushPreference().apply {
                        alerts = next.alerts
                        mentions = next.mentions
                    },
                )
            },
            settle = { answer ->
                answer.fold(
                    onSuccess = { issued ->
                        pendingOp = issued.operationID
                        reconcileTheToken(next)
                        say(PressFeedback.ofSuccess(null))
                    },
                    // THROUGH THE BRIDGE AND NOT THROUGH `ErrorRouter` DIRECTLY
                    // (agents-tracker-os37). This was the one call site in the app that routed a
                    // facade refusal on the Kotlin side's own token table: the message crosses JNI
                    // with the class stamped on it, but `FacadeBridge.routeFacadeError` asks GO to
                    // classify it (`App.ErrorClass`), which is the side that produced it. A token
                    // this build has never heard of degrades to UNKNOWN here and is classified
                    // correctly there, and UNKNOWN's remedy is "try again" -- advice that is wrong
                    // for every permanent class in the taxonomy.
                    onFailure = { refused ->
                        say(PressFeedback.ofRefusal(FacadeBridge(app).routeFacadeError(refused.message.orEmpty()).message))
                        // AND THE TAP ENDS HERE (agents-tracker-o6ut). The command was never
                        // issued, so there is no operation to wait for and no answer that can
                        // arrive: the screen above -- whose `pendingSync` is raised with
                        // `pendingOp` null -- is a "waiting for your machine" notice nothing can
                        // ever clear. Both the switch and the panel go back to the screen the tap
                        // started from.
                        restore(toggle, current)
                        draw(current, machineOf(app), connectionOf(app))
                    },
                )
            },
        )
    }

    /**
     * Raise the platform's permission dialog, having recorded that it was raised.
     *
     * THE BIT IS WRITTEN FIRST AND THAT IS THE ONLY ORDER THAT WORKS. Nothing in this app overrides
     * `onRequestPermissionsResult` -- there is no callback anywhere in the module -- so there is no
     * "after the answer" to write in: what happens next is `PhoneActivity.onResume`, which redraws
     * this panel, and [read] re-resolves the permission there. Without the bit that resolve reports
     * PERMANENTLY_DENIED on a phone that has just been asked for the first time, because
     * `shouldShowRequestPermissionRationale` is false before the first ask as well as after the
     * last one. `SettingsSurfaceNotificationsTest` records that resume dependency as an assertion.
     */
    private fun askForNotifications() {
        PermissionAsks.remember(activity, AppPermission.POST_NOTIFICATIONS)
        activity.requestPermissions(
            arrayOf(AppPermission.POST_NOTIFICATIONS.manifestName),
            NOTIFICATIONS_ASK,
        )
    }

    /**
     * Put one switch back where the model has it, without the write coming back as a press.
     *
     * IT IS NEEDED BECAUSE THE USER HAS ALREADY MOVED IT. `SwitchCompat` changes its own position on
     * touch; the listener runs afterwards. So every path that declines to persist -- the tap that
     * asked for a permission instead (agents-tracker-0dij) and the tap that arrived after the
     * runtime degraded (agents-tracker-po3x) -- leaves a control showing a value nothing recorded,
     * and [draw]'s equality check cannot fix it: the model did not change, so the panel is equal to
     * the drawn one and the view tree is not rebuilt.
     */
    private fun restore(toggle: PushToggle, current: SettingsScreen) {
        val control = when (toggle) {
            PushToggle.FIRST -> needsInput
            PushToggle.SECOND -> finished
        }
        drawing = true
        control.isChecked = current.checkedFor(toggle)
        drawing = false
    }

    /**
     * PB-PUSH-9's "deletion on ... disable", and its inverse.
     *
     * A phone that has turned both categories off has asked for no wake at all, so the token it
     * still holds is a provider-visible identifier the relay would go on using. Deleting it is
     * the requirement; re-registering on the way back is what makes the switch usable twice.
     *
     * IT RUNS ON A LANE AND NOT WHERE IT WAS CALLED (agents-tracker-xla6). `PushTokens.disable`
     * reaches `App.DeletePushToken` -> `dropPushToken` -> `cl.TokenDelete(context.Background())`,
     * and both call sites are main-thread ones: a switch's settle, and [settleWithTheMachine],
     * which runs from [read] on every resume and every journal-event render. So the reconcile the
     * b6iu fix added put a relay round trip on the looper, on a path nobody taps.
     *
     * TEN SECONDS, NOT UNBOUNDED, and the correction matters because the first version of this
     * comment claimed the latter. The context carries no deadline of its own, but the connection
     * bounds the exchange: `Conn.roundtrip` wraps every call in `relay.DefaultCallTimeout`, which
     * is 10 s (internal/remote/relay/client.go). That is not a reprieve -- Android's input
     * dispatch gives an app 5 s before it offers to kill it, so the bound is twice the ANR
     * threshold -- but a fence should say what is true, and "unbounded" would send the next reader
     * looking for a missing deadline instead of at the thread this call is on.
     *
     * s25_mainthread_test.go cannot see this one: its waiting set is derived from `sendContext`
     * and the token verbs do not reach it, so android/gate/il7u_tokenrevert_test.go is the fence.
     *
     * IT IS [VerbDispatch.enqueue] AND NOT [VerbDispatch.press], because a press keyed on a
     * control DROPS a second press while the first is crossing. This is not a tap and must not be
     * dropped: a refusal's reconcile discarded because the tap's is still in flight leaves the
     * token disagreeing with the switches, which is b6iu with an extra step.
     *
     * WHAT IT ANSWERS IS SAID. The re-registration arm is INERT on a build with no Firebase
     * project (agents-tracker-ojnd) -- `requestInitialToken` says so in its return value -- so a
     * user who turns a category back on over a deleted token is told that this build cannot
     * re-register it, rather than shown a switch that promises wakes nothing can deliver.
     */
    private fun reconcileTheToken(next: SettingsScreen) {
        dispatch.enqueue(
            SendPlane.COMMAND,
            work = {
                if (!next.alerts && !next.mentions) {
                    PushTokens.disable(activity)
                    true
                } else {
                    PushTokens.requestInitialToken(activity)
                }
            },
            settle = { answer ->
                answer.fold(
                    onSuccess = { asked ->
                        if (!asked) say(PressFeedback.ofRefusal(SettingsScreen.PUSH_TRANSPORT_ABSENT))
                    },
                    // NOT ROUTED THROUGH THE ERROR TABLE, unlike every other refusal on this
                    // surface, and the reason is what reaches here. The ordinary offline case
                    // never does: `dropPushToken` clears durable state and returns nil when there
                    // is no connection, leaving the relay to `onConnected` -- which is why nothing
                    // retries. What is left is a fault whose remedy is the same whatever class it
                    // carries, and the fact the user needs is the one this sentence states.
                    onFailure = { say(PressFeedback.ofRefusal(SettingsScreen.PUSH_TOKEN_UNRECONCILED)) },
                )
            },
        )
    }

    /**
     * Leave for a system screen, or say that this phone has none (agents-tracker-bpo4).
     *
     * BOTH REDIRECTS CALLED `startActivity` BARE. Neither `ACTION_APP_NOTIFICATION_SETTINGS` nor
     * `ACTION_CHANNEL_NOTIFICATION_SETTINGS` is guaranteed to resolve -- they are Settings
     * activities, and OEM builds rename, gate and remove them -- so an unresolved intent threw
     * `ActivityNotFoundException` out of a click listener. The control offered to a user who
     * cannot receive notifications at all was then the control that killed the app.
     *
     * THE CATCH IS SCOPED TO THE ONE THROW THIS IS ABOUT, for `PushTokens.requestInitialToken`'s
     * reason: a broader catch would swallow faults that are not "this phone has no such screen"
     * and explain them away as one.
     *
     * IT SAYS THE MODEL'S SENTENCE, not one typed here: [SettingsScreen] owns the copy on this
     * screen, and this is the only remedy-less state either redirect has.
     */
    private fun leaveFor(destination: Intent) {
        try {
            activity.startActivity(destination)
        } catch (absent: ActivityNotFoundException) {
            say(PressFeedback.ofRefusal(SettingsScreen.SETTINGS_SCREEN_MISSING))
        }
    }

    /**
     * A destructive chip this surface owns: the deny treatment, the role a `TextView` cannot
     * announce for itself, and PB-SEC-12 clause 1's filter, applied ONCE.
     *
     * IT IS A FACTORY BECAUSE THERE ARE TWO OF THEM NOW (agents-tracker-mrq5) -- the one in the row
     * and the one that confirms it -- and both are gated, both are announced as buttons, and both
     * have to be the same instance every draw. [touchFilteredSwitch] is the same shape for the same
     * reason, one member above.
     *
     * IT CARRIES NO CLICK. What each chip does is its own declaration's, because the two do
     * different things and a factory that took a listener would still have to be read twice.
     */
    private fun actionChip(): TextView = SecureWindow.gate(
        denyChip(activity, "").apply { announceAsButton() },
    )

    /**
     * A `TextView` announces itself as TEXT, and the kit cannot fix that: `CtaButton`'s KDoc records
     * the same gap, because a component with no click has no role to declare. The role goes where
     * the click is, which is here -- for both chips and for the redirect.
     */
    private fun TextView.announceAsButton() {
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
    }

    private fun touchFilteredSwitch(toggle: PushToggle): SwitchCompat = SecureWindow.gate(
        SwitchCompat(activity).apply {
            layoutParams = LinearLayout.LayoutParams(WRAP, WRAP)
            setOnCheckedChangeListener { _: CompoundButton, checked: Boolean ->
                onToggled(toggle, checked)
            }
        },
    )


    private companion object {
        const val MATCH = ViewGroup.LayoutParams.MATCH_PARENT
        const val WRAP = ViewGroup.LayoutParams.WRAP_CONTENT

        /**
         * The request code the platform hands back. NOTHING READS IT, and that is the fact
         * [askForNotifications] depends on: there is no `onRequestPermissionsResult` in this module,
         * so the answer arrives as a resume. It is distinct from `PairingSurface.CAMERA_ASK` anyway,
         * because two screens sharing one code is the kind of thing that only matters on the day
         * somebody adds the callback.
         */
        const val NOTIFICATIONS_ASK = 2
    }
}
