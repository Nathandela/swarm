package dev.swarm.phone.ui.kit

import android.animation.ObjectAnimator
import android.animation.ValueAnimator
import android.content.Context
import android.provider.Settings
import android.util.TypedValue
import android.view.View
import androidx.core.view.animation.PathInterpolatorCompat
import androidx.test.core.app.ApplicationProvider
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotEquals
import org.junit.Assert.assertSame
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import kotlin.math.abs
import kotlin.math.roundToLong

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
 * EVERY NUMBER IS READ FROM THE DOCUMENT THAT DECLARES IT, through [MockCss]. Not one of them is
 * transcribed here -- see [MockCss]'s own note for what that replaced and why.
 *
 * EVERY REDUCED-MOTION ASSERTION HAS A SIBLING that leaves the platform setting alone and
 * expects the FULL duration. That sibling is the negative control: without it, an
 * implementation that always returned a zero-duration animator would pass every "reduced
 * motion collapses to zero" test and nothing here would catch it.
 *
 * AN ANIMATOR IS INSPECTED AFTER IT PLAYS, not only after it is built. A zero-duration
 * [ValueAnimator] still delivers one update, and a test that never calls `start()` cannot see what
 * that update does -- which is how a reduced-motion caret that DIMMED shipped under a test
 * asserting it stayed visible. [a_zero_duration_animator_still_delivers_one_update_at_a_full_fraction]
 * measures that behaviour directly.
 */
@RunWith(RobolectricTestRunner::class)
class MotionTest {

    private val context get() = ApplicationProvider.getApplicationContext<Context>()

    private fun setAnimatorScale(scale: Float) {
        Settings.Global.putFloat(context.contentResolver, Settings.Global.ANIMATOR_DURATION_SCALE, scale)
    }

    /** The mock is a 386x812 frame at device scale, so its CSS px is Android dp at 1:1 -- the same
     * reading `DesignScale.tokenPx` states for the token origin. */
    private fun dp(value: Float): Float =
        TypedValue.applyDimension(TypedValue.COMPLEX_UNIT_DIP, value, context.resources.displayMetrics)

    /**
     * The caret's dim alpha, reached by the join the document itself states: `.stream-caret`'s
     * `animation` names a `@keyframes` block, and that block's one stop carries the opacity.
     * Following the join rather than reading `@keyframes pulse` by a hardcoded name is what makes
     * this the caret's alpha rather than some animation's alpha.
     */
    private fun mockCaretDimAlpha(): Float {
        val animation = MockCss.declaration(".stream-caret", "animation")
        val stop = MockCss.keyframe(MockCss.animationName(animation), "50%")
        return requireNotNull(stop["opacity"]) {
            "@keyframes ${MockCss.animationName(animation)}'s 50% stop declares no opacity"
        }.toFloat()
    }

    @Before
    fun startFromUnreducedMotion() {
        // A prior test's setAnimatorScale(0f) must not leak into this one.
        setAnimatorScale(1f)
    }

    // ------------------------------------------------------------------
    // The named numbers, EACH READ FROM THE DOCUMENT IT COMES FROM.
    //
    // These five assertions used to read `assertEquals(350L, Motion.NAV_DURATION_MS)` -- a literal
    // transcribed out of the implementation, compared against the implementation. Every other
    // assertion in this file is meaningless if these drift from the design, and nothing in the
    // repository read the design at all.
    // ------------------------------------------------------------------

    @Test
    fun nav_duration_is_the_transition_the_mock_declares_on_the_banner_and_the_sheet() {
        val banner = MockCss.millis(MockCss.declaration(".banner", "transition"))
        val sheet = MockCss.millis(MockCss.declaration(".sheet", "transition"))
        assertEquals("the mock gives its two navigation surfaces one shared duration", banner, sheet)
        assertEquals(banner, Motion.NAV_DURATION_MS)
    }

