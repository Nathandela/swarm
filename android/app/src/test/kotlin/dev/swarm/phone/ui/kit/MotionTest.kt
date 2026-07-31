package dev.swarm.phone.ui.kit

import android.animation.ObjectAnimator
import android.animation.ValueAnimator
import android.content.Context
import android.provider.Settings
import android.util.TypedValue
import android.view.View
import androidx.test.core.app.ApplicationProvider
import org.junit.Assert.assertEquals
import org.junit.Assert.assertSame
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner

/**
 * PB-DS-8 -- "Motion: Substrate is static, and the exceptions are named."
 *
 * ADR-007 B134 decision 3 is the standing decision this asserts: no decorative animation
 * anywhere. Only the bottom sheet, the push banner (both `translateY`, 350ms,
 * `cubic-bezier(0.32, 0.72, 0, 1)`) and the streaming caret (a liveness signal, 0.9s
 * `steps(2)`) move. Reduced motion -- `Settings.Global.ANIMATOR_DURATION_SCALE == 0` -- is
 * read once, AT ANIMATOR CONSTRUCTION, and it covers every animator [Motion] builds, including
 * the toggle's, which the artifact's own `prefers-reduced-motion` selector list omits.
 *
 * EVERY REDUCED-MOTION ASSERTION HAS A SIBLING that leaves the platform setting alone and
 * expects the FULL duration. That sibling is the negative control: without it, an
 * implementation that always returned a zero-duration animator would pass every "reduced
 * motion collapses to zero" test and nothing here would catch it.
 */
@RunWith(RobolectricTestRunner::class)
class MotionTest {

    private val context get() = ApplicationProvider.getApplicationContext<Context>()

    private fun setAnimatorScale(scale: Float) {
        Settings.Global.putFloat(context.contentResolver, Settings.Global.ANIMATOR_DURATION_SCALE, scale)
    }

    @Before
    fun startFromUnreducedMotion() {
        // A prior test's setAnimatorScale(0f) must not leak into this one.
        setAnimatorScale(1f)
    }

    // ------------------------------------------------------------------
    // The named numbers. Every other assertion below is meaningless if these drift from
    // ADR-007 B134 decision 3's literal values.
    // ------------------------------------------------------------------

    @Test
    fun nav_duration_is_350ms() {
        assertEquals(350L, Motion.NAV_DURATION_MS)
    }

    @Test
    fun toggle_duration_is_150ms() {
        assertEquals(150L, Motion.TOGGLE_DURATION_MS)
    }

    @Test
    fun caret_period_is_900ms() {
        assertEquals(900L, Motion.CARET_PERIOD_MS)
    }

    // ------------------------------------------------------------------
    // Bottom sheet: translateY 100% -> 0, 350ms, the named bezier.
    // ------------------------------------------------------------------

    @Test
    fun bottomSheetEnter_runs_from_the_sheets_own_height_to_zero_on_the_named_curve() {
        val sheet = View(context)
        val animator = Motion.bottomSheetEnter(context, sheet, sheetHeightPx = 640f) as ObjectAnimator
        assertEquals(350L, animator.duration)
        assertSame(Motion.NAV_EASE, animator.interpolator)

        animator.setCurrentFraction(0f)
        assertEquals(640f, sheet.translationY, 0f)
        animator.setCurrentFraction(1f)
        assertEquals(0f, sheet.translationY, 0f)
    }

    @Test
    fun bottomSheetExit_is_bottomSheetEnter_reversed() {
        val sheet = View(context)
        val animator = Motion.bottomSheetExit(context, sheet, sheetHeightPx = 640f) as ObjectAnimator
        assertEquals(350L, animator.duration)
        assertSame(Motion.NAV_EASE, animator.interpolator)

        animator.setCurrentFraction(0f)
        assertEquals(0f, sheet.translationY, 0f)
        animator.setCurrentFraction(1f)
        assertEquals(640f, sheet.translationY, 0f)
    }

    @Test
    fun bottomSheetEnter_runs_at_full_duration_when_motion_is_not_reduced() {
        val animator = Motion.bottomSheetEnter(context, View(context), 640f)
        assertEquals(350L, animator.duration) // negative control for the test below
    }

    @Test
    fun bottomSheetEnter_collapses_to_zero_duration_when_motion_is_reduced() {
        setAnimatorScale(0f)
        val animator = Motion.bottomSheetEnter(context, View(context), 640f)
        assertEquals(0L, animator.duration)
    }

