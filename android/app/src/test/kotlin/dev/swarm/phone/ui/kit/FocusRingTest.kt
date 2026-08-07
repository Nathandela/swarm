package dev.swarm.phone.ui.kit

import android.content.Context
import android.graphics.Bitmap
import android.graphics.Canvas
import android.graphics.Rect
import android.util.TypedValue
import android.widget.TextView
import androidx.test.core.app.ApplicationProvider
import dev.swarm.phone.theme.SwarmTheme
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotEquals
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.GraphicsMode

/**
 * FAILING-FIRST (TDD RED, GG-5) for PB-DS-10 over derivation row 23.
 *
 * THE EXPECTED INK IS READ OUT OF THE ROW, not written here. Row 23 is the only place the ring is
 * specified -- the artifact's own `:focus-visible` paints the documentation page's amber, which is
 * not a product token at all -- so a suite that transcribed `--p-ink` would agree with itself
 * forever. §1.1 rejects four alternatives by name, and the negative half below asks that the chosen
 * one is actually distinguishable from the two nearest.
 *
 * THE OTHER HALF IS REACHABILITY, and it is the failure mode that looks like success. A ring is a
 * drawable: it can be correct in every value a test reads and never appear, because nothing in the
 * app can take focus. So the claims here are that the ring draws NOTHING unfocused, something
 * focused, and that a component carrying one can be focused at all.
 */
