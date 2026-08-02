package dev.swarm.phone.ui.kit

import android.animation.Animator
import android.content.Context
import android.graphics.Canvas
import android.graphics.ColorFilter
import android.graphics.Paint
import android.graphics.PixelFormat
import android.graphics.RectF
import android.graphics.drawable.Drawable
import android.view.View
import android.widget.FrameLayout
import android.widget.LinearLayout
import dev.swarm.phone.R

/**
 * A capsule: the toggle's track, and the toggle's thumb.
 *
 * ONE CLASS FOR BOTH, because they are the same shape at two sizes -- row 4's exception column
 * covers them in one sentence, "radius = half the track (14) and half the thumb (12)". A second
 * class would be a second place the pill exception is implemented, and the two would drift on the
 * first edit.
 *
 * THE COLOUR IS A `var` AND THE THUMB'S NEVER CHANGES. Row 4 gives the thumb `--p-ink` in both
 * states; it is the TRACK that crosses between two colours, and it crosses through
 * [Motion.colorTransition], which reports each value to a lambda rather than applying it to a view.
 * That indirection is the reason this is a shaped drawable at all: `View.setBackgroundColor` would
 * replace the capsule with a flat rectangle on the first animated frame.
 */
internal class TogglePill(colour: Int, val radiusPx: Float) : Drawable() {

    private val paint = Paint(Paint.ANTI_ALIAS_FLAG)
    private val rect = RectF()

    var colour: Int = colour
        set(value) {
            field = value
            paint.color = value
            invalidateSelf()
        }

    init {
        // The initialiser above does not run through the setter, so the first colour is set here.
        paint.color = colour
    }

    override fun draw(canvas: Canvas) {
        if (bounds.isEmpty) return
        rect.set(bounds)
        canvas.drawRoundRect(rect, radiusPx, radiusPx, paint)
    }

    override fun setAlpha(alpha: Int) { paint.alpha = alpha }
    override fun setColorFilter(colorFilter: ColorFilter?) { paint.colorFilter = colorFilter }
    override fun getOpacity(): Int = PixelFormat.TRANSLUCENT
}

/**
 * The toggle itself: a track, a thumb on it, and the 150 ms that carries one to the other.
 *
 * **IT DOES NOT HANDLE ITS OWN TAP, AND NO COMPONENT IN THIS KIT DOES.** PB-DS-6's sentence is that
 * a screen composes components and passes data; every factory here builds appearance and leaves
 * interaction to the screen, which is also where row 15 puts this one's touch target -- "the whole
 * row is one >=48 dp target when it carries a toggle". A toggle that installed its own click
 * listener would be a 46x28 interactive element, which is the PB-DS-12 floor broken by the
 * component that was trying to meet it.
 *
 * SO THE STATE CHANGE IS A CALL, [moveTo], and it hands back the animators it started. They are
 * returned rather than kept private because the alternative for an appearance test is to build a
 * second pair and assert against those -- which certifies a copy. This is the same arrangement
 * [Motion]'s own builders have with `MotionTest`, one step further out.
 */
