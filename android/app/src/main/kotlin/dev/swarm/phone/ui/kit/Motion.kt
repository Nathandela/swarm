package dev.swarm.phone.ui.kit

import android.animation.Animator
import android.animation.ObjectAnimator
import android.animation.ValueAnimator
import android.content.Context
import android.graphics.Canvas
import android.graphics.Color
import android.graphics.ColorFilter
import android.graphics.LinearGradient
import android.graphics.Paint
import android.graphics.PixelFormat
import android.graphics.Shader
import android.graphics.drawable.Drawable
import android.provider.Settings
import android.view.View
import android.view.animation.Interpolator
import androidx.core.view.animation.PathInterpolatorCompat
import kotlin.math.roundToInt
import kotlin.math.tan

/**
 * PB-DS-8, AS AMENDED BY ADR-009 D5 -- "the exceptions are named", and there are four of them.
 *
 * ADR-007 B134 decision 3, executed, and then amended once. The design source declares no
 * `@keyframes`, no `transition` and no `animation` anywhere -- its working affordance is the
 * STATIC dot glow plus the STATIC workbar gradient, and its own rule is "nothing glows unless it
 * is alive". `remote-control-mock.html`'s `pulse 1.6s` dot is inherited from the pre-skin iOS
 * palette and is a conflict with that rule, not a specification: this object does not implement
 * it, and must not grow a way to.
 *
 * ONLY FOUR THINGS MOVE, and this is the exhaustive list:
 *   - the bottom sheet: `translateY` 100% -> 0
 *   - the push banner: `translateY` -(its own height + its top inset) -> 0
 *   - the streaming caret: a liveness signal reporting text is still arriving, not decoration
 *   - the specular sweep: ADR-009 D5's one new named exception -- a highlight that travels a
 *     promoted slab's top edge ONCE, at the moment that session's Group became NeedsInput.
 *     Substrate's own rule "nothing glows unless it is alive" extends to "nothing sweeps unless
 *     it just started asking"; D8.2 gates its four constraints, and [specularSweep] carries them.
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

    // ------------------------------------------------------------------
    // The specular sweep -- ADR-009 D5's one new named exception.
    //
    // EVERY NUMBER BELOW IS THE DESIGN'S, AND android/gate/o4_sweep_test.go RECOMPUTES ALL EIGHT:
    // the duration and the colour out of `--p-sweep-fx` (typed `effect`, so it has no TSV row and
    // no `res/values` converter -- tokens.json is the only place it exists, which is exactly why a
    // constant derived from it needs a gate), and the geometry out of the maquette's own
    // `.slab.lit.sweep::after` and `@keyframes sweep`. s23_kit_test.go deliberately does not read
    // this file (s23MotionFile records the split), so that gate is where the join lives.
    // ------------------------------------------------------------------

    /** origin: --p-sweep-fx ms */
    const val SWEEP_DURATION_MS = 500L

    /** origin: --p-sweep-fx alpha */
    const val SWEEP_PEAK_ALPHA = 0.30f

    /** origin: .slab.lit.sweep::after { transform } */
    const val SWEEP_SKEW_DEG = -25f

    /** origin: .slab.lit.sweep::after { width } -- the streak's width, as a share of the slab's. */
    const val SWEEP_BAND_SHARE = 0.45f

    /** origin: .slab.lit.sweep::after { height } */
    const val SWEEP_HEIGHT_DP = 1.5f

    /** origin: .slab.lit.sweep::after { left } -- where the streak starts, off the leading edge. */
    const val SWEEP_FROM_SHARE = -0.60f

    /** origin: @keyframes sweep -- the far side, past the trailing edge. */
    const val SWEEP_TO_SHARE = 1.15f

    /**
     * The streak's own light, `rgba(255, 252, 244, 0.30)`.
     *
     * IT IS NOT `--p-ink` AND NOT `Color.WHITE`, AND THE DIFFERENCE IS THE POINT. The key light is
     * the linen the ink is (`--p-card-fx`'s RGB is `--p-ink`, which is why `cardSurface` reaches
     * for `R.color.swarm_text_primary`); this is a SPECULAR highlight -- the light source itself
     * catching an edge, brighter than anything the surface is painted in. It exists only inside
     * `--p-sweep-fx`, which is why it is three named channels here rather than an `R.color`: an
     * `effect` token has no converter, and inventing a 20th colour resource for it would break the
     * count-pinned join (ADR-009 D3).
     *
     * origin: --p-sweep-fx colour
     */
    private const val SWEEP_TINT_R = 255

    /** origin: --p-sweep-fx colour */
    private const val SWEEP_TINT_G = 252

    /** origin: --p-sweep-fx colour */
    private const val SWEEP_TINT_B = 244

    /**
     * The sweep now playing, or null.
     *
     * THIS FIELD IS THE "AT MOST ONE PER VIEWPORT" RULE. D5 states it as a constraint, and a
     * constraint with no mechanism is a comment: motion on near-black is amplified ~80:1, and one
     * journal event promoting two sessions is the normal case rather than the exotic one. Newest
     * wins, and the one it supersedes COMPLETES instantly.
     *
     * READ-ONLY OUTSIDE THIS OBJECT, and internal rather than private so the test suite can ask
     * what is in flight. It is the only observable a runtime test has for a rule about an effect
     * that leaves no trace: a sweep that has played is gone.
     */
    internal var inFlightSweep: Animator? = null
        private set

    /**
     * The streak's leading edge at [fraction], as a share of the slab's own width.
     *
     * A PURE FUNCTION for [caretAlphaAt]'s reason: the travel is testable without driving an
     * animator's update machinery, and the drawable that spends it is private to this file.
     */
    fun sweepOffsetAt(fraction: Float): Float =
        SWEEP_FROM_SHARE + (SWEEP_TO_SHARE - SWEEP_FROM_SHARE) * fraction

    /**
     * Fire the sweep across [slab]'s top edge, once, now -- or return null under reduced motion.
     *
     * IT STARTS THE ANIMATOR ITSELF, unlike every other builder in this file, and that difference
     * is deliberate. The sheet and the banner are transitions a caller SEQUENCES (it decides when
     * the sheet enters and what happens after); this is a signal fired at a moment that has
     * already happened. A caller that had to remember to `start()` it would be a caller that can
     * forget, and the returned animator would then sit in [inFlightSweep] suppressing the next
     * real sweep while animating nothing.
     *
     * REDUCED MOTION COLLAPSES IT TO NOTHING, WHICH IS NOT THE SAME AS TO ZERO. The other builders
     * return a 0ms animator, because the sheet still has to arrive; there is nothing for a sweep
     * to arrive at, so nothing is built and nothing is attached. A zero-duration [ValueAnimator]
     * still delivers one update at fraction 1.0, and the streak's own listener would paint a
     * full-alpha final frame from it -- the exact defect the caret shipped once.
     *
     * THE STREAK IS AN OVERLAY DRAWABLE, not a child view and not a foreground. A child would
     * change the slab's layout for 500ms; a foreground would silently replace the focus ring
     * PB-DS-12 puts there (`Kit.focusable`). An overlay draws over the view's own content, takes
     * no touch, participates in no layout, and detaches when the animator ends.
     *
     * @param slab the promoted row. Its width is read on every frame rather than at construction:
     *  the row is built before it is measured, so a bound captured here would be zero.
     */
    fun specularSweep(context: Context, slab: View): Animator? {
        if (isReducedMotion(context)) return null
        // NEWEST WINS, AND THE OLD ONE COMPLETES. `end()` runs the listeners; `cancel()` skips
        // them, which would leave a half-drawn streak attached to a row nobody is looking at.
        inFlightSweep?.end()

        val streak = SpecularStreak(Kit.dp(context, SWEEP_HEIGHT_DP))
        slab.overlay.add(streak)

        val animator = ValueAnimator.ofFloat(0f, 1f)
        animator.duration = SWEEP_DURATION_MS
        // ONE-SHOT, STATED. The platform's default is 0 as well, and a default is not a decision
        // anyone recorded -- `repeatCount = ValueAnimator.INFINITE` is one line away in this same
        // file, on the caret, which is the animation this one must never become.
        animator.repeatCount = 0
        animator.interpolator = EASE
        animator.addUpdateListener {
            streak.travelTo(sweepOffsetAt(it.animatedFraction), slab.width, slab.height)
        }
        animator.addListener(object : Animator.AnimatorListener {
            override fun onAnimationStart(animation: Animator) = Unit
            override fun onAnimationEnd(animation: Animator) {
                slab.overlay.remove(streak)
                // Only if it is still THIS one: a superseded sweep ends after its successor has
                // already claimed the slot, and clearing it there would leave the live sweep
                // unsupersedable.
                if (inFlightSweep === animation) inFlightSweep = null
            }
            override fun onAnimationCancel(animation: Animator) = Unit
            override fun onAnimationRepeat(animation: Animator) = Unit
        })
        inFlightSweep = animator
        animator.start()
        return animator
    }

    /**
     * The streak itself: a skewed band of light at the slab's top edge.
     *
     * IT IS HERE AND NOT IN Surfaces.kt, where the kit's other drawables live, because it exists
     * only while an animator is driving it -- there is no static "sweep" a component could paint,
     * and D8.2 fences the whole effect to this one file precisely so its four constraints cannot
     * be routed around. A drawable in the surfaces file would be exactly that route.
     *
     * The gradient is transparent -> tint -> transparent across the band, which is the maquette's
     * own `linear-gradient(90deg, transparent, rgba(255,252,244,0.30), transparent)`.
     */
    private class SpecularStreak(private val heightPx: Float) : Drawable() {

        private val paint = Paint(Paint.ANTI_ALIAS_FLAG)
        private var leadingShare = Motion.SWEEP_FROM_SHARE

        /** Place the streak for one frame. Bounds come from the slab because it may not have been
         * measured when the sweep was fired. */
        fun travelTo(share: Float, slabWidthPx: Int, slabHeightPx: Int) {
            leadingShare = share
            setBounds(0, 0, slabWidthPx, slabHeightPx)
            invalidateSelf()
        }

        override fun draw(canvas: Canvas) {
            val width = bounds.width().toFloat()
            if (width <= 0f) return
            val left = bounds.left + leadingShare * width
            val band = Motion.SWEEP_BAND_SHARE * width
            paint.shader = LinearGradient(
                left,
                0f,
                left + band,
                0f,
                intArrayOf(Color.TRANSPARENT, Motion.tint, Color.TRANSPARENT),
                null,
                Shader.TileMode.CLAMP,
            )
            val save = canvas.save()
            canvas.skew(Motion.SWEEP_SKEW_TAN, 0f)
            canvas.drawRect(left, bounds.top.toFloat(), left + band, bounds.top + heightPx, paint)
            canvas.restoreToCount(save)
        }

        override fun setAlpha(alpha: Int) {
            paint.alpha = alpha
        }

        override fun setColorFilter(colorFilter: ColorFilter?) {
            paint.colorFilter = colorFilter
        }

        override fun getOpacity(): Int = PixelFormat.TRANSLUCENT
    }

    /** The peak of the streak's gradient: the token's colour at the token's alpha. */
    private val tint: Int
        get() = Color.argb(
            (SWEEP_PEAK_ALPHA * ALPHA_FULL).roundToInt(),
            SWEEP_TINT_R,
            SWEEP_TINT_G,
            SWEEP_TINT_B,
        )

    /** CSS `skewX(-25deg)` as the horizontal shear a [Canvas] takes. Computed, not transcribed. */
    private val SWEEP_SKEW_TAN: Float = tan(Math.toRadians(SWEEP_SKEW_DEG.toDouble())).toFloat()

    /** An 8-bit alpha channel's full value -- the unit conversion, not a design number. */
    private const val ALPHA_FULL = 255f
}
