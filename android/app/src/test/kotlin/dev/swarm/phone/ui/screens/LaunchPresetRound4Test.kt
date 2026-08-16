package dev.swarm.phone.ui.screens

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotEquals
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * FAILING-FIRST (TDD RED, GG-5) for the Wave R5 round-4 review fix-pack (bead
 * agents-tracker-hggx.6), screen-model half of two findings.
 *
 * MEDIUM 3 (D3 class, narrowed but NOT closed by round 3): the two verbs still shared ONE
 * delivery line. `renderPresetFlow` wrote the fetch refusal first and the launch block
 * overwrote it unconditionally whenever `presetOp` was non-empty -- cleared only on a
 * NON-PENDING outcome. So for the whole in-flight window of any launch, EVERY fetch refusal
 * was replaced by "The launch is on its way to the machine and has not resolved yet." The
 * root cause is the shared slot, not the clearing rule: the fetch verb gets its OWN slot, so
 * one verb's answer can no longer be a function of the other verb's state at all.
 *
 * MAJOR 2 (phone half): the machine now answers an undecidable launch with the stable
 * `outcome_unknown` code (schema.CodeOutcomeUnknown), instead of handing a concurrent
 * double-driver's loser the winner's phase-1 reservation as an APPLIED success. The state
 * already exists in this model with nothing mapping to it; the mapping is what makes the
 * machine's honesty reach the user.
 *
 * The surface half (which field each block writes) is pinned source-level by
 * android/gate/r5_round4_ui_test.go, the module's established idiom for PhoneSurface
 * behaviour the JVM suite cannot reach.
 */
class LaunchPresetRound4Test {

    /** MAJOR 2: the machine's undecidable answer is rendered as undecidable, not as a refusal. */
    @Test
    fun outcomeUnknownCodeMapsToTheUndecidableState() {
        assertEquals(
            "the wire code `outcome_unknown` (schema.CodeOutcomeUnknown) fell through to REFUSED, " +
                "so a launch the machine could not decide tells the user it was refused -- and the " +
                "OUTCOME_UNKNOWN state that exists for exactly this is reachable from nothing",
            LaunchDeliveryNotice.OUTCOME_UNKNOWN,
            LaunchPresetScreen.noticeStateFor("outcome_unknown"),
        )
        assertNotEquals(
            "the undecidable sentence is the refused sentence; the user cannot tell a launch that " +
                "may be running from one the machine turned away",
            LaunchPresetScreen.noticeFor(LaunchDeliveryNotice.REFUSED),
            LaunchPresetScreen.noticeFor(LaunchDeliveryNotice.OUTCOME_UNKNOWN),
        )
    }

    /**
     * MEDIUM 3: the panel carries the fetch verb's answer in its OWN slot, so a fetch refusal
     * and an in-flight launch are both on screen -- the exact window round 3 left open.
     */
    @Test
    fun aFetchRefusalSurvivesAnInFlightLaunch() {
        val fetchRefusal = LaunchPresetScreen.fetchNoticeFor(LaunchDeliveryNotice.NOT_AUTHORIZED)
        val launchPending = LaunchPresetScreen.noticeFor(LaunchDeliveryNotice.PENDING)
        val panel = LaunchPresetPanel(
            availability = LaunchAvailability.AVAILABLE,
            availabilityNotice = "",
            rows = emptyList(),
            deliveryNotice = launchPending,
            fetchNotice = fetchRefusal,
        )
        assertEquals(
            "the in-flight launch sentence displaced the fetch refusal; while ANY launch is " +
                "pending every fetch refusal is invisible (D3: a primary control that refuses in " +
                "silence)",
            fetchRefusal,
            panel.fetchNotice,
        )
        assertEquals(launchPending, panel.deliveryNotice)
        assertTrue(
            "the two verbs' sentences are the same string; one slot cannot carry two answers",
            panel.fetchNotice != panel.deliveryNotice,
        )
    }

    /** The fetch slot is independently empty: a panel with no fetch answer says nothing about one. */
    @Test
    fun theFetchSlotIsEmptyUntilTheFetchVerbAnswers() {
        val panel = LaunchPresetPanel(
            availability = LaunchAvailability.AVAILABLE,
            availabilityNotice = "",
            rows = emptyList(),
            deliveryNotice = LaunchPresetScreen.noticeFor(LaunchDeliveryNotice.APPLIED),
        )
        assertEquals(
            "the fetch slot must default to empty -- a status line about a verb nobody pressed is " +
                "LaunchPresetView's recorded anti-pattern",
            "",
            panel.fetchNotice,
        )
    }
}