@RunWith(RobolectricTestRunner::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
class FocusRingTest {

    private val context: Context
        get() = SwarmTheme.applyTo(ApplicationProvider.getApplicationContext())

    private fun dp(value: Float): Float = TypedValue.applyDimension(
        TypedValue.COMPLEX_UNIT_DIP, value, context.resources.displayMetrics,
    )

    private fun dimen(name: String): Float {
        val id = context.resources.getIdentifier(name, "dimen", context.packageName)
        assertNotEquals("R.dimen.$name is not in the merged resource table", 0, id)
        return context.resources.getDimension(id)
    }

    /** Row 23's leading cell. */
    private val ROW_23 = "Focus ring"

    private fun ring(componentRadiusPx: Float = 0f) = focusRing(context, componentRadiusPx)

    /** Row 23's surface cell: `--p-ink`, and §1.1 spends a page rejecting the alternatives. */
    @Test
    fun `the ring is the ink row 23 states`() {
        assertEquals(
            KitOrigin.token(KitOrigin.cellToken(ROW_23, "Surface")),
            ring().spec.ink,
        )
    }

    /**
     * Row 23's border and spacing cells: "2 dp stroke", "offset `space_2`".
     *
     * THE STROKE IS THE KIT'S CONSTANT, held to the row by `s23_kit_test.go`; the offset is the
     * spacing step the row NAMES, so it is resolved off the merged resource table rather than
     * converted from a number.
     */
    @Test
    fun `the ring is 2 dp of stroke, space_2 clear of what it surrounds`() {
        val spec = ring().spec
        val claims = listOf(
            Claim("row 23 stroke", dp(KitMetrics.FOCUS_RING_DP), spec.strokePx),
            Claim("row 23 offset", dimen("swarm_space_2"), spec.offsetPx),
            Claim("the room the ring needs", spec.offsetPx + spec.strokePx, spec.roomPx),
        )

        assertEquals(mismatches(claims).joinToString("\n"), emptyList<String>(), mismatches(claims))
    }

    /**
     * Row 23's radius cell: "focused component's radius + 2".
     *
     * THE `+ 2` IS THE OFFSET AND NOT A SECOND NUMBER, which §1.1 states as a reason rather than a
     * value: the ring stays CONCENTRIC, and a ring at distance d outside a rounded rectangle of
     * radius r is concentric exactly when its radius is r + d. So the claim is asserted over
     * several radii rather than at one, because a ring that added a constant would pass at the one
     * radius where the constant and the offset agreed.
     */
    @Test
    fun `the ring stays concentric with whatever it surrounds`() {
        val offset = dimen("swarm_space_2")
        val claims = listOf(0f, dp(8f), dp(9f), dp(14f)).map { radius ->
            Claim("ring radius around a $radius px corner", radius + offset, ring(radius).spec.radiusPx)
        }

        assertEquals(mismatches(claims).joinToString("\n"), emptyList<String>(), mismatches(claims))
    }

    /**
     * `:focus-visible` AND NOT `:focus`, expressed as the two things that make it true.
     *
     * The ring draws nothing until the view's own drawable state says focused, and the component
     * that carries one does NOT ask for focus in touch mode -- so a tap leaves no ring behind and a
     * keyboard, D-pad or switch-access traversal finds one.
     */
    @Test
    fun `the ring appears with focus and a tap does not summon it`() {
        val view = TextView(context)
        Kit.focusable(view, componentRadiusPx = 0f)
        val drawn = view.foreground as FocusRingDrawable

        assertFalse("an unfocused control is wearing its focus ring", drawn.focused)
        assertTrue(
            "a component given a focus ring cannot take focus, so the ring is paint nothing " +
                "reaches -- which is indistinguishable from not having implemented it",
            view.isFocusable,
        )
        assertFalse(
            "the control asks for focus in TOUCH mode, so every tap would leave a champagne ring " +
                "behind it. Row 23 cites `:focus-visible`, which is the pseudo-class that does not.",
            view.isFocusableInTouchMode,
        )

        view.isFocusableInTouchMode = true
        assertTrue("the control refused focus outright", view.requestFocus())
        assertTrue("the ring did not follow the view into the focused state", drawn.focused)
    }

    /**
     * The ring is drawn INSIDE its bounds, which is what makes the stated width the drawn width.
     *
     * `Canvas.drawRoundRect` centres a stroke on the path it is given, so a ring laid on the
     * drawable's own bounds loses half of itself to whatever clips the view -- 2 dp in every value
     * a test could read and 1 dp on screen. Asserted by painting it and asking where the ink is.
     */
    @Test
    fun `the stroke is drawn inside the bounds and not astride them`() {
        val size = dp(48f).toInt()
        val drawn = ring(dp(9f))
        drawn.bounds = Rect(0, 0, size, size)
        // The drawable only paints when focused, so the unfocused pass is also the control for
        // this one: identical bounds, no ink.
        assertTrue(
            "the ring painted while unfocused, so `:focus-visible` is decoration",
            inkAtTopEdge(drawn) == 0,
        )
        drawn.state = intArrayOf(android.R.attr.state_focused)
        assertTrue(
            "the focused ring painted nothing at all inside its own bounds",
            inkAtTopEdge(drawn) > 0,
        )
    }

    /** How many of the first two rows of pixels down the middle of the ring's box carry ink. */
    private fun inkAtTopEdge(drawn: FocusRingDrawable): Int {
        val bitmap = Bitmap.createBitmap(
            drawn.bounds.width(), drawn.bounds.height(), Bitmap.Config.ARGB_8888,
        )
        drawn.draw(Canvas(bitmap))
        val middle = bitmap.width / 2
        return (0 until dp(KitMetrics.FOCUS_RING_DP).toInt().coerceAtLeast(1))
            .count { y -> bitmap.getPixel(middle, y) != 0 }
    }

    /** The negative control PB-DS-10 requires, through the same comparison the claims above use. */
    @Test
    fun `the focus ring assertions can actually fail`() {
        val ink = KitOrigin.token(KitOrigin.cellToken(ROW_23, "Surface"))
        val offset = dimen("swarm_space_2")

        assertTrue(
            "an ink one unit from the row's passes the comparison",
            mismatches(listOf(Claim("ink", ink, ink + 1))).isNotEmpty(),
        )
        assertTrue(
            "a radius one pixel from the concentric rule passes the comparison",
            mismatches(listOf(Claim("radius", dp(9f) + offset, dp(9f) + offset + 1f))).isNotEmpty(),
        )
        // WHAT THIS SUITE CANNOT DISTINGUISH, SAID RATHER THAN IMPLIED. At density 1.0 -- which is
        // what these tests run at -- `space_2` resolves to 2 px, so a ring that added a hard-coded
        // 2 and one that spent the step are the same number here and no assertion could separate
        // them. What separates them is structural and mechanical instead: `swarm_space_2` is a
        // resource spend, and TestPBDS6_NoRawDimensionIsTypedInTheKit refuses a bare 2 typed at a
        // dp call site in this package at all. This line fails if that stops being true, which is
        // the condition under which the claim above would start meaning less than it says.
        assertEquals(
            "`space_2` no longer resolves to the same number as row 23's stroke, so the concentric " +
                "claim can now be asserted directly and this note is out of date",
            dp(KitMetrics.FOCUS_RING_DP),
            offset,
        )
        // §1.1's rejections, as distinctions. If the chosen ink compared equal to any of the four
        // the amended §1.1 argues against, the claim above would accept a ring that reads as a
        // hairline or a ring that means state.
        //
        // THE TWO REJECTIONS THIS REPLACES WERE SUBSTRATE'S, AND ADR-009 D3 INVERTS BOTH:
        //
        //     assertNotEquals("`--p-ink` and `--p-hero` resolve to the same colour, so a ring
        //         meaning SELECTED would satisfy row 23", KitOrigin.token("--p-hero"), ink)
        //     assertNotEquals("`--p-ink` and `--p-ink2` resolve to the same colour, so a ring
        //         that reads as a border would satisfy row 23", KitOrigin.token("--p-ink2"), ink)
        //
        // The first is not weakened, it is REVERSED: hero was rejected because it meant selected,
        // and in Obsidian the accent means "you" -- focus is one of the five things it says. The
        // second survives verbatim in substance; it is restated against the chosen ink rather than
        // against the linen one, which is the same distinction pointed at the decision that holds.
        //
        // `--p-att` IS ABSENT FROM THIS LIST ON PURPOSE. It value-aliases `--p-hero` (ADR-009 D6),
        // so asserting them distinct would assert against the decision. What keeps the alias
        // breakable is that each keeps its own row and its own resource, which the token gates
        // fence; there is nothing for this suite to add.
        listOf("--p-ink", "--p-ink2", "--p-work", "--p-ok", "--p-err").forEach { rejected ->
            assertNotEquals(
                "row 23's ink and `$rejected` resolve to the same colour, so a ring that reads " +
                    "as a hairline -- or one that means a status -- would satisfy row 23",
                KitOrigin.token(rejected),
                ink,
            )
        }
    }
}
