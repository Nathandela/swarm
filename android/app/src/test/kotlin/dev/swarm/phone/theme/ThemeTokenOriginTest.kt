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
            "SwarmTheme records ${recorded.size} colours and the origin maps ${fromOrigin.size}. " +
                "The theme's recorded palette must come FROM the origin -- derived at test time " +
                "from the staged tokens.json -- rather than being a third hand-copied list " +
                "beside colors.xml and the JSON.",
            fromOrigin.size,
            recorded.size,
        )
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
