package dev.swarm.phone.ui.kit

import android.content.Context
import android.graphics.BlendMode
import android.graphics.Shader
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
        val tile = grainOverlay(context)

        assertEquals(
            emptyList<String>(),
            mismatches(
                listOf(
                    Claim("`.grain` repeats across", Shader.TileMode.REPEAT, tile.tileModeX),
                    Claim("`.grain` repeats down", Shader.TileMode.REPEAT, tile.tileModeY),
                    Claim(
                        "`--p-grain` opacity",
                        (KitOrigin.fractionToken("--p-grain") * 255f).roundToInt(),
                        tile.opacity,
                    ),
                    Claim("row 21's blend", BlendMode.SOFT_LIGHT, tile.blend),
                    // THE TILE IS SQUARE AND UNSCALED, which is what `drawable-nodpi` buys and the
                    // reason the raster is in that folder: a density-qualified copy would be
                    // resampled to 385 px on a 2.75x handset, so the design's "140x140 tile" would
                    // describe no device and the grain would be coarse on one phone and fine on
                    // another. The exact number is pinned against the FILE in the Go gate; what
                    // this asserts is that the platform did not resample it on the way in.
                    Claim("the tile is square", tile.intrinsicWidth, tile.intrinsicHeight),
                ),
            ),
        )
        assertNotEquals(
            "the grain is fully opaque, so it is a grey wash over every surface in the app rather " +
                "than microstructure in them",
            255,
            tile.opacity,
        )
    }

    /** The negative control PB-DS-10 requires, through the same comparison the claims above use. */
    @Test
    fun `the grain assertions can actually fail`() {
        assertTrue(
            "a CLAMPed tile passes the comparison against a REPEATed one, so the grain could " +
                "render as one square in the corner with the rest of the screen bare",
            mismatches(
                listOf(Claim("tile", Shader.TileMode.REPEAT, Shader.TileMode.CLAMP)),
            ).isNotEmpty(),
        )
        assertTrue(
            "an ordinary source-over composite passes the comparison against SOFT_LIGHT, so the " +
                "grain could paint a flat grey over the ladder",
            mismatches(
                listOf(Claim("blend", BlendMode.SOFT_LIGHT, BlendMode.SRC_OVER)),
            ).isNotEmpty(),
        )
        assertTrue(
            "an opacity one unit from the token's passes the comparison",
            mismatches(listOf(Claim("alpha", 10, 11))).isNotEmpty(),
        )
        // And the reader must be reading the token rather than answering a constant: --p-grain is
        // the only bare fraction in the origin, so this also says the reader found the right one.
        assertEquals(
            "the origin's `--p-grain` is no longer ADR-009 D3's 4%, so either the token moved " +
                "without this suite noticing or the reader is answering something else",
            0.04f,
            KitOrigin.fractionToken("--p-grain"),
            0.0001f,
        )
    }
}
