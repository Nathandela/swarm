package dev.swarm.phone.ui.kit

import android.animation.Animator
import android.animation.ObjectAnimator
import android.animation.ValueAnimator
import android.content.Context
import android.provider.Settings
import android.util.TypedValue
import android.view.View
import android.view.animation.Interpolator
import androidx.core.view.animation.PathInterpolatorCompat

/**
 * PB-DS-8 -- "Motion: Substrate is static, and the exceptions are named."
 *
 * ADR-007 B134 decision 3, executed. `docs/research/remote-control-design-directions.html`
 * declares no `@keyframes`, no `transition` and no `animation` anywhere -- its working
 * affordance is the STATIC dot glow plus the STATIC workbar gradient, and its own rule is
 * "nothing glows unless it is alive". `remote-control-mock.html`'s `pulse 1.6s` dot is
 * inherited from the pre-skin iOS palette and is a conflict with that rule, not a
 * specification: this object does not implement it, and must not grow a way to.
 *
 * ONLY THREE THINGS MOVE, and this is the exhaustive list:
 *   - the bottom sheet: `translateY` 100% -> 0
 *   - the push banner: `translateY` -130dp -> 0
 *   - the streaming caret: a liveness signal reporting text is still arriving, not decoration
 *
 * The first two share [NAV_DURATION_MS] (350ms) and [NAV_EASE]
 * (`cubic-bezier(0.32, 0.72, 0, 1)`, the mock's own curve). The caret is a discrete two-state
 * blink -- [CARET_PERIOD_MS] (0.9s), CSS `steps(2)` -- because a smooth fade would itself be
 * the decoration the ADR forbids.
 *
 * REDUCED MOTION IS READ ONCE, AT CONSTRUCTION, not lazily when an animator starts: every
 * builder below inspects [isReducedMotion] the instant it runs and bakes the answer into the
 * returned animator's duration. An animator already handed to a caller stays reduced even if the
 * platform setting flips back before it plays -- see [duration].
 *
 * IT COVERS THE TOGGLE, which the artifact's own `prefers-reduced-motion` selector list omits
 * (`.g-work, .banner, .sheet, .stream-caret` -- the toggle's 0.15s background and thumb
 * transitions are the only OTHER transitions in the document, and the list leaving them out
 * reads as an omission rather than a considered exclusion). [translateX] and [colorTransition]
 * are the generic primitives a toggle's thumb-slide and track crossfade are built from; a
 * toggle built on them inherits reduced-motion coverage without this file owning the toggle's
 * view code.
 */
object Motion {

    /** 350ms -- the bottom sheet's and the push banner's shared duration. */
    const val NAV_DURATION_MS = 350L

    /** `cubic-bezier(0.32, 0.72, 0, 1)`, the mock's one named easing curve. */
    val NAV_EASE: Interpolator = PathInterpolatorCompat.create(0.32f, 0.72f, 0f, 1f)

    /** 0.9s -- the streaming caret's full blink period. */
    const val CARET_PERIOD_MS = 900L

    /** The caret's dim state, `@keyframes pulse { 50% { opacity: 0.35 } }`. */
    private const val CARET_DIM_ALPHA = 0.35f

    /** 0.15s -- the toggle's background and thumb transitions (`.toggle`, `.toggle::after`). */
    const val TOGGLE_DURATION_MS = 150L

    /** -130dp, the push banner's hidden position. Fixed, not size-relative: unlike the sheet's
     * 100%-of-its-own-height, the mock states this one as a literal offset. */
    private const val BANNER_HIDDEN_DP = -130f

    /**
     * True when the platform's global animator scale is 0 -- Settings > Accessibility > Remove
     * animations, or an equivalent MDM/test override. This is the ONE gate every builder below
     * runs through; there is no second path that constructs an animator without it.
     */
    fun isReducedMotion(context: Context): Boolean =
        Settings.Global.getFloat(context.contentResolver, Settings.Global.ANIMATOR_DURATION_SCALE, 1f) == 0f

    /**
     * [fullMs] under ordinary motion, 0 under reduced motion -- evaluated the instant this is
     * called, which is what makes "checked at animator construction" true of every caller
     * below. Public so a kit component with no named primitive here (the toggle's own view code,
     * say) can still route its own [ObjectAnimator] or [ValueAnimator] through the same
     * reduced-motion check instead of re-deriving it.
     */
    fun duration(context: Context, fullMs: Long): Long = if (isReducedMotion(context)) 0L else fullMs

    // ------------------------------------------------------------------
    // The generic primitives.
    // ------------------------------------------------------------------

    /** A `translationY` animator, reduced-motion-aware. [interpolator] defaults to [NAV_EASE]
     * because every current caller is a navigation affordance; pass another only if a future
     * one deliberately is not. */
    fun translateY(
        context: Context,
        view: View,
        fromPx: Float,
        toPx: Float,
        durationMs: Long,
        interpolator: Interpolator = NAV_EASE,
    ): ObjectAnimator {
        val animator = ObjectAnimator.ofFloat(view, View.TRANSLATION_Y, fromPx, toPx)
        animator.duration = duration(context, durationMs)
        animator.interpolator = interpolator
        return animator
    }

