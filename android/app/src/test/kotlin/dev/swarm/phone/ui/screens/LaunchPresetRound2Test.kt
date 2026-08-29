package dev.swarm.phone.ui.screens

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * FAILING-FIRST (TDD RED, GG-5) for the Wave R5 round-2 review fix-pack (bead
 * agents-tracker-hggx.6), screen-model half:
 *
 *  - BLOCKER 2: the OUTCOME_UNKNOWN copy promised "confirming again re-sends the same operation
 *    and can never create a second session". FALSE twice: `App.SessionLaunch` mints a FRESH
 *    operation id on every call (mobile/commands.go newOperationID, crypto/rand) and no facade
 *    path re-sends a prior id, so a re-confirm IS a new operation and deterministically creates
 *    a second session; and even under one id, launch.go's own W4 ceiling documents that a
 *    re-drive against a live orphan spawns a second agent. The honest copy warns the user that a
 *    re-confirm is NEW and can duplicate -- it must never promise idempotence the machine does
 *    not provide (the D3 defect class: copy the code cannot honor).
 *  - The FIRST-RUN resolver state: before the machine has answered a launch_presets reply the
 *    phone knows neither its tier nor the preset list. An empty tier string is that state --
 *    NOT a granted button (fail closed) and NOT a TIER_FORBIDS slander of a phone whose only
 *    problem is that it has not asked yet. It resolves FETCHING, whose copy names the fetch
 *    remedy.
 *  - `noticeStateFor(code)`: the wire refusal codes resolve to their OWN named states -- the
 *    mapping the composition uses to claim an outcome by operation id and render it, kept in
 *    the model where this suite can drive it (PB-DS-9). An unrecognised refusal code resolves
 *    REFUSED (the machine's own words ride the detail slot), never silence and never an
 *    invented success.
 */
class LaunchPresetRound2Test {

    // ------------------------------------------------------------- honest OUTCOME_UNKNOWN copy

    @Test
    fun outcomeUnknownNeverPromisesAnIdempotentReconfirm() {
        val notice = LaunchPresetScreen.noticeFor(LaunchDeliveryNotice.OUTCOME_UNKNOWN).lowercase()
        assertFalse(
            "outcome_unknown copy \"$notice\" says \"never\" -- it promised a guarantee the machine " +
                "does not make (a re-confirm mints a fresh operation id and can duplicate)",
            notice.contains("never"),
        )
        assertFalse(
            "outcome_unknown copy \"$notice\" claims the re-confirm re-sends the \"same operation\"; " +
                "App.SessionLaunch mints a fresh id on every call",
            notice.contains("same operation"),
        )
        assertTrue(
            "outcome_unknown copy \"$notice\" must send the reader to check before trying again",
            notice.contains("before trying again"),
        )
    }

    // ------------------------------------------------------------- first-run resolver state

    @Test
    fun unknownTierIsTheFirstRunFetchStateNotAGrantedButton() {
        val state = LaunchPresetScreen.launchAvailabilityFor(
            online = true, tier = "", killSwitchOn = true, presetCount = 0,
        )
        assertEquals(
            "an empty tier is the FIRST-RUN state (the machine has not answered launch_presets " +
                "yet); it must be its own named state, not a TIER_FORBIDS slander and not a button",
            LaunchAvailability.FETCHING,
            state,
        )
        val copy = LaunchPresetScreen.noticeFor(LaunchAvailability.FETCHING)
        assertTrue("the first-run state needs its own sentence", copy.isNotEmpty())
    }

    @Test
    fun offlineStillWinsOverTheFirstRunState() {
        assertEquals(
            LaunchAvailability.OFFLINE,
            LaunchPresetScreen.launchAvailabilityFor(
                online = false, tier = "", killSwitchOn = true, presetCount = 0,
            ),
        )
    }

    @Test
    fun aTypodTierStillFailsClosed() {
        assertEquals(
            "a NON-EMPTY unrecognised tier string is a typo, not first-run; it must fail closed",
            LaunchAvailability.TIER_FORBIDS,
            LaunchPresetScreen.launchAvailabilityFor(
                online = true, tier = "fulll", killSwitchOn = true, presetCount = 3,
            ),
        )
    }

    // ------------------------------------------------------------- wire code -> notice state

    @Test
    fun wireRefusalCodesResolveToTheirOwnNoticeStates() {
        // The codes are wire literals held here for SwarmErrorTokens' reason: the unit-test JVM
        // does not load the AAR, so this side cannot read the Go constants.
        assertEquals(LaunchDeliveryNotice.STALE_PRESET, LaunchPresetScreen.noticeStateFor("stale_preset"))
        assertEquals(LaunchDeliveryNotice.UNKNOWN_PRESET, LaunchPresetScreen.noticeStateFor("unknown_preset"))
        assertEquals(LaunchDeliveryNotice.KILL_SWITCH, LaunchPresetScreen.noticeStateFor("kill_switch"))
        assertEquals(LaunchDeliveryNotice.NOT_AUTHORIZED, LaunchPresetScreen.noticeStateFor("not_authorized"))
    }

    @Test
    fun theReplyOpIsTheAppliedCodeAndEmptyIsPending() {
        // mobile/app.go outcomeOf falls back to Control.Op when ErrorCode is empty, so a
        // successful session_launch's outcome code IS the reply op string.
        assertEquals(LaunchDeliveryNotice.APPLIED, LaunchPresetScreen.noticeStateFor("session_launch"))
        assertEquals(LaunchDeliveryNotice.PENDING, LaunchPresetScreen.noticeStateFor(""))
    }

    @Test
    fun anUnrecognisedRefusalIsRefusedNeverSilentNeverSuccess() {
        val state = LaunchPresetScreen.noticeStateFor("policy")
        assertEquals(
            "a refusal code this screen has no named state for must still be a VISIBLE refusal " +
                "(the machine's words ride the detail slot)",
            LaunchDeliveryNotice.REFUSED,
            state,
        )
        val notice = LaunchPresetScreen.noticeFor(LaunchDeliveryNotice.REFUSED, "cwd is not under an allowed root")
        assertTrue("the machine's own words must survive into the sentence", notice.contains("cwd is not under an allowed root"))
    }
}
