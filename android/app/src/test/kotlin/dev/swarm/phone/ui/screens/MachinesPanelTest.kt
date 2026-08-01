package dev.swarm.phone.ui.screens

import dev.swarm.phone.ui.JournalRow
import dev.swarm.phone.ui.MachineFreshness
import dev.swarm.phone.ui.MachinePane
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * FAILING-FIRST (TDD RED, GG-5) for PB-DS-9 over the MACHINES screen's model.
 *
 * WHAT THIS SUITE IS ABOUT AND WHAT IT IS NOT. [MachinePane] already decides what the machine
 * pane IS -- that presence is the relay's opinion and must be rendered with the phone's own
 * freshness beside it (PB-APP-11), and that the kill switch is read-only because
 * `handleRemoteSetControl` refuses the remote tier before consulting its backend (PB-APP-5,
 * PB-SEC-6). It is tested under those requirements. This asks the question PB-DS-9 assigns to a
 * screen: what a person READS, and which of the mock's elements this product can actually back.
 *
 * THE ABSENCES ARE THE MOST IMPORTANT ASSERTIONS HERE, because every one of them has a plausible
 * value that would render beautifully and be a lie:
 *
 *  - **The kill switch has no control.** The mock draws a toggle in a red-bordered panel;
 *    row 12's 2026-08-01 amendment deletes the control. A switch that cannot act is worse than
 *    a sentence, and this screen has no field for one to be driven by.
 *  - **`unknown` is not `offline`.** `App.Presence` has three states and the design gives the dot
 *    two colours, so the cheap reading is that anything not online is offline. The relay having no
 *    record is not the machine being unreachable, and the line beside the dot says the relay's own
 *    word rather than a translation of it.
 *  - **The device fingerprint is absent.** Row 13 gives the device row a fingerprint in
 *    `Mono.Agent` and the mock draws `key e7c2…9f31 · this device`; no facade verb returns one
 *    (mobile/screen_coverage.tsv). The half this phone knows for certain survives; the half it
 *    would have to invent does not.
 *  - **The audit log is not on this panel at all.** It needs the activity row, which is
 *    derivation row 14 and is being built beside this screen. A second one raised here would be
 *    the copy §2's reuse rule exists to prevent.
 */
class MachinesPanelTest {

    /** An Android formatter's stand-in. The model states WHAT to say, never how a time reads. */
    private val formatTime: (Long) -> String = { millis -> "at $millis" }

    private fun pane(
        presence: String = "online",
        killSwitchEngaged: Boolean = false,
        silent: Boolean = false,
        activity: List<JournalRow> = emptyList(),
    ) = MachinePane(
        machineId = "machine-endpoint-0001",
        presence = presence,
        freshness = MachineFreshness(silent = silent, lastHeardUnixMs = 1_753_900_000_000),
        pairedDeviceName = "swarm phone",
        killSwitchEngaged = killSwitchEngaged,
        activity = activity,
    )

    private fun panel(
        presence: String = "online",
        killSwitchEngaged: Boolean = false,
        silent: Boolean = false,
        activity: List<JournalRow> = emptyList(),
    ) = MachinesPanelScreen.of(pane(presence, killSwitchEngaged, silent, activity), formatTime)

    // ---- the machine row ----------------------------------------------------

    @Test
    fun `the panel is titled with the tab inventory C1 names`() {
        assertEquals("Machines", panel().title)
    }

    @Test
    fun `the row names the machine with the only identifier this product has`() {
        assertEquals("machine-endpoint-0001", panel().machine.name)
    }

    /**
     * PB-APP-11, carried into the screen. The line is the pane's own sentence rather than a
     * second wording of it: two files deciding what a user reads about one condition is how they
     * drift, and this is the condition where drift means telling someone their machine is fine.
     */
    @Test
    fun `the presence line is the pane's own sentence, freshness included`() {
        assertEquals(
            pane().presenceExplanation(formatTime),
            panel().machine.presenceLine,
        )
        val quiet = panel(silent = true).machine.presenceLine
        assertTrue(
            "a machine that has not been heard from renders the same line as a live one, so the " +
                "relay's word is standing alone -- which is PB-APP-11's whole subject: $quiet",
            quiet.contains("Not heard from your machine"),
        )
        assertTrue(
            "the qualified line does not say whose word `online` is. The relay is the declared " +
                "adversary (ADR-007 D9) and it answers the presence query: $quiet",
            quiet.contains("the relay's word"),
        )
    }

    /**
     * THE DOT IS GREEN FOR EXACTLY ONE OF THE RELAY'S THREE WORDS.
     *
     * `App.Presence` returns `unknown`, `offline` or `online` (internal/remote/relay/client.go),
     * and `unknown` means the relay has no live record -- after its own restart, for instance,
     * because presence is never persisted. Row 11 gives the dot two colours, so the tempting
     * implementation is `presence != "offline"`, which paints a machine nobody can vouch for as
     * reachable. The recessive ink is the honest answer for both non-online words: §10(c) makes
     * the same reading of the same token on `completed` -- both mean not active.
     */
    @Test
    fun `only the relay's own online reads as reachable`() {
        assertTrue(panel(presence = "online").machine.online)
        assertFalse(panel(presence = "offline").machine.online)
        assertFalse(
            "`unknown` reads as reachable. The relay having no record is not the machine being " +
                "up, and this is the state a relay returns after restarting itself",
            panel(presence = "unknown").machine.online,
        )
    }

