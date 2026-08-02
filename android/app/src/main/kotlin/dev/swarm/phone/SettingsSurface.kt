package dev.swarm.phone

import android.app.Activity
import android.content.pm.PackageManager
import android.os.Build
import android.view.View
import android.view.ViewGroup
import android.view.accessibility.AccessibilityNodeInfo
import android.widget.Button
import android.widget.CompoundButton
import android.widget.LinearLayout
import android.widget.TextView
import androidx.appcompat.widget.SwitchCompat
import dev.swarm.phone.push.PushTokens
import dev.swarm.phone.runtime.AppPermission
import dev.swarm.phone.runtime.PermissionStateResolver
import dev.swarm.phone.ui.ErrorRouter
import dev.swarm.phone.ui.FacadeBridge
import dev.swarm.phone.ui.PushCategory
import dev.swarm.phone.ui.PushToggle
import dev.swarm.phone.ui.SettingsScreen
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
     */
    private val replace: TextView = SecureWindow.gate(
        denyChip(activity, "").apply {
            setOnClickListener { control -> onReplace(control) }
            // A `TextView` announces itself as text, and the kit cannot fix that: `CtaButton`'s
            // KDoc records the same gap, because a component with no click has no role to declare.
            // The role goes where the click is, which is here.
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
     */
    val touchFilteredActions: List<View> = listOf(needsInput, finished, replace)

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
                // The permission is requested on the notification path, not by this panel; what
                // this panel owes is to say the switches change nothing while it is withheld.
                hasAskedBefore = true,
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
     */
    private fun machineOf(app: App): String? = try {
        app.stateSummary().machine.takeIf { it.isNotEmpty() }
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

    private fun detachControls() {
        for (control in listOf(needsInput, finished, replace, outcome)) {
            (control.parent as? ViewGroup)?.removeView(control)
        }
    }

    /**
     * Replace this computer: revoke this device, off the looper, and let the next draw be whatever
     * an unpaired phone is.
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
     * IT NAVIGATES NOWHERE. A revoked phone is an unpaired phone, and `PhoneSurface.renderReady`
     * gates on `PairOnlyScreen.presentationOf` before anything else -- so the next draw is the
     * screen an unpaired phone gets, with one offer on it, and [render] is the whole of the second
     * half. A "now go and pair" step would be a second thing to keep in agreement with that gate.
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
                answer.onFailure {
                    outcome.text = FacadeBridge(app).routeFacadeError(it.message.orEmpty()).message
                }
                render()
            },
        )
    }

    private fun onToggled(toggle: PushToggle, value: Boolean) {
        if (drawing) return
        val current = screen ?: return
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
    }
}
