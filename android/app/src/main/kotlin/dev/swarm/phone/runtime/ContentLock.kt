package dev.swarm.phone.runtime

import android.app.Activity
import android.app.Application
import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.content.IntentFilter
import android.os.Bundle
import dev.swarm.phone.keys.AuthorizationLedger
import dev.swarm.phone.keys.InvalidationEvent

/**
 * PB-KEY-7's lock purge and PB-SEC-2's invalidation clause, wired.
 *
 * WHY THIS FILE EXISTS AT ALL. Both requirements were NOT MET for want of a trigger: there was
 * no [ProcessLifecycleOwner], no `ACTION_SCREEN_OFF` receiver and no `onStop`/`onTrimMemory`
 * anywhere, `App.PurgeKeys` had no production caller, and [AuthorizationLedger.invalidate] had
 * none either. The Go core unwrapped the content key once at Resume and read it from memory for
 * the rest of the process, so after a single unlock neither a screen lock nor the stated
 * 60-second window stopped any content operation (ADR-007 B35/B36).
 *
 * WHY IT IS NOT `PhoneActivity.onPause`, WHICH IS THE OBVIOUS PLACE. That Activity is exported
 * with a LAUNCHER filter -- forced, because a launcher is another process -- and PB-SEC-11's
 * last clause is that no component reachable by a third-party app may act on the session. Its
 * own doc records that as the reason it reaches no facade verb. So the two requirements pull in
 * opposite directions and only PB-SEC-11 was ever resolved; this file is the resolution of the
 * other side, and it is better than the Activity would have been rather than a way around it:
 *
 *  - `registerActivityLifecycleCallbacks` is installed by [dev.swarm.phone.SwarmApplication] on
 *    the Application object. An Application subclass is not a component: it has no intent
 *    filter, appears in no exported allowlist, and nothing outside the process can start it.
 *  - The screen-off receiver is registered AT RUNTIME with `RECEIVER_NOT_EXPORTED`, so it has no
 *    manifest entry and no third-party app can reach it. `ACTION_SCREEN_OFF` is a protected
 *    system broadcast, which is the second, independent reason a forged one cannot arrive.
 *  - A hostile app can start the Activity, which brings the app to the FOREGROUND and purges
 *    nothing. It cannot background this process and it cannot turn the screen off, so the two
 *    signals here are not third-party-reachable in the way an exported entry point is.
 *
 * WHAT IS DELIBERATELY NOT HERE: a foreground timer for [InvalidationEvent.AUTH_TIMEOUT_EXPIRED].
 * The content KEK is provisioned with `setUserAuthenticationParameters(60,
 * AUTH_BIOMETRIC_STRONG)`, so the platform refuses the unwrap once the window has lapsed and the
 * remedy is a fresh authentication. This app ships no `BiometricPrompt` -- androidx.biometric is
 * not a dependency -- so a timer that locked a live foreground session would produce a refusal
 * with no in-app way to satisfy it, which is the "gate whose only exit is unbuilt" failure that
 * makes a phone worse than one that purges nothing. The enum value stays, and it routes here
 * exactly like the others for whoever wires the prompt.
 */
class ContentLock(
    private val core: ContentLockSink,
    private val ledger: AuthorizationLedger = AuthorizationLedger(),
) {

    /** The last event that reached [invalidate], for a test and for a bug report. */
    @Volatile
    var lastEvent: InvalidationEvent? = null
        private set

    /**
     * End content custody. Every [InvalidationEvent] routes here and none of them is optional.
     *
     * THE LEDGER GOES FIRST, and it is not decoration. It is not the gate -- the gate is the
     * Keystore refusing an unwrap -- but it decides whether prompting is worth doing, and one
     * left holding a live authorization across a screen lock would answer "already authorized"
     * for an operation the platform is about to refuse.
     *
     * It is unconditional and idempotent: a screen lock and a backgrounding arrive together all
     * the time, and a guard that skipped the second is a guard that cannot fail.
     */
    fun invalidate(event: InvalidationEvent) {
        lastEvent = event
        ledger.invalidate(event)
        core.lockContent()
    }

    /**
     * Whether [operation] still holds a live authorization. Never consulted as the gate; it is
     * here so the ledger this class invalidates has a reader, because a record with no reader is
     * the same as no record.
     */
    fun authorized(operation: dev.swarm.phone.keys.GatedOperation, atMillis: Long): Boolean =
        ledger.authorized(operation, atMillis)
}

