package dev.swarm.phone.ui.kit

import android.content.Context
import android.graphics.Canvas
import android.graphics.ColorFilter
import android.graphics.Paint
import android.graphics.PixelFormat
import android.graphics.RectF
import android.graphics.drawable.Drawable
import dev.swarm.phone.R

/**
 * The reticle's geometry.
 *
 * THE SPEC IS EXPOSED FOR [FocusRingSpec]'s REASON: a `Paint` has no readable stroke geometry once
 * it is inside a `Drawable`, so an appearance test could otherwise assert the colour and nothing
 * about where the frame is or how much of it is painted.
 */
internal data class ScanReticleSpec(
    val ink: Int,
    val strokePx: Float,
    /** The side of the square the brackets mark out, before any clamping. */
    val framePx: Float,
    /** How far one bracket runs from its corner along each of the two edges that meet there. */
    val armPx: Float,
    val radiusPx: Float,
)

/**
 * derived: docs/design/substrate-components.md §4 Scanner reticle
 *
 * The framing square over the camera preview: where to point the phone, and how close to hold it.
 *
 * **IT IS A DRAWABLE AND NOT A VIEW, AND THAT IS A SAFETY PROPERTY RATHER THAN A STYLE.** A view
 * laid over a live camera preview is one `isClickable` away from being the surface PB-SEC-12
 * clause 1 exists for -- something over content the user is looking at that can take the tap they
 * aimed past it. A foreground drawable cannot take a touch at all, so the property is structural:
 * there is no later edit to this file that makes the reticle tappable without first making it a
 * different kind of thing.
 *
 * **AND IT CANNOT OUTLIVE THE PREVIEW.** It is the foreground of `QrScanner`'s own `PreviewView`,
 * which `QrScanner.stop()` sets `GONE` -- so releasing the camera takes the reticle with it in the
 * same statement, rather than leaving a green frame hanging over a dead viewfinder. The obvious
 * implementation is a sibling view in the pairing screen's scanner host, and it is the one that
 * has a second lifetime to keep in step.
 *
 * **THE INK IS `--p-hero` AND THE ROW REJECTS `--p-ink` ON SCANNABILITY.** §1.1 rules hero out of
 * the focus ring because it means *selected*; here the same token's other gloss is the one that
 * applies -- `android/design-tokens.tsv` calls it brand and LIVE, and a viewfinder is the most
 * literal live surface in the app. What decides it against white is row 6: the symbol this frames
 * is drawn as a `#FFFFFFFF` tile, so a white reticle over a code held to fill it is a white line
 * on white.
 *
 * **IT DOES NOT MOVE.** ADR-007 B134 decision 3 keeps three motions and this is not among them.
 * The decode-confirmation flash asked for alongside it is recorded at §8.9 and is not built --
 * it would be a fourth motion, and it could not be seen in any case, because the payload's
 * hand-off and the preview's teardown happen inside one main-thread message.
 */
fun scanReticle(context: Context): ScanReticleDrawable = ScanReticleDrawable(
    ScanReticleSpec(
        ink = Kit.colour(context, R.color.swarm_hero),
        strokePx = Kit.dp(context, KitMetrics.RETICLE_STROKE_DP),
        framePx = Kit.dp(context, KitMetrics.RETICLE_FRAME_DP),
        armPx = Kit.dp(context, KitMetrics.RETICLE_ARM_DP),
        // Row 6's radius, because this frames row 6's tile. It is a TOKEN with a resource, so it
        // is read rather than carried as a constant -- which is the split KitMetrics exists for.
        radiusPx = Kit.dimen(context, R.dimen.swarm_radius_card),
    ),
)

/**
 * The reticle as a drawable: four corner brackets on a centred square.
 *
 * THE BRACKETS ARE THE WHOLE FRAME, CLIPPED. Each corner draws the same rounded rectangle through
 * a clip that admits only that corner's own box, so the radius is exactly `--p-card-r` at every
 * corner and the middle of every edge is left alone. Drawing four separate paths would put the
 * corner's curve in this file as arithmetic of its own, and a curve computed twice is a curve that
 * disagrees with itself the day the radius moves.
 *
 * THE STROKE IS DRAWN INSIDE THE FRAME, for [FocusRingDrawable]'s reason: `Canvas.drawRoundRect`
 * centres a stroke on the path it is given, so a square laid on the frame's own edge loses half
 * its width wherever the preview clips -- a 2 dp mark in every value a test reads and 1 dp on
 * screen.
 */
class ScanReticleDrawable internal constructor(internal val spec: ScanReticleSpec) : Drawable() {

    private val paint = Paint(Paint.ANTI_ALIAS_FLAG).apply {
        style = Paint.Style.STROKE
        color = spec.ink
        strokeWidth = spec.strokePx
    }

    /** Reused across draws: this is painted on every frame the camera produces. */
    private val frame = RectF()

    override fun draw(canvas: Canvas) {
        if (bounds.isEmpty) return
        val half = spec.strokePx / 2f
        // CLAMPED RATHER THAN SCALED. The frame is an absolute size and the preview is the screen's
        // width, so on a narrow handset the frame is the larger of the two -- and a reticle drawn
        // past the preview is a green line across whatever is beside it.
        val reach = minOf(spec.framePx, bounds.width().toFloat(), bounds.height().toFloat()) / 2f
        frame.set(
            bounds.exactCenterX() - reach,
            bounds.exactCenterY() - reach,
            bounds.exactCenterX() + reach,
            bounds.exactCenterY() + reach,
        )
        frame.inset(half, half)

        // AND THE ARM IS CLAMPED WITH IT, so the brackets never meet. Two arms longer than half the
        // side would close the edge between them, which is the one drawing this component must not
        // produce: a closed rectangle is a border and says nothing about where to aim.
        val arm = minOf(spec.armPx, frame.width() / 2f)
        bracket(canvas, frame.left - half, frame.top - half, frame.left + arm, frame.top + arm)
        bracket(canvas, frame.right - arm, frame.top - half, frame.right + half, frame.top + arm)
        bracket(canvas, frame.left - half, frame.bottom - arm, frame.left + arm, frame.bottom + half)
        bracket(canvas, frame.right - arm, frame.bottom - arm, frame.right + half, frame.bottom + half)
    }

    /** The frame, admitted through one corner's box and nothing of the edges running away from it. */
    private fun bracket(canvas: Canvas, left: Float, top: Float, right: Float, bottom: Float) {
        canvas.save()
        canvas.clipRect(left, top, right, bottom)
        canvas.drawRoundRect(frame, spec.radiusPx, spec.radiusPx, paint)
        canvas.restore()
    }

    override fun setAlpha(alpha: Int) { paint.alpha = alpha }
    override fun setColorFilter(colorFilter: ColorFilter?) { paint.colorFilter = colorFilter }
    override fun getOpacity(): Int = PixelFormat.TRANSLUCENT
}