    @Test
    fun nav_duration_does_not_match_a_transition_the_mock_does_not_declare() {
        // NEGATIVE CONTROL for the test above, run through MockCss.millis -- the same parse the
        // real assertion calls, fed a perturbed declaration. Without it a millis() that returned a
        // constant, or that echoed Motion's own value, would satisfy the assertion above.
        val perturbed = MockCss.millis("transform 0.5s cubic-bezier(0.32,0.72,0,1)")
        assertEquals(500L, perturbed)
        assertNotEquals(perturbed, Motion.NAV_DURATION_MS)
    }

    @Test
    fun nav_ease_is_the_cubic_bezier_the_mock_names() {
        val fromBanner = MockCss.cubicBezier(MockCss.declaration(".banner", "transition"))
        val fromSheet = MockCss.cubicBezier(MockCss.declaration(".sheet", "transition"))
        assertEquals("the banner and the sheet share one curve", fromBanner, fromSheet)
        assertEquals(
            fromBanner,
            listOf(Motion.NAV_EASE_P1X, Motion.NAV_EASE_P1Y, Motion.NAV_EASE_P2X, Motion.NAV_EASE_P2Y),
        )
    }

    @Test
    fun nav_ease_interpolates_as_the_curve_the_document_declares() {
        // The four control points above are what BUILDS the interpolator, so comparing them is a
        // comparison of inputs. This compares OUTPUTS: the curve the app runs against a curve
        // built here from the parsed values, sampled across the whole fraction range.
        val p = MockCss.cubicBezier(MockCss.declaration(".banner", "transition"))
        val fromTheDocument = PathInterpolatorCompat.create(p[0], p[1], p[2], p[3])
        for (i in 0..100) {
            val f = i / 100f
            assertEquals(
                "at fraction $f",
                fromTheDocument.getInterpolation(f),
                Motion.NAV_EASE.getInterpolation(f),
                1e-4f,
            )
        }
    }

    @Test
    fun nav_ease_is_not_indistinguishable_from_a_different_curve() {
        // NEGATIVE CONTROL for the test above, built through the same PathInterpolatorCompat.create
        // with one control point perturbed. If sampling could not tell two curves apart under this
        // harness, the comparison above would pass over any curve at all.
        val p = MockCss.cubicBezier(MockCss.declaration(".banner", "transition"))
        val perturbed = PathInterpolatorCompat.create(p[0] + 0.4f, p[1], p[2], p[3])
        val differs = (0..100).any { i ->
            val f = i / 100f
            abs(perturbed.getInterpolation(f) - Motion.NAV_EASE.getInterpolation(f)) > 1e-3f
        }
        assertTrue("a curve with a different control point must sample differently", differs)
    }

    @Test
    fun toggle_duration_is_the_transition_the_mock_declares_on_the_track_and_the_thumb() {
        val track = MockCss.millis(MockCss.declaration(".toggle", "transition"))
        val thumb = MockCss.millis(MockCss.declaration(".toggle::after", "transition"))
        assertEquals("the track's background and the thumb's slide share one duration", track, thumb)
        assertEquals(track, Motion.TOGGLE_DURATION_MS)
    }

    @Test
    fun toggle_duration_does_not_match_a_transition_the_mock_does_not_declare() {
        // NEGATIVE CONTROL, same parse, perturbed declaration.
        val perturbed = MockCss.millis("background 0.25s")
        assertEquals(250L, perturbed)
        assertNotEquals(perturbed, Motion.TOGGLE_DURATION_MS)
    }

    @Test
    fun caret_period_is_the_animation_the_mock_declares_on_the_streaming_caret() {
        val caret = MockCss.millis(MockCss.declaration(".stream-caret", "animation"))
        assertEquals(caret, Motion.CARET_PERIOD_MS)
    }

    @Test
    fun caret_period_is_not_the_working_dots_period() {
        // NEGATIVE CONTROL, and the one perturbation the document supplies itself: `.g-work` runs
        // the SAME @keyframes at a different duration. ADR-007 B134 decision 3 does not implement
        // that animation at all, and a caret that read the wrong rule would take its period.
        val workingDot = MockCss.millis(MockCss.declaration(".g-work", "animation"))
        assertEquals(1600L, workingDot)
        assertNotEquals(workingDot, Motion.CARET_PERIOD_MS)
    }