class ToggleSwitch internal constructor(
    context: Context,
    initiallyChecked: Boolean,
) : FrameLayout(context) {

    /** Row 4's `inset`, off PB-DS-1's grid: the gap between the thumb and the track's edge. */
    private val insetPx = Kit.dimenPx(context, R.dimen.swarm_space_2)
    private val thumbPx = Kit.dpPx(context, KitMetrics.TOGGLE_THUMB_DP)
    private val travelPx = Kit.dpPx(context, KitMetrics.TOGGLE_TRAVEL_DP)

    /**
     * Row 4's `track 46x28`, as the sum it is: `24 + 2 + 2` and `24 + 18 + 2 + 2`.
     *
     * THE SUM RATHER THAN THE STATED PAIR, and the reason is the same one EmptyState.kt gives for
     * spending `space_24 + space_24` instead of `2 * space_24`: a literal in this package needs a
     * design origin or a row on a nine-line exemption table, and `46` and `28` have neither -- the
     * derivation-table reader parses `field <number>` and cannot read `track 46x28` at all. The sum
     * is the row's own arithmetic with nothing added to it, and it is what makes the track follow
     * the thumb if the thumb ever moves.
     */
    private val trackHeightPx = thumbPx + insetPx + insetPx
    private val trackWidthPx = thumbPx + travelPx + insetPx + insetPx

    private val offColour = Kit.toggleTrackOff(context)
    private val onColour = Kit.colour(context, R.color.swarm_hero)

    /**
     * The pill exception, first half: radius = half the track.
     *
     * Substrate's shape ladder has four steps and no pill among them, and row 4's exception column
     * argues the case -- a squared track reads as a checkbox. Half a height is not a fifth radius
     * any more than the status dot's circle is: it is what "capsule" means.
     */
    private val track = TogglePill(colourFor(initiallyChecked), trackHeightPx / 2f)

    /** The pill exception, second half: radius = half the thumb, which makes it a circle. */
    private val thumb = View(context).apply {
        background = TogglePill(Kit.colour(context, R.color.swarm_text_primary), thumbPx / 2f)
        layoutParams = LayoutParams(thumbPx, thumbPx).apply {
            marginStart = insetPx
            topMargin = insetPx
        }
        tag = KitTag.TOGGLE_THUMB
    }

    /** Which end of the travel the thumb is at. Changed through [moveTo] and nowhere else. */
    var checked: Boolean = initiallyChecked
        private set

    init {
        background = track
        addView(thumb)
        thumb.translationX = travelFor(initiallyChecked)
        layoutParams = LinearLayout.LayoutParams(trackWidthPx, trackHeightPx)
        tag = KitTag.TOGGLE_TRACK
    }

    /**
     * Move to [checked] on row 4's 150 ms, and return the two animators that carry it there.
     *
     * BOTH PARTS MOVE. The thumb slides its 18 dp and the track crosses between two colours, and on
     * a control whose whole travel is 18 dp it is the crossfade that reads as the state change --
     * a thumb that slid over a track that jumped would look like two unrelated events.
     *
     * ADR-007 B134 decision 3 is why this goes through [Motion] rather than building its own
     * animators: the artifact's `prefers-reduced-motion` selector list is `.g-work, .banner, .sheet,
     * .stream-caret`, and the toggle's own 0.15s transitions are the only OTHER transitions in the
     * document -- so the list leaving them out reads as an omission rather than a considered
     * exclusion. [Motion.translateX] and [Motion.colorTransition] exist exactly so a toggle inherits
     * the `ANIMATOR_DURATION_SCALE` check without that file owning any toggle view code.
     *
     * A ZERO-DURATION ANIMATOR STILL LANDS THE END STATE, which is what makes reduced motion an
     * accommodation rather than a broken switch: it delivers one update at a full fraction, so the
     * thumb arrives and the track arrives, immediately and without moving.
     */
    fun moveTo(checked: Boolean): List<Animator> {
        this.checked = checked
        val slide = Motion.translateX(
            context,
            thumb,
            thumb.translationX,
            travelFor(checked),
            Motion.TOGGLE_DURATION_MS,
        )
        val cross = Motion.colorTransition(
            context,
            track.colour,
            colourFor(checked),
            Motion.TOGGLE_DURATION_MS,
        ) { track.colour = it }
        slide.start()
        cross.start()
        return listOf(slide, cross)
    }

    /**
     * Row 4: on is `--p-hero`, not `--p-ok`.
     *
     * After ADR-007 B134 `--p-ok` carries ReadyForReview, and a control's on-state is not a status.
     * `.chip.on` is the precedent the row cites: a hero fill is what "engaged" looks like in this
     * skin, and the same green on a toggle and on an inbox dot would be one colour saying two
     * unrelated things.
     */
    private fun colourFor(checked: Boolean): Int = if (checked) onColour else offColour

    private fun travelFor(checked: Boolean): Float = if (checked) travelPx.toFloat() else 0f
}

/**
 * derived: docs/design/substrate-components.md #4 Toggle
 *
 * The settings row's other trailing control: a switch the user changes, where [statusLabel] is a
 * state the row reports.
 *
 * There is deliberately no `origin:` line, and the distinction is load-bearing rather than
 * bookkeeping. The shared Substrate block declares no `.toggle` rule AT ALL -- the artifact draws
 * four candidate skins and a triage inbox, and no settings screen to put a switch on -- so row 4 is
 * the entire specification for one, exactly as row 8 is for [emptyState] and row 9 for [textField].
 * Citing a selector the artifact does not contain is how a value acquires an authority nobody can
 * look up.
 *
 * ITS OFF TRACK IS THE FIFTH ENTRY IN `internal/design.Derivations()`, and the first one whose
 * authority is that document rather than a `color-mix()` the artifact itself writes. That table's
 * header already invited it: mock-derived values "belong in this table the moment a Substrate spec
 * exists for them", and row 4 is that spec. The alternatives all fail honestly -- the doc-metric
 * grammar reads `field <number>` and the only match for "at 40%" cites a preposition, the toggle is
 * not in `tokens.json`, and PB-TOK-7 forbids resolving `--p-ink3` at 40% into a literal resource.
 *
 * THE >=48 dp TOUCH TARGET IS NOT SET HERE, AND ROW 4 IS WHY. Its clause is ">=48 with the VISUAL
 * UNCHANGED", and a 46x28 control grown to 48 dp meets the number by destroying the drawing the
 * same clause protects. Row 15 says where it belongs instead -- "the whole row is one >=48 dp
 * target when it carries a toggle" -- so `settingsRow` spends `KitMetrics.MIN_TARGET_DP` and this
 * is the 46x28 visual inside it. `s23TouchTargets` in `android/gate/s23_kit_test.go` records that
 * assignment and fails if either row stops stating the floor or the row stops carrying it.
 *
 * @param description PB-DS-12's floor, and it is required rather than nullable. A toggle is four
 *  moving dp and no text, so a screen reader gets nothing at all from the visual -- there is no
 *  such thing as a decorative one, which is the case [statusDot]'s optional description exists for.
 *  The words are the screen's (PB-DS-9). What this component does NOT do is announce the STATE:
 *  "on" and "off" are copy too, `stateDescription` is where they would go, and a kit that invented
 *  them would be a component with an opinion about wording. Recorded as a gap rather than guessed.
 */
fun toggle(context: Context, checked: Boolean, description: CharSequence): ToggleSwitch =
    ToggleSwitch(context, checked).apply { contentDescription = description }
