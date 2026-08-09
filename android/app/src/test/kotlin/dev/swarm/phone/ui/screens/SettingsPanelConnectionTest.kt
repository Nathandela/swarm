package dev.swarm.phone.ui.screens

import dev.swarm.phone.ui.ClockBanner
import dev.swarm.phone.ui.MachineFreshness
import dev.swarm.phone.ui.SettingsScreen
import dev.swarm.phone.ui.StreamView
import dev.swarm.phone.ui.kit.PresenceMark
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * FAILING-FIRST (TDD RED, GG-5) for agents-tracker-nx44.3 -- the CONNECTION section, which is what
 * is left of the Machines destination once the destination itself is deleted.
 *
 * WHAT THE FOLD IS. Field test 3 (2026-08-09) put a primary tab in front of the owner whose entire
 * content was four "The X view has a gap" cards and a sentence saying this phone could not read its
 * machine's details. The owner asked what the page was for. It is deleted rather than filled in:
 * the two facts a person actually wants from it -- which computer am I attached to, and is what I
 * am looking at current -- move here, under the PAIRING section that already answers the first
 * half, and everything that was failure prose about a screen nobody could act on goes.
 *
 * WHY THE HEALTH LINE IS ONE LINE AND NOT FOUR ROWS. `LinkPanel` rendered all four repair channels
 * unconditionally and argued for it: hiding the healthy ones makes "all four are fine"
 * indistinguishable from "this screen forgot the reply channel". That argument was right for a
 * screen whose whole subject was the link, and this is a SECTION on a screen about preferences --
 * the unconditional readout it justifies is now the sync detail sheet (agents-tracker-nx44.2),
 * which draws every channel including the healthy ones and is one tap from every destination. What
 * belongs here is the fault report, and this app's standing rule for a fault report is that it is
 * silent when there is no fault (`ConnectionBanner`: "online is the only quiet state").
 *
 * WHAT IT MUST NOT DO IS COLLAPSE PB-APP-11. Presence is the RELAY's opinion and the relay is the
 * declared adversary, so the word never stands on its own: the section pairs it with the phone's
 * own freshness, through [dev.swarm.phone.ui.MachinePane]'s sentence rather than a second copy of
 * it written here.
 */
class SettingsPanelConnectionTest {

    private fun screen() = SettingsScreen(alerts = true, mentions = true)

    private fun streams(vararg gapped: String): List<StreamView> =
        listOf("journal", "terminal", "reply", "grant").map { name ->
            StreamView(stream = name, stale = name in gapped, resyncPending = false)
        }

    private fun repairing(vararg names: String): List<StreamView> =
        listOf("journal", "terminal", "reply", "grant").map { name ->
            StreamView(stream = name, stale = name in names, resyncPending = name in names)
        }

    private fun connection(
        machineName: String = "nathans-mbp",
        machineId: String = "ep-1a2b3c4d",
        presence: String = "online",
        freshness: MachineFreshness = MachineFreshness(silent = false, lastHeardUnixMs = 1_000L),
        streams: List<StreamView> = streams(),
        clock: ClockBanner = ClockBanner.of(""),
    ) = SettingsPanelScreen.connectionOf(
        machineId = machineId,
        machineName = machineName,
        presence = presence,
        freshness = freshness,
        streams = streams,
        clock = clock,
        formatTime = { "14:57" },
    )

    // ---- where the section sits -------------------------------------------

    @Test
    fun `the connection section sits under Pairing and above the preferences`() {
        val panel = SettingsPanelScreen.of(
            screen(),
            machine = "nathans-mbp",
            connection = connection(),
        )

        assertEquals(
            "PAIRING answers which computer this phone is attached to and CONNECTION answers " +
                "whether it is currently reachable, so the second qualifies the first and has to " +
                "follow it. The preferences are a different subject and trail both.",
            listOf("Pairing", "Connection", "Notifications"),
            panel.sectionHeadingsInOrder,
        )
    }

    @Test
    fun `a phone with no connection to describe carries no section, and the order is unchanged`() {
        val panel = SettingsPanelScreen.of(screen(), machine = "nathans-mbp")

        assertNull(
            "a phone that cannot read its own link was given a CONNECTION section anyway, which " +
                "would print a presence word nothing supplied",
            panel.connection,
        )
        assertEquals(listOf("Pairing", "Notifications"), panel.sectionHeadingsInOrder)
    }

