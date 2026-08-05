package dev.swarm.phone.ui.screens

import dev.swarm.phone.runtime.PermissionState
import dev.swarm.phone.ui.PushToggle
import dev.swarm.phone.ui.SettingsScreen
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * FAILING-FIRST (TDD RED, GG-5) for PB-DS-9 over the SETTINGS screen's model.
 *
 * WHAT THIS SUITE IS ABOUT AND WHAT IT DELIBERATELY IS NOT. [SettingsScreen] already decides
 * what a push preference IS -- what survives a process death, what an unacknowledged change
 * means, what a withheld POST_NOTIFICATIONS makes true -- and it is tested under PB-APP-7. This
 * asks the other question, the one PB-DS-9 assigns to a screen: what a person READS. The section
 * heading, the label and sublabel on each row, which preference each row is bound to, and what
 * the panel says when the switches are inert.
 *
 * EVERY STRING BELOW IS THE RECORDED COPY. Inventory C6 draws the settings screen as a
 * `Notifications` section over `Needs your decision` / `Approvals and blocked prompts` and
 * `Task done` / `Completions and failures`. This suite asserts those words rather than a
 * paraphrase, because the copy is the artifact's and re-wording it here would make the screen
 * agree with the test instead of with the design.
 *
 * THE ABSENCES ARE ASSERTED TOO, and that is the point of the section-count claim. C6 draws
 * three more rows this product cannot back: `Quiet hours` has no field in the shipped preference
 * model, `Require Face ID to approve` was voided by ADR-007 B133 (and
 * `docs/design/substrate-components.md` §8.8 says so), and the `End-to-end encryption` status
 * row has no live source on the wire. Shipping any of them would be a control wired to nothing,
 * which is worse than a gap because it looks finished.
 */
class SettingsPanelScreenTest {

    private fun screen(
        alerts: Boolean = true,
        mentions: Boolean = true,
        permission: PermissionState? = PermissionState.GRANTED,
    ) = SettingsScreen(alerts = alerts, mentions = mentions).let { base ->
        if (permission == null) base else base.withNotificationPermission(permission)
    }

    private fun rows(panel: SettingsPanel) = panel.sections.flatMap { it.rows }

    // ---- the composition --------------------------------------------------

    @Test
    fun `the panel is titled with the screen inventory C6 names`() {
        assertEquals("Settings", SettingsPanelScreen.of(screen()).title)
    }

    @Test
    fun `notifications is the only section, because the other C6 rows have nothing behind them`() {
        // C6 also draws a Security section. Its first row is void post-B133 and its second has
        // no live source, so a heading over them would be a section the product cannot fill.
        assertEquals(
            listOf("Notifications"),
            SettingsPanelScreen.of(screen()).sections.map { it.heading },
        )
    }

    @Test
    fun `the rows carry C6's recorded labels in C6's order`() {
        assertEquals(
            listOf("Needs your decision", "Task done"),
            rows(SettingsPanelScreen.of(screen())).map { it.label },
        )
    }

    @Test
    fun `each row carries the sublabel C6 records under its label`() {
        assertEquals(
            listOf("Approvals and blocked prompts", "Completions and failures"),
            rows(SettingsPanelScreen.of(screen())).map { it.sublabel },
        )
    }

    @Test
    fun `the two rows are bound to two different preferences`() {
        // The bijection [SettingsScreen] argues for, at the layer that can break it: two rows
        // wired to one preference leaves the other category unreachable and the user cannot tell.
        val bound = rows(SettingsPanelScreen.of(screen())).map { it.toggle }

        assertEquals(2, bound.size)
        assertNotEquals("both rows drive the same push preference", bound[0], bound[1])
        assertEquals(PushToggle.FIRST, bound[0])
        assertEquals(PushToggle.SECOND, bound[1])
    }

    // ---- the rows report what was persisted, never a default ---------------

    @Test
    fun `a row renders the preference that was persisted`() {
        assertEquals(
            listOf(false, true),
            rows(SettingsPanelScreen.of(screen(alerts = false, mentions = true)))
                .map { it.checked },
        )
        assertEquals(
            listOf(true, false),
            rows(SettingsPanelScreen.of(screen(alerts = true, mentions = false)))
                .map { it.checked },
        )
    }

