package dev.swarm.phone.ui.kit

import android.content.Context
import android.text.Spanned
import android.text.style.ForegroundColorSpan
import android.text.style.TextAppearanceSpan
import android.util.TypedValue
import android.view.Gravity
import android.view.View
import android.widget.FrameLayout
import android.widget.TextView
import androidx.test.core.app.ApplicationProvider
import dev.swarm.phone.theme.SwarmTheme
import dev.swarm.phone.theme.TypeScale
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
 * FAILING-FIRST (TDD RED, GG-5) for PB-DS-10 over derivation row 1 -- the toast.
 *
 * THE COMPONENT THE APP HAS NEVER HAD. There is no toast, snackbar or banner anywhere in this
 * codebase, so every answer to a press lands in one of three persistent text lines -- and the one
 * on the inbox tab is cleared to `""` before a command is dispatched, which is also what a success
 * leaves behind. Row 1 is the whole specification: Substrate's shared block declares no `.toast`
 * rule at all, `.toast` is the retired mock's class, and §2 replaces its translucency, its 0.5 px
 * border and its radius 12 outright.
 *
 * **THE SURFACE IS `--p-elev` AND THE PLAUSIBLE WRONG ANSWER IS `--p-card`.** Every other opaque
 * block in this kit is a card, and a toast built by copying `cardSurface` would be five units too
 * dark against the screen it floats over -- which is the one thing an elevated surface is for in a
 * skin that bans drop shadows. Row 1 says `--p-elev` and §2 says why it is opaque.
 *
 * **THE ANNOUNCEMENT IS NOT THE VISIBILITY.** §8.7 is explicit: the toast's 3200 ms visual
 * lifetime is shorter than a TalkBack reading of its longest copy, so the announcement must be a
 * live region with a lifetime of its own rather than a side effect of the view being on screen.
 * The half of that this suite can hold is that the view IS a live region; `ToastHostTest` holds
 * the other half -- that expiry hides the view and destroys nothing it was announced from.
 */
