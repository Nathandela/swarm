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
import dev.swarm.phone.keys.PerUseRefusalReason
import dev.swarm.phone.keys.PerUseRefusalText
import dev.swarm.phone.keys.PromptAvailability
import dev.swarm.phone.ui.Remedy
import dev.swarm.phone.ui.RoutedError

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
 * remedy is a fresh authentication.
 *
 * THE REASON ORIGINALLY RECORDED HERE IS NOW STALE, and it is corrected rather than quietly
 * dropped. It read: "this app ships no `BiometricPrompt` -- androidx.biometric is not a
 * dependency -- so a timer would produce a refusal with no in-app way to satisfy it". That was
 * true and is no longer: [ContentUnlockPolicy] and `dev.swarm.phone.keys.BiometricPrompts` build
 * exactly that exit (ADR-007 B59). What remains is a SCOPE decision on its own merits, and it is
 * a smaller claim than the one it replaces: a continuously-foregrounded session means the device
 * is unlocked and the user is present, every re-acquisition after a lock, a backgrounding or
 * process death is already Keystore-gated, and a timer would be a second clock beside the one
 * the platform keeps. The enum value stays and routes here exactly like the others.
 */
class ContentLock(
    private val core: ContentLockSink,
    /**
     * THE PROCESS'S ONE LEDGER, and it is public because `dev.swarm.phone.keys.PerUseGate` must
     * be given THIS one rather than a second. A gate holding a ledger of its own would keep an
     * in-flight prompt marker across the screen lock that this class exists to react to -- and
     * `AuthorizationLedger.beginPrompt` refuses every later prompt while one is marked in
     * flight, so the gate would wedge shut for the life of the process, with no way to
     * authorize anything and nothing on screen explaining it.
     */
    val ledger: AuthorizationLedger = AuthorizationLedger(),
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
 * THE WAY BACK IN -- ADR-007 B44's hole, which was that there was none.
 *
 * B44 made a screen lock return the content tier to locked on every screen-off, and
 * `PhoneSurface.renderReady` asks `PhoneRuntime.unlockContent` on the way back with a comment
 * asserting that this "is the moment the Keystore-backed content KEK will answer". It is not,
 * in four states: after a device-credential unlock (mandatory post-reboot), after the biometric
 * idle timeout, after repeated failures, and always on a handset with no enrolled Class-3
 * biometric -- because the content KEK carries `AUTH_BIOMETRIC_STRONG` and nothing else
 * ([dev.swarm.phone.keys.KeystoreSpecs.kek]). On refusal the app offered nothing at all.
 *
 * SO THIS DECIDES WHEN TO OFFER A PROMPT, AND WHEN NOT TO. Both halves are the same defect
 * class. A refusal a prompt can fix, with no prompt offered, is a dead end. A prompt offered for
 * a refusal it cannot fix -- a destroyed key, an unsupported handset, a lost grant -- is a
 * button the user can press forever, which is PB-APP-10's failure LOOP reached through the
 * remedy.
 *
 * IT ROUTES ON THE REMEDY, NOT ON THE ERROR STATE. [dev.swarm.phone.ui.Remedy.AUTHENTICATE] is
 * the taxonomy's own name for "a fresh authentication fixes this", and it is decided once, in
 * `ErrorRouter`'s single table. Matching on states here would be a second copy of that decision,
 * and the two would drift on the first row anybody added.
 */
/**
 * Whether this handset can hold a content KEK, and if not, whether that is the user's to fix.
 *
 * The two refusals are kept APART for the reason the whole error taxonomy keeps things apart:
 * their remedies are opposites. [NEEDS_ENROLMENT] is an action the user performs in system
 * settings; [UNSUPPORTED] is a fact about the hardware that nothing they do changes. Collapsing
 * them either tells a fixable user to give up, or tells a stuck one to keep trying.
 */
enum class ContentProvisioning { PROCEED, NEEDS_ENROLMENT, UNSUPPORTED }

object ContentUnlockPolicy {

    /** @param error what `PhoneRuntime.unlockContent` answered; null means it did not refuse. */
    fun offersUnlock(error: RoutedError?): Boolean = error?.remedy == Remedy.AUTHENTICATE

    /**
     * Whether a prompt is worth putting on screen at all. A control that cannot prompt is a
     * control that does nothing, which is the same dead end one step further along.
     */
    fun canPrompt(availability: PromptAvailability): Boolean =
        availability == PromptAvailability.READY

    /**
     * What the user is told INSTEAD of a prompt. Every state has an answer, including READY --
     * where the answer is what the prompt is for, so a screen never has an empty string to show.
     *
     * The wording is shared with the per-use gate's table wherever the situation is the same
     * one, because it is: a handset with nothing enrolled cannot use the content tier and cannot
     * revoke, for one reason.
     */
    /**
     * Whether the content KEK can be PROVISIONED at all on this handset, which is a different
     * question from whether it can be unwrapped now -- and the one nothing was asking.
     *
     * THE HOLE THIS CLOSES. `KeystoreSpecs.kek(CONTENT)` requests
     * `setUserAuthenticationRequired(true)` with `AUTH_BIOMETRIC_STRONG`, and the platform refuses
     * to GENERATE such a key when no Class-3 biometric is enrolled -- `KeyGenerator.init` throws
     * `InvalidAlgorithmParameterException`. `DeviceCapabilities.probe` cannot see that: it answers
     * USER_AUTH_PER_USE from the API LEVEL alone, which is a fact about the platform and not about
     * this handset's enrolment. So a PIN-only user -- a large population, not an edge case -- got
     * an app that could not start, reported as `SwarmErrorTokens.UNKNOWN`: "something failed in a
     * way the app does not recognise", whose remedy is [Remedy.NONE].
     *
     * That is a gate whose exit is unbuilt, and it is the direct consequence of ADR-007 B59's
     * refusal of `AUTH_DEVICE_CREDENTIAL` -- so it is B59's to answer, not a separate concern.
     *
     * [NEEDS_ENROLMENT] routes to `KeyCustodyException.UserAuthenticationRequired`, whose remedy is
     * [Remedy.AUTHENTICATE] -- which is also what [offersUnlock] keys on, so the same control
     * appears, finds it cannot prompt, and shows [adviceFor]'s "add one in system settings". One
     * mechanism, reached by two roads.
     *
     * A TRANSIENT ANSWER PROCEEDS, deliberately. Generation checks ENROLMENT, not whether the
     * sensor is free this second, and an app that refuses to start because the sensor is busy is
     * residuals section 2.8 -- an app that will not start -- reached by a new door.
     */
    fun provisioningFor(availability: PromptAvailability): ContentProvisioning = when (availability) {
        PromptAvailability.READY,
        PromptAvailability.TEMPORARILY_UNAVAILABLE,
        PromptAvailability.SECURITY_UPDATE_REQUIRED,
        -> ContentProvisioning.PROCEED

        PromptAvailability.NONE_ENROLLED -> ContentProvisioning.NEEDS_ENROLMENT
        PromptAvailability.NO_HARDWARE -> ContentProvisioning.UNSUPPORTED
    }

    fun adviceFor(availability: PromptAvailability): String = when (availability) {
        PromptAvailability.READY ->
            "Unlock to restore your sessions on this phone."

        PromptAvailability.NONE_ENROLLED ->
            PerUseRefusalText.messageFor(PerUseRefusalReason.NO_BIOMETRIC_ENROLLED)

        PromptAvailability.NO_HARDWARE ->
            PerUseRefusalText.messageFor(PerUseRefusalReason.NO_BIOMETRIC_HARDWARE)

        PromptAvailability.TEMPORARILY_UNAVAILABLE ->
            PerUseRefusalText.messageFor(PerUseRefusalReason.BIOMETRIC_UNAVAILABLE)

        PromptAvailability.SECURITY_UPDATE_REQUIRED ->
            PerUseRefusalText.messageFor(PerUseRefusalReason.SECURITY_UPDATE_REQUIRED)
    }
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