    @Test
    fun the_carets_dim_alpha_is_the_pulse_keyframes_only_declared_stop() {
        val name = MockCss.animationName(MockCss.declaration(".stream-caret", "animation"))
        assertEquals(
            "@keyframes $name declares exactly one stop. The LIT state is not a keyframe at all -- " +
                "it is the element's own opacity, which is why 1f is not read from this block",
            setOf("50%"),
            MockCss.keyframeStops(name).keys,
        )
        assertEquals(mockCaretDimAlpha(), Motion.caretAlphaAt(0.5f), 0f)
        assertEquals(1f, Motion.caretAlphaAt(0f), 0f)
    }

    @Test
    fun the_carets_dim_alpha_does_not_match_an_opacity_the_document_does_not_declare() {
        // NEGATIVE CONTROL for the test above, through Motion.caretAlphaAt -- the same function the
        // real assertion calls -- fed the document's own value perturbed. Without it, a
        // caretAlphaAt that returned whatever it was compared against would pass.
        val perturbed = mockCaretDimAlpha() + 0.1f
        assertNotEquals(perturbed.toDouble(), Motion.caretAlphaAt(0.5f).toDouble(), 1e-6)
    }

    // ------------------------------------------------------------------
    // Bottom sheet: translateY 100% -> 0, the mock's duration, the mock's curve.
    // ------------------------------------------------------------------

    @Test
    fun bottomSheetEnter_runs_from_the_sheets_own_height_to_zero_on_the_named_curve() {
        val sheet = View(context)
        val animator = Motion.bottomSheetEnter(context, sheet, sheetHeightPx = 640f) as ObjectAnimator
        assertEquals(Motion.NAV_DURATION_MS, animator.duration)
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
        assertEquals(Motion.NAV_DURATION_MS, animator.duration)
        assertSame(Motion.NAV_EASE, animator.interpolator)

        animator.setCurrentFraction(0f)
        assertEquals(0f, sheet.translationY, 0f)
        animator.setCurrentFraction(1f)
        assertEquals(640f, sheet.translationY, 0f)
    }

