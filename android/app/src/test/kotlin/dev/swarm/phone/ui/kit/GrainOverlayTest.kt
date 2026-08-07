package dev.swarm.phone.ui.kit

import android.content.Context
import android.graphics.Bitmap
import android.graphics.BlendMode
import android.graphics.Canvas
import androidx.test.core.app.ApplicationProvider
import dev.swarm.phone.theme.SwarmTheme
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotEquals
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.GraphicsMode
import kotlin.math.roundToInt

/**
 * FAILING-FIRST (TDD RED, GG-5) for PB-DS-5 and derivation row 21: the grain overlay.
 *
 * WHY THERE IS A ROBOLECTRIC SUITE FOR A DRAWABLE MADE OF ONE PNG. `android/gate/o3_material_test.go`
 * measures the checked-in raster -- its size, that it is centred on the soft-light neutral, that it
 * is warm rather than cool, that it is noise rather than a flat rectangle -- and follows the two
 * source hops that put it in front of a screen. What it cannot say is what the app RESOLVES: a
 * drawable can name the right file and still render as a single stretched smear, at full strength,
 * with the ordinary source-over blend, and every one of those is a value only the platform can be
 * asked for.
 *
 * THREE MISTAKES HERE ARE INVISIBLE IN A DIFF AND OBVIOUS ON A PANEL, and each has its own claim
 * below: a tile that does not repeat (one 140 px square in the corner, the rest bare), a tile at
 * full alpha (a grey wash over the whole app), and a tile composited SRC_OVER rather than
 * SOFT_LIGHT (the same grey wash, arrived at differently).
 */
@RunWith(RobolectricTestRunner::class)
// NATIVE, because LEGACY graphics stubs the bitmap stack and an intrinsic size read off a stubbed
// decode says nothing about the file on disk.
@GraphicsMode(GraphicsMode.Mode.NATIVE)
class GrainOverlayTest {

    private val context: Context
        get() = SwarmTheme.applyTo(ApplicationProvider.getApplicationContext())

    /**
     * Row 21: a 140x140 tile, REPEATED, at `--p-grain` under `BlendMode.SOFT_LIGHT`.
     *
     * THE OPACITY IS READ FROM THE TOKEN, not from the row and not from a number typed here.
     * `--p-grain` is `effect`-typed, so PB-TOK-6's converters produce no resource for it and the
     * kit carries the alpha as a named constant -- which is exactly the arrangement that lets a
     * value drift, and exactly why this asserts against the origin.
     */
    @Test
    fun `the grain is the checked-in tile, repeated, at the token's opacity, in soft light`() {
        val grain = grainOverlay(context)

        assertEquals(
            emptyList<String>(),
            mismatches(
                listOf(
                    Claim(
                        "`--p-grain` opacity",
                        (KitOrigin.fractionToken("--p-grain") * 255f).roundToInt(),
                        grain.grainAlpha,
                    ),
                    // READ OUT OF ROW 21 rather than typed: the row names the mode, and a suite
                    // that wrote SOFT_LIGHT here would agree with the kit about a cell neither of
                    // them had opened.
                    Claim("row 21's blend", rowBlendMode(), grain.blend),
                    // THE TILE IS SQUARE AND UNSCALED, which is what `drawable-nodpi` buys and the
                    // reason the raster is in that folder: a density-qualified copy would be
                    // resampled to 385 px on a 2.75x handset, so the design's "140x140 tile" would
                    // describe no device and the grain would be coarse on one phone and fine on
                    // another. The exact number is pinned against the FILE in the Go gate; what
                    // this asserts is that the platform did not resample it on the way in.
                    Claim("the tile is square", grain.intrinsicWidth, grain.intrinsicHeight),
                ),
            ),
        )
        assertNotEquals(
            "the grain is fully opaque, so it is a grey wash over every surface in the app rather " +
                "than microstructure in them",
            255,
            grain.grainAlpha,
        )
    }

