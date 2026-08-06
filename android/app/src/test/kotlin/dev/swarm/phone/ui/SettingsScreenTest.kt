package dev.swarm.phone.ui

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * FAILING-FIRST (TDD RED, GG-5) for PB-APP-7: settings.
 *
 * "Two coarse push toggles honored by PB-PUSH-0's trigger, ~~plus the biometric gate toggle~~.
 *  UI test; toggles persist and demonstrably suppress delivery."
 *
 * PB-APP-7 NARROWS TO TWO SWITCHES (ADR-007 B133). The biometric-gate toggle lost its subject:
 * there is no local authentication on this handset to turn on or off. The test that drove it --
 * `disabling the biometric gate never relaxes a per-use action` -- is DELETED rather than
 * rewritten, and what it fenced is worth restating so nobody looks for it later: it required
 * that consent could not relax PB-SEC-2's per-use tier, so a settings switch could not become
 * the "in-memory authenticated = true" the requirement said a test must fail on. PB-SEC-2 is
 * VOID and the per-use tier it protected does not exist, so the assertion has nothing left to
 * hold apart.
 *
 * THE SUPPRESSION HALF IS NOT HERE AND MUST NOT BE. PB-PUSH-8 is explicit that local filtering
 * does not satisfy it -- the push would still have been sent and the provider would still see
 * the token, the timing and the size -- so "demonstrably suppress" is verified AT THE SENDER,
 * in mobile/conformance/s16_pushprefs_test.go, by counting calls at the relay's push seam. A
 * Kotlin test asserting a notification was not displayed would be exactly the local filtering
 * the requirement rules out, dressed as evidence.
 *
 * What belongs here is what the SCREEN owes: two switches that mean two different things, a
 * value that survives the screen being rebuilt, and an honest account of what is not yet in
 * effect.
 */
class SettingsScreenTest {

    /** Each switch carries its own category. Two controls wired to one preference is the defect. */
    @Test
    fun `the two push toggles address different categories`() {
        val s = SettingsScreen(alerts = true, mentions = false)
        assertNotEquals(
            "the two toggles must name different push categories: needs_input is the agent " +
                "blocked on the owner, finished is the agent handing work back",
            s.toggleCategory(PushToggle.FIRST),
            s.toggleCategory(PushToggle.SECOND),
        )
        assertEquals(setOf(PushCategory.NEEDS_INPUT, PushCategory.FINISHED),
            setOf(s.toggleCategory(PushToggle.FIRST), s.toggleCategory(PushToggle.SECOND)))
    }

    /**
     * The screen renders what was PERSISTED, not a default.
     *
     * phonecore.State.PushPreference exists for this ("persisted so the settings screen renders
     * the user's choice after a restart rather than a default"), and the failure it guards
     * against is specific: a screen that defaults to ON after a process death silently re-enables
     * notifications the user turned off, and Android kills this process routinely.
     */
    @Test
    fun `a rebuilt screen shows the persisted values and never a default`() {
        val persisted = SettingsScreen(alerts = false, mentions = true)
        val rebuilt = SettingsScreen.restore(persisted.snapshot())
        assertFalse(rebuilt.alerts)
        assertTrue(rebuilt.mentions)
        assertFalse(
            "a restored screen must not claim the machine has acknowledged what it just read " +
                "off disk: the values are persisted, the acknowledgement is not",
            rebuilt.pendingSync,
        )
    }

    /**
     * A toggle is not in effect until the MACHINE has acknowledged it, and the screen says so.
     *
     * This is the honest half of the cross-slice split. The preference is carried by a signed
     * push_prefs command whose version must strictly advance (remotegw.filePushPrefs.SavePrefs
     * refuses anything else, because the relay may replay a frame from before the user turned
     * pushes off), so a toggle flipped offline is a local value the machine has never seen. A
     * screen that showed it as settled would be telling the user notifications are off while
     * they keep arriving.
     */
    @Test
    fun `a toggle not yet acknowledged by the machine is shown as pending`() {
        val s = SettingsScreen(alerts = true, mentions = true)
        val pending = s.setAlerts(false)
        assertFalse(pending.alerts)
        assertTrue("the machine has not answered yet", pending.pendingSync)
        assertTrue(pending.pendingNotice.isNotBlank())

        val settled = pending.acknowledged()
        assertFalse(settled.pendingSync)
        assertTrue(settled.pendingNotice.isBlank())
    }

