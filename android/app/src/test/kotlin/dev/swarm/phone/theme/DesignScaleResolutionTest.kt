package dev.swarm.phone.theme

import android.content.Context
import android.util.TypedValue
import androidx.test.core.app.ApplicationProvider
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotEquals
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner

/**
 * FAILING-FIRST (TDD RED, GG-5) for PB-DS-1, PB-DS-2 and PB-DS-4 -- the half only a running
 * resource table can answer.
 *
 * WHY THIS EXISTS BESIDE THE GO GATE, in the words [ThemeTokenOriginTest] already uses for the
 * colour half: android/gate/s22b_*_test.go compares the two FILES, which is the join the
 * requirements ask for. It cannot say what the app RESOLVES. Android picks a dimension and a
 * style from the MERGED resource table at runtime, and a value that is right in dimens.xml can
 * still be overridden by a library resource of the same name, or by a qualifier nobody noticed --
 * appcompat, biometric, camera-view and firebase-messaging all contribute resources to this
 * module's table.
 *
 * EVERY EXPECTED NUMBER COMES FROM THE DESIGN, NOT FROM THE IMPLEMENTATION. [DesignScale] reads
 * the staged tokens.json and the staged design artifact and computes them. A test that recorded
 * 9dp here because dimens.xml says 9dp would certify that the app renders whatever dimens.xml
 * says, which is exactly what it would do if dimens.xml were wrong -- and colors.xml was wrong,
 * through three files, with its own test green the whole time.
 *
 * RESOURCES ARE RESOLVED BY NAME rather than through generated R constants. An R field for a
 * resource that does not exist is a COMPILE error, and a suite that does not compile is not a
 * failing test -- it is a broken build, which cannot be read as RED evidence for anything.
 * getIdentifier returns 0 for an absent resource, so the first run reports the requirement.
 */
@RunWith(RobolectricTestRunner::class)
class DesignScaleResolutionTest {

    private val context: Context get() = SwarmTheme.applyTo(ApplicationProvider.getApplicationContext())

    private fun id(name: String, type: String): Int =
        context.resources.getIdentifier(name, type, context.packageName)

    /** The resolved value of a `dimen` in dp, or null when the resource does not exist. */
    private fun dimenDp(name: String): Float? {
        val res = id(name, "dimen")
        if (res == 0) return null
        return context.resources.getDimension(res) / context.resources.displayMetrics.density
    }

    /**
     * PB-DS-1's frame constants, resolved, against the design's own `.pscreen` and `.ptabs`.
     *
     * The design's px is Android dp at 1:1, so the assertion is an equality rather than a
     * conversion -- which is itself the claim being checked.
     */
    @Test
    fun `the frame constants resolve to the design's own frame`() {
        val screenPadding = DesignScale.rule(".pscreen").getValue("padding").split(" ")
        val expected = mapOf(
            "swarm_screen_top" to DesignScale.px(screenPadding[0]),
            "swarm_screen_bottom" to DesignScale.px(screenPadding[2]),
            "swarm_tabbar_height" to DesignScale.px(DesignScale.rule(".ptabs").getValue("height")),
        )
        expected.forEach { (name, want) ->
            assertNotNull("the design source yielded no value for $name", want)
            assertEquals(
                "R.dimen.$name did not resolve to the design's own value. The frame is the one " +
                    "part of the layout the scale does not govern -- 54/76/74 are not on a 2dp " +
                    "grid -- so it is the part most likely to be retyped from prose.",
                want!!,
                dimenDp(name) ?: MISSING,
                0f,
            )
        }
    }

    /**
     * PB-DS-4's four radii, resolved, against the four radius tokens.
     *
     * `--p-dot-r` is absent by design and the Go gate asserts that it stays absent; here the
     * point is narrower and it is the one Robolectric adds: a radius resource that resolves to a
     * DIFFERENT number than its token, because something in the merged table shadows it.
     */
    @Test
    fun `the radii resolve to the radius tokens`() {
        mapOf(
            "swarm_radius_card" to "--p-card-r",
            "swarm_radius_sheet" to "--p-sheet-r",
            "swarm_radius_button" to "--p-btn-r",
            "swarm_radius_chip" to "--p-chip-r",
        ).forEach { (name, token) ->
            assertEquals(
                "R.dimen.$name did not resolve to $token",
                DesignScale.tokenPx(token),
                dimenDp(name) ?: MISSING,
                0f,
            )
        }
    }