    @Test
    fun reduced_motion_is_fixed_at_construction_not_re_read_at_start() {
        setAnimatorScale(0f)
        val animator = Motion.bottomSheetEnter(context, View(context), 640f)
        setAnimatorScale(1f) // flips back AFTER the animator already exists
        assertEquals(
            "duration must be baked in when the animator is built; re-reading the setting " +
                "lazily would un-reduce an animator already handed to a caller",
            0L,
            animator.duration,
        )
    }

    // ------------------------------------------------------------------
    // Push banner: translateY -130dp -> 0, same duration and curve.
    // ------------------------------------------------------------------

    @Test
    fun pushBannerEnter_runs_from_minus130dp_to_zero_on_the_named_curve() {
        val banner = View(context)
        val expectedHiddenPx = TypedValue.applyDimension(
            TypedValue.COMPLEX_UNIT_DIP,
            -130f,
            context.resources.displayMetrics,
        )
        val animator = Motion.pushBannerEnter(context, banner) as ObjectAnimator
        assertEquals(350L, animator.duration)
        assertSame(Motion.NAV_EASE, animator.interpolator)

        animator.setCurrentFraction(0f)
        assertEquals(expectedHiddenPx, banner.translationY, 0f)
        animator.setCurrentFraction(1f)
        assertEquals(0f, banner.translationY, 0f)
    }

    @Test
    fun pushBannerExit_is_pushBannerEnter_reversed() {
        val banner = View(context)
        val expectedHiddenPx = TypedValue.applyDimension(
            TypedValue.COMPLEX_UNIT_DIP,
            -130f,
            context.resources.displayMetrics,
        )
        val animator = Motion.pushBannerExit(context, banner) as ObjectAnimator
        animator.setCurrentFraction(0f)
        assertEquals(0f, banner.translationY, 0f)
        animator.setCurrentFraction(1f)
        assertEquals(expectedHiddenPx, banner.translationY, 0f)
    }

    @Test
    fun pushBannerEnter_runs_at_full_duration_when_motion_is_not_reduced() {
        val animator = Motion.pushBannerEnter(context, View(context))
        assertEquals(350L, animator.duration) // negative control for the test below
    }

    @Test
    fun pushBannerEnter_collapses_to_zero_duration_when_motion_is_reduced() {
        setAnimatorScale(0f)
        val animator = Motion.pushBannerEnter(context, View(context))
        assertEquals(0L, animator.duration)
    }

    // ------------------------------------------------------------------
    // The generic primitives -- translateX and colorTransition -- are what a toggle's
    // thumb-slide and track crossfade are built from. Covering THEM covers the toggle without
    // this file owning the toggle's own view code: the artifact's own prefers-reduced-motion
    // selector list omits `.toggle`, and ADR-007 B134 decision 3 requires coverage anyway.
    // ------------------------------------------------------------------

    @Test
    fun translateX_runs_at_full_150ms_when_motion_is_not_reduced() {
        val animator = Motion.translateX(context, View(context), 0f, 18f, Motion.TOGGLE_DURATION_MS)
        assertEquals(150L, animator.duration) // negative control for the test below
    }

    @Test
    fun translateX_collapses_to_zero_when_motion_is_reduced() {
        setAnimatorScale(0f)
        val animator = Motion.translateX(context, View(context), 0f, 18f, Motion.TOGGLE_DURATION_MS)
        assertEquals(0L, animator.duration)
    }

    @Test
    fun translateX_runs_the_declared_endpoints() {
        val thumb = View(context)
        val animator = Motion.translateX(context, thumb, 2f, 20f, Motion.TOGGLE_DURATION_MS)
        animator.setCurrentFraction(0f)
        assertEquals(2f, thumb.translationX, 0f)
        animator.setCurrentFraction(1f)
        assertEquals(20f, thumb.translationX, 0f)
    }

    @Test
    fun colorTransition_runs_at_full_150ms_when_motion_is_not_reduced() {
        val animator = Motion.colorTransition(context, 0xFF39393D.toInt(), 0xFF30D158.toInt(), Motion.TOGGLE_DURATION_MS) {}
        assertEquals(150L, animator.duration) // negative control for the test below
    }

    @Test
    fun colorTransition_collapses_to_zero_when_motion_is_reduced() {
        setAnimatorScale(0f)
        val animator = Motion.colorTransition(context, 0xFF39393D.toInt(), 0xFF30D158.toInt(), Motion.TOGGLE_DURATION_MS) {}
        assertEquals(0L, animator.duration)
    }

