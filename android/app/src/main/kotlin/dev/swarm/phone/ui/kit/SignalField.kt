package dev.swarm.phone.ui.kit

import android.content.Context
import android.graphics.Canvas
import android.graphics.Color
import android.graphics.ColorFilter
import android.graphics.LinearGradient
import android.graphics.Paint
import android.graphics.PixelFormat
import android.graphics.Rect
import android.graphics.Shader
import android.graphics.drawable.Drawable
import android.util.TypedValue
import android.view.Gravity
import android.view.View
import android.view.ViewGroup
import android.widget.FrameLayout
import android.widget.ImageView
import android.widget.LinearLayout
import androidx.core.widget.TextViewCompat
import dev.swarm.phone.R

/**
 * derived: docs/design/substrate-components.md §4 Signal field welcome
 *
 * The atmospheric swarm that introduces a true first run. The raster is the same approved mark
 * used by the launcher, with no landscape or moon behind it; this view only gives it the measured
 * field and quiet opacity from frame 01 of the mobile direction.
 *
 * It is decorative by construction. The title directly below says what it means, so exposing the
 * image as a second accessibility node would make a screen reader announce the identity twice.
 */
fun signalFieldMark(context: Context): ImageView = ImageView(context).apply {
    setImageResource(R.drawable.swarm_atmospheric_mark)
    scaleType = ImageView.ScaleType.FIT_CENTER
    alpha = KitMetrics.SIGNAL_FIELD_MARK_ALPHA
    importantForAccessibility = View.IMPORTANT_FOR_ACCESSIBILITY_NO
    layoutParams = LinearLayout.LayoutParams(
        ViewGroup.LayoutParams.MATCH_PARENT,
        Kit.dpPx(context, KitMetrics.SIGNAL_FIELD_MARK_DP),
    )
}

/**
 * derived: docs/design/substrate-components.md #7 SAS display
 *
 * Six autonomous beacons on one ordered, static trajectory. The protocol still supplies and
 * compares the six symbols; this component only gives that existing sequence the approved frame
 * 03 grammar. The path is behind opaque cards, so it joins the beacons without crossing a glyph.
 *
 * Accessibility exposes one ordered sentence rather than seven stops. The visual children are
 * decorative; order remains present in both the child list and the one spoken description.
 */
fun sasSequence(context: Context, symbols: List<String>): FrameLayout = FrameLayout(context).apply {
    layoutParams = LinearLayout.LayoutParams(MATCH, WRAP)
    importantForAccessibility = View.IMPORTANT_FOR_ACCESSIBILITY_YES
    contentDescription = "Verification symbols, in order: ${symbols.joinToString(", ")}"

    addView(
        View(context).apply {
            importantForAccessibility = View.IMPORTANT_FOR_ACCESSIBILITY_NO
            background = SignalPathDrawable(
                SignalPathSpec(
                    ink = Kit.colour(context, R.color.swarm_hero),
                    strokePx = Kit.dpPx(context, KitMetrics.HAIRLINE_DP).toFloat(),
                ),
            )
        },
        FrameLayout.LayoutParams(MATCH, MATCH).apply {
            marginStart = Kit.dimenPx(context, R.dimen.swarm_space_24)
            marginEnd = Kit.dimenPx(context, R.dimen.swarm_space_24)
            topMargin = Kit.dimenPx(context, R.dimen.swarm_space_18)
            bottomMargin = Kit.dimenPx(context, R.dimen.swarm_space_8)
        },
    )

    addView(
        LinearLayout(context).apply {
            orientation = LinearLayout.HORIZONTAL
            importantForAccessibility = View.IMPORTANT_FOR_ACCESSIBILITY_NO
            symbols.forEach { symbol ->
                addView(
                    Kit.textView(context).apply {
                        Kit.appearance(this, R.style.TextAppearance_Swarm_Display_SAS)
                        text = symbol
                        gravity = Gravity.CENTER
                        maxLines = 1
                        background = cardSurface(context, attention = false)
                        importantForAccessibility = View.IMPORTANT_FOR_ACCESSIBILITY_NO
                        val beaconPx = Kit.dpPx(context, KitMetrics.SAS_BEACON_DP)
                        TextViewCompat.setAutoSizeTextTypeUniformWithConfiguration(
                            this,
                            beaconPx / 2,
                            textSize.toInt(),
                            1,
                            TypedValue.COMPLEX_UNIT_PX,
                        )
                    },
                    LinearLayout.LayoutParams(
                        0,
                        Kit.dpPx(context, KitMetrics.SAS_BEACON_DP),
                        KitMetrics.SIGNAL_FIELD_EQUAL_WEIGHT,
                    ).apply {
                        marginStart = Kit.dimenPx(context, R.dimen.swarm_space_4)
                        marginEnd = Kit.dimenPx(context, R.dimen.swarm_space_4)
                    },
                )
            }
        },
        FrameLayout.LayoutParams(MATCH, WRAP).apply {
            topMargin = Kit.dimenPx(context, R.dimen.swarm_space_18)
            bottomMargin = Kit.dimenPx(context, R.dimen.swarm_space_8)
        },
    )
}

internal data class SignalPathSpec(val ink: Int, val strokePx: Float)

/** A static transparent → hero → transparent trajectory behind the SAS beacons. */
internal class SignalPathDrawable(internal val spec: SignalPathSpec) : Drawable() {
    internal val clearInk = Color.argb(
        0,
        Color.red(spec.ink),
        Color.green(spec.ink),
        Color.blue(spec.ink),
    )
    private val paint = Paint(Paint.ANTI_ALIAS_FLAG).apply { strokeWidth = spec.strokePx }

    override fun onBoundsChange(bounds: Rect) {
        super.onBoundsChange(bounds)
        paint.shader = LinearGradient(
            bounds.left.toFloat(),
            bounds.exactCenterY(),
            bounds.right.toFloat(),
            bounds.exactCenterY(),
            intArrayOf(clearInk, spec.ink, clearInk),
            null,
            Shader.TileMode.CLAMP,
        )
    }

    override fun draw(canvas: Canvas) {
        if (bounds.isEmpty) return
        canvas.drawLine(
            bounds.left.toFloat(),
            bounds.exactCenterY(),
            bounds.right.toFloat(),
            bounds.exactCenterY(),
            paint,
        )
    }

    override fun setAlpha(alpha: Int) { paint.alpha = alpha }
    override fun setColorFilter(colorFilter: ColorFilter?) { paint.colorFilter = colorFilter }
    override fun getOpacity(): Int = PixelFormat.TRANSLUCENT
}
