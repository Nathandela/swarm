package dev.swarm.phone.ui.screens

import dev.swarm.phone.ui.SettingsScreen
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Test

/**
 * FAILING-FIRST (TDD RED, GG-5) for [SettingsPanelScreen.of] carrying the paired-machine row
 * [PairedMachineRowTest] pins on its own.
 *
 * WHY A ROW WITH NO SECTION TO SIT IN IS THE SAME BUG AGAIN. agents-tracker-64rf exists because
 * the pairing control was correctly built and correctly wired, and an owner still could not find
 * it on a real handset -- it was composed below the inbox list with nothing on screen saying it
 * was there. A [PairedMachineRow] that [SettingsPanelScreen.of] never reaches a section for
 * reproduces exactly that failure one layer up: a component that exists and renders nothing an
 * owner can find. This suite is what makes "the row is on the Settings screen" a checkable fact
 * rather than a claim.
 *
 * WHY [SettingsPanel] GAINS A NEW FIELD RATHER THAN A ROW SQUEEZED INTO [SettingsSection]. That
 * type's `rows` is `List<SettingsRow>`, and [SettingsPanelScreenTest] already reads it that way
 * (`panel.sections.flatMap { it.rows }`) -- untouched, because this suite may not edit an existing
 * test to make room for itself. Reshaping `SettingsSection` into something that can also hold a
 * [PairedMachineRow] would break that file's compilation for a reason that has nothing to do with
 * what it tests. [SettingsPanel.machineSection] is additive instead: a new, independent,
 * nullable field, so every existing call site and every existing assertion is exactly as true as
 * it was before this file existed.
 *
 * WHY NULL MEANS UNPAIRED AND `""` MEANS PAIRED-BUT-UNREADABLE, AND THE TWO ARE NOT THE SAME
 * CASE. [PairingPanelScreen.titleFor] already treats an empty machine name as "paired, but this
 * phone cannot read the name" -- it renders `Paired`, not nothing. This screen asks a different
 * question first: IS there a pairing to show a row about at all. `machine: String? = null` on
 * [SettingsPanelScreen.of] answers that question independently of what the name turns out to be,
 * which is what lets `of(settings)` -- every existing single-argument call -- keep meaning
 * "unpaired" without a caller having to say so.
 *
 * WHERE THE SECTION SITS: FIRST, ABOVE NOTIFICATIONS. Inventory C6 (`docs/research/
 * remote-control-mock.html`'s `renderSettings`) has no opinion here -- it draws `Notifications`
 * and the now-void `Security` and nothing about a paired machine, because the entry point moving
 * onto this screen is agents-tracker-64rf's decision, not the retired mock's. Absent an inventory
 * instruction, "which computer am I attached to" is the fact this screen exists to answer for an
 * owner who could not find it at all; it leads, not trails. [SettingsPanel.sectionHeadingsInOrder]
 * is the one place that order is a fact something can read rather than an accident of two fields
 * happening to be declared in a particular sequence.
 */
class SettingsPanelMachineSectionTest {

    private fun screen(alerts: Boolean = true, mentions: Boolean = true) =
        SettingsScreen(alerts = alerts, mentions = mentions)

    // ---- the row reaches a section ------------------------------------------

    @Test
    fun `a paired phone's panel carries a machine section holding the row`() {
        val panel = SettingsPanelScreen.of(screen(), machine = "nathans-mbp")

        assertEquals(PairedMachineRowScreen.of("nathans-mbp"), panel.machineSection?.row)
    }

    @Test
    fun `a paired phone whose machine name cannot be read still gets the section, honestly`() {
        // Paired, not unreadable-as-unpaired: PairingSurface.machineOf answers "" rather than a
        // placeholder for exactly this case, and this screen must not read "" as "no pairing".
        val panel = SettingsPanelScreen.of(screen(), machine = "")

        assertEquals("Paired", panel.machineSection?.row?.label)
    }

    // ---- the section's own heading -------------------------------------------

    @Test
    fun `the machine section is headed Pairing`() {
        // The word this feature is already named after everywhere else in this app --
        // PairingSurface, PairingFlow, PairingPanel, and the "Pair a computer" step title itself
        // -- so a reader who came from that flow meets the same word again rather than a new one
        // ("Computer", "Device") invented for this one heading alone.
        assertEquals(
            "Pairing",
            SettingsPanelScreen.of(screen(), machine = "nathans-mbp").machineSection?.heading,
        )
    }

    // ---- where the section sits ------------------------------------------------

    @Test
    fun `the machine section renders before Notifications`() {
        val panel = SettingsPanelScreen.of(screen(), machine = "nathans-mbp")

        assertEquals(
            listOf("Pairing", "Notifications"),
            panel.sectionHeadingsInOrder,
        )
    }

    @Test
    fun `an unpaired phone's section order is unchanged from before this row existed`() {
        assertEquals(
            listOf("Notifications"),
            SettingsPanelScreen.of(screen()).sectionHeadingsInOrder,
        )
    }

    // ---- an unpaired phone gets no row at all -----------------------------------

    @Test
    fun `an unpaired phone's panel carries no machine section`() {
        val panel = SettingsPanelScreen.of(screen(), machine = null)

        assertNull(
            "a phone with no pinned machine was given a replace-this-computer control, which " +
                "promises an action BeginPairing already permits nobody to take -- there is " +
                "nothing paired to replace",
            panel.machineSection,
        )
    }

    @Test
    fun `omitting the machine argument means unpaired, the same as every call site before this row existed`() {
        assertNull(SettingsPanelScreen.of(screen()).machineSection)
    }

    // ---- the addition changes nothing it does not own ---------------------------

    @Test
    fun `the machine section leaves the existing toggle sections untouched`() {
        val withMachine = SettingsPanelScreen.of(screen(), machine = "nathans-mbp").sections
        val withoutMachine = SettingsPanelScreen.of(screen()).sections

        assertEquals(withoutMachine, withMachine)
    }
}