@RunWith(RobolectricTestRunner::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
class ToastTest {

    private val context: Context
        get() = SwarmTheme.applyTo(ApplicationProvider.getApplicationContext())

    private val spScale: Float
        get() = TypedValue.applyDimension(
            TypedValue.COMPLEX_UNIT_SP, 1f, context.resources.displayMetrics,
        )

    private val density: Float get() = context.resources.displayMetrics.density

    private fun dimen(name: String): Float {
        val id = context.resources.getIdentifier(name, "dimen", context.packageName)
        assertNotEquals("R.dimen.$name is not in the merged resource table", 0, id)
        return context.resources.getDimension(id)
    }

    private fun dimenPx(name: String): Int = dimen(name).roundToInt()

    /** The mock's own longest toast, which is the copy §8.7 measures its announcement against. */
    private val message = "Controller lease taken"

    private val suffix = "generation 7 to 8"

    private fun subject(suffix: CharSequence? = null): TextView = toast(context, message, suffix)

    private fun spanned(view: TextView): Spanned? = view.text as? Spanned

    private inline fun <reified T> spansOf(view: TextView): List<T> {
        val text = spanned(view) ?: return emptyList()
        return text.getSpans(0, text.length, T::class.java).toList()
    }

    // ---- the surface ----------------------------------------------------------

    /**
     * Row 1's first four cells: `--p-elev` opaque, a 1 dp `--p-hair` border, `--p-card-r`, and the
     * `--p-card-fx` key light "as on every card".
     */
    @Test
    fun `the toast is the elevated surface with the card's hairline, radius and key light`() {
        val surface = subject().background as? SubstrateSurface
        assertTrue("the toast's background is not a kit surface", surface != null)

        val claims = listOf(
            Claim("row 1 fill", KitOrigin.token("--p-elev"), surface!!.spec.fill),
            Claim("row 1 border", KitOrigin.token("--p-hair"), surface.spec.stroke),
            // `.prow { border: 1px solid var(--p-hair) }` -- the hairline every surface in this
            // kit spends, read out of the design rather than off the resource it becomes.
            Claim(
                "row 1 border width",
                (KitOrigin.cssFirstPx(".prow", "border") * density).roundToInt(),
                surface.spec.strokeWidthPx,
            ),
            Claim("row 1 radius", dimen("swarm_radius_card"), surface.spec.radiusPx),
            Claim("row 1 key light", KitOrigin.rgbaToken("--p-card-fx"), surface.spec.keyLight),
            Claim("row 1 has no rail", null, surface.spec.rail),
        )
        assertEquals(mismatches(claims).joinToString("\n"), emptyList<String>(), mismatches(claims))
        assertNotEquals(
            "the toast is painted `--p-card`, which is the fill every other opaque block in this " +
                "kit takes. Row 1 says `--p-elev`: a toast floats OVER the screen, and in a skin " +
                "with no drop shadows one ladder step lighter is the whole of what that means",
            KitOrigin.token("--p-card"),
            surface.spec.fill,
        )
    }

    @Test
    fun `the toast spends row 1's two padding steps`() {
        val view = subject()
        val claims = listOf(
            Claim("row 1 padding top", dimenPx("swarm_space_10"), view.paddingTop),
            Claim("row 1 padding bottom", dimenPx("swarm_space_10"), view.paddingBottom),
            Claim("row 1 padding start", dimenPx("swarm_space_16"), view.paddingStart),
            Claim("row 1 padding end", dimenPx("swarm_space_16"), view.paddingEnd),
        )
        assertEquals(mismatches(claims).joinToString("\n"), emptyList<String>(), mismatches(claims))
    }

    /**
     * Row 1's placement cell: `bottom toast_bottom = tabbar_height + space_18 = 92; centred`.
     *
     * IT IS THE COMPONENT'S AND NOT THE HOST'S, because the row states it as part of the toast --
     * and because the alternative is a screen choosing where a floating component sits, which is
     * the decision `android/gate/s24_screens_test.go` fences a screen out of making.
     */
    @Test
    fun `the toast sits toast_bottom above the bottom edge, centred`() {
        val params = subject().layoutParams as FrameLayout.LayoutParams
        val claims = listOf(
            Claim("row 1 bottom", dimenPx("swarm_toast_bottom"), params.bottomMargin),
            Claim(
                "row 1 gravity",
                Gravity.BOTTOM or Gravity.CENTER_HORIZONTAL,
                params.gravity,
            ),
        )
        assertEquals(mismatches(claims).joinToString("\n"), emptyList<String>(), mismatches(claims))
    }

    // ---- the two type roles ---------------------------------------------------

    /** Row 1's body cell: `Body.Message` / `--p-ink`. */
    @Test
    fun `the body is row 1's role in row 1's ink`() {
        val claims = KitOrigin.textClaims(
            // `.m2` is what type.xml records as Body.Message's origin.
            view = subject(),
            selector = ".m2",
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
     * Row 1's second type cell: `Mono.CodeSmall` / `--p-ink2`, over the suffix and nothing else.
     *
     * IT IS A SPAN AND NOT A SECOND VIEW, which is what the mock's own template says it is --
     * `msg + " " + <span class="m">mono</span>` -- and what keeps the two from being laid out as a
     * label and a value when the message wraps.
     */
    @Test
    fun `the mono suffix is row 1's role in row 1's ink, over its own words`() {
        val view = subject(suffix)
        val text = requireNotNull(spanned(view)) {
            "the toast holds a plain String, so the suffix was never styled and every claim " +
                "below would be about nothing"
        }
        val spec = TypeScale.designSpec(".tcard .b")
        val appearance = spansOf<TextAppearanceSpan>(view).single()
        val ink = spansOf<ForegroundColorSpan>(view).single()
        val start = text.toString().indexOf(suffix)

        val claims = listOf(
            Claim("suffix appearance start", start, text.getSpanStart(appearance)),
            Claim("suffix appearance end", start + suffix.length, text.getSpanEnd(appearance)),
            Claim("suffix ink start", start, text.getSpanStart(ink)),
            Claim("suffix ink end", start + suffix.length, text.getSpanEnd(ink)),
            Claim(
                "`.tcard .b` size",
                KitOrigin.quantisedTextSize(spec.sizePx * spScale).roundToInt(),
                appearance.textSize,
            ),
            // ADR-009 D7: pitch, not a family string -- a bundled family reaches the span as a
            // resolved Typeface and leaves getFamily() null.
            Claim("`.tcard .b` family", spec.isMono, KitOrigin.isFixedPitch(appearance)),
            Claim("row 1 suffix ink", KitOrigin.token("--p-ink2"), ink.foregroundColor),
        )
        assertEquals(mismatches(claims).joinToString("\n"), emptyList<String>(), mismatches(claims))
        assertTrue(
            "the toast reads `${text}`, which does not carry the message the caller passed",
            text.toString().startsWith(message),
        )
    }

    /**
     * A toast with no suffix carries no span, which is the state most of them are in: the mock
     * passes a mono fragment to three of its seven toasts and nothing to the other four.
     */
    @Test
    fun `a toast with no suffix is one unmarked sentence`() {
        val view = subject()
        assertEquals(message, view.text.toString())
        assertTrue(
            "the toast applied a text appearance span over a suffix nobody passed",
            spansOf<TextAppearanceSpan>(view).isEmpty(),
        )
        assertTrue(
            "the toast applied an ink span over a suffix nobody passed",
            spansOf<ForegroundColorSpan>(view).isEmpty(),
        )
    }

    // ---- what a screen reader gets --------------------------------------------

    /**
     * §8.7: the toast is announced through a LIVE REGION.
     *
     * The plausible wrong answer is a content description, which is what every other announced
     * component in this kit carries -- and it is wrong here for a reason particular to this one: a
     * description is read when the view is FOCUSED, and nothing ever focuses a toast. A live
     * region is read when its content changes, which is the only moment a toast has.
     */
    @Test
    fun `the toast is an accessibility live region`() {
        val view = subject()
        assertEquals(
            "the toast is not a live region, so a screen reader announces it only if something " +
                "moves focus onto it -- and nothing ever does",
            View.ACCESSIBILITY_LIVE_REGION_POLITE,
            view.accessibilityLiveRegion,
        )
        assertNull(
            "the toast carries a content description, which a screen reader reads INSTEAD of the " +
                "text on it. The words are the message; there is nothing else to say about them",
            view.contentDescription,
        )
    }

    /**
     * PB-DS-10's control, fed to the SAME function every assertion above calls.
     */
    @Test
    fun `the comparison fails when a value diverges`() {
        val view = subject()
        assertTrue(
            "a perturbed fill produced no mismatch, so the surface claims above would hold " +
                "against a toast painted in any colour at all",
            mismatches(
                listOf(
                    Claim(
                        "row 1 fill",
                        KitOrigin.token("--p-elev") + 1,
                        (view.background as SubstrateSurface).spec.fill,
                    ),
                ),
            ).isNotEmpty(),
        )
        assertTrue(
            "a perturbed padding produced no mismatch, so the spacing claims are about nothing",
            mismatches(listOf(Claim("row 1 padding top", view.paddingTop + 1, view.paddingTop)))
                .isNotEmpty(),
        )
        assertTrue(
            "the comparison reports a mismatch for a value that agrees, so it fails on everything",
            mismatches(listOf(Claim("row 1 padding top", view.paddingTop, view.paddingTop)))
                .isEmpty(),
        )
    }
}
