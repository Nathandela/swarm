package dev.swarm.phone.ui

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotEquals
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * FAILING-FIRST (TDD RED, GG-5) for PB-APP-7: settings.
 *
 * "Two coarse push toggles honored by PB-PUSH-0's trigger, plus the biometric gate toggle.
 *  UI test; toggles persist and demonstrably suppress delivery."
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
        val s = SettingsScreen(alerts = true, mentions = false, biometricGate = true)
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
        val persisted = SettingsScreen(alerts = false, mentions = true, biometricGate = false)
        val rebuilt = SettingsScreen.restore(persisted.snapshot())
        assertFalse(rebuilt.alerts)
        assertTrue(rebuilt.mentions)
        assertFalse(rebuilt.biometricGate)
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
        val s = SettingsScreen(alerts = true, mentions = true, biometricGate = true)
        val pending = s.setAlerts(false)
        assertFalse(pending.alerts)
        assertTrue("the machine has not answered yet", pending.pendingSync)
        assertTrue(pending.pendingNotice.isNotBlank())

        val settled = pending.acknowledged()
        assertFalse(settled.pendingSync)
        assertTrue(settled.pendingNotice.isBlank())
    }

    /**
     * The biometric gate toggle governs the 60-second window actions only. It can never turn OFF
     * the per-use gate on revoke, kill switch, launch and kill: PB-SEC-2's table is not a
     * preference, and a settings switch that relaxed it would be the "in-memory
     * authenticated = true" the requirement says a test must fail on, reached by consent.
     */
    @Test
    fun `disabling the biometric gate never relaxes a per-use action`() {
        val off = SettingsScreen(alerts = true, mentions = true, biometricGate = false)
        assertEquals(BiometricFreshness.PER_USE, off.freshnessFor(GatedAction.REVOKE_DEVICE))
        assertEquals(BiometricFreshness.PER_USE, off.freshnessFor(GatedAction.KILL_SESSION))
        assertEquals(BiometricFreshness.PER_USE, off.freshnessFor(GatedAction.LAUNCH))
        assertEquals(BiometricFreshness.NONE, off.freshnessFor(GatedAction.SEND_INPUT))

        val on = SettingsScreen(alerts = true, mentions = true, biometricGate = true)
        assertEquals(BiometricFreshness.WINDOW_60S, on.freshnessFor(GatedAction.SEND_INPUT))
    }

    /**
     * PB-RUN-2: with POST_NOTIFICATIONS denied the toggles are moot and the screen must say so
     * rather than presenting switches that change nothing. Below API 33 the permission does not
     * exist and notifications work, which is a third answer and not a convenience --
     * PermissionStateResolver already models exactly that.
     */
    @Test
    fun `a denied notification permission is surfaced beside the toggles`() {
        val denied = SettingsScreen(alerts = true, mentions = true, biometricGate = true)
            .withNotificationPermission(dev.swarm.phone.runtime.PermissionState.PERMANENTLY_DENIED)
        assertTrue(denied.notificationsBlockedNotice.isNotBlank())
        assertTrue(denied.togglesDisabled)

        val notApplicable = SettingsScreen(alerts = true, mentions = true, biometricGate = true)
            .withNotificationPermission(dev.swarm.phone.runtime.PermissionState.NOT_APPLICABLE)
        assertFalse("below API 33 notifications work and the toggles are live", notApplicable.togglesDisabled)
    }
}
