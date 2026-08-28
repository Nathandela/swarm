package dev.swarm.phone.ui.kit

import android.content.Context
import android.util.TypedValue
import android.widget.TextView
import androidx.test.core.app.ApplicationProvider
import dev.swarm.phone.R
import dev.swarm.phone.theme.SwarmTheme
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotEquals
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.GraphicsMode
import kotlin.math.roundToInt

/**
 * FAILING-FIRST (TDD RED, GG-5) for phone refit W8.1 -- a style's leading reaches the view
 * (docs/specifications/phone-refit-playbook.md section 8b, bead agents-tracker-a84l).
 *
 * **THE DEFECT.** `android:lineHeight` is a `TextView` attribute and not a `TextAppearance` one,
 * so `setTextAppearance(style)` on the kit's framework `TextView` applies the size, the weight,
 * the family and the tracking a style states and drops its leading. The five leadings `type.xml`
 * declares never rendered: the W4 evidence measured a styled notice at the same 16 px as a bare
 * view. `DesignScaleResolutionTest` reads the XML text, which is why the suite stayed green over
 * a leading nothing drew.
 *
 * **WHAT IS ASSERTED.** [Kit.appearance] is the kit's one way of putting a style on a view, and a
 * view styled through it reports the leading its style declares: `lineHeight` equal to the stated
 * sp at the display's scaled density, rounded the way `getDimensionPixelSize` rounds. The table is
 * the five styles by name with the sp `type.xml` states for each, transcribed rather than read
 * back through the resource table -- a test that asked the style what it declares and checked the
 * view against the answer would pass a style whose leading was edited to anything. THE CONTROL IS
 * THE PATH THE HELPER REPLACES: the same style through bare `setTextAppearance` does not report
 * that leading, so this is an assertion the kit as it stood fails.
 *
 * **THE OTHER HALF OF THE CONTRACT** is that a style stating no leading leaves the platform's line
 * height alone: the helper is a no-op across the rest of the ladder, which is what keeps every
 * golden and every density claim where it was.
 */
@RunWith(RobolectricTestRunner::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
class LeadingTest {

    private val context: Context
        get() = SwarmTheme.applyTo(ApplicationProvider.getApplicationContext())

    /** The px a stated sp resolves to on this display, rounded as `getDimensionPixelSize` rounds. */
    private fun spPx(sp: Float): Int =
        TypedValue.applyDimension(TypedValue.COMPLEX_UNIT_SP, sp, context.resources.displayMetrics)
            .roundToInt()

    private fun styled(style: Int): TextView = Kit.textView(context).also { Kit.appearance(it, style) }

    /** The platform path: what every kit site did before the helper. */
    private fun bare(style: Int): TextView = Kit.textView(context).apply { setTextAppearance(style) }

    @Test
    fun `a styled view reports the leading its style declares`() {
        // type.xml's five `android:lineHeight` items: the style, and the sp it states.
        val leadings = listOf(
            Triple("Body.Message", R.style.TextAppearance_Swarm_Body_Message, 20.3f),
            Triple("Body.Secondary", R.style.TextAppearance_Swarm_Body_Secondary, 19.6f),
            Triple("Mono.Code", R.style.TextAppearance_Swarm_Mono_Code, 18.75f),
            Triple("Mono.CodeSmall", R.style.TextAppearance_Swarm_Mono_CodeSmall, 19.375f),
            Triple("Mono.Fine", R.style.TextAppearance_Swarm_Mono_Fine, 17.6f),
        )
        val claims = leadings.map { (name, style, sp) ->
            Claim("$name leading (${sp}sp)", spPx(sp), styled(style).lineHeight)
        }
        assertEquals(mismatches(claims).joinToString("\n"), emptyList<String>(), mismatches(claims))
        // The control: the platform path drops the leading on every one of the five, so the
        // claims above are ones the kit as it stood fails rather than ones any TextView passes.
        val dropped = leadings.filter { (_, style, sp) -> bare(style).lineHeight == spPx(sp) }
        assertTrue(
            "bare setTextAppearance already reports the declared leading for " +
                "${dropped.map { it.first }}, so the claims above cannot tell the helper from " +
                "the platform path it replaces",
            dropped.isEmpty(),
        )
    }

    @Test
    fun `a style without a leading leaves the platform line height`() {
        val style = R.style.TextAppearance_Swarm_Title_Row
        // The control's premise, read off the resource table: Title.Row states no leading.
        val attrs = context.obtainStyledAttributes(style, intArrayOf(android.R.attr.lineHeight))
        try {
            assertFalse(
                "Title.Row declares an android:lineHeight, so this test's control style carries " +
                    "a leading and the claims below assert nothing about a style without one",
                attrs.hasValue(0),
            )
        } finally {
            attrs.recycle()
        }
        val platform = bare(style)
        val view = styled(style)
        val claims = listOf(
            Claim("line height", platform.lineHeight, view.lineHeight),
            Claim("line spacing extra", 0f, view.lineSpacingExtra),
            Claim("line spacing multiplier", 1f, view.lineSpacingMultiplier),
            Claim("text size (the appearance itself was applied)", platform.textSize, view.textSize),
        )
        assertEquals(mismatches(claims).joinToString("\n"), emptyList<String>(), mismatches(claims))
        assertNotEquals(
            "Title.Row renders at the platform default size, so the text-size claim above " +
                "cannot tell an applied appearance from a dropped one",
            Kit.textView(context).textSize,
            view.textSize,
        )
    }
}
