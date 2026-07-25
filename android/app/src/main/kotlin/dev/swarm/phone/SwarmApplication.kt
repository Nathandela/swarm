package dev.swarm.phone

import android.app.Application
import dev.swarm.phone.theme.SwarmTheme

/**
 * The application entry point. It exists in this slice for one reason: PB-TOK-4's third route
 * into the system uiMode is AppCompatDelegate's default night mode, which has to be pinned
 * before any activity is created.
 *
 * S16 owns the screens; nothing else belongs here yet.
 */
class SwarmApplication : Application() {

    override fun onCreate() {
        super.onCreate()
        SwarmTheme.applyDefaultNightMode()
    }
}