    /**
     * THE SET IS EXACTLY TWO, and that is the assertion PB-APP-7's narrowing needs (ADR-007
     * B133). What the snapshot carries is what the screen renders after a process death, so a
     * third preference re-introduced here would be a switch that survives a restart, and this is
     * where a settings surface silently re-grows one.
     */
    @Test
    fun `the persisted set is the two push categories and nothing else`() {
        val fields = SettingsSnapshot::class.java.declaredFields
            .filterNot { it.isSynthetic }
            .map { it.name }
            .toSet()
        assertEquals(
            "SettingsSnapshot persists $fields. PB-APP-7 is two coarse push toggles; the " +
                "biometric-gate toggle left with PB-SEC-2 and there is no local authentication " +
                "on this handset for a third preference to govern",
            setOf("alerts", "mentions"),
            fields,
        )
    }

    /**
     * PB-RUN-2: with POST_NOTIFICATIONS denied the toggles are moot and the screen must say so
     * rather than presenting switches that change nothing. Below API 33 the permission does not
     * exist and notifications work, which is a third answer and not a convenience --
     * PermissionStateResolver already models exactly that.
     */
    @Test
    fun `a denied notification permission is surfaced beside the toggles`() {
        val denied = SettingsScreen(alerts = true, mentions = true)
            .withNotificationPermission(dev.swarm.phone.runtime.PermissionState.PERMANENTLY_DENIED)
        assertTrue(denied.notificationsBlockedNotice.isNotBlank())
        assertTrue(denied.togglesDisabled)

        val notApplicable = SettingsScreen(alerts = true, mentions = true)
            .withNotificationPermission(dev.swarm.phone.runtime.PermissionState.NOT_APPLICABLE)
        assertFalse("below API 33 notifications work and the toggles are live", notApplicable.togglesDisabled)
    }

    // ---- agents-tracker-0dij: the guided flow the notification permission never had ----------
    //
    // THE DEFECT, IN ONE LOOP. `PermissionStateResolver` answers `!hasAskedBefore -> DENIED`, the
    // settings surface hard-coded `hasAskedBefore = true`, and this model disabled both switches on
    // DENIED as well as on PERMANENTLY_DENIED. So on API 33+ a fresh install drew two dead switches
    // and a sentence sending the owner to system settings -- and the app has no other place that
    // asks for POST_NOTIFICATIONS, so nothing could ever ask. It is agents-tracker-qx9m's closed
    // loop on the other permission: no permission, so no live control; no live control, so nothing
    // asks; nothing asks, so no permission, for the life of the install.

    /**
     * DENIED is the state the ask is made FROM, so the controls that ask must be alive in it.
     *
     * A disabled switch cannot be the thing that requests the permission -- Android delivers no
     * touch to it at all -- which is why "both denials disable the row" and "the tap is what asks"
     * cannot both be true.
     */
    @Test
    fun `a merely denied permission leaves the switches tappable, because the tap is what asks`() {
        val denied = SettingsScreen(alerts = true, mentions = true)
            .withNotificationPermission(dev.swarm.phone.runtime.PermissionState.DENIED)

        assertFalse(
            "agents-tracker-0dij: the switches are disabled while POST_NOTIFICATIONS is merely " +
                "DENIED. That is the state a fresh API 33+ install is in, the ask is issued from " +
                "the switch's own tap, and a disabled control receives no tap -- so the permission " +
                "can never be requested for the life of the install",
            denied.togglesDisabled,
        )
        assertTrue(
            "PERMANENTLY_DENIED is the one state where the platform will not ask again, so it is " +
                "the one state where the switches are dead and something else has to be offered",
            SettingsScreen(alerts = true, mentions = true)
                .withNotificationPermission(
                    dev.swarm.phone.runtime.PermissionState.PERMANENTLY_DENIED,
                ).togglesDisabled,
        )
    }