    @Test
    fun bottomSheetEnter_runs_at_full_duration_when_motion_is_not_reduced() {
        val animator = Motion.bottomSheetEnter(context, View(context), 640f)
        assertEquals(Motion.NAV_DURATION_MS, animator.duration) // negative control for the test below
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
    // Push banner: translateY -(own height + top inset) -> 0, same duration and curve.
    //
    // docs/design/substrate-components.md row 2 states that derivation and calls the mock's own
    // -130px a magic number. The two agree AT THE MOCK'S DIMENSIONS and nowhere else, so the tests
    // below pin both halves: the mock's figure is reproduced from the mock's own height and inset,
    // and the offset moves when the banner does.
    // ------------------------------------------------------------------

    /** The `top` the mock rests the banner at -- `banner_top` in the component doc. */
    private fun mockBannerTopDp(): Float = MockCss.px(MockCss.declaration(".banner", "top"))

    /** The hidden offset the mock hardcodes: the magic number the derivation replaces. */
    private fun mockBannerHiddenDp(): Float =
        MockCss.translateYPx(MockCss.declaration(".banner", "transform"))

    /** The banner height the mock's own two numbers imply, per the doc's `-(own height + top)`. */
    private fun mockBannerHeightDp(): Float = -mockBannerHiddenDp() - mockBannerTopDp()

    @Test
    fun pushBannerEnter_hides_by_the_banners_own_height_plus_the_top_inset() {
        // A banner whose message wraps to a second line is taller than the one the mock drew, and
        // the document's rule says it hides that much further behind the same inset. A fixed
        // -130dp does not move at all, which is the whole content of the rule.
        val tallBanner = mockBannerHeightDp() * 2
        val banner = View(context)
        val animator = Motion.pushBannerEnter(
            context,
            banner,
            bannerHeightPx = dp(tallBanner),
            topInsetPx = dp(mockBannerTopDp()),
        ) as ObjectAnimator
        animator.setCurrentFraction(0f)
        assertEquals(dp(-(tallBanner + mockBannerTopDp())), banner.translationY, 0f)
    }

    @Test
    fun the_banners_hidden_offset_moves_with_both_of_its_inputs() {
        // NEGATIVE CONTROL for the two tests either side of this one, through
        // Motion.pushBannerHiddenTranslation -- the same function pushBannerEnter calls. The
        // mock's own -130 satisfies the "at the mock's dimensions" test below by construction, so
        // the only thing that tells a derivation from a constant is perturbing each input and
        // requiring the result to follow. It must follow BOTH: an implementation that tracked the
        // height and ignored the inset would pass a one-sided version of this.
        val top = mockBannerTopDp()
        val height = mockBannerHeightDp()
        assertEquals(mockBannerHiddenDp(), Motion.pushBannerHiddenTranslation(height, top), 0f)
        assertEquals(mockBannerHiddenDp() - 24f, Motion.pushBannerHiddenTranslation(height + 24f, top), 0f)
        assertEquals(mockBannerHiddenDp() - 12f, Motion.pushBannerHiddenTranslation(height, top + 12f), 0f)
    }

    @Test
    fun pushBannerEnter_reproduces_the_mocks_own_offset_at_the_mocks_own_dimensions() {
        // The spec's example, kept as an anchor: at the mock's own height and `banner_top`, the
        // derivation must land exactly on the -130px the mock hardcodes. The magic number is
        // rejected as a CONSTANT, not as a value.
        val banner = View(context)
        val animator = Motion.pushBannerEnter(
            context,
            banner,
            bannerHeightPx = dp(mockBannerHeightDp()),
            topInsetPx = dp(mockBannerTopDp()),
        ) as ObjectAnimator
        assertEquals(Motion.NAV_DURATION_MS, animator.duration)
        assertSame(Motion.NAV_EASE, animator.interpolator)

        animator.setCurrentFraction(0f)
        assertEquals(dp(mockBannerHiddenDp()), banner.translationY, 0f)
        animator.setCurrentFraction(1f)
        assertEquals(0f, banner.translationY, 0f)
    }

    @Test
    fun pushBannerExit_is_pushBannerEnter_reversed() {
        val banner = View(context)
        val animator = Motion.pushBannerExit(
            context,
            banner,
            bannerHeightPx = dp(mockBannerHeightDp()),
            topInsetPx = dp(mockBannerTopDp()),
        ) as ObjectAnimator
        animator.setCurrentFraction(0f)
        assertEquals(0f, banner.translationY, 0f)
        animator.setCurrentFraction(1f)
        assertEquals(dp(mockBannerHiddenDp()), banner.translationY, 0f)
    }

    @Test
    fun pushBannerEnter_runs_at_full_duration_when_motion_is_not_reduced() {
        val animator = Motion.pushBannerEnter(
            context,
            View(context),
            dp(mockBannerHeightDp()),
            dp(mockBannerTopDp()),
        )
        assertEquals(Motion.NAV_DURATION_MS, animator.duration) // negative control for the test below
    }

    @Test
    fun pushBannerEnter_collapses_to_zero_duration_when_motion_is_reduced() {
        setAnimatorScale(0f)
        val animator = Motion.pushBannerEnter(
            context,
            View(context),
            dp(mockBannerHeightDp()),
            dp(mockBannerTopDp()),
        )
        assertEquals(0L, animator.duration)
    }

    // ------------------------------------------------------------------
    // The generic primitives -- translateX and colorTransition -- are what a toggle's
    // thumb-slide and track crossfade are built from. Covering THEM covers the toggle without
    // this file owning the toggle's own view code: the artifact's own prefers-reduced-motion
    // selector list omits `.toggle`, and ADR-007 B134 decision 3 requires coverage anyway.
    // ------------------------------------------------------------------

    @Test
    fun translateX_runs_at_full_toggle_duration_when_motion_is_not_reduced() {
        val animator = Motion.translateX(context, View(context), 0f, 18f, Motion.TOGGLE_DURATION_MS)
        assertEquals(Motion.TOGGLE_DURATION_MS, animator.duration) // negative control for the test below
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
    fun colorTransition_runs_at_full_toggle_duration_when_motion_is_not_reduced() {
        val animator = Motion.colorTransition(context, 0xFF39393D.toInt(), 0xFF30D158.toInt(), Motion.TOGGLE_DURATION_MS) {}
        assertEquals(Motion.TOGGLE_DURATION_MS, animator.duration) // negative control for the test below
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
        val dim = mockCaretDimAlpha()
        assertEquals(1f, Motion.caretAlphaAt(0f), 0f)
        assertEquals(1f, Motion.caretAlphaAt(0.49f), 0f)
        assertEquals(dim, Motion.caretAlphaAt(0.5f), 0f)
        assertEquals(dim, Motion.caretAlphaAt(0.99f), 0f)
    }

    @Test
    fun caretAlphaAt_takes_exactly_as_many_states_as_the_mocks_steps_declares() {
        // NEGATIVE CONTROL for the test above: a smooth 1.0 -> dim fade also satisfies the four
        // fixed points checked there. Sampling densely and asserting the number of distinct values
        // is what tells a step function from a fade -- and the count is the document's own
        // `steps(2)`, not a 2 typed here.
        val steps = MockCss.steps(MockCss.declaration(".stream-caret", "animation"))
        assertEquals(2, steps)
        val seen = (0..100).map { Motion.caretAlphaAt(it / 100f) }.toSet()
        assertEquals(steps, seen.size)
        assertEquals(setOf(1f, mockCaretDimAlpha()), seen)
    }

    @Test
    fun streamingCaretBlink_repeats_at_the_full_period_when_motion_is_not_reduced() {
        val animator = Motion.streamingCaretBlink(context, View(context)) as ValueAnimator
        assertEquals(Motion.CARET_PERIOD_MS, animator.duration) // negative control for the test below
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

    @Test
    fun streamingCaretBlink_leaves_the_caret_fully_visible_AFTER_IT_STARTS_when_motion_is_reduced() {
        setAnimatorScale(0f)
        val caret = View(context)
        val animator = Motion.streamingCaretBlink(context, caret) as ValueAnimator
        animator.start()
        assertEquals(
            "reduced motion promises a STATIC, FULLY VISIBLE caret (ADR-007 B134 decision 3, and " +
                "the KDoc on streamingCaretBlink). A zero-duration ValueAnimator still delivers " +
                "one update, at animatedFraction 1.0, so an update listener left wired dims the " +
                "caret the instant it starts. Inspecting the animator before it plays cannot see " +
                "this, which is how it shipped.",
            1f,
            caret.alpha,
            0f,
        )
    }

    @Test
    fun a_zero_duration_animator_still_delivers_one_update_at_a_full_fraction() {
        // MEASURED, NOT ASSUMED -- the platform behaviour the test above exists for, with no
        // Motion involved, followed by what Motion.caretAlphaAt (THE SAME FUNCTION
        // streamingCaretBlink wires to that fraction) returns for it. Together they are the
        // mechanism: fraction 1.0 is delivered, and 1.0 is a dim frame.
        var last = -1f
        val probe = ValueAnimator.ofFloat(0f, 1f).apply {
            duration = 0L
            addUpdateListener { last = it.animatedFraction }
        }
        probe.start()
        assertEquals("a 0ms animator delivers one update", 1f, last, 0f)
        assertEquals(mockCaretDimAlpha(), Motion.caretAlphaAt(last), 0f)
    }

    @Test
    fun streamingCaretBlink_still_dims_the_caret_when_motion_is_not_reduced() {
        // NEGATIVE CONTROL for the reduced-motion test above: the blink must still be WIRED when
        // motion is not reduced, or "the caret stays fully visible" would be satisfied by a builder
        // that never animates anything at all.
        val caret = View(context)
        val animator = Motion.streamingCaretBlink(context, caret) as ValueAnimator
        animator.start()
        animator.setCurrentFraction(0.75f)
        assertEquals(mockCaretDimAlpha(), caret.alpha, 0f)
        animator.setCurrentFraction(0.25f)
        assertEquals(1f, caret.alpha, 0f)
    }
}

/**
 * `docs/research/remote-control-mock.html`, PARSED -- the document [Motion]'s five numbers come
 * from, read as a document instead of transcribed into a test.
 *
 * WHY IT EXISTS. Before it, this file asserted `assertEquals(350L, Motion.NAV_DURATION_MS)` and
 * four more like it: literals transcribed from the implementation, compared against the
 * implementation. Nothing in the repository read the mock at all -- it was named only in comments
 * -- so all five constants could drift from the design with every assertion staying green. That is
 * `SwarmTheme.EXPECTED_DARK_COLORS` recurring inside the slice built to eradicate it, and the
 * remedy is S22's: stage the source on the unit-test classpath (app/build.gradle.kts does it) and
 * assert against what it says.
 *
 * ONLY TOP-LEVEL RULES ARE PARSED, and at-rules are reached by name. The mock's
 * `@media (prefers-reduced-motion: reduce)` block re-declares `.banner`, `.sheet` and
 * `.stream-caret` with `animation: none` and `transition: none`; a sweep that flattened every
 * nested block into one selector map would resolve `.banner`'s transition to whichever of the two
 * it met last, and could report the SUPPRESSION as the duration.
 */
private object MockCss {

    private const val RESOURCE = "remote-control-mock.html"

    private val COMMENT = Regex("/\\*.*?\\*/", RegexOption.DOT_MATCHES_ALL)
    private val WHITESPACE = Regex("\\s+")
    private val SECONDS = Regex("(?<![\\w.-])(\\d*\\.?\\d+)s(?![\\w-])")
    private val BEZIER = Regex("cubic-bezier\\(([^)]*)\\)")
    private val STEPS = Regex("steps\\(\\s*(\\d+)")
    private val TRANSLATE_Y = Regex("translateY\\(\\s*(-?\\d*\\.?\\d+)px\\s*\\)")
    private val PX = Regex("^(-?\\d*\\.?\\d+)px$")

    /** The one `<style>` block, comments stripped. The rest of the file is markup and script, and
     * the script's own braces would derail the block scanner below. */
    private val style: String by lazy {
        val html = readResource(RESOURCE)
        val open = html.indexOf("<style>")
        val close = html.indexOf("</style>", open + 1)
        require(open >= 0 && close > open) {
            "$RESOURCE no longer carries a <style> block; every number these tests expect would be " +
                "read out of nothing"
        }
        COMMENT.replace(html.substring(open + "<style>".length, close), "\n")
    }

    /** Every TOP-LEVEL rule: selector -> declarations. At-rules are skipped here and reached by
     * name through [keyframeStops]. */
    private val rules: Map<String, Map<String, String>> by lazy {
        val out = LinkedHashMap<String, LinkedHashMap<String, String>>()
        blocks(style).forEach { (prelude, body) ->
            if (prelude.startsWith("@")) return@forEach
            prelude.split(",").forEach { raw ->
                val selector = raw.trim().split(WHITESPACE).joinToString(" ")
                if (selector.isNotEmpty()) out.getOrPut(selector) { LinkedHashMap() }.putAll(declarations(body))
            }
        }
        require(out.isNotEmpty()) {
            "no CSS rules parsed from $RESOURCE; every assertion over the design would be vacuous"
        }
        out
    }

    /** One declaration's raw value. Fails loudly rather than returning null: an assertion over a
     * value the document does not carry says nothing. */
    fun declaration(selector: String, property: String): String {
        val decls = requireNotNull(rules[selector]) { "$RESOURCE declares no `$selector`" }
        return requireNotNull(decls[property]) { "$RESOURCE's `$selector` declares no `$property`" }
    }

    /** Every stop `@keyframes [name]` declares, so a test can assert WHICH stops exist rather than
     * only reading the one it expected. */
    fun keyframeStops(name: String): Map<String, Map<String, String>> {
        val body = blocks(style).firstOrNull { (prelude, _) ->
            prelude.split(WHITESPACE) == listOf("@keyframes", name)
        }?.second ?: error("$RESOURCE declares no @keyframes $name")
        return blocks(body).associate { (stop, decls) -> stop.trim() to declarations(decls) }
    }

    /** One stop's declarations -- `pulse`, `50%` -> `{opacity=0.35}`. */
    fun keyframe(name: String, stop: String): Map<String, String> =
        keyframeStops(name)[stop] ?: error("$RESOURCE's @keyframes $name declares no `$stop` stop")

    /** The `@keyframes` name an `animation` shorthand runs -- `pulse 0.9s steps(2) infinite`. */
    fun animationName(value: String): String = value.trim().split(WHITESPACE).first()

    /**
     * A CSS `<time>` in seconds, as milliseconds. THE CONVERSION EVERY TIMING ASSERTION AND ITS
     * NEGATIVE CONTROL SHARE: a control that did the arithmetic itself would be checking a second
     * implementation of the parse rather than the one the real assertion runs.
     */
    fun millis(value: String): Long {
        val m = requireNotNull(SECONDS.find(value)) { "\"$value\" carries no <time> in seconds" }
        return (m.groupValues[1].toFloat() * 1000f).roundToLong()
    }

    /** `60px` -> 60f. */
    fun px(value: String): Float =
        requireNotNull(PX.find(value.trim())) { "\"$value\" is not a px length" }.groupValues[1].toFloat()

    /** `translateY(-130px)` -> -130f. */
    fun translateYPx(value: String): Float =
        requireNotNull(TRANSLATE_Y.find(value)) { "\"$value\" carries no translateY(<px>)" }
            .groupValues[1].toFloat()

    /** `cubic-bezier(0.32,0.72,0,1)` -> its four control values, in declaration order. */
    fun cubicBezier(value: String): List<Float> {
        val m = requireNotNull(BEZIER.find(value)) { "\"$value\" names no cubic-bezier() curve" }
        val points = m.groupValues[1].split(",").map { it.trim().toFloat() }
        require(points.size == 4) { "cubic-bezier() in \"$value\" carries ${points.size} values, want 4" }
        return points
    }

    /** `steps(2)` -> 2. */
    fun steps(value: String): Int =
        requireNotNull(STEPS.find(value)) { "\"$value\" names no steps() timing function" }
            .groupValues[1].toInt()

    /** `prelude { body }` pairs at the TOP LEVEL of [css]: a nested block stays inside its parent's
     * body rather than being lifted out of it. */
    private fun blocks(css: String): List<Pair<String, String>> {
        val out = mutableListOf<Pair<String, String>>()
        var i = 0
        var start = 0
        while (i < css.length) {
            when (css[i]) {
                '{' -> {
                    val prelude = css.substring(start, i).trim()
                    var depth = 1
                    var j = i + 1
                    while (j < css.length && depth > 0) {
                        if (css[j] == '{') depth++
                        if (css[j] == '}') depth--
                        j++
                    }
                    out += prelude to css.substring(i + 1, if (depth == 0) j - 1 else css.length)
                    i = j
                    start = j
                }
                '}' -> {
                    i++
                    start = i
                }
                else -> i++
            }
        }
        return out
    }

    private fun declarations(body: String): Map<String, String> {
        val out = LinkedHashMap<String, String>()
        body.split(";").forEach { decl ->
            val colon = decl.indexOf(':')
            if (colon <= 0) return@forEach
            val prop = decl.substring(0, colon).trim()
            val value = decl.substring(colon + 1).trim()
            if (prop.isNotEmpty() && value.isNotEmpty()) out[prop] = value
        }
        return out
    }

    private fun readResource(name: String): String =
        javaClass.classLoader?.getResourceAsStream(name)?.bufferedReader()?.use { it.readText() }
            ?: error(
                "$name is not on the unit-test classpath. app/build.gradle.kts must stage it as a " +
                    "unit-test resource so these assertions read the design itself rather than a " +
                    "number copied out of the implementation they are checking",
            )
}