    /** The word is the relay's, verbatim. A screen that translated it would be inventing state. */
    @Test
    fun `the relay's word reaches the screen untranslated`() {
        assertTrue(
            "the line says something other than the relay's own `unknown`, so the screen has " +
                "decided what the relay meant: ${panel(presence = "unknown").machine.presenceLine}",
            panel(presence = "unknown").machine.presenceLine.contains("unknown"),
        )
    }

    // ---- remote access, which is a statement and not a control --------------

    @Test
    fun `remote access is a titled statement of the daemon-side switch`() {
        assertEquals("Remote access", panel().remoteAccess.title)
        assertTrue(
            "the panel does not carry the pane's own sentence about the switch, so a second " +
                "wording of one condition now exists: ${panel().remoteAccess.body}",
            panel().remoteAccess.body.startsWith(pane().killSwitchExplanation),
        )
    }

    @Test
    fun `the body changes with the switch and says who can move it`() {
        val off = panel(killSwitchEngaged = true).remoteAccess.body
        val on = panel(killSwitchEngaged = false).remoteAccess.body

        assertTrue(
            "an engaged kill switch reads the same as a disengaged one, so the panel reports " +
                "nothing: $off",
            off != on,
        )
        assertTrue(
            "the panel does not say the switch is the machine owner's. The phone can never set " +
                "it -- protocol/server.go refuses the remote tier before consulting its backend " +
                "-- and a status line that leaves that out reads as a control someone forgot: $off",
            off.contains("Only the machine's owner"),
        )
    }

    /**
     * Row 12's inline command, and the half of it the mock gets wrong.
     *
     * `swarm remote off` and `swarm remote on` are both real verbs (`cmd/swarm/remote.go`). The
     * mock prints the first unconditionally, which is an instruction that does nothing for the
     * user who is reading this panel BECAUSE their remote control is off. The command rendered is
     * the one that moves the switch from where it actually is, and it must occur in the body or
     * the component cannot mark it.
     */
    @Test
    fun `the command is the verb that moves the switch from the state it is in`() {
        val engaged = panel(killSwitchEngaged = true).remoteAccess
        val open = panel(killSwitchEngaged = false).remoteAccess

        assertEquals("swarm remote on", engaged.command)
        assertEquals("swarm remote off", open.command)
        listOf(engaged, open).forEach { row ->
            assertTrue(
                "the command `${row.command}` is not in the body it is supposed to be marked " +
                    "inside: ${row.body}",
                row.body.contains(row.command),
            )
        }
    }

    // ---- the paired device ---------------------------------------------------

    @Test
    fun `the paired device is named, with a revoke label and a revoke sentence`() {
        val device = panel().pairedDevice
        assertEquals("Paired devices", panel().pairedDevicesHeading)
        assertEquals("swarm phone", device.name)
        assertEquals("Revoke", device.revokeLabel)
        assertEquals("Revoke swarm phone, this device", device.revokeDescription)
    }

    /**
     * Row 13's second cell is a fingerprint, and the fingerprint has no source.
     *
     * `mobile/screen_coverage.tsv` gives this screen `App.Presence`, `App.RevokeThisDevice` and
     * `App.KillSwitchEngaged`; nothing on that list returns a key fingerprint, and neither does
     * [MachinePane]. A plausible-looking one -- a hash of the device name, the machine id's first
     * eight characters -- would render exactly like the real thing and be the defect ADR-007 B135
     * names. So the cell is ABSENT: the model has no field for it, and the half of the mock's
     * string that IS a fact ("this device") is on the control's description, where it says which
     * device the irreversible action destroys.
     */
    @Test
    fun `no fingerprint is invented for the device the product cannot fingerprint`() {
        val device = panel().pairedDevice
        assertFalse(
            "the device row claims a key: $device. Nothing on this handset can compute one, so " +
                "whatever is in there was made up",
            (device.name + device.revokeLabel + device.revokeDescription).contains("key"),
        )
        assertTrue(
            "the revoke control does not say it acts on this handset. It is the one irreversible " +
                "thing on the screen and its label is a single word: ${device.revokeDescription}",
            device.revokeDescription.contains("this device"),
        )
    }

    // ---- what is deliberately not here --------------------------------------

    /**
     * The audit log belongs to the activity row, which is a different component and a different
     * agent's slice. A screen that raised its own would be the second `.arow` in the tree.
     */
    @Test
    fun `the activity log does not reach this panel`() {
        val busy = panel(
            activity = listOf(
                JournalRow(cursor = 3, sessionId = "swarm", type = "launch", group = "working"),
                JournalRow(cursor = 4, sessionId = "dotfiles", type = "approval", group = "needs_input"),
            ),
        )
        assertEquals(
            "the panel changed when the journal did, so the audit log is being rendered here as " +
                "well as by the activity screen",
            panel(),
            busy,
        )
    }
}