    /**
     * Each withheld state's sentence names the action that state actually offers.
     *
     * The shipped DENIED copy read "Allow notifications to use them" with nothing on screen that
     * could allow anything, and the PERMANENTLY_DENIED copy said "Turn them on in system settings"
     * with no way to get there. Copy that names an action the screen does not offer is worse than
     * silence: it reads as a step the user has failed to find.
     */
    @Test
    fun `each withheld state offers the remedy its own sentence names`() {
        val denied = SettingsScreen(alerts = true, mentions = true)
            .withNotificationPermission(dev.swarm.phone.runtime.PermissionState.DENIED)
        val blocked = SettingsScreen(alerts = true, mentions = true)
            .withNotificationPermission(dev.swarm.phone.runtime.PermissionState.PERMANENTLY_DENIED)

        assertEquals(NotificationRemedy.ASK, denied.notificationRemedy)
        assertNull(
            "the DENIED screen offers a settings redirect. The platform will still ask on this " +
                "state, so sending the user to a system screen is sending them past the prompt " +
                "that would have fixed it",
            denied.notificationRedirectLabel,
        )
        assertFalse(
            "the DENIED sentence names a control this state does not offer",
            denied.notificationsBlockedNotice.contains(SettingsScreen.OPEN_NOTIFICATION_SETTINGS),
        )

        assertEquals(NotificationRemedy.SYSTEM_SETTINGS, blocked.notificationRemedy)
        assertEquals(SettingsScreen.OPEN_NOTIFICATION_SETTINGS, blocked.notificationRedirectLabel)
        assertTrue(
            "the PERMANENTLY_DENIED sentence does not name the control beside it, so the words " +
                "and the button are two separate things a reader has to join up",
            blocked.notificationsBlockedNotice.contains(SettingsScreen.OPEN_NOTIFICATION_SETTINGS),
        )

        for (live in listOf(
            dev.swarm.phone.runtime.PermissionState.GRANTED,
            dev.swarm.phone.runtime.PermissionState.NOT_APPLICABLE,
        )) {
            val screen = SettingsScreen(alerts = true, mentions = true)
                .withNotificationPermission(live)
            assertNull("$live has nothing to remedy", screen.notificationRemedy)
            assertNull(screen.notificationRedirectLabel)
        }
        assertNull(
            "a permission nobody has resolved yet is not a withheld one",
            SettingsScreen(alerts = true, mentions = true).notificationRemedy,
        )
    }

    /**
     * The tap that turns a switch ON while the permission is askable is the ask, and nothing else.
     *
     * TURNING ONE OFF IS NOT. A user reaching for less notification does not want a permission
     * prompt, and the preference itself is honoured by the machine whether or not this handset can
     * display anything.
     */
    @Test
    fun `only a tap that turns a switch on while the permission is askable asks for it`() {
        val denied = SettingsScreen(alerts = false, mentions = true)
            .withNotificationPermission(dev.swarm.phone.runtime.PermissionState.DENIED)

        assertTrue(denied.tapAsksForPermission(turningOn = true))
        assertFalse(
            "turning a switch OFF raised a permission prompt. The user is asking for less, and " +
                "the preference reaches the machine either way",
            denied.tapAsksForPermission(turningOn = false),
        )

        for (state in listOf(
            dev.swarm.phone.runtime.PermissionState.GRANTED,
            dev.swarm.phone.runtime.PermissionState.NOT_APPLICABLE,
            dev.swarm.phone.runtime.PermissionState.PERMANENTLY_DENIED,
        )) {
            assertFalse(
                "$state asked the platform for a permission it will not prompt for (or already " +
                    "has), which spends a tap on a dialog that never appears",
                SettingsScreen(alerts = false, mentions = true)
                    .withNotificationPermission(state)
                    .tapAsksForPermission(turningOn = true),
            )
        }
    }