    // ---- the machine, and the relay's word about it ------------------------

    @Test
    fun `the row names the machine and keeps the endpoint id in its own cell`() {
        val row = connection().machine

        assertEquals("nathans-mbp", row.name)
        assertEquals(
            "the endpoint id is a SECOND fact beside the name and row 11 gives it its own cell",
            "ep-1a2b3c4d",
            row.endpoint,
        )
    }

    @Test
    fun `a machine inside the freshness budget prints no line, and the dot speaks instead`() {
        val row = connection(presence = "online").machine

        assertEquals(
            "a healthy machine printed a sentence restating what the dot beside it already says " +
                "in colour, which is the always-on notice this app refuses everywhere else",
            "",
            row.presenceLine,
        )
        assertEquals(
            "the sentence is gone and the dot is the only thing left carrying the state, so it " +
                "must carry it in words for a screen reader",
            "Your machine is online.",
            row.presenceDescription,
        )
        assertEquals(PresenceMark.ONLINE, row.mark)
    }

    @Test
    fun `a silent machine gets the relay's word attributed to the relay`() {
        val row = connection(
            presence = "online",
            freshness = MachineFreshness(silent = true, lastHeardUnixMs = 1_000L),
        ).machine

        assertTrue(
            "PB-APP-11: the relay is the party withholding the machine's frames, so a phone that " +
                "has not heard from its machine must not print the relay's \"online\" as though " +
                "the machine had said it. Line was: ${row.presenceLine}",
            row.presenceLine.contains("Not heard from your machine since 14:57.") &&
                row.presenceLine.contains("the relay's word and not your machine's"),
        )
        assertNull(
            "the line already says the machine's state in words, so a described dot would have a " +
                "screen reader announce presence twice",
            row.presenceDescription,
        )
    }

    @Test
    fun `a word the phone does not recognise is unknown rather than reachable`() {
        assertEquals(PresenceMark.OFFLINE, connection(presence = "offline").machine.mark)
        assertEquals(
            "a presence word this phone has never seen is a word it has learned nothing from; " +
                "rendering it as anything but unknown paints a machine nobody can vouch for as " +
                "reachable",
            PresenceMark.UNKNOWN,
            connection(presence = "sort-of").machine.mark,
        )
        assertEquals(PresenceMark.UNKNOWN, connection(presence = "").machine.mark)
    }

    // ---- the channels, as ONE line ----------------------------------------

    @Test
    fun `every channel current says nothing at all`() {
        assertEquals(
            "a section that reports \"all four channels are fine\" on every visit is a warning " +
                "over a working system, which trains the reader to skip the one that matters",
            "",
            connection().health,
        )
    }

    @Test
    fun `one channel with a hole is named, in the wire's own word`() {
        assertEquals(
            "The journal view has a gap.",
            connection(streams = streams("journal")).health,
        )
    }

    @Test
    fun `two channels are one sentence and not two`() {
        assertEquals(
            "the four gap cards are what the fold deletes; naming the channels in one line is " +
                "what replaces them, and the per-channel detail is the sync sheet's",
            "The journal and terminal views have gaps.",
            connection(streams = streams("journal", "terminal")).health,
        )
    }

    @Test
    fun `three channels keep the list readable`() {
        assertEquals(
            "The journal, terminal and reply views have gaps.",
            connection(streams = streams("journal", "terminal", "reply")).health,
        )
    }

    @Test
    fun `a repair in flight is still a gap`() {
        assertEquals(
            "PB-SYNC-3: a requested repair does not clear the hole -- the mark clears when the " +
                "repair LANDS -- so a repairing channel reported as current is the optimistic " +
                "clear that requirement forbids",
            "The journal view has a gap.",
            connection(streams = repairing("journal")).health,
        )
    }

    // ---- the clock, which is the phone's own fault to report ---------------

    @Test
    fun `a clock in budget is silent and a skewed one is quoted verbatim`() {
        assertEquals("", connection().clockNotice)
        assertEquals(
            "the daemon's verdict is the user-legible reason and re-wording it here would put a " +
                "second copy of a measurement's meaning on the handset",
            "Your clock is 42s ahead of your machine's.",
            connection(clock = ClockBanner.of("Your clock is 42s ahead of your machine's."))
                .clockNotice,
        )
    }

    @Test
    fun `the section is headed with the word the fold is named after`() {
        assertNotNull(connection().heading)
        assertEquals("Connection", connection().heading)
    }
}
