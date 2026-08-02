package dev.swarm.phone.theme

import android.content.Context
import android.content.res.Configuration
import android.util.TypedValue
import androidx.appcompat.app.AppCompatDelegate
import androidx.test.core.app.ApplicationProvider
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.Config

/**
 * PB-TOK-4 -- "The Android app does not follow the system uiMode."
 *
 * The behavioural half. android/gate/theme_test.go asserts the two structural
 * routes -- a DayNight theme parent and a -night resource qualifier -- and this
 * asserts the outcome, because neither test subsumes the other:
 *
 *   - the structural test catches a DayNight parent even in an app that
 *     overrides every colour attribute, where the resolved values would be
 *     identical under both qualifiers and this test would pass;
 *   - this test catches the third route, AppCompatDelegate's default night mode,
 *     which is MODE_NIGHT_FOLLOW_SYSTEM unless the app sets otherwise and leaves
 *     no trace in any resource file at all.
 *
 * §5 defers light mode to Phase C. So on a system-light handset -- the majority
 * configuration -- a theme that follows uiMode has no light token set to resolve
 * against and renders unstyled or low-contrast. PB-E2E-2's screenshots would
 * show it, and PB-E2E-2 runs on an emulator whose default is dark.
 */
@RunWith(RobolectricTestRunner::class)
class ThemeNightModeTest {

    /** The attributes the terminal peek and the triage list actually paint with. */
    private val themedAttributes = intArrayOf(
        android.R.attr.colorBackground,
        android.R.attr.textColorPrimary,
        android.R.attr.textColorSecondary,
    )

    private fun resolvedColors(context: Context): List<Int> {
        val themed = android.view.ContextThemeWrapper(context, SwarmTheme.STYLE_RES)
        return themedAttributes.map { attr ->
            val value = TypedValue()
            assertTrue(
                "theme does not resolve attribute $attr; the comparison below would be " +
                    "over an empty set",
                themed.theme.resolveAttribute(attr, value, true),
            )
            if (value.resourceId != 0) {
                androidx.core.content.ContextCompat.getColor(themed, value.resourceId)
            } else {
                value.data
            }
        }
    }

    @Test
    @Config(qualifiers = "notnight")
    fun theme_resolves_identically_under_system_light() {
        val light = resolvedColors(ApplicationProvider.getApplicationContext())
        assertEquals(
            "the theme resolved different colours under the notnight qualifier than the " +
                "recorded dark values, so it follows the system uiMode. §5 defers light " +
                "mode, so there is no light token set for it to resolve to",
            SwarmTheme.EXPECTED_DARK_COLORS.toList(),
            light,
        )
    }

    @Test
    @Config(qualifiers = "night")
    fun theme_resolves_identically_under_system_dark() {
        val dark = resolvedColors(ApplicationProvider.getApplicationContext())
        assertEquals(SwarmTheme.EXPECTED_DARK_COLORS.toList(), dark)
    }

    /**
     * The route that leaves no trace in the resources. AppCompatActivity applies
     * AppCompatDelegate.getDefaultNightMode() on create, and its default is
     * MODE_NIGHT_FOLLOW_SYSTEM -- so an app can have a non-DayNight theme, no
     * values-night directory, and still hand the platform the system uiMode.
     */
    @Test
    fun default_night_mode_is_pinned_to_yes_and_not_left_following_the_system() {
        SwarmTheme.applyDefaultNightMode()
        assertEquals(
            "AppCompatDelegate's default night mode is MODE_NIGHT_FOLLOW_SYSTEM unless the " +
                "app sets it. Following the system is precisely what PB-TOK-4 forbids",
            AppCompatDelegate.MODE_NIGHT_YES,
            AppCompatDelegate.getDefaultNightMode(),
        )
    }

    /**
     * And the configuration the app actually runs under, asserted from the
     * light side: even started under notnight, the applied configuration must
     * report night.
     */
    @Test
    @Config(qualifiers = "notnight")
    fun applied_configuration_reports_night_even_when_the_system_is_light() {
        SwarmTheme.applyDefaultNightMode()
        val context = ApplicationProvider.getApplicationContext<Context>()
        val applied = SwarmTheme.applyTo(context).resources.configuration.uiMode and
            Configuration.UI_MODE_NIGHT_MASK
        assertEquals(
            "the app rendered in light mode on a system-light device",
            Configuration.UI_MODE_NIGHT_YES,
            applied,
        )
    }
}
