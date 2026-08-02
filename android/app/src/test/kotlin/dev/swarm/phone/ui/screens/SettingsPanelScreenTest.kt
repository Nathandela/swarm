package dev.swarm.phone.ui.screens

import dev.swarm.phone.runtime.PermissionState
import dev.swarm.phone.ui.PushToggle
import dev.swarm.phone.ui.SettingsScreen
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotEquals
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

    @Test
    fun `a withheld notification permission disables every row`() {
        listOf(PermissionState.DENIED, PermissionState.PERMANENTLY_DENIED).forEach { state ->
            assertEquals(
                "a row stayed live under $state, so the screen offers a switch that changes nothing",
                listOf(false, false),
                rows(SettingsPanelScreen.of(screen(permission = state))).map { it.enabled },
            )
        }
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
}
