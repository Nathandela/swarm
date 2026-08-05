package dev.swarm.phone.ui

import dev.swarm.phone.runtime.NotificationDelivery
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
 * What the machine has said about the push_prefs command this screen issued (agents-tracker-os37).
 *
 * THREE ANSWERS AND NOT TWO. [PENDING] is both "no answer yet" and "an answer for somebody else's
 * operation", which are the same fact about THIS one: nothing is known. Collapsing it into either
 * of the others is how a screen resolves an operation by proximity, which PB-SYNC-2's whole
 * outcome-by-id design exists to stop.
 */
enum class PushSync {
    /** No answer this screen may claim. The switch stays where it is and the notice stands. */
    PENDING,

    /** `protocol.OpOK`. The machine holds what the screen shows. */
    ACCEPTED,

    /** Anything else the machine sealed. The switch goes back and the reason is shown. */
    REFUSED,
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
    /**
     * The switch positions the MACHINE last confirmed, held while a change is unacknowledged and
     * null the rest of the time (agents-tracker-os37).
     *
     * IT IS THE REVERT POINT AND IT HAS TO BE CARRIED, not recomputed. `PushPreference` answers
     * what is persisted LOCALLY, which `setPushPreference` has already updated by the time an
     * answer arrives, so re-reading the facade after a refusal returns the very value the machine
     * rejected. This is the only record of what it holds.
     *
     * IT IS TAKEN ONCE PER PENDING RUN. Two flips before one answer revert to the position before
     * EITHER: the surface watches one operation id at a time, so an answer landing after a second
     * flip is about the first, and the last position the machine is known to hold is the one before
     * both.
     */
    val settled: SettingsSnapshot? = null,
    /**
     * Whether the FRAMEWORK will show what this app posts, or null until something has asked
     * (agents-tracker-2yfn).
     *
     * IT IS A SECOND, INDEPENDENT FACT and not a restatement of [notificationPermission]. A user who
     * long-presses a wake and blocks `Agent updates` leaves POST_NOTIFICATIONS GRANTED -- blocking a
     * channel does not revoke a permission -- so every check this screen made answered "fine" while
     * every wake was dropped before the app was started. Push is the sole path to a backgrounded
     * phone (ADR-007 B16), so nothing else in the product would ever have said so.
     *
     * NULL IS THE STATE BEFORE ANYTHING HAS LOOKED, which is [notificationPermission]'s convention
     * and carries its reason: claiming a state nobody checked is how a screen reports a fault on a
     * phone where nothing is wrong.
     */
    val notificationDelivery: NotificationDelivery? = null,
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
    fun setAlerts(value: Boolean): SettingsScreen =
        copy(alerts = value, pendingSync = true, settled = settled ?: snapshot())

    fun setMentions(value: Boolean): SettingsScreen =
        copy(mentions = value, pendingSync = true, settled = settled ?: snapshot())

    /** The machine took it. What is on screen IS what it holds, so there is nothing to revert to. */
    fun acknowledged(): SettingsScreen = copy(pendingSync = false, settled = null)

    /**
     * The machine REFUSED it, so the switches go back where it has them (agents-tracker-os37).
     *
     * LEAVING THEM WHERE THE USER DRAGGED THEM IS THE SAME LIE AS CLEARING THE NOTICE. Delivery is
     * decided at the sender (PB-PUSH-8) from the preference the MACHINE holds, so a switch showing
     * the refused position is a screen reporting a setting nobody has -- which is the defect this
     * whole issue is about, one step later in the same flow.
     *
     * A REFUSAL IS AN ANSWER, so `pendingSync` clears with it. "Saved on this phone, waiting for
     * your machine" over a change the machine has already declined is a wait with no end.
     */
    fun refused(): SettingsScreen = copy(
        alerts = settled?.alerts ?: alerts,
        mentions = settled?.mentions ?: mentions,
        pendingSync = false,
        settled = null,
    )

    val pendingNotice: String
        get() = if (pendingSync) {
            "Saved on this phone. It takes effect once your machine confirms it."
        } else {
            ""
        }

    fun withNotificationPermission(state: PermissionState): SettingsScreen =
        copy(notificationPermission = state)

    fun withNotificationDelivery(state: NotificationDelivery): SettingsScreen =
        copy(notificationDelivery = state)

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
        get() = if (notificationRemedy == NotificationRemedy.SYSTEM_SETTINGS ||
            blockedDelivery == NotificationDelivery.APP_BLOCKED
        ) {
            // THE APP-LEVEL BLOCK REUSES THIS CONTROL rather than getting one of its own
            // (agents-tracker-2yfn). Its remedy IS this screen's destination -- the app's own
            // notification page -- and the two states cannot both hold, because a permission notice
            // suppresses the delivery one (see [blockedDelivery]). A second control to the same
            // place would be a second thing to keep in agreement with this one.
            OPEN_NOTIFICATION_SETTINGS
        } else {
            null
        }