    /**
     * What a switch shows when the surface has to put it back.
     *
     * IT IS THE MODEL'S AND NOT THE SURFACE'S because the surface has to restore a control in three
     * places -- the tap that asked for a permission instead of persisting, a tap that arrived after
     * the runtime degraded (agents-tracker-po3x), and a change the machine refused
     * (agents-tracker-os37) -- and a second reading of the bijection at any of them is the defect
     * [toggleCategory] exists to prevent.
     */
    @Test
    fun `a switch is restored to the preference its own category holds`() {
        val screen = SettingsScreen(alerts = false, mentions = true)

        assertFalse(screen.checkedFor(PushToggle.FIRST))
        assertTrue(screen.checkedFor(PushToggle.SECOND))
    }

    // ---- agents-tracker-os37: the machine's answer, read rather than counted ------------------
    //
    // THE DEFECT. `SettingsSurface.machineAnswered` read `outcome.code.isNotEmpty()` and threw the
    // code and the message away, so ANY answer cleared `pendingSync` and removed the notice. A
    // machine that REFUSED the push_prefs command was indistinguishable on screen from one that
    // accepted it: the switch read as settled while notifications kept arriving -- which is the
    // exact failure `setAlerts`'s own KDoc says `pendingSync` exists to prevent. The gateway seals a
    // reply on every exit (`internal/remotegw/command_loop.go`: "an unanswered push_prefs leaves the
    // phone's settings screen waiting forever"), so the refusal was always there to read.

    /**
     * The three answers the screen can get, told apart by the CODE.
     *
     * `ok` is `protocol.OpOK`, which is what `mobile/app.go`'s `outcomeOf` falls back to when the
     * reply carries no `ErrorCode`; a refused push_prefs replies `protocol.OpError` and carries the
     * machine's reason in `Error`. PB-SYNC-2 claims an outcome BY OPERATION ID, never by proximity,
     * so somebody else's answer leaves this one pending -- which is the honest state, as
     * `LaunchScreen.resolve` already argues for the same reason.
     */
    @Test
    fun `the machine's answer is read as a code and not as its presence`() {
        val id = "op-push-prefs-1"

        assertEquals(
            PushSync.ACCEPTED,
            SettingsScreen.syncAnswer(OperationOutcome(id, code = "ok", message = "ok"), id),
        )
        assertEquals(
            "agents-tracker-os37: a refusal read as an acceptance. The switch then shows as " +
                "settled while the machine goes on sending what the user turned off",
            PushSync.REFUSED,
            SettingsScreen.syncAnswer(
                OperationOutcome(id, code = "error", message = "no durable preference custody"),
                id,
            ),
        )
        assertEquals(
            "an outcome with no code yet is not an answer",
            PushSync.PENDING,
            SettingsScreen.syncAnswer(OperationOutcome(id, code = "", message = ""), id),
        )
        assertEquals(
            "PB-SYNC-2: an outcome for somebody else's operation was claimed by this one",
            PushSync.PENDING,
            SettingsScreen.syncAnswer(
                OperationOutcome("op-launch-9", code = "ok", message = "ok"),
                id,
            ),
        )
    }

    /**
     * A refusal puts the switch back where the machine has it.
     *
     * Leaving it where the user dragged it is the same lie as clearing the notice: the preference
     * the machine holds is the one that decides what is delivered (PB-PUSH-8 suppresses at the
     * sender), so a switch showing the refused position is a screen reporting a setting nobody has.
     */
    @Test
    fun `a refused change puts the switches back where the machine has them`() {
        val settled = SettingsScreen(alerts = true, mentions = true)
        val pending = settled.setAlerts(false)

        val refused = pending.refused()
        assertTrue(
            "agents-tracker-os37: the switch stayed where the user dragged it after the machine " +
                "refused the change, so the screen shows a preference the machine does not hold",
            refused.alerts,
        )
        assertTrue(refused.mentions)
        assertFalse("a refusal is an answer: nothing is still pending", refused.pendingSync)
        assertTrue(refused.pendingNotice.isBlank())
    }

