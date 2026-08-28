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
 * ONE CLASS FOR BOTH, because they are the same shape at two sizes -- the maquette draws
 * `.tog { border-radius: 12px }` on a 24 dp track and `.tog i { border-radius: 50% }` on a 16 dp
 * thumb, which is "half the height" twice. A second class would be a second place the pill
 * exception is implemented, and the two would drift on the first edit.
 *
 * **BOTH COLOURS ARE `var`, AND THE THUMB'S DOES CHANGE.** This KDoc used to say the opposite in
 * capitals -- "THE COLOUR IS A `var` AND THE THUMB'S NEVER CHANGES. Row 4 gives the thumb
 * `--p-ink` in both states" -- which was row 4's specification for a control Substrate never drew.
 * The maquette gives the thumb `--p-ink3` off and `--p-hero-ink` on, and it has to: a pale thumb
 * on the accent track is light-on-light, the one contrast pair the fill's own ceiling makes
 * unwinnable (ADR-009 D8.1's amendment measured max |Lc| on champagne `#c9a876` at 59.73; ADR-021
 * re-measured it on slate `#8eb4e6` at 62.04, with pure white reaching only 46.58). So three colours
 * cross between the two states -- this drawable's fill, this drawable's border, and the thumb's
 * own fill -- each through [Motion.colorTransition], which reports values to a lambda rather than
 * applying them to a view. That indirection is the reason this is a shaped drawable at all:
 * `View.setBackgroundColor` would replace the capsule with a flat rectangle on the first frame.
 *
 * THE BORDER IS DRAWN INSIDE THE BOUNDS, because the maquette sets `box-sizing: border-box`: the
 * track's stated 40x24 INCLUDES its 1 px border. A stroke straddles its path, so the path is inset
 * by half the weight and the corner radius shrinks by the same amount -- otherwise the outer edge
 * lands half a hairline outside the geometry every other assertion is made against.
 */
