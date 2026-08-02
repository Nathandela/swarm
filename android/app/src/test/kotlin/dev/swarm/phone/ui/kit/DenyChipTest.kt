package dev.swarm.phone.ui.kit

import android.content.Context
import android.util.TypedValue
import android.view.ViewGroup
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
 * FAILING-FIRST (TDD RED, GG-5) for PB-DS-10 over the Revoke control in derivation row 13.
 *
 * WHAT IS BEING ASSERTED IS A PAIRING. Row 13 specifies this control as "the `.a2-no` treatment at
 * chip metrics", and both halves are drawn by Substrate for OTHER components -- so every expected
 * value below is read from the rule that declares it: the fill and the ink from `.a2-no`, the
 * radius, the padding and the type from `.chip`. Nothing here is read from `ctaButton` or from
 * `filterChip`, which is what makes this a check on the pairing rather than on a copy of one.
 *
 * THE FILL IS THE ASSERTION WITH A PLAUSIBLE WRONG ANSWER. `--p-err` straight is a saturated red
 * button; row 13 asks for 13% of it. The two differ by one word in a call and by everything on
 * screen, so the negative half below is what holds it.
 */
@RunWith(RobolectricTestRunner::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
class DenyChipTest {

    private val context: Context
        get() = SwarmTheme.applyTo(ApplicationProvider.getApplicationContext())

    private val spScale: Float
        get() = TypedValue.applyDimension(
            TypedValue.COMPLEX_UNIT_SP, 1f, context.resources.displayMetrics,
        )

    private fun dimen(name: String): Float {
        val id = context.resources.getIdentifier(name, "dimen", context.packageName)
        assertNotEquals("R.dimen.$name is not in the merged resource table", 0, id)
        return context.resources.getDimension(id)
    }

    private fun dimenPx(name: String): Int = dimen(name).roundToInt()

    /** The design's px is Android dp at 1:1 -- the artifact is a 386x812 frame at device scale. */
    private fun dp(value: Float): Float = TypedValue.applyDimension(
        TypedValue.COMPLEX_UNIT_DIP, value, context.resources.displayMetrics,
    )

    /** A phone's content width, so a MATCH_PARENT component measures against something real. */
    private val PARENT_WIDTH_DP = 360f

    private fun chip(description: CharSequence? = null) =
        denyChip(context, label = "Revoke", description = description)

    /**
     * Row 13's revoke cell ends "48 dp target", and unlike row 9 it states no smaller visual: the
     * chip's own box is the target.
     *
     * IT IS MEASURED AND NOT READ OFF A MINIMUM, because this chip hugs its content -- a WRAP
     * control is exactly the shape where a minimum can be set and then lost to a parent's spec, and
     * the destructive action in a device row is the last control in the app that should be hard to
     * hit.
     */
    @Test
    fun `the revoke chip clears PB-DS-12's floor in both directions`() {
        val faults = touchTargetFaults(
            chip(),
            dp(KitMetrics.MIN_TARGET_DP).roundToInt(),
            dp(PARENT_WIDTH_DP).roundToInt(),
        )

        assertEquals(faults.joinToString("\n"), emptyList<String>(), faults)
    }

    // ---- the treatment, from `.a2-no` ----------------------------------------

    @Test
    fun `the chip is the deny fill and the deny ink, at the chip's radius`() {
        val subject = chip()
        val surface = subject.background as? SubstrateSurface
        assertTrue("the chip's background is not a kit surface", surface != null)

        val claims = listOf(
            Claim(
                "`.a2-no` fill",
                KitOrigin.overTransparent(
                    KitOrigin.token("--p-err"),
                    KitOrigin.cssPercent(".a2-no", "background"),
                ),
                surface!!.spec.fill,
            ),
            Claim("`.chip` radius", dimen("swarm_radius_chip"), surface.spec.radiusPx),
            // `.a2-no { border: none }` -- the tint IS the affordance, and a border would be the
            // outline-destructive idiom §2 drops.
            Claim("`.a2-no` border width", 0, surface.spec.strokeWidthPx),
            // No key light: the design gives `--p-card-fx` to `.prow`, `.sheet2` and `.tcard` and
            // to nothing else. A chip with one would be a card at chip size.
            Claim("`.chip` key light", null, surface.spec.keyLight),
        )
        assertEquals(mismatches(claims).joinToString("\n"), emptyList<String>(), mismatches(claims))

        assertNotEquals(
            "the chip is painted `--p-err` straight rather than the 13% tint row 13 states. A " +
                "saturated red control beside a device name reads as an alarm rather than as an " +
                "action, and it is the one thing on this screen that destroys something",
            KitOrigin.token("--p-err"),
            surface.spec.fill,
        )
    }

    // ---- the metrics, from `.chip` --------------------------------------------

    @Test
    fun `the chip spends the scope chip's padding and label style`() {
        val subject = chip()
        val claims = listOf(
            Claim("`.chip` padding-y (top)", dimenPx("swarm_space_8"), subject.paddingTop),
            Claim("`.chip` padding-y (bottom)", dimenPx("swarm_space_8"), subject.paddingBottom),
            Claim("`.chip` padding-x (start)", dimenPx("swarm_space_10"), subject.paddingStart),
            Claim("`.chip` padding-x (end)", dimenPx("swarm_space_10"), subject.paddingEnd),
        ) + KitOrigin.textClaims(subject, ".chip", KitOrigin.token("--p-err"), spScale)

        assertTrue(
            "the pitch probe cannot answer: ${KitOrigin.typefaceProbeFaults()}",
            KitOrigin.typefaceProbeFaults().isEmpty(),
        )
        assertEquals(mismatches(claims).joinToString("\n"), emptyList<String>(), mismatches(claims))
    }

    /**
     * It hugs its own width, which is the difference between this and `ctaButton` that shows up in
     * a layout rather than in a colour.
     *
     * A MATCH_PARENT control at the trailing edge of a row whose text column is weighted is
     * measured against the whole row, and LinearLayout then has nothing left for the text: the
     * device name would vanish and the row would render as one wide button.
     */
    @Test
    fun `the chip hugs its own width so the row beside it keeps its space`() {
        assertEquals(
            ViewGroup.LayoutParams.WRAP_CONTENT,
            chip().layoutParams.width,
        )
    }

    // ---- what a screen reader hears --------------------------------------------

    /**
     * The ordinary case is NO description: the chip carries the word `Revoke`, and a description
     * would be read INSTEAD of it. What the parameter is for is the caller who has to say which
     * device this one destroys.
     */
    @Test
    fun `an undescribed chip is read by its label and a described one by its words`() {
        val plain = chip()
        assertNull("the chip invented words it was never given", plain.contentDescription)
        assertNull(
            announcementFault(plain.contentDescription, plain.text),
            announcementFault(plain.contentDescription, plain.text),
        )

        val spoken = chip(description = "Revoke swarm phone, this device")
        assertEquals("Revoke swarm phone, this device", spoken.contentDescription)
        assertNull(
            announcementFault(spoken.contentDescription, spoken.text),
            announcementFault(spoken.contentDescription, spoken.text),
        )
    }

    // ---- PB-DS-10's control ------------------------------------------------------

    @Test
    fun `the comparison fails when a value diverges`() {
        val subject = chip()
        val surface = subject.background as SubstrateSurface

        assertTrue(
            "a perturbed fill produced no mismatch, so the tint claim holds against any colour",
            mismatches(listOf(Claim("`.a2-no` fill", surface.spec.fill + 1, surface.spec.fill)))
                .isNotEmpty(),
        )
        assertTrue(
            "a perturbed padding produced no mismatch, so the chip could spend any step",
            mismatches(
                listOf(Claim("`.chip` padding", subject.paddingTop + 1, subject.paddingTop)),
            ).isNotEmpty(),
        )
        assertTrue(
            "the comparison reports a mismatch for a value that agrees, so it fails on everything",
            mismatches(listOf(Claim("`.a2-no` fill", surface.spec.fill, surface.spec.fill))).isEmpty(),
        )
    }
}
