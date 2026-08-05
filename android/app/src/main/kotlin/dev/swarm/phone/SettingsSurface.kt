package dev.swarm.phone

import android.app.Activity
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
import dev.swarm.phone.push.PushTokens
import dev.swarm.phone.runtime.AppPermission
import dev.swarm.phone.runtime.PermissionAsks
import dev.swarm.phone.runtime.PermissionStateResolver
import dev.swarm.phone.ui.ErrorRouter
import dev.swarm.phone.ui.FacadeBridge
import dev.swarm.phone.ui.PressFeedback
import dev.swarm.phone.ui.PushCategory
import dev.swarm.phone.ui.PushToggle
import dev.swarm.phone.ui.SettingsScreen
import dev.swarm.phone.ui.kit.CtaKind
import dev.swarm.phone.ui.kit.ToastHost
import dev.swarm.phone.ui.kit.ctaButton
import dev.swarm.phone.ui.kit.denyChip
import dev.swarm.phone.ui.screens.PairedMachineRow
import dev.swarm.phone.ui.screens.SettingsPanel
import dev.swarm.phone.ui.screens.SettingsPanelScreen
import dev.swarm.phone.ui.screens.SettingsRow
import dev.swarm.phone.ui.screens.settingsPanelView
import swarmmobile.App
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
     */
    private val outcome = label()

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
                activity.startActivity(
                    Intent(Settings.ACTION_APP_NOTIFICATION_SETTINGS)
                        .putExtra(Settings.EXTRA_APP_PACKAGE, activity.packageName),
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
        listOf(needsInput, finished, replace, confirmReplace, openNotificationSettings)

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

    fun render() {
        when (val startup = runtime.phone()) {
            is PhoneStartup.Unavailable -> {
                // NOTHING, rather than an empty settings screen. `PhoneSurface` renders the routed
                // startup failure; a panel of switches over a phone that could not start would
                // offer preferences nothing can carry to a machine.
                screen = null
                drawn = null
                detachControls()
                host.removeAllViews()
                outcome.visibility = View.GONE
            }

            is PhoneStartup.Ready -> draw(read(startup.app), machineOf(startup.app))
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
        val base = if (held != null && held.pendingSync) {
            if (machineAnswered(bridge)) held.acknowledged() else held
        } else {
            bridge.pushSettings()
        }
        return base.withNotificationPermission(
            PermissionStateResolver.resolve(
                permission = AppPermission.POST_NOTIFICATIONS,
                sdkInt = Build.VERSION.SDK_INT,
                granted = activity.checkSelfPermission(
                    AppPermission.POST_NOTIFICATIONS.manifestName,
                ) == PackageManager.PERMISSION_GRANTED,
                // THE PERSISTED BIT, AND IT USED TO BE THE LITERAL `true` (agents-tracker-0dij).
                // The comment beside it claimed "the permission is requested on the notification
                // path" and no such path existed -- the app's only `requestPermissions` call was
                // the camera's -- so on API 33+ every ungranted phone resolved PERMANENTLY_DENIED
                // five seconds after install: both switches disabled, and a notice sending the
                // owner to system settings where nothing was wrong. The ask is now [onToggled]'s,
                // and this is the bit that tells a first run from a permanent refusal.
                hasAskedBefore = PermissionAsks.hasAsked(
                    activity,
                    AppPermission.POST_NOTIFICATIONS,
                ),
                showRationale = activity.shouldShowRequestPermissionRationale(
                    AppPermission.POST_NOTIFICATIONS.manifestName,
                ),
            ),
        )
    }

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
    private fun machineOf(app: App): String? = try {
        app.stateSummary().takeIf { it.paired }?.machine?.takeIf { it.isNotEmpty() }
    } catch (unreadable: Exception) {
        null
    }

    /** True once the outcome for the op this panel issued has landed. */
    private fun machineAnswered(bridge: FacadeBridge): Boolean {
        val id = pendingOp ?: return false
        val answered = try {
            bridge.launchOutcome(id).code.isNotEmpty()
        } catch (unreadable: Exception) {
            false
        }
        if (answered) pendingOp = null
        return answered
    }

    /**
     * Draw the screen, and rebuild the view hierarchy only when what it shows has changed.
     *
     * THE EQUALITY CHECK IS NOT AN OPTIMISATION, for the reason `PhoneSurface.drawInbox`
     * records: [render] runs on every resume and after every switch press, and rebuilding on each
     * one would take the switch out from under the finger that just moved it. [SettingsPanel] is
     * a data class of data classes, so "has anything a user can see changed" is one comparison.
     */
    private fun draw(next: SettingsScreen, machine: String?) {
        screen = next
        val panel = SettingsPanelScreen.of(next, machine)
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
                below = outcome,
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

    private fun detachControls() {
        for (control in listOf(needsInput, finished, replace, openNotificationSettings, outcome)) {
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
        val app = (runtime.phone() as? PhoneStartup.Ready)?.app ?: return
        // The line holds the LAST answer, and leaving it under a press in flight reads as this
        // press's -- `PhoneSurface.dispatchPress` clears it in the same place and for the reason.
        outcome.text = ""
        dispatch.press(
            control,
            SendPlane.COMMAND,
            work = {
                try {
                    app.revokeThisDevice()
                } finally {
                    // Off the looper with the verb: PhoneRuntime is `@Synchronized` throughout and
                    // a purge is Keystore work of its own.
                    runtime.purgeKeys()
                }
            },
            settle = { answer ->
                // Every facade refusal arrives as an exception whose message carries the error
                // class as a prefix, so it routes through the table rather than being shown raw.
                //
                // AND IT IS SAID TWICE, which is `PhoneSurface.dispatchPress`'s decision applied
                // to the one press this panel owns: the line keeps the remedy, and the toast puts
                // it where the chip that was pressed is. A refused revoke is the worst message in
                // this app to leave in a view somebody has to scroll to -- the situation the
                // control exists for is one where the phone may not reach its machine at all.
                answer.onFailure {
                    val feedback = PressFeedback.ofRefusal(
                        FacadeBridge(app).routeFacadeError(it.message.orEmpty()).message,
                    )
                    outcome.text = feedback.line
                    if (!feedback.saysNothing) toasts?.show(feedback.toast)
                }
                // THE WHOLE WINDOW, not this panel. The purge ran in the `finally` above whether
                // or not the command reached the machine, so the presentation gate's answer has
                // already changed and something has to ask it again -- see [onReplaced]. It
                // redraws this surface on its way past, so there is no second `render()` here.
                onReplaced?.invoke() ?: render()
            },
        )
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
        val app = (runtime.phone() as? PhoneStartup.Ready)?.app ?: return
        try {
            val op = app.setPushPreference(
                PushPreference().apply {
                    alerts = next.alerts
                    mentions = next.mentions
                },
            )
            pendingOp = op.operationID
            reconcileTheToken(next)
            outcome.text = ""
        } catch (refused: Exception) {
            outcome.text = ErrorRouter.route(refused.message.orEmpty()).message
        }
        draw(next, machineOf(app))
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
     */
    private fun reconcileTheToken(next: SettingsScreen) {
        if (!next.alerts && !next.mentions) {
            PushTokens.disable(activity)
        } else {
            PushTokens.requestInitialToken(activity)
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

    /**
     * The routed-error line.
     *
     * IT CARRIES NO TEXT APPEARANCE, and that is a statement about what is missing rather than a
     * regression. It used to be `setTypeface(typeface, Typeface.BOLD)`, then
     * `R.style.TextAppearance_Swarm_Title_Row` on a heading this panel no longer has -- the
     * heading is now [dev.swarm.phone.ui.kit.navHeader]'s. What is left is one line of body copy,
     * and the component that would style it is derivation row 15's neighbour: there is no
     * notice or body-copy factory in the kit, so it renders at the theme's default until there is.
     */
    private fun label() = TextView(activity).apply {
        layoutParams = LinearLayout.LayoutParams(MATCH, WRAP)
    }

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