    /**
     * TWO FLIPS BEFORE ONE ANSWER GO BACK TO THE LAST SETTLED VALUES, not to the previous flip. The
     * surface watches one operation id at a time, so an answer that arrives after a second flip is
     * about the first: the only position the machine is known to hold is the one before either.
     */
    @Test
    fun `a refusal after several flips reverts to what the machine last confirmed`() {
        val settled = SettingsScreen(alerts = true, mentions = true)

        val refused = settled.setAlerts(false).setMentions(false).refused()

        assertTrue(refused.alerts)
        assertTrue(refused.mentions)
    }

    /** An acknowledgement clears the revert point too: what is on screen IS what the machine has. */
    @Test
    fun `an acknowledged change has nothing left to revert to`() {
        val acknowledged = SettingsScreen(alerts = true, mentions = true)
            .setAlerts(false)
            .acknowledged()

        assertFalse(acknowledged.pendingSync)
        assertFalse("the acknowledged value is the value", acknowledged.alerts)
        assertFalse(
            "a later refusal reverted to a snapshot the machine has already superseded",
            acknowledged.refused().alerts,
        )
    }

    /**
     * The two things a token reconciliation can leave behind both have words
     * (agents-tracker-xla6).
     *
     * NEITHER IS A COMMAND REFUSAL and neither routes through the error table, which is why they
     * are constants here rather than a `RoutedError`. [SettingsScreen.PUSH_TOKEN_UNRECONCILED] is
     * a fault with no remedy the user can act on -- the ordinary offline case never reaches it,
     * because `App.DeletePushToken` clears durable state and leaves the relay to the reconnect --
     * and [SettingsScreen.PUSH_TRANSPORT_ABSENT] is a property of the BUILD: this module
     * configures no Firebase project, so the re-registration half of PB-PUSH-9 cannot run at all.
     *
     * THEY MUST DIFFER, because they send the reader to different places: one says the phone will
     * catch up, the other says it will not. One sentence for both would be the same collapse
     * agents-tracker-0dij made between a permission that will be asked again and one that will
     * not.
     */
    @Test
    fun `a token that could not be reconciled says which of the two things happened`() {
        assertTrue(SettingsScreen.PUSH_TOKEN_UNRECONCILED.isNotBlank())
        assertTrue(SettingsScreen.PUSH_TRANSPORT_ABSENT.isNotBlank())
        assertNotEquals(
            "agents-tracker-xla6: a reconciliation this phone will retry on its next connection " +
                "and one that cannot happen on this build at all read as the same event",
            SettingsScreen.PUSH_TOKEN_UNRECONCILED,
            SettingsScreen.PUSH_TRANSPORT_ABSENT,
        )
    }

    /**
     * The machine's own words are what the user reads, and there is a sentence for the refusal that
     * carries none.
     *
     * `refusePushPrefs` seals its reply with `Error` set and NO error code -- "none of the six in
     * the taxonomy describes a machine-side custody failure" -- so the message is the only thing
     * that says what happened. An empty one still has to say something, or the switch snaps back
     * with no explanation at all, which is the silent failure this issue is about wearing a
     * different coat.
     */
    @Test
    fun `a refusal says what the machine said, or says that it said nothing`() {
        assertEquals(
            "the machine's reason was discarded, so the user is told a change failed and not why",
            "push_prefs: no durable preference custody configured",
            SettingsScreen.refusalNotice(
                OperationOutcome("op-1", code = "error", message = "push_prefs: no durable preference custody configured"),
            ),
        )
        assertEquals(
            SettingsScreen.SYNC_REFUSED,
            SettingsScreen.refusalNotice(OperationOutcome("op-1", code = "error", message = "")),
        )
        assertTrue(SettingsScreen.SYNC_REFUSED.isNotBlank())
    }

