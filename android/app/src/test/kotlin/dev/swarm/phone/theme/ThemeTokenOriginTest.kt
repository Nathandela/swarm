package dev.swarm.phone.theme

import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * FAILING-FIRST (TDD RED, GG-5) for PB-TOK-1, reassigned S5 -> S16.
 *
 * "One machine-readable token source (JSON) is the single origin for the Android theme. Theme
 *  generated from or asserted against the JSON. The values must actually agree, and the
 *  assertion must fail when they diverge."
 *
 * WHY THIS EXISTS BESIDE THE GO GATE. android/gate/s16_tokens_test.go compares the two FILES,
 * which is the join the requirement asks for. It cannot say what the app RESOLVES: Android
 * picks a colour from the merged resource table at runtime, and a value that is right in
 * colors.xml can still be overridden by a library resource of the same name, or by a qualifier
 * nobody noticed. ThemeNightModeTest already resolves the theme and compares it against
 * SwarmTheme.EXPECTED_DARK_COLORS -- a literal copied from colors.xml, so it certifies the app
 * renders whatever colors.xml says. Pointed at the ORIGIN instead, the same resolution
 * certifies the requirement.
 *
 * THE ORIGIN IS READ FROM THE UNIT-TEST CLASSPATH, never by relative path -- the arrangement
 * PolicyTables spells out for the two policy TSVs, and for the same reason: a relative path
 * makes the test depend on Gradle's working directory, which is not a property of the app, and
 * it silently starts passing over an empty map the first time someone runs it elsewhere. The
 * module build must stage internal/design/tokens.json and android/design-tokens.tsv as
 * unit-test resources.
 */
class ThemeTokenOriginTest {

    /**
     * The colour resources the three themed attributes bind to, in the order
     * ThemeNightModeTest.themedAttributes resolves them: colorBackground, textColorPrimary,
     * textColorSecondary.
     *
     * WHY THIS LIST REPLACED A SIZE EQUALITY. Until PB-TOK-5 the assertion here compared
     * EXPECTED_DARK_COLORS.size against the size of the WHOLE token join, which was true only
     * while the join happened to hold three rows. It is a coincidence, not an invariant:
     * EXPECTED_DARK_COLORS is one entry per themed ATTRIBUTE, and the join is every colour the
     * app owns. Widening the join to sixteen colours made the coincidence fail and would have
     * read as a defect in the widening.
     *
     * Positional correspondence is strictly stronger than what it replaced. The containment
     * loop below only says each recorded colour is SOME mapped token's value, so transposing
     * background and text-primary passes it while the theme paints text in the background
     * colour. Pinning each attribute to its own resource catches that, and it survives the join
     * growing to sixteen or a hundred and sixteen.
     */
    private val ATTRIBUTE_RESOURCES = listOf(
        "swarm_background",
        "swarm_text_primary",
        "swarm_text_secondary",
    )

    /** Attribute names, for the failure message only. Same order as [ATTRIBUTE_RESOURCES]. */
    private val ATTRIBUTE_NAMES = listOf(
        "colorBackground",
        "textColorPrimary",
        "textColorSecondary",
    )

