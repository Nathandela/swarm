package dev.swarm.phone.ui.screens

import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotEquals
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * FAILING-FIRST (TDD RED, GG-5) for the Wave R5 round-3 review fix-pack (bead
 * agents-tracker-hggx.6), screen-model half of the D3 MAJOR: the FETCH PRESETS verb's wire
 * refusal was rendered (when it rendered at all) through the LAUNCH verb's notice vocabulary,
 * so a refused fetch read "this phone is not authorized to launch sessions" -- copy about a
 * verb the user did not press. The two verbs are different reads with different remedies, so
 * the model gains `fetchNoticeFor`: the fetch verb's OWN sentences over the same named states,
 * never sharing the launch verb's copy for a state both can reach (shared copy collapses
 * distinct remedies -- noticeFor's own stated rule, applied across verbs).
 *
 * The surface half (the stale launch sentence silently overwriting the fetch refusal in the
 * same render pass, because `presetOp` was never cleared) is pinned source-level by
 * android/gate/r5_round3_ui_test.go; this suite pins the copy the fixed surface renders.
 */
class LaunchPresetRound3Test {

    @Test
    fun fetchRefusalSpeaksAboutTheFetchNotAboutLaunching() {
        for (state in listOf(
            LaunchDeliveryNotice.NOT_AUTHORIZED,
            LaunchDeliveryNotice.KILL_SWITCH,
            LaunchDeliveryNotice.REFUSED,
        )) {
            val fetch = LaunchPresetScreen.fetchNoticeFor(state)
            val launch = LaunchPresetScreen.noticeFor(state)
            assertNotEquals(
                "state $state: the fetch verb's refusal copy is the LAUNCH verb's sentence -- " +
                    "a refused fetch then claims a launch was refused, which is copy about a " +
                    "verb the user did not press",
                launch,
                fetch,
            )
            assertTrue(
                "state $state: fetch refusal \"$fetch\" never names the fetch/preset-list verb, " +
                    "so the user cannot tell WHICH ask the machine refused",
                fetch.lowercase().contains("fetch") || fetch.lowercase().contains("preset"),
            )
            assertFalse(
                "state $state: fetch refusal \"$fetch\" claims the phone may not LAUNCH -- the " +
                    "refused verb was a read",
                fetch.lowercase().contains("launch"),
            )
        }
    }

    @Test
    fun fetchKillSwitchRefusalSaysThePresetsCouldNotLoad() {
        val fetch = LaunchPresetScreen.fetchNoticeFor(LaunchDeliveryNotice.KILL_SWITCH).lowercase()
        assertTrue(
            "kill-switch fetch refusal \"$fetch\" does not say the presets could not be loaded; " +
                "the switch itself is the machine's own words, in the detail (phone refit W5.4)",
            fetch.startsWith("couldn't load presets"),
        )
    }

    @Test
    fun fetchRefusalCarriesTheMachinesOwnWords() {
        val detail = "policy: no launch preset source configured"
        val fetch = LaunchPresetScreen.fetchNoticeFor(LaunchDeliveryNotice.REFUSED, detail)
        assertTrue(
            "catch-all fetch refusal \"$fetch\" drops the machine's own words \"$detail\"; the " +
                "codes are the machine's and this side must not claim to know the whole set",
            fetch.contains(detail),
        )
    }
}
