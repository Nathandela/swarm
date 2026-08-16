package dev.swarm.phone.ui.screens

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * FAILING-FIRST (TDD RED, GG-5) for Wave R5 deliverable 4 (bead agents-tracker-hggx.6): the
 * phone remote-launch flow's SCREEN MODEL, in the module's established pure-function shape
 * (PairOnlyScreen, TriageInboxScreen, MachinesScreen -- PB-DS-9).
 *
 * COMPILE-RED ON PURPOSE: `LaunchPresetScreen`, `LaunchAvailability`, `PresetRowModel`,
 * `LaunchConfirmationModel` and `LaunchDeliveryNotice` do not exist. This suite is the frozen
 * contract the R5 implementation must supply (the MachinesScreenTest convention).
 *
 * WHAT THE CONTRACT IS:
 *
 *  - `LaunchPresetScreen.launchAvailabilityFor(online, tier, killSwitchOn, presetCount)`: the
 *    availability RESOLVER for the NEW_SESSION affordance (playbook 4.3: "available only on a
 *    selected, online machine whose paired phone has the `full` authorization tier and whose
 *    remote kill switch is on"). Every denial is a NAMED state with its own copy -- a missing
 *    button with no reason is the D3 defect class. Zero presets is its own state: the remedy
 *    (author presets at the terminal) is different from every other denial's.
 *  - `PresetRowModel`: one selectable preset -- id, display name, provider, workspace display
 *    path, revision. The row carries the REVISION it displayed because that is what the confirm
 *    signs: the phone echoes, it never derives.
 *  - `LaunchPresetScreen.confirmationFor(...)`: the confirm sheet model with the five facts of
 *    playbook 4.3:219-220 -- machine, provider, resolved workspace display path, worktree
 *    behavior, initial-prompt presence. Nothing else is editable on the sheet: no cwd field, no
 *    env field, no argv field EXISTS in the model.
 *  - `LaunchPresetScreen.noticeFor(state, detail)`: the outcome notice -- visible SUCCESS and
 *    visible REFUSAL for the one verb, each stable refusal code with its own sentence
 *    (STALE_PRESET, UNKNOWN_PRESET, KILL_SWITCH, NOT_AUTHORIZED, OFFLINE), and the delivery
 *    states PENDING / OUTCOME_UNKNOWN rendered honestly (ADR-017 T9): outcome_unknown never
 *    claims success and never claims failure.
 */
class LaunchPresetScreenTest {

    // ------------------------------------------------------------- availability resolver

    @Test
    fun fullTierOnlineKillSwitchOnWithPresetsIsAvailable() {
        assertEquals(
            LaunchAvailability.AVAILABLE,
            LaunchPresetScreen.launchAvailabilityFor(
                online = true, tier = "full", killSwitchOn = true, presetCount = 2,
            ),
        )
    }

    @Test
    fun readTiersAreDeniedWithTheirOwnNamedReason() {
        for (tier in listOf("read_only", "read_approve")) {
            assertEquals(
                "tier $tier must deny launch with a NAMED reason, not a missing button",
                LaunchAvailability.TIER_FORBIDS,
                LaunchPresetScreen.launchAvailabilityFor(
                    online = true, tier = tier, killSwitchOn = true, presetCount = 2,
                ),
            )
        }
    }

    @Test
    fun offlineMachineDeniesBeforeAnythingElse() {
        // Offline wins even when the tier would also deny: the user's first problem is
        // reachability, and the refusal happens phone-side before anything is composed.
        assertEquals(
            LaunchAvailability.OFFLINE,
            LaunchPresetScreen.launchAvailabilityFor(
                online = false, tier = "read_only", killSwitchOn = false, presetCount = 0,
            ),
        )
    }

    @Test
    fun killSwitchOffIsItsOwnDenial() {
        assertEquals(
            LaunchAvailability.KILL_SWITCH_OFF,
            LaunchPresetScreen.launchAvailabilityFor(
                online = true, tier = "full", killSwitchOn = false, presetCount = 2,
            ),
        )
    }

    @Test
    fun zeroPresetsIsTheSetupStateNamingTheTerminalRemedy() {
        assertEquals(
            LaunchAvailability.NO_PRESETS,
            LaunchPresetScreen.launchAvailabilityFor(
                online = true, tier = "full", killSwitchOn = true, presetCount = 0,
            ),
        )
        val copy = LaunchPresetScreen.noticeFor(LaunchAvailability.NO_PRESETS)
        assertTrue(
            "the empty-preset state must point at the terminal-side authoring verb; got \"$copy\"",
            copy.contains("swarm remote presets"),
        )
    }

    // ------------------------------------------------------------- confirmation sheet

    private fun row() = PresetRowModel(
        id = "preset-api",
        displayName = "API repo",
        provider = "claude",
        workspacePath = "/home/nathan/code/api",
        revision = "rev-1",
    )

    @Test
    fun confirmationCarriesTheFiveFactsAndTheConfirmedRevision() {
        val sheet = LaunchPresetScreen.confirmationFor(
            preset = row(),
            machineName = "buildbox",
            worktreeIsolation = true,
            promptPresent = true,
        )
        assertEquals("buildbox", sheet.machineName)
        assertEquals("claude", sheet.provider)
        assertEquals("/home/nathan/code/api", sheet.workspacePath)
        assertTrue("worktree behavior must be a rendered fact, not an implicit default", sheet.worktreeBehavior.isNotEmpty())
        assertTrue(sheet.hasInitialPrompt)
        assertEquals(
            "the sheet signs EXACTLY the revision the row displayed -- echo, never derive",
            "rev-1",
            sheet.presetRevision,
        )
    }

    // ------------------------------------------------------------- outcome notices (D3)

    @Test
    fun stalePresetRefusalHasItsOwnSentenceAndRemedy() {
        val notice = LaunchPresetScreen.noticeFor(LaunchDeliveryNotice.STALE_PRESET)
        assertTrue("stale_preset copy is empty; a refusal with no sentence is invisible", notice.isNotEmpty())
        assertFalse(
            "stale_preset must not render as a generic failure; its remedy (re-pick the preset) is its own",
            notice.equals(LaunchPresetScreen.noticeFor(LaunchDeliveryNotice.UNKNOWN_PRESET)),
        )
    }

    @Test
    fun outcomeUnknownNeverClaimsSuccessOrFailure() {
        val notice = LaunchPresetScreen.noticeFor(LaunchDeliveryNotice.OUTCOME_UNKNOWN).lowercase()
        assertTrue("outcome_unknown copy is empty; honest uncertainty still needs a sentence", notice.isNotEmpty())
        for (overclaim in listOf("launched", "started", "running", "failed", "refused")) {
            assertFalse(
                "outcome_unknown copy \"$notice\" claims \"$overclaim\"; the machine could not prove " +
                    "the outcome and the screen must not invent one",
                notice.contains(overclaim),
            )
        }
    }

    @Test
    fun everyStableRefusalStateHasDistinctCopy() {
        val states = listOf(
            LaunchDeliveryNotice.STALE_PRESET,
            LaunchDeliveryNotice.UNKNOWN_PRESET,
            LaunchDeliveryNotice.KILL_SWITCH,
            LaunchDeliveryNotice.NOT_AUTHORIZED,
            LaunchDeliveryNotice.OFFLINE,
        )
        val sentences = states.map { LaunchPresetScreen.noticeFor(it) }
        assertEquals(
            "each stable refusal code needs its OWN sentence; shared copy collapses distinct remedies",
            sentences.size,
            sentences.toSet().size,
        )
    }
}
