package dev.swarm.phone.theme

import android.content.Context
import android.content.res.Configuration
import android.view.ContextThemeWrapper
import androidx.appcompat.app.AppCompatDelegate
import dev.swarm.phone.R

/**
 * PB-TOK-4 -- the app does not follow the system uiMode.
 *
 * Three independent routes hand the platform the system mode and blocking one leaves the
 * others open. Two are structural and live in the resources: a DayNight theme parent (see
 * res/values/themes.xml) and a -night qualified resource directory (there is none).
 *
 * The third is here, because it leaves no trace in any resource file: AppCompatActivity
 * applies [AppCompatDelegate.getDefaultNightMode] on create and its default is
 * MODE_NIGHT_FOLLOW_SYSTEM, so an app with a fixed-dark theme and no values-night directory
 * still follows the system unless it says otherwise.
 */
object SwarmTheme {

    /** The one theme the app renders with. */
    val STYLE_RES: Int = R.style.Theme_Swarm

    /**
     * The colours [STYLE_RES] resolves to, recorded as literals so ThemeNightModeTest compares
     * the theme against a number rather than against itself.
     *
     * PB-TOK-1 (S16): THE NUMBER RECORDED HERE IS THE ORIGIN'S, not colors.xml's. The instinct
     * behind this constant was right and it was pointed at the wrong value: recorded from
     * colors.xml it certified that the app renders whatever colors.xml says, which is exactly
     * what it would do if colors.xml were wrong -- and it was. Each entry is now the value of
     * the token that android/design-tokens.tsv maps to that resource, so the fence certifies
     * the requirement instead of the transcription.
     *
     * android/gate/s16_tokens_test.go refuses any literal here that is not a mapped token's
     * value, which is what stops this becoming a third copy of the palette again.
     */
    val EXPECTED_DARK_COLORS: IntArray = intArrayOf(
        0xFF0B0E14.toInt(), // android:colorBackground   <- --p-bg
        0xFFEEF2F8.toInt(), // android:textColorPrimary  <- --p-ink
        0xFF9AA6BA.toInt(), // android:textColorSecondary <- --p-ink2
    )

    /** Pins the delegate's night mode. Called from [dev.swarm.phone.SwarmApplication]. */
    fun applyDefaultNightMode() {
        AppCompatDelegate.setDefaultNightMode(AppCompatDelegate.MODE_NIGHT_YES)
    }

    /**
     * Returns [context] with the app's theme and its night uiMode applied, so a component that
     * inflates through it renders identically on a system-light and a system-dark handset.
     */
    fun applyTo(context: Context): Context {
        val night = Configuration(context.resources.configuration).apply {
            uiMode = (uiMode and Configuration.UI_MODE_NIGHT_MASK.inv()) or
                Configuration.UI_MODE_NIGHT_YES
        }
        return ContextThemeWrapper(context.createConfigurationContext(night), STYLE_RES)
    }
}
