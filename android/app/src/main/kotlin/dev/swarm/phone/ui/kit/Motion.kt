package dev.swarm.phone.ui.kit

import android.animation.Animator
import android.animation.ObjectAnimator
import android.animation.ValueAnimator
import android.content.Context
import android.provider.Settings
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
 *   - the push banner: `translateY` -(its own height + its top inset) -> 0
 *   - the streaming caret: a liveness signal reporting text is still arriving, not decoration
 *
 * The first two share [NAV_DURATION_MS] (300ms) and [EASE]
 * (`cubic-bezier(0.22, 1, 0.36, 1)`, ADR-009 D5's curve). The caret is a discrete two-state
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

    /**
     * 300ms -- the bottom sheet's and the push banner's shared duration.
     *
     * origin: ADR-009 D5 Navigation (sheet, banner)
     *
     * IT WAS 350ms AND THE MOCK STILL SAYS SO. `.banner` and `.sheet` declare `0.35s` in
     * `remote-control-mock.html`, which is a Substrate drawing; D5's Navigation row replaces that
     * duration and that curve in as many words ("replaces 350ms / (0.32, 0.72, 0, 1)"), and
     * MotionTest reads BOTH cells so the supersession is asserted rather than assumed.
     */
    const val NAV_DURATION_MS = 300L

    /**
     * 200ms -- an entrance: a component arriving on screen.
     *
     * origin: ADR-009 D5 Entrance
     *
     * NO ANIMATOR HERE TAKES IT YET, and that is the honest state rather than an omission. D5
     * states the register for the whole skin; the app's three moving things are the sheet, the
     * banner and the caret, so the entrance row is the number a future one is built at -- and it
     * is here, joined to the decision, so that when one arrives it is not a fresh 200 typed at a
     * call site. [ENTRANCE_MAX_TRAVEL_DP] is the other half of the same row.
     */
    const val ENTRANCE_DURATION_MS = 200L

    /**
     * 4dp -- the FURTHEST an entrance may travel, not the distance one travels.
     *
     * origin: ADR-009 D5 Entrance
     *
     * The row's reason is a property of the ground rather than of taste: "larger travel visibly
     * bounces on a dark ground", which is the same amplification (~80:1 on near-black) that limits
     * this skin to one moving element per viewport.
     */
    const val ENTRANCE_MAX_TRAVEL_DP = 4f

    /**
     * 120ms -- the ceiling on a control's FIRST VISIBLE RESPONSE to a press.
     *
     * origin: ADR-009 D5 Press feedback
     *
     * IT IS AUDITED, NOT ANIMATED, which is the row's own clause and the reason no animator below
     * takes this constant. Anything slower "reads as latency": the response has to be the
     * platform's own pressed state on the ACTION_DOWN frame, not a transition into one.
     * `PressFeedbackAuditTest` measures what the platform actually does against this number.
     */
    const val PRESS_RESPONSE_CEILING_MS = 120L

    /** `cubic-bezier(0.22, 1, 0.36, 1)`, ADR-009 D5's one named easing curve, as the four control
     * values it is built from. Named rather than inlined into [EASE] so a test can compare them
     * against the curve the document declares: an [Interpolator] does not report the points it was
     * built from, so the only other check available is sampling the curve.
     *
     * origin: ADR-009 D5 Entrance */
    const val EASE_P1X = 0.22f
    const val EASE_P1Y = 1f
    const val EASE_P2X = 0.36f
    const val EASE_P2Y = 1f

    /**
     * The register's one easing curve.
     *
     * IT IS `EASE` AND NOT `NAV_EASE`, WHICH IS A DECISION AND NOT A RENAME. D5 gives the entrance
     * and both navigation surfaces the same curve -- the Navigation row writes it as "same curve"
     * rather than as four control points of its own -- and two names for one curve is where the
     * two come to differ. The sweep runs on it too.
     */
    val EASE: Interpolator =
        PathInterpolatorCompat.create(EASE_P1X, EASE_P1Y, EASE_P2X, EASE_P2Y)

    /** 0.9s -- the streaming caret's full blink period. D5 calls this row unchanged. */
    const val CARET_PERIOD_MS = 900L

    /** The caret's dim state, `@keyframes pulse { 50% { opacity: 0.35 } }`. */
    private const val CARET_DIM_ALPHA = 0.35f

    /** 0.15s -- the toggle's background and thumb transitions (`.toggle`, `.toggle::after`).
     * D5 calls this row unchanged. */
    const val TOGGLE_DURATION_MS = 150L

    /**
     * True when the platform's global animator scale is 0 -- Settings > Accessibility > Remove
     * animations, or an equivalent MDM/test override. This is the ONE gate every builder below
     * runs through.
     *
     * "No second path constructs an animator without it" is a claim about the whole app, and it is
     * android/gate/s23_motion_test.go that makes it true rather than this sentence: that gate
     * exempts THIS FILE BY NAME and scans every other production Kotlin file, kit components
     * included, for a raw ObjectAnimator/ValueAnimator/ViewPropertyAnimator/animate()/
     * startAnimation()/TransitionManager. While the exemption was the ui/kit PACKAGE, any
     * component beside this one could have bypassed [duration] and stayed green -- the claim was
     * unenforced exactly where it was most likely to be broken.
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

    /** A `translationY` animator, reduced-motion-aware. [interpolator] defaults to [EASE]
     * because every current caller is a navigation affordance; pass another only if a future
     * one deliberately is not. */
    fun translateY(
        context: Context,
        view: View,
        fromPx: Float,
        toPx: Float,
        durationMs: Long,
        interpolator: Interpolator = EASE,
    ): ObjectAnimator {
        val animator = ObjectAnimator.ofFloat(view, View.TRANSLATION_Y, fromPx, toPx)
        animator.duration = duration(context, durationMs)
        animator.interpolator = interpolator
        return animator
    }

    /** A `translationX` animator, reduced-motion-aware. No named easing curve is applied --
     * the toggle thumb is this primitive's only caller today and the artifact names no curve
     * for it, so the platform default stands rather than borrowing [EASE] unasked. */
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

    /**
     * Where the banner sits while hidden: far enough above its resting position to clear its own
     * height AND the inset it rests below -- `-(bannerHeightPx + topInsetPx)`.
     *
     * DERIVED, NOT THE MOCK'S LITERAL. docs/design/substrate-components.md row 2 states the rule as
     * `translateY(-(own height + banner_top))` and calls the mock's own `-130px` a magic number.
     * The two agree at the mock's dimensions -- a 70dp banner at `banner_top` 60 -- and nowhere
     * else: a banner whose message wraps to a second line is taller, and a handset's real
     * status-bar inset is not the iPhone notch constant the mock drew. A fixed -130 leaves such a
     * banner partly on screen while "hidden", or slides it further than it needs to.
     *
     * BOTH INPUTS ARE THE CALLER'S TO MEASURE, as [bottomSheetEnter]'s height is: `banner.height
     * .toFloat()` after layout, and the top inset from `WindowInsets.statusBars` plus `space_6`
     * (row 19 makes the same point about `screen_top` 54 being a design-time preview value only).
     */
    fun pushBannerHiddenTranslation(bannerHeightPx: Float, topInsetPx: Float): Float =
        -(bannerHeightPx + topInsetPx)

    fun pushBannerEnter(context: Context, banner: View, bannerHeightPx: Float, topInsetPx: Float): Animator =
        translateY(
            context,
            banner,
            pushBannerHiddenTranslation(bannerHeightPx, topInsetPx),
            0f,
            NAV_DURATION_MS,
        )

    fun pushBannerExit(context: Context, banner: View, bannerHeightPx: Float, topInsetPx: Float): Animator =
        translateY(
            context,
            banner,
            0f,
            pushBannerHiddenTranslation(bannerHeightPx, topInsetPx),
            NAV_DURATION_MS,
        )

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
     * AND THE UPDATE LISTENER IS NOT WIRED AT ALL when motion is reduced, which is the third
     * collapse and the one that was missing. A zero-duration [ValueAnimator] is not inert: it
     * still delivers ONE update, at `animatedFraction` 1.0, the moment it starts -- and
     * [caretAlphaAt] returns the DIM state for that fraction. So a listener left attached turned
     * "static and fully visible" into "static and dimmed to 0.35" on the first `start()`, while
     * the animator's own properties (alpha 1, duration 0, repeatCount 0) all still read correctly
     * beforehand. MotionTest's
     * `streamingCaretBlink_leaves_the_caret_fully_visible_AFTER_IT_STARTS_when_motion_is_reduced`
     * calls `start()` for exactly that reason, and
     * `a_zero_duration_animator_still_delivers_one_update_at_a_full_fraction` measures the platform
     * behaviour behind it rather than assuming it.
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
        if (!reduced) animator.addUpdateListener { caret.alpha = caretAlphaAt(it.animatedFraction) }
        return animator
    }
}