    /**
     * The delivery fault this screen should report, or null where there is none to report
     * (agents-tracker-2yfn).
     *
     * A PERMISSION NOTICE SUPPRESSES THIS ONE, and that is the whole of the de-duplication. On API
     * 33+ the app-level notification toggle IS POST_NOTIFICATIONS, so a global disable makes both
     * facts true at once and two notices would say the same thing twice over two switches. The
     * permission's is the one that survives because it is the one whose remedy the screen can still
     * offer: on DENIED the platform will still prompt, and a redirect shown there would walk the
     * user past the prompt that fixes it.
     */
    val blockedDelivery: NotificationDelivery?
        get() = notificationDelivery
            ?.takeIf { it != NotificationDelivery.DELIVERABLE }
            ?.takeIf { notificationRemedy == null }

    /**
     * What the screen says about a wake the framework will drop, in words of its own.
     *
     * IT IS NOT THE PERMISSION'S SENTENCE AND MUST NOT BE. The remedies differ -- a channel block is
     * undone on the channel's own page, an app-level disable on the app's, a withheld permission by
     * a prompt -- and copy naming an action the screen does not offer reads as a step the user has
     * failed to find. Each sentence here names the control its own state puts on screen, which is
     * [notificationsBlockedNotice]'s rule applied to the state that rule could not see.
     */
    val deliveryBlockedNotice: String
        get() = when (blockedDelivery) {
            NotificationDelivery.APP_BLOCKED ->
                "Android is not showing notifications from this app, so these switches change " +
                    "nothing. $OPEN_NOTIFICATION_SETTINGS to turn them back on."

            NotificationDelivery.CHANNEL_BLOCKED ->
                "Android has this app's alert category switched off, so wakes are dropped before " +
                    "they reach you and these switches change nothing. $OPEN_CHANNEL_SETTINGS to " +
                    "turn it back on."

            NotificationDelivery.DELIVERABLE, null -> ""
        }

    /**
     * The words on the control that leads to the WAKE CHANNEL's own page, or null where that is not
     * where the problem is. See [notificationRedirectLabel], whose reasoning this shares.
     */
    val deliveryRedirectLabel: String?
        get() = if (blockedDelivery == NotificationDelivery.CHANNEL_BLOCKED) {
            OPEN_CHANNEL_SETTINGS
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

        /**
         * The channel redirect's words, once, for [OPEN_NOTIFICATION_SETTINGS]'s reason --
         * [deliveryBlockedNotice] interpolates this constant, so the sentence and the control cannot
         * drift apart.
         *
         * IT IS DIFFERENT WORDS BECAUSE IT IS A DIFFERENT DESTINATION (agents-tracker-2yfn). The
         * other label leads to this app's notification list, on which the wake category is one row;
         * this one leads to that row's own page, which is the only place a blocked category can be
         * turned back on. Two controls wearing one label is a screen that cannot say where either
         * goes.
         *
         * IT DOES NOT NAME THE CATEGORY. `Agent updates` is a string RESOURCE
         * (`R.string.wake_channel_name`) because it is what the system settings screen displays, and
         * a second copy typed here would be a second copy that drifts -- the same argument
         * [SettingsSurface]'s controls make about carrying no words at construction.
         */
        const val OPEN_CHANNEL_SETTINGS = "Open the alert category"

        /**
         * What a refusal says when the machine sent no words with it (agents-tracker-os37).
         *
         * IT IS NEEDED BECAUSE THE REPLY CAN CARRY NONE. `remotegw.refusePushPrefs` seals its reply
         * with no error code at all -- "none of the six in the taxonomy describes a machine-side
         * custody failure" -- so the message is the only thing that ever says what happened, and an
         * empty one would put the switch back with no explanation, which is this defect in a
         * different coat.
         */
        const val SYNC_REFUSED = "Your machine did not save this change."

        /**
         * PB-SYNC-2's answer for the push_prefs command [operationId], read as a CODE.
         *
         * THE OLD READING WAS `code.isNotEmpty()` AND THAT IS THE BUG. Any answer counted as an
         * acceptance, so a refusal cleared the pending notice and left the switch reading settled
         * while the machine went on sending what the user turned off.
         *
         * `ok` is `protocol.OpOK`: `mobile/app.go`'s `outcomeOf` falls back to the reply's `Op`
         * when it carries no `ErrorCode`, and an accepted command replies OK. A refusal replies
         * `protocol.OpError` with the reason in `Error`. Anything else the machine can seal is a
         * refusal too -- kill switch, not authorized, invalid field -- for `LaunchScreen.resolve`'s
         * reason: the codes are the machine's and this side must not claim to know the whole set.
         *
         * SOMEBODY ELSE'S OPERATION LEAVES THIS ONE PENDING, which is the honest state. PB-SYNC-2
         * claims outcomes by id precisely so a screen never resolves one by proximity.
         */
        fun syncAnswer(outcome: OperationOutcome, operationId: String): PushSync = when {
            outcome.operationId != operationId || outcome.code.isBlank() -> PushSync.PENDING
            outcome.code == CODE_OK -> PushSync.ACCEPTED
            else -> PushSync.REFUSED
        }

        /** The machine's own words about a refusal, or [SYNC_REFUSED] where it sent none. */
        fun refusalNotice(outcome: OperationOutcome): String =
            outcome.message.ifBlank { SYNC_REFUSED }

        /** protocol.OpOK, as [LaunchScreen] holds it and for the same reason: the AAR is off-JVM. */
        private const val CODE_OK = "ok"

        fun restore(snapshot: SettingsSnapshot): SettingsScreen = SettingsScreen(
            alerts = snapshot.alerts,
            mentions = snapshot.mentions,
        )
    }
}
