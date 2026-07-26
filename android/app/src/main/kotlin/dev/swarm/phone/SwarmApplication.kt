package dev.swarm.phone

import android.app.Application
import dev.swarm.phone.push.PushTokens
import dev.swarm.phone.runtime.ContentLock
import dev.swarm.phone.runtime.ContentLockTriggers
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

    /**
     * PB-KEY-7's lock purge and PB-SEC-2's invalidation clause, as a process-wide observer.
     *
     * IT IS OWNED HERE AND NOWHERE ELSE, and that is the resolution of the conflict ADR-007 B36
     * recorded. The obvious home is `PhoneActivity.onPause`, and it is the wrong one: that
     * Activity is exported with a LAUNCHER filter, and PB-SEC-11 forbids a component a
     * third-party app can start from acting on the session. An Application subclass is not a
     * component -- no intent filter, no allowlist row, unreachable from outside the process --
     * so the two requirements stop pulling against each other.
     *
     * `by lazy` on [contentLock] would be wrong: the observers have to be REGISTERED, and one
     * registered on first use is one that was not listening for the lock before it.
     */
    val contentLock: ContentLock by lazy { ContentLock({ phoneRuntime.lockContent() }) }

    override fun onCreate() {
        super.onCreate()
        SwarmTheme.applyDefaultNightMode()
        // Before anything else can put a screen up: a lock that starts observing after the first
        // Activity is a lock that missed the first backgrounding.
        ContentLockTriggers(contentLock).install(this)
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
