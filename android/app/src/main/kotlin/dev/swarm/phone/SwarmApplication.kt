package dev.swarm.phone

import android.app.Application
import dev.swarm.phone.push.PushTokens
import dev.swarm.phone.push.WakeNotifications
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
     * answers a `PhoneStartup` instead of throwing. Doing that work here would make a handset
     * whose Keystore refuses a process that dies before any screen exists to report it -- and
     * PB-APP-9's whole point is that a failure reaches the user.
     */
    val phoneRuntime: PhoneRuntime by lazy { PhoneRuntime(this) }

    override fun onCreate() {
        super.onCreate()
        SwarmTheme.applyDefaultNightMode()
        // THE WAKE CHANNEL EXISTS FROM THE START OF THE PROCESS, NOT FROM THE FIRST WAKE
        // (agents-tracker-2yfn). `ensureChannel`'s only caller was `SwarmMessagingService`, so
        // before a push had ever arrived there was no channel at all: `getNotificationChannel`
        // answered null and anything that wanted to ask whether delivery was blocked had nothing to
        // ask about. The settings screen is precisely somewhere a user goes BEFORE any wake -- it is
        // where the two switches are turned on -- so the one screen that reports on notifications
        // could not see the object it reports on.
        //
        // THE PER-WAKE CALL IS KEPT and this is IN ADDITION to it. `WakeNotifications`'s own KDoc
        // gives the reason and it still holds: the process handling a wake is routinely a fresh one
        // Android built for that message alone. Two calls to an idempotent creator is the same
        // guarantee made at both moments it has to be true.
        //
        // IT IS SAFE HERE for the reason stated below about the token: it touches NotificationManager
        // and nothing that can refuse -- no Keystore, no filesystem, no native library.
        WakeNotifications.ensureChannel(this)
        // NOTHING OBSERVES THE SCREEN LOCK ANY MORE (ADR-007 B133). This used to install
        // `ContentLockTriggers` here, so that a lock or a backgrounding purged the content tier
        // and the user re-authenticated on the way back. The trust boundary is now the WIRE, and
        // PB-KEY-7's purge survives with a different trigger: revoke and unpair, which is where
        // `PhoneRuntime.purgeKeys` is reached from.
        //
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