    @Test
    fun `the row announces its label and its sublabel together`() {
        // Derivation row 15: "the whole row is one >=48 dp target when it carries a toggle". A
        // row that is one target is one accessibility node, so the sublabel has to be inside its
        // description or a screen reader user hears the heading and never the qualifier.
        val row = rows(SettingsPanelScreen.of(screen())).first()

        assertTrue(
            "the row's description omits its own label: ${row.description}",
            row.description.contains(row.label),
        )
        assertTrue(
            "the row's description omits the sublabel, so the only place `Approvals and blocked " +
                "prompts` exists is a second TextView the row's own target has swallowed",
            row.description.contains(row.sublabel),
        )
    }

    // ---- what the panel says when the switches are inert -------------------

    /**
     * REWRITTEN BY agents-tracker-0dij, and what it used to say is worth stating because the old
     * assertion was the defect.
     *
     * It read `listOf(DENIED, PERMANENTLY_DENIED).forEach { ... listOf(false, false) }` -- both
     * denials disable both rows -- and that is the closed loop the bug report describes.
     * `PermissionStateResolver` answers `!hasAskedBefore -> DENIED`, so a fresh API 33+ install is
     * in DENIED before anyone has been asked anything; the app's ONLY POST_NOTIFICATIONS request is
     * issued from the switch's own tap; and Android delivers no tap to a disabled control. Disabled
     * on DENIED therefore means the permission can never be requested for the life of the install,
     * which is exactly what the owner reported ("the toggles do nothing").
     *
     * So the row's live/dead split moves to the one state that is really dead: PERMANENTLY_DENIED,
     * where the platform will not prompt again and [SettingsPanel.permissionRedirectLabel] is what
     * the screen offers instead.
     */
    @Test
    fun `only a permanently denied permission disables the rows`() {
        assertEquals(
            "agents-tracker-0dij: a row is dead under DENIED. That is the state a fresh install " +
                "is in, and the tap on the row's own switch is the app's only way to ask for the " +
                "permission -- a disabled control receives no tap, so nothing can ever ask",
            listOf(true, true),
            rows(SettingsPanelScreen.of(screen(permission = PermissionState.DENIED)))
                .map { it.enabled },
        )
        assertEquals(
            "a row stayed live under PERMANENTLY_DENIED, so the screen offers a switch that " +
                "changes nothing and no way to fix it",
            listOf(false, false),
            rows(SettingsPanelScreen.of(screen(permission = PermissionState.PERMANENTLY_DENIED)))
                .map { it.enabled },
        )
    }

    /**
     * The panel carries the model's redirect, on the one state that has one.
     *
     * IT IS A LABEL AND NOT A CONTROL, for the reason the toggle and the replace chip are
     * parameters: pressing it leaves the app, so it carries PB-SEC-12 clause 1's touch filter and
     * an identity that survives a redraw, and both of those are `SettingsSurface`'s. What the model
     * owes is the words and the decision to offer them at all.
     */
    @Test
    fun `a permanently denied permission is the one state that offers a way out`() {
        val blocked = screen(permission = PermissionState.PERMANENTLY_DENIED)
        assertEquals(
            "the panel invented its own wording for the redirect instead of carrying the model's",
            blocked.notificationRedirectLabel,
            SettingsPanelScreen.of(blocked).permissionRedirectLabel,
        )

        for (state in listOf(PermissionState.DENIED, PermissionState.GRANTED, PermissionState.NOT_APPLICABLE)) {
            assertNull(
                "$state offers a settings redirect. On DENIED the platform still prompts, so a " +
                    "redirect walks the user past the prompt that would have fixed it; on the " +
                    "other two there is nothing to fix",
                SettingsPanelScreen.of(screen(permission = state)).permissionRedirectLabel,
            )
        }
        assertNull(SettingsPanelScreen.of(screen(permission = null)).permissionRedirectLabel)
    }

    @Test
    fun `a granted permission leaves both rows live`() {
        assertEquals(
            listOf(true, true),
            rows(SettingsPanelScreen.of(screen(permission = PermissionState.GRANTED)))
                .map { it.enabled },
        )
    }

