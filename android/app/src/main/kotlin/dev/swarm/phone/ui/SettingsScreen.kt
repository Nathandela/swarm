package dev.swarm.phone.ui

import dev.swarm.phone.runtime.PermissionState

/**
 * Phase B slice S16 -- PB-APP-7: settings.
 *
 * THE SUPPRESSION HALF IS NOT HERE AND MUST NOT BE. PB-PUSH-8 is explicit that local filtering
 * does not satisfy it -- the push would still have been sent and the provider would still see
 * the token, the timing and the size -- so "demonstrably suppress" is verified AT THE SENDER,
 * by counting calls at the relay's push seam. A Kotlin assertion that a notification was not
 * displayed would be exactly the local filtering the requirement rules out, dressed as
 * evidence.
 *
 * What the SCREEN owes is here: two switches that mean two different things, a value that
 * survives the screen being rebuilt, and an honest account of what is not yet in effect.
 */

/** The two push switches PB-APP-7 asks for. */
enum class PushToggle { FIRST, SECOND }

/**
 * What each switch governs. The mapping to [PushToggle] is a BIJECTION on purpose: two controls
 * wired to one preference leaves the other category unreachable, and the user cannot tell.
 */
enum class PushCategory {
    /** The agent is blocked on the owner. */
    NEEDS_INPUT,

    /** The agent is handing work back. */
    FINISHED,
}

/** What survives a process death. Exactly the two values the screen renders. */
data class SettingsSnapshot(
    val alerts: Boolean,
    val mentions: Boolean,
)

/**
 * PB-APP-7's screen.
 *
 * IT RENDERS WHAT WAS PERSISTED, NEVER A DEFAULT. phonecore.State.PushPreference exists for
 * this, and the failure it guards against is specific: a screen that defaults to ON after a
 * process death silently re-enables notifications the user turned off, and Android kills this
 * process routinely.
 */
data class SettingsScreen(
    /** `swarmmobile.PushPreference.Alerts` -- [PushCategory.NEEDS_INPUT]. */
    val alerts: Boolean,
    /** `swarmmobile.PushPreference.Mentions` -- [PushCategory.FINISHED]. */
    val mentions: Boolean,
    /** True while the machine has not acknowledged the last change. */
    val pendingSync: Boolean = false,
    /**
     * Null until the permission has actually been resolved. It is not defaulted to GRANTED:
     * claiming a permission state nobody checked is how a screen ends up telling a user their
     * toggles are live while POST_NOTIFICATIONS is denied.
     */
    val notificationPermission: PermissionState? = null,
) {
    fun toggleCategory(toggle: PushToggle): PushCategory = when (toggle) {
        PushToggle.FIRST -> PushCategory.NEEDS_INPUT
        PushToggle.SECOND -> PushCategory.FINISHED
    }

    fun snapshot(): SettingsSnapshot = SettingsSnapshot(alerts, mentions)

    /**
     * A push preference is carried to the machine by a SIGNED push_prefs command whose version
     * must strictly advance (remotegw.filePushPrefs.SavePrefs refuses anything else, because
     * the relay may replay a frame from before the user turned pushes off). So a toggle flipped
     * offline is a local value the machine has never seen, and showing it as settled would tell
     * the user notifications are off while they keep arriving.
     */
    fun setAlerts(value: Boolean): SettingsScreen = copy(alerts = value, pendingSync = true)

    fun setMentions(value: Boolean): SettingsScreen = copy(mentions = value, pendingSync = true)

    fun acknowledged(): SettingsScreen = copy(pendingSync = false)

    val pendingNotice: String
        get() = if (pendingSync) {
            "Saved on this phone. It takes effect once your machine confirms it."
        } else {
            ""
        }

    fun withNotificationPermission(state: PermissionState): SettingsScreen =
        copy(notificationPermission = state)

    /**
     * PB-RUN-2. Below API 33 the permission does not exist and notifications work, which is a
     * THIRD answer and not a convenience -- PermissionStateResolver already models it, and
     * folding it into "granted" or "denied" would either disable working toggles or claim a
     * grant nobody gave.
     */
    val notificationsBlockedNotice: String
        get() = when (notificationPermission) {
            PermissionState.DENIED ->
                "Notifications are turned off for this app, so these switches change nothing " +
                    "yet. Allow notifications to use them."

            PermissionState.PERMANENTLY_DENIED ->
                "Notifications are blocked for this app, so these switches change nothing yet. " +
                    "Turn them on in system settings."

            PermissionState.GRANTED, PermissionState.NOT_APPLICABLE, null -> ""
        }

    val togglesDisabled: Boolean
        get() = notificationPermission == PermissionState.DENIED ||
            notificationPermission == PermissionState.PERMANENTLY_DENIED

    companion object {
        fun restore(snapshot: SettingsSnapshot): SettingsScreen = SettingsScreen(
            alerts = snapshot.alerts,
            mentions = snapshot.mentions,
        )
    }
}
