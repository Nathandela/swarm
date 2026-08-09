package dev.swarm.phone

import org.junit.Assert.assertEquals
import org.junit.Test

/**
 * FAILING-FIRST (TDD RED, GG-5) for agents-tracker-nx44.1's second item.
 *
 * `swarm_screen_top` (dimens.xml, 54dp) is gate-pinned against the design source
 * (android/gate/s22b_spacing_test.go) and had no Kotlin consumer anywhere in the app: only the
 * measured system-bar inset reached [PhoneActivity.insetTheSystemBars]'s `setPadding`, with
 * nothing under it for a window that reports a thinner inset than the design's own minimum --
 * most concretely, before this listener's first `WindowInsets` dispatch, when the padded root
 * would otherwise start at 0.
 *
 * Derivation row 20 states the scaffold's own top padding as "`screen_top` (OR THE REAL INSET)"
 * -- an explicit fallback relationship, not an instruction to add the two. [screenTopOrRealInset]
 * is that "or", pulled out as a pure function so the arithmetic is testable without driving a
 * real `WindowInsets` dispatch through Robolectric, which this app has never done and which the
 * platform makes awkward to construct in a unit test.
 */
class PhoneActivityInsetTest {

    @Test
    fun `the design's floor wins when the platform reports no inset yet`() {
        assertEquals(
            "before the first WindowInsets dispatch the measured inset is 0, and the padded " +
                "root should not start flush against the status bar while it waits for one",
            54,
            screenTopOrRealInset(measuredTopPx = 0, screenTopPx = 54),
        )
    }

    @Test
    fun `the real inset wins once the platform reports one taller than the design's floor`() {
        assertEquals(
            "row 19: screen_top 54 is a design-time preview value only -- a real handset's " +
                "inset must win once WindowInsets reports one",
            120,
            screenTopOrRealInset(measuredTopPx = 120, screenTopPx = 54),
        )
    }

    @Test
    fun `the two agree at the design's own preview value`() {
        assertEquals(54, screenTopOrRealInset(measuredTopPx = 54, screenTopPx = 54))
    }
}