    /**
     * The scale itself: ten steps, every one resolving, on a 2dp grid, strictly ascending.
     *
     * This one CANNOT be derived from the design -- the design has no scale, which is PB-DS-1's
     * first sentence. So what is asserted is the property a scale has rather than a list of
     * numbers: an odd step or a repeated one is the moment it stops being a grid.
     */
    @Test
    fun `the spacing scale resolves as an ascending 2dp grid`() {
        val steps = listOf(2, 4, 6, 8, 10, 12, 14, 16, 18, 24)
        var previous = 0f
        steps.forEach { dp ->
            val name = "swarm_space_$dp"
            val got = dimenDp(name)
            assertNotNull("R.dimen.$name does not exist in the merged resource table", got)
            assertEquals("R.dimen.$name", dp.toFloat(), got!!, 0f)
            assertEquals("$name is not on the 2dp grid", 0f, got % 2f, 0f)
            assertTrue("the scale is not ascending at $name ($got after $previous)", got > previous)
            previous = got
        }
    }

    /**
     * PB-DS-2, resolved: every style is in the table and every attribute it carries is the
     * design's.
     *
     * THE STYLE SET IS DISCOVERED, NOT LISTED. The names come from type.xml's own origin
     * comments via [TypeScale], so this test cannot drift out of step with the file it checks;
     * what it asserts is that each one RESOLVES, and that what it resolves to is the number the
     * CSS rule it names declares.
     */
    @Test
    fun `every text style resolves to its design rule`() {
        val styles = TypeScale.styles()
        assertTrue(
            "no TextAppearance.Swarm.* styles were found; every assertion here would be vacuous",
            styles.isNotEmpty(),
        )

        styles.forEach { (name, origin) ->
            val res = id(name, "style")
            assertNotEquals(
                "R.style.${name.replace('.', '_')} is not in the merged resource table",
                0,
                res,
            )
            // AUTHORIZED REWRITE, ADR-012 phase 2 (owner ruling R1, 2026-08-09). What this line
            // said before:
            //
            //     val spec = TypeScale.designSpec(origin)
            //
            // The rule is still resolved and every attribute below still comes out of it. Only
            // the SIZE moves to the ruled rung, because R1 consolidated the ladder to five and
            // a rung is a decision about this app's hierarchy that no CSS rule can state.
            val spec = TypeScale.renderedSpec(origin)
            val values = context.obtainStyledAttributes(res, ATTR_IDS)
            try {
                // Size. sp, and read back in px at the test display's density and font scale --
                // so this also asserts the two are 1:1 at the default scale, which is what makes
                // "the design's px is Android sp" true rather than assumed.
                //
                // The sp->px factor is asked for rather than read off DisplayMetrics.scaledDensity,
                // which is deprecated from API 34 because it stopped being a single number when
                // non-linear font scaling landed. applyDimension is the supported question.
                val scale = TypedValue.applyDimension(
                    TypedValue.COMPLEX_UNIT_SP, 1f, context.resources.displayMetrics,
                )
                assertEquals(
                    "$name (origin `$origin`) textSize",
                    spec.sizePx,
                    values.getDimension(IDX_SIZE, MISSING) / scale,
                    0.001f,
                )
                assertEquals(
                    "$name (origin `$origin`) textFontWeight",
                    spec.weight,
                    values.getInt(IDX_WEIGHT, -1),
                )
                assertEquals(
                    "$name (origin `$origin`) letterSpacing -- CSS em and Android em are the " +
                        "same unit, so a difference here is a transcription error and nothing else",
                    spec.trackingEm,
                    values.getFloat(IDX_TRACKING, MISSING),
                    1e-6f,
                )
                // FAMILY, and ADR-009 D7 split this assertion in two because Android resolves the
                // two kinds of family differently. A platform NAME arrives as a string; a BUNDLED
                // family arrives as a resource reference, and getString on it returns the
                // resource's file path -- so the string comparison that was here would have
                // compared "@font/jetbrains_mono" against "res/font/jetbrains_mono.xml" and
                // reported the bundling as a drift. The reference is checked as a reference.
                //
                // What this assertion said before (ADR-009 D8.3):
                //
                //     assertEquals(
                //         "$name (origin `$origin`) fontFamily",
                //         spec.androidFamily,
                //         values.getString(IDX_FAMILY),
                //     )
                val bundled = TypeScale.bundledFontName(spec.androidFamily)
                if (bundled == null) {
                    assertEquals(
                        "$name (origin `$origin`) fontFamily",
                        spec.androidFamily,
                        values.getString(IDX_FAMILY),
                    )
                } else {
                    val want = id(bundled, "font")
                    assertNotEquals(
                        "R.font.$bundled is not in the merged resource table, so " +
                            "`$name` names a family Android cannot resolve -- which does not " +
                            "fail the build, it falls back to the default sans in silence",
                        0,
                        want,
                    )
                    assertEquals(
                        "$name (origin `$origin`) fontFamily does not resolve to R.font.$bundled",
                        want,
                        values.getResourceId(IDX_FAMILY, 0),
                    )
                }

                // FONT FEATURES, ADR-009 D7: tabular figures, slashed zero and the contextual
                // alternates the bundled family ships on, wherever machine data renders -- which
                // in this design is exactly the mono styles. Asserted in both directions, so a
                // feature string on a proportional face is as much a failure as its absence on a
                // mono one.
                assertEquals(
                    "$name (origin `$origin`) fontFeatureSettings",
                    if (spec.isMono) MONO_FONT_FEATURES else null,
                    values.getString(IDX_FEATURES),
                )
                // LEADING, IN BOTH DIRECTIONS -- and the null arm is the half that used to be
                // unasserted here (ADR-009 D7, amended 2026-08-08). A design fact stating
                // `line-height: 1` states NO EXTRA LEADING, which on Android is the absence of
                // `android:lineHeight` rather than `android:lineHeight` equal to the text size:
                // the attribute sets the line box's absolute height, so the same number shrinks a
                // box the font wants taller and the platform pays for it with a negative
                // `lineSpacingExtra`. Asserting the absence is what stops `Label.Button` sitting
                // low in its own CTA again. See TypeScale.Spec.lineHeightPx.
                val resolvedLeading = values.getDimension(IDX_LINE_HEIGHT, MISSING)
                when (val wantLeading = spec.lineHeightPx) {
                    null -> assertEquals(
                        "$name (origin `$origin`) resolves a lineHeight and the design states " +
                            "no leading to transcribe (line-height ${spec.lineHeightMultiplier})",
                        MISSING,
                        resolvedLeading,
                        0f,
                    )
                    else -> assertEquals(
                        "$name (origin `$origin`) lineHeight: the design's multiplier on the " +
                            "size this style renders at is " +
                            "${spec.lineHeightMultiplier} x ${spec.sizePx}sp",
                        wantLeading,
                        resolvedLeading / scale,
                        0.001f,
                    )
                }
            } finally {
                values.recycle()
            }
        }
    }