    /** A `translationX` animator, reduced-motion-aware. No named easing curve is applied --
     * the toggle thumb is this primitive's only caller today and the artifact names no curve
     * for it, so the platform default stands rather than borrowing [NAV_EASE] unasked. */
    fun translateX(context: Context, view: View, fromPx: Float, toPx: Float, durationMs: Long): ObjectAnimator {
        val animator = ObjectAnimator.ofFloat(view, View.TRANSLATION_X, fromPx, toPx)
        animator.duration = duration(context, durationMs)
        return animator
    }

    /**
     * An ARGB crossfade, reduced-motion-aware, reported through [onUpdate] rather than applied
     * to a [View] directly: a toggle's track is typically a shaped drawable (a rounded pill),
     * and mutating it through `View.setBackgroundColor` would replace the shape with a flat
     * rect. The caller applies each value to whatever it actually paints.
     */
    fun colorTransition(
        context: Context,
        fromColor: Int,
        toColor: Int,
        durationMs: Long,
        onUpdate: (Int) -> Unit,
    ): ValueAnimator {
        val animator = ValueAnimator.ofArgb(fromColor, toColor)
        animator.duration = duration(context, durationMs)
        animator.addUpdateListener { onUpdate(it.animatedValue as Int) }
        return animator
    }

    // ------------------------------------------------------------------
    // The bottom sheet.
    // ------------------------------------------------------------------

    /** [sheetHeightPx] is the sheet's own measured height -- CSS's `100%` is relative to the
     * element, so the caller supplies it (typically `sheet.height.toFloat()` after layout). */
    fun bottomSheetEnter(context: Context, sheet: View, sheetHeightPx: Float): Animator =
        translateY(context, sheet, sheetHeightPx, 0f, NAV_DURATION_MS)

    /** The same transform reversed -- the mock's `transition` sits on `.sheet` itself, not
     * scoped to `.show`, so dismissal animates on the identical duration and curve. */
    fun bottomSheetExit(context: Context, sheet: View, sheetHeightPx: Float): Animator =
        translateY(context, sheet, 0f, sheetHeightPx, NAV_DURATION_MS)

    // ------------------------------------------------------------------
    // The push banner.
    // ------------------------------------------------------------------

    fun pushBannerEnter(context: Context, banner: View): Animator =
        translateY(context, banner, dpToPx(context, BANNER_HIDDEN_DP), 0f, NAV_DURATION_MS)

    fun pushBannerExit(context: Context, banner: View): Animator =
        translateY(context, banner, 0f, dpToPx(context, BANNER_HIDDEN_DP), NAV_DURATION_MS)

    private fun dpToPx(context: Context, dp: Float): Float =
        TypedValue.applyDimension(TypedValue.COMPLEX_UNIT_DIP, dp, context.resources.displayMetrics)

    // ------------------------------------------------------------------
    // The streaming caret.
    // ------------------------------------------------------------------

    /** The caret's alpha at a point in its blink cycle -- `steps(2)`: no interpolation, one
     * flip at the cycle's midpoint, never a value between the two. Exposed as a pure function
     * so the stepping itself is testable without driving an [Animator]'s update-listener
     * machinery; [streamingCaretBlink] wires it to the animated fraction. */
    fun caretAlphaAt(fraction: Float): Float = if (fraction < 0.5f) 1f else CARET_DIM_ALPHA

    /**
     * The streaming caret's blink: infinite, [CARET_PERIOD_MS], [caretAlphaAt]'s two-state
     * step -- unless motion is reduced, in which case it is a STATIC, fully-visible caret
     * rather than a zero-duration infinite loop. A liveness signal turned off still has to say
     * "streaming" somehow, and a 0ms animator that still repeats forever is a busy loop, not an
     * accessibility accommodation -- so reduced motion collapses [ValueAnimator.repeatCount] to
     * 0 alongside the duration, not only the duration.
     *
     * `ValueAnimator.INFINITE` is the correct repeat value on a real device; MotionTest's
     * `robolectric_translates_the_infinite_repeat_sentinel_on_readback` records that Robolectric's
     * test harness alone translates it on readback, and why that is a test-tool limitation to
     * document rather than a reason to replace the constant with a literal.
     */
    fun streamingCaretBlink(context: Context, caret: View): Animator {
        val reduced = isReducedMotion(context)
        caret.alpha = 1f
        val animator = ValueAnimator.ofFloat(0f, 1f)
        animator.duration = if (reduced) 0L else CARET_PERIOD_MS
        animator.repeatCount = if (reduced) 0 else ValueAnimator.INFINITE
        animator.addUpdateListener { caret.alpha = caretAlphaAt(it.animatedFraction) }
        return animator
    }
}