    // ---- agents-tracker-2yfn: the block the permission cannot see -----------
    //
    // FAILING-FIRST (TDD RED, GG-5). A user who long-presses a wake and blocks `Agent updates`
    // leaves POST_NOTIFICATIONS GRANTED -- blocking a channel does not revoke a permission -- so
    // every check this screen made answered "fine" while the framework dropped every wake. Push is
    // the sole path to a backgrounded phone (ADR-007 B16), so the product stopped working with two
    // live switches on screen and no sentence anywhere about why.
    //
    // IT IS A DISTINCT NOTICE BECAUSE THE REMEDY IS DISTINCT, which is the same argument
    // agents-tracker-0dij makes for splitting ASK from SYSTEM_SETTINGS: a channel block is undone
    // on the CHANNEL's own page, an app-level disable on the APP's, and a withheld permission by a
    // prompt. Copy that named the wrong one would read as a step the user has failed to find.

    private fun deliveryOf(
        state: dev.swarm.phone.runtime.NotificationDelivery?,
        permission: dev.swarm.phone.runtime.PermissionState =
            dev.swarm.phone.runtime.PermissionState.GRANTED,
    ): SettingsScreen = SettingsScreen(alerts = true, mentions = true)
        .withNotificationPermission(permission)
        .let { if (state == null) it else it.withNotificationDelivery(state) }

    /** The defect's own state: permission intact, channel blocked, nothing on screen said so. */
    @Test
    fun `a blocked channel is said, and the sentence names the control that fixes it`() {
        val blocked = deliveryOf(dev.swarm.phone.runtime.NotificationDelivery.CHANNEL_BLOCKED)

        assertTrue(
            "agents-tracker-2yfn: a channel the user set to None produces no notice. The " +
                "permission is GRANTED and stays GRANTED in this state, so this sentence is the " +
                "only thing in the product that can tell the owner why nothing arrives",
            blocked.deliveryBlockedNotice.isNotEmpty(),
        )
        assertEquals(
            "the way out of a blocked channel is not offered",
            SettingsScreen.OPEN_CHANNEL_SETTINGS,
            blocked.deliveryRedirectLabel,
        )
        assertTrue(
            "the notice does not name the control beside it, so the two can drift into a " +
                "sentence describing an action the screen does not offer",
            SettingsScreen.OPEN_CHANNEL_SETTINGS in blocked.deliveryBlockedNotice,
        )
    }

    /**
     * The channel's page and the app's page are two different destinations, so they are two
     * different labels: a single one would put the same words on controls that go elsewhere.
     */
    @Test
    fun `an app-level block is sent to the app's own notification screen, not the channel's`() {
        val blocked = deliveryOf(dev.swarm.phone.runtime.NotificationDelivery.APP_BLOCKED)

        assertTrue(
            "agents-tracker-2yfn: areNotificationsEnabled() is false and the screen says nothing",
            blocked.deliveryBlockedNotice.isNotEmpty(),
        )
        assertNull(
            "an app-level disable offers the CHANNEL's page, whose one switch cannot undo it",
            blocked.deliveryRedirectLabel,
        )
        assertEquals(
            "an app-level disable offers no way out at all",
            SettingsScreen.OPEN_NOTIFICATION_SETTINGS,
            blocked.notificationRedirectLabel,
        )
        assertNotEquals(
            "the two blocks share one sentence, so a user reads about a category page that is " +
                "not where their problem is",
            blocked.deliveryBlockedNotice,
            deliveryOf(dev.swarm.phone.runtime.NotificationDelivery.CHANNEL_BLOCKED)
                .deliveryBlockedNotice,
        )
    }

