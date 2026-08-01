package dev.swarm.phone

import android.app.Activity
import android.content.pm.PackageManager
import android.os.Build
import android.view.View
import android.view.ViewGroup
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
 */
class SettingsSurface(
    private val activity: Activity,
    private val runtime: PhoneRuntime,
) {

    /**
     * The error line, which is NOT part of inventory C6 and is hosted under the panel rather than
     * inside it. C6 draws sections of rows; a routed facade refusal is the same class of thing as
     * `PhoneSurface`'s outcome line and belongs on the same seam.
     */
    private val outcome = label()

    private val needsInput = touchFilteredSwitch(PushToggle.FIRST)
    private val finished = touchFilteredSwitch(PushToggle.SECOND)

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

    /** PB-SEC-12 clause 1: a switch that stops notifications is worth an overlay. */
    val touchFilteredActions: List<View> = listOf(needsInput, finished)

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

            is PhoneStartup.Ready -> draw(read(startup.app))
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
    private fun draw(next: SettingsScreen) {
        screen = next
        val panel = SettingsPanelScreen.of(next)
        outcome.visibility = if (outcome.text.isEmpty()) View.GONE else View.VISIBLE
        if (panel == drawn && host.childCount > 0) return
        drawn = panel
        detachControls()
        host.removeAllViews()
        host.addView(settingsPanelView(activity, panel, ::controlFor, outcome))
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

    private fun detachControls() {
        for (control in listOf(needsInput, finished, outcome)) {
            (control.parent as? ViewGroup)?.removeView(control)
        }
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
        draw(next)
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