    /**
     * The negative control, in the shape PB-DS-10 names: `TestPBTOK1_TheComparisonCanActuallyFail`.
     *
     * Every assertion above is an equality between a resolved resource and a parsed design value,
     * and both halves have a failure mode that is green forever. If [DesignScale] returned the
     * same number for everything -- an empty map read as zeroes, a regex that stopped matching --
     * the comparisons would pass over values that differ. So the parse is exercised against
     * design facts that are known and are NOT equal to each other.
     */
    @Test
    fun `the design parse can distinguish two values`() {
        // AUTHORIZED VALUE MIGRATION, ADR-009 O2. These are KNOWN ANSWERS -- numbers typed
        // independently of the reader so that a reader returning a constant is contradicted --
        // and ADR-009 D3 moved the answers. What they said before:
        //
        //     assertEquals("--p-card-r is 9px in the origin", 9f, ...tokenPx("--p-card-r"), 0f)
        //     assertEquals("--p-sheet-r is 14px", 14f, ...tokenPx("--p-sheet-r"), 0f)
        //     assertEquals("`.pnav .big` resolves --p-display-wt", 650, nav.weight)
        //     assertEquals("`.pnav .big` resolves --p-display-tr", -0.025f, nav.trackingEm, 1e-6f)
        //
        // They are NOT rewritten to read the value they check -- that would be the reader
        // certifying itself, which is the exact defect this test exists to catch. The sizes below
        // (27px, 9.5px) are unchanged, because ADR-009 moves material and not the type scale.
        assertEquals("--p-card-r is 14px in the origin", 14f, DesignScale.tokenPx("--p-card-r"), 0f)
        assertEquals("--p-sheet-r is 18px", 18f, DesignScale.tokenPx("--p-sheet-r"), 0f)
        assertNotEquals(
            "the token reader returns the same number for two different tokens, so every " +
                "radius assertion above is vacuous",
            DesignScale.tokenPx("--p-card-r"),
            DesignScale.tokenPx("--p-sheet-r"),
        )

        val nav = TypeScale.designSpec(".pnav .big")
        val tab = TypeScale.designSpec(".ptabs div")
        assertEquals("`.pnav .big` is 27px in the design", 27f, nav.sizePx, 0f)
        assertEquals("`.ptabs div` is 9.5px", 9.5f, tab.sizePx, 0f)
        // AND THE RUNG READER'S OWN KNOWN ANSWERS (ADR-012 phase 2, owner ruling R1). Typed
        // independently of the reader, exactly as the two above are, and DIFFERENT from them --
        // a rung reader that fell back to the design px for everything would make every size
        // assertion in this suite an assertion about the ladder R1 retired, and would do it
        // silently.
        assertEquals(
            "`.pnav .big` renders on the 22 sp display rung",
            22f,
            TypeScale.renderedSizeSp(".pnav .big"),
            0f,
        )
        assertEquals(
            "`.ptabs div` renders on the 10 sp micro rung",
            10f,
            TypeScale.renderedSizeSp(".ptabs div"),
            0f,
        )
        assertNotEquals(
            "the rung reader answers the design's own px, so no style was consolidated at all",
            nav.sizePx,
            TypeScale.renderedSizeSp(".pnav .big"),
        )
        assertEquals("`.pnav .big` resolves --p-display-wt", 500, nav.weight)
        assertEquals("`.pnav .big` resolves --p-display-tr", -0.015f, nav.trackingEm, 1e-6f)
        assertNotEquals(
            "the CSS parse returns the same family for a sans rule and a mono rule",
            TypeScale.designSpec(".prow .pj").androidFamily,
            TypeScale.designSpec(".prow .ag").androidFamily,
        )
        // AUTHORIZED VALUE MIGRATION, ADR-009 D8.3 / D7. What the mono half said before:
        //
        //     assertEquals("monospace", TypeScale.designSpec(".sheet2 .cmd").androidFamily)
        //
        // The sans half does not move. `.sheet2 .cmd` is the command well, and D7 is what put a
        // bundled face under it.
        assertEquals("sans-serif", nav.androidFamily)
        assertEquals("@font/jetbrains_mono", TypeScale.designSpec(".sheet2 .cmd").androidFamily)
    }