internal class TogglePill(
    colour: Int,
    val radiusPx: Float,
    borderColour: Int = colour,
    val borderPx: Float = 0f,
) : Drawable() {

    private val paint = Paint(Paint.ANTI_ALIAS_FLAG)
    private val border = Paint(Paint.ANTI_ALIAS_FLAG).apply {
        style = Paint.Style.STROKE
        strokeWidth = borderPx
    }
    private val rect = RectF()

    var colour: Int = colour
        set(value) {
            field = value
            paint.color = value
            invalidateSelf()
        }

    var borderColour: Int = borderColour
        set(value) {
            field = value
            border.color = value
            invalidateSelf()
        }

    init {
        // The initialisers above do not run through the setters, so the first colours are set here.
        paint.color = colour
        border.color = borderColour
    }

    override fun draw(canvas: Canvas) {
        if (bounds.isEmpty) return
        rect.set(bounds)
        canvas.drawRoundRect(rect, radiusPx, radiusPx, paint)
        if (borderPx <= 0f) return
        val half = borderPx / 2f
        rect.inset(half, half)
        canvas.drawRoundRect(rect, radiusPx - half, radiusPx - half, border)
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

    /** `.tog i { top }`: the gap between the thumb and the track's INNER edge. Off PB-DS-1's grid
     *  on purpose -- see [KitMetrics.TOGGLE_INSET_DP]. */
    private val insetPx = Kit.dpPx(context, KitMetrics.TOGGLE_INSET_DP)
    private val thumbPx = Kit.dpPx(context, KitMetrics.TOGGLE_THUMB_DP)

    /** `.tog { border }` -- the same `1px solid` every other bordered surface in this design has. */
    private val borderPx = Kit.dpPx(context, KitMetrics.HAIRLINE_DP)

    /**
     * `.tog { width: 40px; height: 24px }`, stated by the design rather than summed from the parts.
     *
     * THE DIRECTION OF THE ARITHMETIC IS REVERSED FROM WHAT THIS FILE USED TO DO, and that is
     * ADR-009 D2's doing. It read
     *
     *	private val trackHeightPx = thumbPx + insetPx + insetPx
     *	private val trackWidthPx = thumbPx + travelPx + insetPx + insetPx
     *
     * because row 4's `track 46x28` was a pair of literals with no citable origin -- the
     * derivation-table reader parses `field <number>` and cannot read `track 46x28` at all -- so
     * the sum was the only checkable form. The maquette DECLARES the width and the height, so they
     * are transcribed, and the travel is what is left over.
     */
    private val trackWidthPx = Kit.dpPx(context, KitMetrics.TOGGLE_TRACK_WIDTH_DP)
    private val trackHeightPx = Kit.dpPx(context, KitMetrics.TOGGLE_TRACK_HEIGHT_DP)

    /**
     * How far the thumb slides: the width with everything else taken out of it.
     *
     * `.tog i` is absolutely positioned at `left: 3px` and, when on, at `right: 3px` -- and
     * `box-sizing: border-box` puts both insets inside the 1 px border. So the two rest positions
     * are `border + inset` from each end, and the distance between them is what remains.
     */
    private val travelPx = trackWidthPx - 2 * (borderPx + insetPx) - thumbPx

    private val offColour = Kit.colour(context, R.color.swarm_surface_elevated)
    private val onColour = Kit.colour(context, R.color.swarm_hero)
    private val offEdge = Kit.colour(context, R.color.swarm_hairline)
    private val onEdge = onColour
    private val offThumb = Kit.colour(context, R.color.swarm_text_tertiary)
    private val onThumb = Kit.colour(context, R.color.swarm_hero_ink)

    /**
     * The pill exception, first half: radius = half the track, plus the design's hairline edge.
     *
     * Substrate's shape ladder has four steps and no pill among them, and the exception's argument
     * survives the reskin unchanged -- a squared track reads as a checkbox. Half a height is not a
     * fifth radius any more than the status dot's circle is: it is what "capsule" means, and the
     * maquette's own `border-radius: 12px` on a 24 px track says the same thing twice.
     */
    private val track = TogglePill(
        colour = colourFor(initiallyChecked),
        radiusPx = trackHeightPx / 2f,
        borderColour = edgeFor(initiallyChecked),
        borderPx = borderPx.toFloat(),
    )

    /**
     * The pill exception, second half: radius = half the thumb, which makes it a circle.
     *
     * ITS MARGINS CARRY THE BORDER AS WELL AS THE INSET. `.tog i { top: 3px }` is measured from
     * the padding box, which starts inside `.tog`'s 1 px border; a thumb inset by 3 alone would
     * sit a hairline high and left of where the design draws it, and the travel computed above
     * would carry it a hairline past the far edge.
     */
    private val thumb = View(context).apply {
        background = TogglePill(thumbFor(initiallyChecked), thumbPx / 2f)
        layoutParams = LayoutParams(thumbPx, thumbPx).apply {
            marginStart = borderPx + insetPx
            topMargin = borderPx + insetPx
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
     * Move to [checked] on ADR-009 D5's 150 ms, and return the four animators that carry it there.
     *
     * ALL FOUR PARTS MOVE, AND THE COUNT USED TO BE TWO. The maquette gives the two states four
     * differences -- the thumb's position, the track's fill, the track's border and the thumb's
     * own ink -- and on a control whose whole travel is 16 dp it is the crossfades that read as
     * the state change. A thumb that slid over a track that jumped would look like two unrelated
     * events, and that is as true of the border and the thumb ink as it was of the fill.
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
        val fill = Motion.colorTransition(
            context,
            track.colour,
            colourFor(checked),
            Motion.TOGGLE_DURATION_MS,
        ) { track.colour = it }
        val edge = Motion.colorTransition(
            context,
            track.borderColour,
            edgeFor(checked),
            Motion.TOGGLE_DURATION_MS,
        ) { track.borderColour = it }
        val ink = Motion.colorTransition(
            context,
            thumbPill.colour,
            thumbFor(checked),
            Motion.TOGGLE_DURATION_MS,
        ) { thumbPill.colour = it }
        val moving = listOf(slide, fill, edge, ink)
        moving.forEach { it.start() }
        return moving
    }

    private val thumbPill: TogglePill get() = thumb.background as TogglePill

    /**
     * `.tog.on { background: var(--p-hero) }`: on is the hero, not `--p-ok`.
     *
     * After ADR-007 B134 `--p-ok` carries ReadyForReview, and a control's on-state is not a status.
     * `.chip.on` is the precedent: a hero fill is what "engaged" looks like in this skin, and the
     * same green on a toggle and on an inbox dot would be one colour saying two unrelated things.
     */
    private fun colourFor(checked: Boolean): Int = if (checked) onColour else offColour

    /** `.tog { border: 1px solid var(--p-hair) }` / `.tog.on { border-color: var(--p-hero) }`. */
    private fun edgeFor(checked: Boolean): Int = if (checked) onEdge else offEdge

    /** `.tog i { background: var(--p-ink3) }` / `.tog.on i { background: var(--p-hero-ink) }`. */
    private fun thumbFor(checked: Boolean): Int = if (checked) onThumb else offThumb

    private fun travelFor(checked: Boolean): Float = if (checked) travelPx.toFloat() else 0f
}

/**
 * derived: docs/design/substrate-components.md #4 Toggle
 *
 * The settings row's other trailing control: a switch the user changes, where [statusLabel] is a
 * state the row reports.
 *
 * **THE `derived:` LINE ABOVE IS NOW A CITATION FOR THE BEHAVIOUR, NOT FOR THE DRAWING**, and the
 * distinction is what ADR-009 D2 changed. This KDoc used to argue that there is deliberately no
 * `origin:` line because "the shared Substrate block declares no `.toggle` rule AT ALL", which was
 * true of that artifact and is false of `docs/research/obsidian-maquette.html`: it draws `.tog`,
 * `.tog i`, `.tog.on` and `.tog.dis`, and every number and colour this component renders is
 * transcribed from those rules through `KitMetrics`' `origin: maquette` annotations. Row 4 still
 * supplies what the maquette does not draw -- the 150 ms, the touch-target assignment, and the
 * argument for a hero fill over `--p-ok`.
 *
 * ITS OFF TRACK IS NO LONGER A DERIVED COLOUR. It was the fifth entry in
 * `internal/design.Derivations()` -- `color-mix(in srgb, --p-ink3 40%, transparent)`, row 4's,
 * the only one whose authority was that document rather than a `color-mix()` the design itself
 * writes. The maquette gives the track `--p-elev` inside a `--p-hair` hairline, which are two
 * tokens and no blend, so the derivation was retired with its last consumer.
 *
 * THE >=48 dp TOUCH TARGET IS NOT SET HERE, AND ROW 4 IS STILL WHY. Its clause is ">=48 with the
 * VISUAL UNCHANGED", and a 40x24 control grown to 48 dp meets the number by destroying the drawing
 * the same clause protects. Row 15 says where it belongs instead -- "the whole row is one >=48 dp
 * target when it carries a toggle" -- so `settingsRow` spends `KitMetrics.MIN_TARGET_DP` and this
 * is the 40x24 visual inside it. `s23TouchTargets` in `android/gate/s23_kit_test.go` records that
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
