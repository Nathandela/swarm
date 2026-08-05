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
 * What this screen can offer to do about a POST_NOTIFICATIONS it does not have
 * (agents-tracker-0dij).
 *
 * THE TWO ARE NOT DEGREES OF THE SAME THING and folding them together is the defect: on [ASK] the
 * platform will still raise its dialog, and on [SYSTEM_SETTINGS] it will not, ever again. A screen
 * that treated both as "blocked" offers a settings redirect to a user who has never been asked --
 * walking them past the prompt that would have fixed it -- and a screen that treated both as
 * askable spends every tap on a dialog the platform silently drops.
 */
enum class NotificationRemedy {
    /**
     * The permission is askable, and the TAP ON A SWITCH is what asks. That is why the switches
     * stay live in this state: Android delivers no touch to a disabled control, so a screen that
     * greyed them out could never obtain the permission -- which is the loop agents-tracker-0dij
     * reports and agents-tracker-qx9m already shipped once on the camera.
     */
    ASK,

    /** Only the system's own screen can undo it now. See [SettingsScreen.notificationRedirectLabel]. */
    SYSTEM_SETTINGS,
}

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
        get() = when (notificationRemedy) {
            // EACH SENTENCE NAMES THE ACTION ITS OWN STATE OFFERS (agents-tracker-0dij). This read
            // "Allow notifications to use them" with nothing on screen that could allow anything,
            // and the state below read "Turn them on in system settings" with no way to get there.
            // Copy naming an action the screen does not offer is worse than silence: it reads as a
            // step the user has failed to find.
            NotificationRemedy.ASK ->
                "Notifications are turned off for this app, so these switches change nothing " +
                    "yet. Turn one on and Android will ask for permission."

            NotificationRemedy.SYSTEM_SETTINGS ->
                "Notifications are blocked for this app, so these switches change nothing yet. " +
                    "$OPEN_NOTIFICATION_SETTINGS to allow them."

            null -> ""
        }

    /** PB-RUN-2's two withheld states, told apart by what can still be done about them. */
    val notificationRemedy: NotificationRemedy?
        get() = when (notificationPermission) {
            PermissionState.DENIED -> NotificationRemedy.ASK
            PermissionState.PERMANENTLY_DENIED -> NotificationRemedy.SYSTEM_SETTINGS
            PermissionState.GRANTED, PermissionState.NOT_APPLICABLE, null -> null
        }

    /**
     * The words on the control that leads to the system's own notification screen, or null where
     * there is nothing to lead to.
     *
     * IT IS A LABEL AND NOT A CONTROL because pressing it leaves the app: the Intent, PB-SEC-12
     * clause 1's touch filter and an identity that survives a redraw are all the surface's, exactly
     * as they are for the toggle and the replace chip. What the model owes is the words and the
     * decision to offer them at all.
     */
    val notificationRedirectLabel: String?
        get() = if (notificationRemedy == NotificationRemedy.SYSTEM_SETTINGS) {
            OPEN_NOTIFICATION_SETTINGS
        } else {
            null
        }

    /**
     * Whether this tap must ASK for the permission instead of persisting a preference
     * (agents-tracker-0dij).
     *
     * ONLY A TAP THAT TURNS A SWITCH ON. A user reaching for fewer notifications is not asking for
     * a permission prompt, and the preference reaches the machine either way -- suppression is
     * decided at the sender (PB-PUSH-8), not by what this handset can display.
     *
     * @param turningOn the position the switch has just been moved to.
     */
    fun tapAsksForPermission(turningOn: Boolean): Boolean =
        turningOn && notificationRemedy == NotificationRemedy.ASK

    /**
     * What the switch for [toggle] shows -- the preference its own CATEGORY holds.
     *
     * IT IS THE MODEL'S AND NOT THE SURFACE'S because a control has to be put back in three places:
     * a tap that asked for a permission rather than persisting, a tap that arrived after the runtime
     * degraded (agents-tracker-po3x), and a change the machine refused (agents-tracker-os37). A
     * second reading of the bijection at any of them is the defect [toggleCategory] exists to
     * prevent -- one category left unreachable, with nothing on screen to say so.
     */
    fun checkedFor(toggle: PushToggle): Boolean = when (toggleCategory(toggle)) {
        PushCategory.NEEDS_INPUT -> alerts
        PushCategory.FINISHED -> mentions
    }

    /**
     * DEAD MEANS THE PLATFORM WILL NOT ASK AGAIN, and nothing weaker (agents-tracker-0dij).
     *
     * This used to be true on DENIED as well, and that single `||` was the reported bug: a fresh
     * API 33+ install resolves to DENIED before anyone has been asked anything, the tap on the
     * switch is the app's only POST_NOTIFICATIONS request, and Android delivers no touch to a
     * disabled control. So the switches were dead, nothing could ask, and nothing ever would --
     * for the life of the install, with push the sole background wake path (ADR-007 B16).
     */
    val togglesDisabled: Boolean
        get() = notificationPermission == PermissionState.PERMANENTLY_DENIED

    companion object {
        /**
         * The redirect's words, once, so the sentence that names the control and the control itself
         * cannot drift apart -- [notificationsBlockedNotice] interpolates this constant.
         */
        const val OPEN_NOTIFICATION_SETTINGS = "Open notification settings"

        fun restore(snapshot: SettingsSnapshot): SettingsScreen = SettingsScreen(
            alerts = snapshot.alerts,
            mentions = snapshot.mentions,
        )
    }
}