    /**
     * IT TILES, ASSERTED IN PIXELS BECAUSE NOTHING ELSE CAN ASSERT IT.
     *
     * `BitmapShader` has no getter for its tile mode, so a `tileModeX` property on the drawable
     * would return REPEAT because the constructor passed REPEAT -- the drawable agreeing with
     * itself, which is the self-comparison this project has already shipped once. What settles it
     * is drawing the overlay across an area LARGER than one tile and asking whether the second
     * tile is there: a CLAMPed shader paints one 140 px square and stretches its last row and
     * column across the rest, so the region beyond the tile is a smear rather than a repeat.
     */
    @Test
    fun `the tile repeats across an area larger than itself`() {
        val grain = grainOverlay(context)
        val tile = grain.intrinsicWidth
        val size = tile * 2
        grain.setBounds(0, 0, size, size)

        val bitmap = Bitmap.createBitmap(size, size, Bitmap.Config.ARGB_8888)
        grain.draw(Canvas(bitmap))

        // Every pixel one whole tile to the right and one down must be its origin's twin. A
        // clamped shader repeats only its last COLUMN, so it agrees on the seam and disagrees
        // everywhere else; a sample of interior points separates them.
        val offBy = (4 until tile step 17).count { i ->
            bitmap.getPixel(i, i) != bitmap.getPixel(i + tile, i + tile)
        }
        assertEquals(
            "the overlay drawn across two tiles is not periodic at the tile's own size, so the " +
                "shader is not repeating: one square of noise in the corner and a stretched smear " +
                "over the rest of the screen",
            0,
            offBy,
        )
        assertTrue(
            "the region beyond the first tile is untouched, so the overlay painted one tile and " +
                "left the rest of its bounds bare",
            (0 until 8).any { bitmap.getPixel(tile + 20 + it, tile + 20 + it) != 0 },
        )
    }

    /** The negative control PB-DS-10 requires, through the same comparison the claims above use. */
    @Test
    fun `the grain assertions can actually fail`() {
        assertTrue(
            "an ordinary source-over composite passes the comparison against SOFT_LIGHT, so the " +
                "grain could paint a flat grey over the ladder",
            mismatches(
                listOf(Claim("blend", rowBlendMode(), BlendMode.SRC_OVER)),
            ).isNotEmpty(),
        )
        assertTrue(
            "an opacity one unit from the token's passes the comparison",
            mismatches(listOf(Claim("alpha", 10, 11))).isNotEmpty(),
        )
        // AUTHORIZED KNOWN ANSWER, ADR-009 D3. The reader must be reading the origin rather than
        // answering a constant, and this is the one number in this suite typed independently of it
        // -- a token that had quietly become 1.0 is then contradicted by a number rather than by
        // itself, which is the failure a 4% effect is most likely to hide.
        assertEquals(
            "the origin's `--p-grain` is no longer ADR-009 D3's 4%, so either the token moved " +
                "without this suite noticing or the reader is answering something else",
            0.04f,
            KitOrigin.fractionToken("--p-grain"),
            0.0001f,
        )
        // And the row reader must find a real mode rather than defaulting to one.
        assertEquals(
            "derivation row 21 no longer names BlendMode.SOFT_LIGHT, so the claim above is " +
                "reading a cell that has stopped stating the blend",
            BlendMode.SOFT_LIGHT,
            rowBlendMode(),
        )
    }

    /** Row 21's `States, motion, notes` cell names the blend mode; this is that name, resolved. */
    private fun rowBlendMode(): BlendMode {
        val cell = KitOrigin.rowCell("Grain overlay", "States, motion, notes")
        val name = requireNotNull(Regex("BlendMode\\.([A-Z_]+)").find(cell)?.groupValues?.get(1)) {
            "derivation row 21 names no `BlendMode.X`, so there is nothing to composite the grain " +
                "with and every claim about how it blends would be this suite's own opinion"
        }
        return BlendMode.valueOf(name)
    }
}