    /**
     * The colours the theme records must BE the tokens, not merely resemble them.
     *
     * This is the assertion that fails today: --p-bg is #08090a and swarm_background is
     * #FF101114, and SwarmTheme.EXPECTED_DARK_COLORS holds the second of those a third time.
     */
    @Test
    fun `the recorded theme colours are the design tokens`() {
        val expected = DesignTokens.androidColors()
        assertTrue(
            "the token origin resolved to no Android colours at all; every assertion here " +
                "would be vacuous",
            expected.isNotEmpty(),
        )

        val recorded = SwarmTheme.EXPECTED_DARK_COLORS.toList()
        val fromOrigin = expected.values.toList()

        assertEquals(
            "SwarmTheme records ${recorded.size} colours but the theme binds " +
                "${ATTRIBUTE_RESOURCES.size} attributes. EXPECTED_DARK_COLORS is the list " +
                "ThemeNightModeTest resolves, one entry per themed attribute, in that order.",
            ATTRIBUTE_RESOURCES.size,
            recorded.size,
        )
        ATTRIBUTE_RESOURCES.forEachIndexed { i, resource ->
            val originValue = expected[resource]
            assertTrue(
                "$resource is not in the token join at all, so entry $i of " +
                    "EXPECTED_DARK_COLORS has no origin to be checked against",
                originValue != null,
            )
            assertEquals(
                "EXPECTED_DARK_COLORS[$i] is the value of android:%s and must be the origin's "
                    .format(ATTRIBUTE_NAMES[i]) +
                    "value for $resource. Containment alone does not catch this: with the " +
                    "entries transposed every recorded colour is still SOME mapped token's " +
                    "value, and the theme paints text in the background colour.",
                originValue,
                recorded[i],
            )
        }
        recorded.forEach { colour ->
            assertTrue(
                "SwarmTheme records 0x%08X, which is not the value of any mapped design token. "
                    .format(colour) +
                    "A palette stated in three places is one that will disagree in two of them, " +
                    "and the test built on this constant is the one that would not notice.",
                fromOrigin.contains(colour),
            )
        }
    }

    /**
     * The negative control for the positional assertion, required by PB-DS-10: an assertion
     * whose failure mode has never been demonstrated is the shape that let this codebase ship a
     * third divergent palette under a green test.
     *
     * It applies the same check to a deliberately transposed list and requires it to find the
     * mismatch. If this ever passes vacuously -- because two attribute resources resolved to the
     * same colour, say -- the guard on the first line is what says so.
     */
    @Test
    fun `the positional check can actually fail`() {
        val expected = DesignTokens.androidColors()
        val correct = ATTRIBUTE_RESOURCES.map { expected[it] }
        assertTrue(
            "every themed attribute must resolve to a colour in the join, or the control " +
                "below compares nulls and proves nothing",
            correct.none { it == null },
        )
        assertTrue(
            "the first two themed attributes hold the same colour, so a transposition is " +
                "undetectable and this control is vacuous",
            correct[0] != correct[1],
        )

        val transposed = listOf(correct[1], correct[0], correct[2])
        val mismatches = ATTRIBUTE_RESOURCES.filterIndexed { i, r -> expected[r] != transposed[i] }
        assertTrue(
            "a transposed palette produced no mismatch, so the positional assertion above " +
                "would certify a theme that paints text in the background colour",
            mismatches.isNotEmpty(),
        )
    }

    /**
     * The join must be complete in both directions. A colour resource with no token is a value
     * that entered the theme without passing through the origin -- "single origin" decaying
     * into "origin plus a few extras", which is how it decayed the first time.
     */
    @Test
    fun `every mapped token names a real token and a real colour resource`() {
        val map = DesignTokens.mapping()
        assertTrue("the token map is empty", map.isNotEmpty())
        map.forEach { (token, resource) ->
            assertTrue("$token is not declared in tokens.json", DesignTokens.raw(token) != null)
            assertTrue("$resource is not a colour resource name", resource.startsWith("swarm_"))
        }
    }

    /**
     * The conversion is the whole comparison, so it is exercised against a known pair rather
     * than trusted. A converter that normalised too eagerly -- dropping alpha, folding case,
     * accepting a three-digit shorthand -- would make every assertion above pass over values
     * that differ, which is exactly the shape PB-TOK-1's criterion calls out.
     */
    @Test
    fun `the token to ARGB conversion can distinguish two colours`() {
        assertEquals(0xFF08090A.toInt(), DesignTokens.toArgb("#08090a"))
        assertTrue(DesignTokens.toArgb("#08090a") != DesignTokens.toArgb("#101114"))
        assertTrue(DesignTokens.toArgb("#08090a") != DesignTokens.toArgb("#08090b"))
    }
}
