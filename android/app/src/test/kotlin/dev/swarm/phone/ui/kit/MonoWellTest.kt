package dev.swarm.phone.ui.kit

import android.content.Context
import android.util.TypedValue
import android.view.View
import android.widget.HorizontalScrollView
import androidx.test.core.app.ApplicationProvider
import dev.swarm.phone.theme.DesignScale
import dev.swarm.phone.theme.SwarmTheme
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.GraphicsMode
import kotlin.math.roundToInt

/**
 * FAILING-FIRST (TDD RED, GG-5) for PB-DS-10 over `.sheet2 .cmd`, and for the one PB-TOK-3 pin
 * that has never reached a pixel.
 *
 * THE TERMINAL VARIANT IS WHY THIS SUITE MATTERS MORE THAN ITS SIZE SUGGESTS. `tokens.json` pins
 * `terminal_peek.fg` to `--p-hero`, PB-TOK-3 has enforced that against the JSON since S22, and no
 * Android code ever read it -- so the assertion below is the first thing in the repository that
 * would fail if the phosphor foreground were wrong, as opposed to absent. A pin checked only
 * against the file that declares it is a pin checked against itself.
 */
@RunWith(RobolectricTestRunner::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
class MonoWellTest {

    private val context: Context
        get() = SwarmTheme.applyTo(ApplicationProvider.getApplicationContext())

    private val spScale: Float
        get() = TypedValue.applyDimension(
            TypedValue.COMPLEX_UNIT_SP, 1f, context.resources.displayMetrics,
        )

    private fun dimenPx(name: String): Int {
        val id = context.resources.getIdentifier(name, "dimen", context.packageName)
        assertNotEquals("R.dimen.$name is not in the merged resource table", 0, id)
        return context.resources.getDimension(id).roundToInt()
    }

    private fun spec(terminal: Boolean = false) =
        (monoWell(context, "$ swarm remote off", terminal).background as SubstrateSurface).spec

    // ---- the surface ------------------------------------------------------

    @Test
    fun `the well is the design's recessed surface`() {
        val claims = listOf(
            Claim("`.sheet2 .cmd` fill", KitOrigin.cssColour(".sheet2 .cmd", "background"), spec().fill),
            Claim("`.sheet2 .cmd` border", KitOrigin.cssColour(".sheet2 .cmd", "border"), spec().stroke),
            Claim(
                "`.sheet2 .cmd` border width",
                KitOrigin.cssFirstPx(".sheet2 .cmd", "border") * context.resources.displayMetrics.density,
                spec().strokeWidthPx.toFloat(),
            ),
            // AUTHORIZED REWRITE, ADR-009 O2. What this claim said before:
            //
            //     Claim(
            //         "`.sheet2 .cmd` radius",
            //         KitOrigin.cssFirstPx(".sheet2 .cmd", "border-radius") *
            //             context.resources.displayMetrics.density,
            //         spec().radiusPx,
            //     ),
            //
            // It cited a CSS rule that writes a LITERAL 9px, and it passed because Substrate set
            // --p-card-r and --p-btn-r to 9px as well: three numbers agreed and nothing could say
            // which one the well was spending. ADR-009 separates the two radii (14 and 10), and
            // the maquette settles it -- `.well { border-radius: var(--p-btn-r) }`, a
            // control-sized recess rather than a slab. So the claim now cites the TOKEN, which
            // is what it should always have cited: a literal in the design source is a number
            // with no origin, and pinning against one is how a coincidence survives review.
            Claim(
                "--p-btn-r radius",
                DesignScale.tokenPx("--p-btn-r") * context.resources.displayMetrics.density,
                spec().radiusPx,
            ),
        )

        assertEquals(mismatches(claims).joinToString("\n"), emptyList<String>(), mismatches(claims))
    }

    @Test
    fun `the well carries no key light`() {
        // `--p-card-fx` belongs to `.prow`, `.sheet2` and `.tcard` and to nothing else. A well is
        // the opposite gesture: `--p-well` is darker than the ground, so a highlight along its top
        // edge would light the one surface in the skin meant to read as cut into the page.
        assertNull(spec().keyLight)
        assertNull(spec().rail)
    }

    // ---- the type, and the promise ----------------------------------------

    @Test
    fun `the well is monospace at the design's size`() {
        val claims = KitOrigin.textClaims(
            view = monoWell(context, "go test ./internal/attach/"),
            selector = ".sheet2 .cmd",
            ink = KitOrigin.token("--p-ink"),
            spScale = spScale,
        )

        assertTrue(
            "the pitch probe cannot answer: ${KitOrigin.typefaceProbeFaults()}",
            KitOrigin.typefaceProbeFaults().isEmpty(),
        )
        assertEquals(mismatches(claims).joinToString("\n"), emptyList<String>(), mismatches(claims))
    }

    /**
     * PB-TOK-3's `terminal_peek.fg`, applied.
     *
     * The expected value is followed out of the TOKEN ORIGIN rather than out of `tokens.json`'s
     * `terminal_peek` block, and the difference is the point: the pin says the foreground IS
     * `--p-hero`, so resolving `--p-hero` and comparing is the join. Reading the pin's own string
     * and comparing it to itself is what "enforced against the JSON" already did for three slices
     * while nothing rendered.
     */
    @Test
    fun `the terminal variant finally paints the phosphor foreground`() {
        assertEquals(
            KitOrigin.token("--p-hero"),
            monoWell(context, "ok  internal/attach  1.24s", terminal = true).currentTextColor,
        )
    }

    @Test
    fun `only the ink changes between the two variants`() {
        // The well fill is `--p-well` in both. Painting the terminal's ground green as well would
        // turn a recessed block into a highlight, which is the obvious over-application of a pin
        // that names a FOREGROUND.
        assertEquals(spec(terminal = false), spec(terminal = true))
        assertNotEquals(
            monoWell(context, "x", terminal = false).currentTextColor,
            monoWell(context, "x", terminal = true).currentTextColor,
        )
    }

    // `white-space: pre` IS NOT ASSERTED, and the omission is recorded rather than papered over.
    // `setHorizontallyScrolling` has no getter -- the flag it sets is private to TextView -- so
    // the only observable form is a measured layout, and the first draft of this file asserted
    // `assertNotNull(view.movementMethod == null)`, which compares a Boolean against null and is
    // true for every possible component. A test that cannot observe the property is worse than
    // its absence: it reads as coverage. What is checkable about the well is asserted above.

    // ---- spacing ----------------------------------------------------------

    @Test
    fun `the well spends the ledger's step on both edges`() {
        val well = monoWell(context, "x")
        val step = dimenPx("swarm_space_10")

        // `.sheet2 .cmd { padding: 10px 11px }`, with 11px absorbed into `space_10` by PB-DS-1's
        // ledger. `android/gate/s23_kit_test.go` is what joins those two facts; this asserts what
        // the resource table resolves the step to, which is the half a source scan cannot see.
        val claims = listOf(
            Claim("padding top", step, well.paddingTop),
            Claim("padding bottom", step, well.paddingBottom),
            Claim("padding start", step, well.paddingStart),
            Claim("padding end", step, well.paddingEnd),
        )
        assertEquals(mismatches(claims).joinToString("\n"), emptyList<String>(), mismatches(claims))
    }

    /**
     * agents-tracker-ksvb.3: the well's height is the GRID's, not this frame's.
     *
     * **A WELL THAT WRAPS ITS CONTENT IS A WELL THAT RESIZES PER FRAME.** The terminal peek prints
     * `Snapshot.Text`, which arrives again every time the agent writes a byte, and the number of
     * lines in it is whatever the daemon rendered this instant -- so the card grew and shrank
     * under the reader while everything below it (the note, `[Take control]`, the lease sentence)
     * moved with it. The machine's grid has a fixed row count and that is the stable number: a
     * floor of `rows` lines makes the well one size for as long as the terminal is one size.
     *
     * A FLOOR AND NOT A HEIGHT, so a snapshot the daemon rendered TALLER than its own grid is
     * still shown whole rather than clipped -- what is refused is shrinking, which is what jumps.
     *
     * ZERO IS "NOBODY SAID", and it must leave the well exactly as it was. The pairing command
     * line is one line of shell that arrives once and never changes; a floor there would be a
     * height nobody asked for under a block that cannot move.
     */
    @Test
    fun `the well takes its floor from the grid rather than from this frame`() {
        assertEquals(
            "the well does not pin a line floor, so its height is whatever this frame happened " +
                "to render and everything under it moves whenever the agent writes",
            24,
            monoWell(context, "$ ls\nfile\n", terminal = true, lines = 24).minLines,
        )
        assertEquals(
            "a well told no line count pinned one anyway, so the pairing command line acquired a " +
                "height nobody asked for",
            0,
            monoWell(context, "$ swarm remote on").minLines,
        )
    }

    // ---- reachability (agents-tracker-ksvb.7) ------------------------------

    /**
     * FAILING-FIRST for agents-tracker-ksvb.7's part (a): `setHorizontallyScrolling(true)` tells
     * the well not to WRAP a long line; it says nothing about where the part past the visible
     * edge goes. With `MATCH_PARENT` width and no scroller, it went nowhere -- silently clipped,
     * not merely unreadable. [TextView.scrolledHorizontally] is what gives the overflow somewhere
     * to be: the well measures its unclipped width inside it, and this asserts both halves of
     * "reachable" -- the scroller is a [HorizontalScrollView], and the well's own measured width
     * is wider than a viewport that could not have shown a 500-character line whole.
     *
     * IT IS `monoWell(...).scrolledHorizontally()` AND NOT `monoWell(...)` ALONE, because
     * `monoWell` itself still has to return a bare, unparented well -- `ApprovalSheetTest` and
     * `PairingStepRowTest` both add its return value directly as another component's own child,
     * and a well arriving pre-parented would make that `addView` throw.
     */
    @Test
    fun `a line longer than the well is reachable through its scroller`() {
        val well = monoWell(context, "$ " + "x".repeat(500))
        val scroller = well.scrolledHorizontally()

        assertTrue(
            "the well's own scroller is not a HorizontalScrollView, so a line wider than the " +
                "viewport is clipped rather than merely off-screen",
            scroller is HorizontalScrollView,
        )

        val viewportWidthPx = 300
        scroller.measure(
            View.MeasureSpec.makeMeasureSpec(viewportWidthPx, View.MeasureSpec.EXACTLY),
            View.MeasureSpec.makeMeasureSpec(0, View.MeasureSpec.UNSPECIFIED),
        )
        scroller.layout(0, 0, scroller.measuredWidth, scroller.measuredHeight)

        assertTrue(
            "the well measured no wider than its viewport, so a 500-character line has nowhere " +
                "left to scroll to",
            well.measuredWidth > viewportWidthPx,
        )
    }

    /** The negative control, through the same comparison the assertions above use. */
    @Test
    fun `the mono well assertions can actually fail`() {
        val well = KitOrigin.cssColour(".sheet2 .cmd", "background")
        val hero = KitOrigin.token("--p-hero")

        assertTrue(
            "a fill one unit from the design's passes the comparison",
            mismatches(listOf(Claim("fill", well, well + 1))).isNotEmpty(),
        )
        assertTrue(
            "the phosphor foreground compares equal to the ordinary ink, so the terminal " +
                "assertion would pass on a peek that never applied the pin",
            hero != KitOrigin.token("--p-ink"),
        )
        assertTrue(
            "a radius one pixel from the design's passes the comparison",
            mismatches(listOf(Claim("radius", 9f, 10f))).isNotEmpty(),
        )
    }
}
