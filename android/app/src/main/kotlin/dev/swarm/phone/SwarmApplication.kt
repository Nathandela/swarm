package dev.swarm.phone

import android.app.Application
import dev.swarm.phone.push.PushTokens
import dev.swarm.phone.theme.SwarmTheme

/**
 * The application entry point. PB-TOK-4's third route into the system uiMode is
 * AppCompatDelegate's default night mode, which has to be pinned before any activity is
 * created, and the process-wide [PhoneRuntime] is owned here because there must be exactly one
 * -- two runtimes would give one state directory two owners.
 */
class SwarmApplication : Application() {

    /**
     * The phone, built on first use and never in [onCreate].
     *
     * `by lazy` constructs the RUNTIME, which touches nothing; the phone itself is built by
     * `PhoneRuntime.phone()`, which reaches Keystore, the filesystem and the native library and
     * answers a `PhoneStartup` instead of throwing. Doing that work here would make a locked
     * handset, or a re-enrolled fingerprint, a process that dies before any screen exists to
     * report it -- and PB-APP-9's whole point is that a failure reaches the user.
     */
    val phoneRuntime: PhoneRuntime by lazy { PhoneRuntime(this) }

    override fun onCreate() {
        super.onCreate()
        SwarmTheme.applyDefaultNightMode()
        // PB-PUSH-9's "initial getToken", which is listed FIRST and separately from onNewToken
        // for a reason: the callback fires only on ROTATION, so an app that implements it alone
        // never registers on a fresh install -- and a fresh install is a phone that has just been
        // paired and is about to be backgrounded, which is the state push exists for.
        //
        // It is safe HERE despite the rule above: the token arrives in a listener, so the phone
        // is built off this thread and onCreate touches nothing that can refuse.
        PushTokens.requestInitialToken(this)
    }
}