    @Test
    fun colorTransition_reports_the_endpoints_through_the_callback() {
        val from = 0xFF39393D.toInt()
        val to = 0xFF30D158.toInt()
        var last = 0
        val animator = Motion.colorTransition(context, from, to, Motion.TOGGLE_DURATION_MS) { last = it }
        animator.setCurrentFraction(0f)
        assertEquals(from, last)
        animator.setCurrentFraction(1f)
        assertEquals(to, last)
    }

    // ------------------------------------------------------------------
    // The streaming caret -- a liveness signal, not decoration. steps(2) at 0.9s: two states,
    // never a fade between them.
    // ------------------------------------------------------------------

    @Test
    fun caretAlphaAt_is_a_two_state_step_not_a_fade() {
        assertEquals(1f, Motion.caretAlphaAt(0f), 0f)
        assertEquals(1f, Motion.caretAlphaAt(0.49f), 0f)
        assertEquals(0.35f, Motion.caretAlphaAt(0.5f), 0f)
        assertEquals(0.35f, Motion.caretAlphaAt(0.99f), 0f)
    }

    @Test
    fun caretAlphaAt_never_returns_a_third_value() {
        // NEGATIVE CONTROL for the test above: a smooth 1.0 -> 0.35 fade also satisfies the
        // four fixed points checked there. Sampling densely and asserting only two values ever
        // appear is what actually tells a step function from a fade.
        val seen = (0..100).map { Motion.caretAlphaAt(it / 100f) }.toSet()
        assertEquals(setOf(1f, 0.35f), seen)
    }

    @Test
    fun streamingCaretBlink_repeats_at_full_period_when_motion_is_not_reduced() {
        val animator = Motion.streamingCaretBlink(context, View(context)) as ValueAnimator
        assertEquals(900L, animator.duration) // negative control for the test below
        // NOT assertEquals(ValueAnimator.INFINITE, ...) -- see
        // robolectric_translates_the_infinite_repeat_sentinel_on_readback below for why that
        // specific comparison cannot be made to pass OR fail meaningfully under this harness.
        // "repeats at all" is what remains observable, and it is what the reduced-motion sibling
        // test negates: 0 there, non-zero here.
        assertTrue(animator.repeatCount != 0)
    }

    /**
     * MEASURED, NOT ASSUMED. `ValueAnimator.setRepeatCount(ValueAnimator.INFINITE)` followed
     * immediately by `getRepeatCount()` -- no [Motion] involved -- returns `1` under this
     * Robolectric harness, and only the exact value `-1` is affected: -2, -3, 0, 1, 2 and 5 all
     * round-trip unchanged (probed below). That makes `-1` indistinguishable from a genuinely
     * wrong `repeatCount = 1` by reading the property back in a test, on THIS harness only -- a
     * real device runs the unmodified framework class and has no such translation. Recorded so
     * nobody "fixes" [Motion.streamingCaretBlink]'s correct use of `ValueAnimator.INFINITE` into
     * a magic literal in pursuit of an equality this test tool cannot be made to report.
     */
    @Test
    fun robolectric_translates_the_infinite_repeat_sentinel_on_readback() {
        val translated = ValueAnimator.ofFloat(0f, 1f).apply { repeatCount = -1 }.repeatCount
        assertEquals(1, translated)
        for (v in listOf(-2, -3, 0, 1, 2, 5)) {
            val roundTripped = ValueAnimator.ofFloat(0f, 1f).apply { repeatCount = v }.repeatCount
            assertEquals("repeatCount=$v should round-trip unmolested", v, roundTripped)
        }
    }

    @Test
    fun streamingCaretBlink_is_static_rather_than_a_zero_duration_infinite_loop_when_reduced() {
        setAnimatorScale(0f)
        val animator = Motion.streamingCaretBlink(context, View(context)) as ValueAnimator
        assertEquals(0L, animator.duration)
        // repeatCount must ALSO collapse: a 0ms animator that still repeats INFINITE times is a
        // busy loop, not a reduced-motion caret -- the degenerate case a fix that only zeroed
        // the duration would leave behind.
        assertEquals(0, animator.repeatCount)
    }

    @Test
    fun streamingCaretBlink_leaves_the_caret_visible_up_front() {
        val caret = View(context)
        caret.alpha = 0f
        Motion.streamingCaretBlink(context, caret)
        assertEquals(1f, caret.alpha, 0f)
    }
}
