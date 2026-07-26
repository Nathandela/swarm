package dev.swarm.phone

import android.app.Activity
import android.content.pm.PackageManager
import android.graphics.Typeface
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
 * THE BIOMETRIC GATE IS NOT ON THIS PANEL. [SettingsScreen.biometricGate] is a handset-local
 * preference that rides no command, and this module has nowhere durable to keep one; rendering
 * a switch for it would show the user a setting whose value nobody chose and whose state does
 * not survive the process. `false` is passed below because it is the value that CLAIMS the
 * least -- `freshnessFor` then answers PER_USE for PB-SEC-2's per-use actions and NONE for the
 * windowed ones, so no windowed gate is asserted anywhere. PB-SEC-2's own requirement is
 * unaffected: that table is not a preference and no switch can relax it.
 */
class SettingsSurface(
    private val activity: Activity,
    private val runtime: PhoneRuntime,
) {

    private val title = label(bold = true).apply { text = "Notifications" }
    private val blocked = label()
    private val pending = label()
    private val outcome = label()

    private val needsInput = gatedSwitch(PushToggle.FIRST)
    private val finished = gatedSwitch(PushToggle.SECOND)

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

    val root: View = LinearLayout(activity).apply {
        orientation = LinearLayout.VERTICAL
        layoutParams = ViewGroup.LayoutParams(MATCH, WRAP)
        for (child in listOf(title, needsInput, finished, blocked, pending, outcome)) {
            addView(child)
        }
    }

    /** PB-SEC-12 clause 1: a switch that stops notifications is worth an overlay. */
    val gatedActions: List<View> = listOf(needsInput, finished)

    fun render() {
        when (val startup = runtime.phone()) {
            is PhoneStartup.Unavailable -> {
                screen = null
                for (view in listOf(title, needsInput, finished, blocked, pending, outcome)) {
                    view.visibility = View.GONE
                }
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
     *
     * @param biometricGate false, and deliberately: see the class comment. It is a PARAMETER on
     *  the bridge rather than a default precisely so this decision sits at a call site someone
     *  can read.
     */
    private fun read(app: App): SettingsScreen {
        val bridge = FacadeBridge(app)
        val held = screen
        val base = if (held != null && held.pendingSync) {
            if (machineAnswered(bridge)) held.acknowledged() else held
        } else {
            bridge.pushSettings(biometricGate = false)
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

    private fun draw(next: SettingsScreen) {
        screen = next
        drawing = true
        needsInput.isChecked = next.alerts
        finished.isChecked = next.mentions
        drawing = false

        for (view in listOf(title, needsInput, finished)) view.visibility = View.VISIBLE
        needsInput.isEnabled = !next.togglesDisabled
        finished.isEnabled = !next.togglesDisabled

        blocked.text = next.notificationsBlockedNotice
        pending.text = next.pendingNotice
        blocked.visibility = if (blocked.text.isEmpty()) View.GONE else View.VISIBLE
        pending.visibility = if (pending.text.isEmpty()) View.GONE else View.VISIBLE
        outcome.visibility = if (outcome.text.isEmpty()) View.GONE else View.VISIBLE
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

    private fun gatedSwitch(toggle: PushToggle): SwitchCompat = SecureWindow.gate(
        SwitchCompat(activity).apply {
            text = when (toggle) {
                PushToggle.FIRST -> "Tell me when an agent is waiting on me"
                PushToggle.SECOND -> "Tell me when an agent has finished"
            }
            layoutParams = LinearLayout.LayoutParams(MATCH, WRAP)
            setOnCheckedChangeListener { _: CompoundButton, checked: Boolean ->
                onToggled(toggle, checked)
            }
        },
    )

    private fun label(bold: Boolean = false) = TextView(activity).apply {
        if (bold) setTypeface(typeface, Typeface.BOLD)
        layoutParams = LinearLayout.LayoutParams(MATCH, WRAP)
    }

    private companion object {
        const val MATCH = ViewGroup.LayoutParams.MATCH_PARENT
        const val WRAP = ViewGroup.LayoutParams.WRAP_CONTENT
    }
}