/**
 * The Go core, as the lifecycle layer sees it. One method, so nothing else can be reached from
 * a lifecycle callback, and no key material crosses in either direction.
 */
fun interface ContentLockSink {
    fun lockContent()
}

/**
 * The two platform signals that end content custody, and the registration that makes them
 * arrive. Owned by the Application object; see [ContentLock] for why not the Activity.
 *
 * BACKGROUNDING IS COUNTED, NOT OBSERVED PER SCREEN. `onActivityStopped` fires for a rotation
 * and for a panel opening on top of another Activity, neither of which is the app leaving the
 * foreground. The started-Activity count reaching zero is what "the app is in the background"
 * means, and `isChangingConfigurations` excludes the rotation case from the count going down at
 * all.
 *
 * THE FRAMEWORK CALLBACK IS USED RATHER THAN `ProcessLifecycleOwner`. It says the same thing,
 * and `androidx.lifecycle:lifecycle-process` resolves onto this module's RUNTIME classpath only
 * -- declaring it means regenerating the dependency lockfile and the checksum verification
 * metadata PB-SEC-14 pins, for a signal the platform already provides.
 */
class ContentLockTriggers(private val lock: ContentLock) : Application.ActivityLifecycleCallbacks {

    private var started = 0

    /**
     * The screen going off, which is the event PB-KEY-7 names first and the one the Activity
     * callbacks alone do not give: a handset locked while this app is in the foreground must
     * purge, and the ordering of `onStop` against the keyguard is not something to rely on.
     *
     * Registered at runtime and NOT EXPORTED. `ACTION_SCREEN_OFF` is a protected broadcast that
     * only the system may send, and the action is re-checked here regardless -- a receiver that
     * acted on whatever intent arrived would be the shape PB-SEC-11 forbids, wherever it lives.
     */
    private val screenOff = object : BroadcastReceiver() {
        override fun onReceive(context: Context?, intent: Intent?) {
            if (intent?.action != Intent.ACTION_SCREEN_OFF) return
            lock.invalidate(InvalidationEvent.DEVICE_LOCKED)
        }
    }

    /** Install both signals. Called once, from `Application.onCreate`. */
    fun install(application: Application) {
        application.registerActivityLifecycleCallbacks(this)
        application.registerReceiver(
            screenOff,
            IntentFilter(Intent.ACTION_SCREEN_OFF),
            Context.RECEIVER_NOT_EXPORTED,
        )
    }

    override fun onActivityStarted(activity: Activity) = activityStarted()

    override fun onActivityStopped(activity: Activity) = activityStopped(activity.isChangingConfigurations)

    /**
     * The counting, split from the framework override so it is drivable without a real Activity.
     * The override is then a one-liner with nothing in it to get wrong, and the rule below is
     * asserted directly rather than through a shadow that has to be believed.
     */
    internal fun activityStarted() {
        started++
    }

    /**
     * @param changingConfigurations `Activity.isChangingConfigurations`. A rotation stops and
     *  restarts the Activity without the app leaving the foreground; counting it would end
     *  content custody on every orientation change, which is a re-authentication the user cannot
     *  connect to anything they did.
     */
    internal fun activityStopped(changingConfigurations: Boolean) {
        if (changingConfigurations) return
        started = (started - 1).coerceAtLeast(0)
        if (started == 0) lock.invalidate(InvalidationEvent.APP_BACKGROUNDED)
    }

    override fun onActivityCreated(activity: Activity, savedInstanceState: Bundle?) = Unit

    override fun onActivityResumed(activity: Activity) = Unit

    override fun onActivityPaused(activity: Activity) = Unit

    override fun onActivitySaveInstanceState(activity: Activity, outState: Bundle) = Unit

    override fun onActivityDestroyed(activity: Activity) = Unit
}