    @Test
    fun `the panel repeats the model's own reason for a dead switch`() {
        val settings = screen(permission = PermissionState.PERMANENTLY_DENIED)

        assertEquals(
            "the panel invented its own wording for a blocked permission instead of carrying " +
                "the model's, so two files now decide what the user reads",
            listOf(settings.notificationsBlockedNotice),
            SettingsPanelScreen.of(settings).notices,
        )
    }

    @Test
    fun `a change the machine has not acknowledged is reported`() {
        val settings = screen().setAlerts(false)

        assertEquals(
            listOf(settings.pendingNotice),
            SettingsPanelScreen.of(settings).notices,
        )
    }

    @Test
    fun `a blocked permission and a pending change are both said, blocked first`() {
        // Both are true at once the moment someone flips a switch that is already inert, and the
        // order is not arbitrary: the blocked notice is the reason nothing will happen, and the
        // pending notice is about a change that will not take effect until it is fixed.
        val settings = screen(permission = PermissionState.DENIED).setAlerts(false)

        assertEquals(
            listOf(settings.notificationsBlockedNotice, settings.pendingNotice),
            SettingsPanelScreen.of(settings).notices,
        )
    }

    @Test
    fun `a settled, permitted screen says nothing extra`() {
        assertEquals(
            "the panel drew a notice over a screen with nothing to report -- an empty line of " +
                "body copy under two switches reads as a warning nobody wrote",
            emptyList<String>(),
            SettingsPanelScreen.of(screen()).notices,
        )
    }

    @Test
    fun `a permission nobody has resolved yet is not a blocked one`() {
        // `notificationPermission` is null until something has actually checked. Treating that as
        // denied would tell a user their switches are dead before anyone asked the platform.
        val panel = SettingsPanelScreen.of(screen(permission = null))

        assertEquals(emptyList<String>(), panel.notices)
        assertEquals(listOf(true, true), rows(panel).map { it.enabled })
    }

    // ---- agents-tracker-2yfn: the blocked channel reaches the panel ---------
    //
    // FAILING-FIRST (TDD RED, GG-5). The model can decide a channel is blocked and be entirely
    // right about it while nothing on screen says so -- which is this screen's own recorded defect
    // class ("the model is beautiful and nothing renders it"). What this suite owes is that the
    // panel carries the model's sentence and the model's label, and re-words neither.

    private fun blockedChannel() = screen()
        .withNotificationDelivery(dev.swarm.phone.runtime.NotificationDelivery.CHANNEL_BLOCKED)

    @Test
    fun `a blocked channel is carried to the panel in the model's own words`() {
        val settings = blockedChannel()

        assertEquals(
            "agents-tracker-2yfn: the panel drops the delivery notice, so a phone whose channel " +
                "is blocked shows two live switches and no explanation -- the permission is " +
                "GRANTED in this state, so no other notice can appear either",
            listOf(settings.deliveryBlockedNotice),
            SettingsPanelScreen.of(settings).notices,
        )
        assertEquals(
            "the panel names no channel redirect, so the sentence describes a control that is " +
                "not on screen",
            settings.deliveryRedirectLabel,
            SettingsPanelScreen.of(settings).deliveryRedirectLabel,
        )
    }

    @Test
    fun `a blocked channel and a pending change are both said, blocked first`() {
        // The same order and the same reason as the permission case above: the block is why nothing
        // will happen, and the pending notice is about a change that cannot take effect until it is
        // fixed. Read the other way round the user is told what has been saved before being told it
        // is inert.
        val settings = blockedChannel().setAlerts(false)

        assertEquals(
            listOf(settings.deliveryBlockedNotice, settings.pendingNotice),
            SettingsPanelScreen.of(settings).notices,
        )
    }

    @Test
    fun `a channel nobody has resolved yet offers no redirect`() {
        assertNull(
            "a channel redirect was offered over a phone whose channel nothing has inspected, so " +
                "its owner is sent to a system page about a fault nobody found",
            SettingsPanelScreen.of(screen()).deliveryRedirectLabel,
        )
    }
}
