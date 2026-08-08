package dev.swarm.phone.ui.kit

import android.content.Context
import android.graphics.Bitmap
import android.graphics.BitmapFactory
import android.graphics.BitmapShader
import android.graphics.BlendMode
import android.graphics.Canvas
import android.graphics.ColorFilter
import android.graphics.Paint
import android.graphics.PixelFormat
import android.graphics.Shader
import android.graphics.drawable.Drawable
import dev.swarm.phone.R
import kotlin.math.roundToInt

/**
 * derived: docs/design/substrate-components.md #21 Grain overlay
 *
 * ADR-009 D4.3's microstructure: a pre-rendered warm-neutral tile, repeated across the whole
 * surface at `--p-grain` under `BlendMode.SOFT_LIGHT`.
 *
 * **IT IS AN ASSET AND NOT A COLOUR, WHICH IS ROW 21's FIRST CELL.** `feTurbulence` output is
 * implementation-defined, so the design's noise is rendered ONCE and checked in;
 * `scripts/render-grain.go` is its origin -- a fixed seed, a stated distribution and a stated tint
 * -- and `android/gate/o3_material_test.go` measures what that produced. A tile regenerated at
 * build time would make every screenshot in `docs/verification/` a different picture for no
 * recorded reason.
 *
 * **WHY GRAIN AT ALL, WHICH IS THE HALF THAT LOOKS LIKE DECORATION AND IS NOT.** ADR-009's
 * research constraint is that a premium signifier detected as decoration inverts into cheapness,
 * so every effect must be traceable to the interface's own geometry and light. Grain is traceable
 * to neither, and it is not an effect: it is MATERIAL -- the reason a surface reads as volcanic
 * glass rather than as a rectangle of hex colour -- and at 4% it is present as texture and
 * invisible as a thing. If you can see it, it is wrong.
 *
 * **SOFT_LIGHT AND NOT AN ALPHA WASH.** Soft-light returns the backdrop unchanged where the source
 * is mid-grey and lightens or darkens it either side, so a centred tile adds variance WITHOUT
 * moving the ladder. A source-over composite at the same opacity lays a flat grey over everything
 * -- lighter near-blacks, a compressed ladder -- and looks identical in a diff.
 *
 * **IT IS A FOREGROUND WHEREVER IT IS USED**, which is a fact about what it is rather than a
 * suggestion: row 21 says non-interactive, and a foreground drawable cannot take a touch at all.
 * `scanReticle` makes the same argument over a camera preview for the same reason.
 *
 * @return a fresh instance per call. A `Drawable` carries mutable bounds, so a shared one would be
 *  resized by whichever view laid out last.
 */
fun grainOverlay(context: Context): GrainDrawable = GrainDrawable(
    // `drawable-nodpi` RETURNS THE FILE'S OWN PIXELS, which is the whole reason the raster lives in
    // that folder. A density-qualified copy would be resampled to 385 px on a 2.75x handset, so
    // the design's 140x140 tile would describe no device and the grain would be coarse on one
    // phone and fine on another.
    tile = BitmapFactory.decodeResource(context.resources, R.drawable.swarm_grain),
    // The token is a bare fraction and the platform wants 8 bits. ROUNDED, not truncated, for
    // ColorMix.quantise's reason: rounding is what an 8-bit quantisation must do, and the two
    // arithmetics agree at most values and separate exactly where it matters.
    grainAlpha = (KitMetrics.GRAIN_ALPHA * 255f).roundToInt(),
    blend = BlendMode.SOFT_LIGHT,
)

/**
 * The grain as a drawable: one tiled shader, one blend mode, one alpha.
 *
 * THE BLEND AND THE ALPHA ARE EXPOSED FOR [SurfaceSpec]'s REASON. A `Paint` inside a `Drawable` has
 * no readable blend mode -- `Drawable` declares `setTintBlendMode` and no getter for it anywhere --
 * so an appearance test could otherwise assert that a bitmap was loaded and nothing at all about
 * how it is composited, which is the half that is invisible in a diff and obvious on a panel.
 *
 * THE TILING IS NOT EXPOSED AND IS ASSERTED IN PIXELS INSTEAD. `BitmapShader` has no getter for its
 * tile mode either, and a property here that returned `REPEAT` because the constructor passed
 * `REPEAT` would be the drawable agreeing with itself -- the self-comparison this project has
 * already shipped once. `GrainOverlayTest` draws the thing and looks at whether the second tile is
 * there.
 *
 * IT DRAWS A RECT RATHER THAN THE BITMAP, and that is what makes it tile. `Canvas.drawBitmap`
 * paints one copy; a `BitmapShader` on REPEAT fills whatever geometry is drawn with copies of it,
 * so filling the bounds IS the tiling.
 */
class GrainDrawable internal constructor(
    internal val tile: Bitmap,
    /**
     * `--p-grain` as 8 bits.
     *
     * IT IS NOT NAMED `alpha` OR `opacity`, and neither is available: `Drawable` already declares
     * `getAlpha`/`setAlpha` and `getOpacity`, so a property of either name is a JVM signature
     * clash with the platform. The design's opacity and a caller's are different numbers anyway,
     * which is what [setAlpha] below says out loud.
     */
    internal val grainAlpha: Int,
    internal val blend: BlendMode,
) : Drawable() {

    private val paint = Paint().apply {
        shader = BitmapShader(tile, Shader.TileMode.REPEAT, Shader.TileMode.REPEAT)
        alpha = grainAlpha
        blendMode = blend
    }

    override fun getIntrinsicWidth(): Int = tile.width

    override fun getIntrinsicHeight(): Int = tile.height

    override fun draw(canvas: Canvas) {
        if (bounds.isEmpty) return
        canvas.drawRect(bounds, paint)
    }

    /**
     * DELIBERATELY INERT. The design's opacity is the whole of `--p-grain`, and a caller that
     * dimmed the overlay -- or a fade that drove it -- would be re-deciding a token at a call site,
     * which is what PB-DS-11 exists to stop. Overriding it to do nothing is louder than leaving the
     * base class's behaviour, which would accept the change silently.
     */
    override fun setAlpha(alpha: Int) = Unit

    override fun setColorFilter(colorFilter: ColorFilter?) { paint.colorFilter = colorFilter }

    override fun getOpacity(): Int = PixelFormat.TRANSLUCENT
}
