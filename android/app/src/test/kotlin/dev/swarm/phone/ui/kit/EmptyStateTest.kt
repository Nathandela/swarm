package dev.swarm.phone.ui.kit

import android.content.Context
import android.util.TypedValue
import android.view.Gravity
import androidx.test.core.app.ApplicationProvider
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
 * FAILING-FIRST (TDD RED, GG-5) for PB-DS-10 over the one component S24 adds to the kit.
 *
 * WHY THE KIT GREW A THIRTEENTH FILE, since it was declared done. PB-DS-9 requires an empty
 * section to render "as a section with its empty copy" -- the model's own comment explains that
 * dropping it is the obvious implementation and is wrong for a triage surface -- and the block
 * that copy goes in is derivation table row 8. The kit shipped twelve components and not that one,
 * so the triage inbox could render four headings and nothing under the empty three. Building it in
 * the screen package was the alternative and it is the worse one: a visual factory outside
 * `ui/kit` contradicts PB-DS-6 in the same breath as claiming it, and S24's own fence would have
 * had to allowlist the file its author wrote.
 *
 * EVERY EXPECTED VALUE COMES FROM THE DESIGN, not from the component. Row 8 states the type, the
 * ink and the spacing; [KitOrigin] resolves the token origin and the type scale. The one thing it
 * cannot resolve is the row itself -- `.empty` is NOT in the Substrate artifact, which is why row
 * 8 exists at all -- so the padding is read out of `docs/design/substrate-components.md` by
 * `android/gate/s24_screens_test.go`, on the same principle and in the lane that can read files.
 *
 * ROW 8 SPECIFIES A COMPACT VARIANT AND THIS COMPONENT DOES NOT SHIP ONE. `space_24` all round is
 * for a block inside a card; the triage inbox has no caller for it, and a parameter with no call
 * site is a second spelling of the same component that the first screen to need it would find
 * already wrong. Recorded here rather than left to look like an oversight.
 */
@RunWith(RobolectricTestRunner::class)
// NATIVE, for the same reason InboxChromeTest gives: LEGACY graphics stubs the text stack and
// returns one pixel per character, which makes every font measure fixed-pitch and would certify
// the opposite of the truth about this component's family.
@GraphicsMode(GraphicsMode.Mode.NATIVE)
class EmptyStateTest {

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

    /** Row 8: `Body.Message` / `--p-ink2`, centred. */
    @Test
    fun `the empty state is the design's body copy in the secondary ink`() {
        val block = emptyState(context, "Nothing is waiting on you.")

        val claims = KitOrigin.textClaims(
            view = block,
            // `.m2` is the rule type.xml records as Body.Message's origin, so the size, the
            // tracking and the family are followed out of the artifact rather than out of the
            // style this component asks for.
            selector = ".m2",
            ink = KitOrigin.token("--p-ink2"),
            spScale = spScale,
        )

        assertTrue("the pitch probe cannot answer: ${KitOrigin.typefaceProbeFaults()}",
            KitOrigin.typefaceProbeFaults().isEmpty())
        assertEquals(mismatches(claims).joinToString("\n"), emptyList<String>(), mismatches(claims))
    }

    /**
     * Row 8's spacing: `padding 48 (2 x space_24) vertical, space_24 horizontal`.
     *
     * THE 48 IS SPENT AS `space_24 + space_24` AND NOT AS `2 * space_24`, and the difference is a
     * literal. The kit's literal-accounting fence requires every bare number in the package to
     * have a design origin or an entry on a nine-line exemption table; adding a `2` there would
     * have meant either a tenth exemption or borrowing the one written for the status dot's two
     * halo sides, which is a different number that happens to be spelled the same. A sum of two
     * steps is the design's own sentence with nothing added to it.
     */
    @Test
    fun `the empty state spends the scale steps row 8 states`() {
        val block = emptyState(context, "Nothing is running.")
        val step = dimenPx("swarm_space_24")

        val claims = listOf(
            Claim("row 8 padding-x (start)", step, block.paddingStart),
            Claim("row 8 padding-x (end)", step, block.paddingEnd),
            Claim("row 8 padding-y (top)", step + step, block.paddingTop),
            Claim("row 8 padding-y (bottom)", step + step, block.paddingBottom),
        )

        assertEquals(mismatches(claims).joinToString("\n"), emptyList<String>(), mismatches(claims))
    }

    /** Row 8: "none (the ground shows)" -- no surface, no border, no radius. */
    @Test
    fun `the empty state paints no surface at all`() {
        val block = emptyState(context, "Nothing has finished here yet.")

        assertNull(
            "the empty state was given a background. Row 8's surface column is \"none (the " +
                "ground shows)\" and its border and radius columns are \"none\": a block that " +
                "acquired a card would be a card with a sentence in it, which is the one thing " +
                "an empty state must not look like",
            block.background,
        )
    }

    @Test
    fun `the empty state is centred`() {
        val block = emptyState(context, "Nothing is waiting to be reviewed.")

        // Row 8's type column ends "centred". Left-aligned it reads as a row that failed to load
        // rather than as a statement about the section.
        assertEquals(
            Gravity.CENTER,
            block.gravity and (Gravity.HORIZONTAL_GRAVITY_MASK or Gravity.VERTICAL_GRAVITY_MASK),
        )
    }

    @Test
    fun `the empty state says what it was given and nothing else`() {
        // The copy is the SCREEN's (PB-DS-9). This component decides what it looks like; a default
        // string here would be a component with an opinion about which section it is in.
        assertEquals("Nothing is running.", emptyState(context, "Nothing is running.").text.toString())
    }

    /**
     * The negative control PB-DS-10 requires, through the SAME comparison the assertions above
     * use: one unit of colour, one pixel of padding, one step of type.
     *
     * A control that rebuilt the comparison inline would prove the copy works. Every claim above
     * goes through [mismatches], and so does every perturbation here.
     */
    @Test
    fun `the empty state assertions can actually fail`() {
        val ink = KitOrigin.token("--p-ink2")
        val step = dimenPx("swarm_space_24")

        assertTrue(
            "an ink one unit from the origin's passes the comparison",
            mismatches(listOf(Claim("ink", ink, ink + 1))).isNotEmpty(),
        )
        assertTrue(
            "a padding one pixel from the row's passes the comparison",
            mismatches(listOf(Claim("padding-y", step + step, step + step + 1))).isNotEmpty(),
        )
        assertTrue(
            "spending ONE space_24 vertically where row 8 states two passes the comparison, " +
                "which is the mistake a reader who skimmed the row would make",
            mismatches(listOf(Claim("padding-y", step + step, step))).isNotEmpty(),
        )
        assertTrue(
            "a proportional face passes the monospace claim",
            mismatches(listOf(Claim("is monospace", false, true))).isNotEmpty(),
        )
    }
}
