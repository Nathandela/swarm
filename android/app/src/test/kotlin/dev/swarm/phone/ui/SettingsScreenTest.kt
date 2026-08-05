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
}