    /**
     * A CHANNEL NOBODY HAS INSPECTED IS NOT A BLOCKED ONE, and neither is one that delivers.
     *
     * Null is the state before anything has looked -- the same convention `notificationPermission`
     * already uses, and for the reason its own KDoc gives: claiming a state nobody checked is how a
     * screen ends up reporting a fault on a phone where nothing is wrong.
     */
    @Test
    fun `a channel that delivers, or that nobody has resolved, says nothing`() {
        for (state in listOf(null, dev.swarm.phone.runtime.NotificationDelivery.DELIVERABLE)) {
            val quiet = deliveryOf(state)
            assertEquals(
                "a delivery notice was drawn over the state `$state`, which reports no fault",
                "",
                quiet.deliveryBlockedNotice,
            )
            assertNull(quiet.deliveryRedirectLabel)
        }
    }

    /**
     * WHILE THE PERMISSION IS THE REASON, THE PERMISSION IS THE NOTICE -- one sentence, not two.
     *
     * On API 33+ the app-level notification toggle IS POST_NOTIFICATIONS, so a global disable makes
     * both facts true at once and both notices would appear together, saying the same thing twice
     * over two switches. The permission notice is the one that survives because it is the one whose
     * remedy the screen can still offer: on DENIED the platform will still prompt, and a redirect
     * shown there walks the user past the prompt that would have fixed it.
     */
    @Test
    fun `a withheld permission is reported once, not twice`() {
        for (permission in listOf(
            dev.swarm.phone.runtime.PermissionState.DENIED,
            dev.swarm.phone.runtime.PermissionState.PERMANENTLY_DENIED,
        )) {
            val screen = deliveryOf(
                dev.swarm.phone.runtime.NotificationDelivery.APP_BLOCKED,
                permission = permission,
            )
            assertTrue(
                "the permission notice went missing under $permission",
                screen.notificationsBlockedNotice.isNotEmpty(),
            )
            assertEquals(
                "agents-tracker-2yfn: under $permission the screen says the same thing twice -- " +
                    "the app-level disable and the withheld permission are one fact on API 33+, " +
                    "and two notices about it is two paragraphs over two switches",
                "",
                screen.deliveryBlockedNotice,
            )
        }
    }

    // ---- agents-tracker-u7sl: battery saver and Doze are disclosed, not discovered -----------
    //
    // FAILING-FIRST (TDD RED, GG-5). ADR-007 B16 already ruled that this "should be stated in
    // the docs rather than discovered", and B143 executes that ruling as screen copy. UNLIKE
    // `notificationsBlockedNotice` and `deliveryBlockedNotice`, this is not a fault this phone
    // detected -- battery saver and Doze delaying a push is the product working as B16 designed
    // it -- so it does not depend on `notificationPermission` or `notificationDelivery` at all.

    @Test
    fun `the push-delay disclosure is the fixed sentence, regardless of state`() {
        val settled = SettingsScreen(alerts = true, mentions = true)
        val blocked = settled
            .withNotificationPermission(dev.swarm.phone.runtime.PermissionState.PERMANENTLY_DENIED)
        val channelBlocked = settled
            .withNotificationPermission(dev.swarm.phone.runtime.PermissionState.GRANTED)
            .withNotificationDelivery(dev.swarm.phone.runtime.NotificationDelivery.CHANNEL_BLOCKED)

        for (screen in listOf(settled, blocked, channelBlocked)) {
            assertEquals(
                "agents-tracker-u7sl: the disclosure changed with state, so it read as a fault " +
                    "notice rather than the unconditional fact B16 records",
                SettingsScreen.PUSH_DELAY_DISCLOSURE,
                screen.pushDelayDisclosure,
            )
        }
    }

    @Test
    fun `the disclosure is never blank, unlike the notices it sits beside`() {
        assertTrue(
            "an empty disclosure renders as a blank line nobody wrote",
            SettingsScreen(alerts = true, mentions = true).pushDelayDisclosure.isNotEmpty(),
        )
    }
}