    private fun assertNotNull(message: String, value: Any?) =
        assertTrue(message, value != null)

    private companion object {
        /**
         * A sentinel no design value can be. 0 would be indistinguishable from a style that
         * resolved but declared nothing, which is the failure most worth catching.
         */
        const val MISSING = -1f

        /**
         * SORTED, because obtainStyledAttributes requires it -- the platform binary-searches the
         * array against the style's entries, and an unsorted one silently returns defaults for
         * whatever it fails to find. Every generated R.styleable array is sorted for the same
         * reason; hand-written ones are where this goes wrong.
         */
        /**
         * ADR-009 D7's feature string, verbatim. The Go gate (s22bMonoFontFeatures) is the
         * authority; this copy exists because a Robolectric test cannot read a Go constant, and
         * both are joined to the ADR's own words rather than to each other.
         */
        const val MONO_FONT_FEATURES = "tnum, zero, calt"

        val ATTR_IDS = intArrayOf(
            android.R.attr.textSize,
            android.R.attr.textFontWeight,
            android.R.attr.letterSpacing,
            android.R.attr.fontFamily,
            android.R.attr.lineHeight,
            android.R.attr.fontFeatureSettings,
        ).sortedArray()

        val IDX_SIZE = ATTR_IDS.indexOf(android.R.attr.textSize)
        val IDX_WEIGHT = ATTR_IDS.indexOf(android.R.attr.textFontWeight)
        val IDX_TRACKING = ATTR_IDS.indexOf(android.R.attr.letterSpacing)
        val IDX_FAMILY = ATTR_IDS.indexOf(android.R.attr.fontFamily)
        val IDX_LINE_HEIGHT = ATTR_IDS.indexOf(android.R.attr.lineHeight)
        val IDX_FEATURES = ATTR_IDS.indexOf(android.R.attr.fontFeatureSettings)
    }
}
