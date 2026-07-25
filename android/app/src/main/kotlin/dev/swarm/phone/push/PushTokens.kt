package dev.swarm.phone.push

import android.content.Context
import android.util.Log
import com.google.firebase.messaging.FirebaseMessaging
import dev.swarm.phone.PhoneStartup
import dev.swarm.phone.SwarmApplication
import swarmmobile.App

/**
 * Phase B slice S17 -- PB-PUSH-9's CLIENT half: the FCM token lifecycle.
 *
 * The requirement enumerates it: "initial getToken, onNewToken rotation, re-registration on
 * every authenticated reconnect, deletion on revoke/disable, and correct behavior across
 * process death and app upgrade." Three of those five are the Go core's and are already there
 * -- the token is durable and wake-tiered (PB-STATE-9), so it survives process death and app
 * upgrade, and `mobile/relay.go` onConnected reconciles the relay to durable state on every
 * authenticated reconnect. What is HERE is the two ends that are Android's: asking Firebase for
 * a token, and handing a rotated one on.
 *
 * PB-PUSH-9 CARRIES ITS OWN WARNING -- "a façade method can exist while no Android code ever
 * calls it" -- and it is this project's standing defect class written into a requirement. This
 * object is the caller side of it: [requestInitialToken] runs from `SwarmApplication.onCreate`
 * and [register] from `SwarmMessagingService.onNewToken`, which are the only two moments
 * Android hands this app a token.
 *
 * WHY BOTH ENDS SHARE ONE [register]. The initial token and a rotated one are the same fact
 * arriving by two routes, and two copies of "hand it to the phone" is two places for the
 * persist-first ordering to be got wrong. The Go verb persists BEFORE it speaks to the relay
 * precisely because a rotation usually arrives with no connection -- FCM rotates on reinstall,
 * on data restore, on TTL expiry, and the app is disconnected whenever it is backgrounded
 * (ADR-007 B16) -- so nothing here needs to queue, retry or remember anything.
 */
object PushTokens {
    private const val TAG = "SwarmPush"


    /**
     * Ask Firebase for this install's token and register it.
     *
     * IT IS SEPARATE FROM onNewToken AND BOTH ARE NEEDED. onNewToken fires only on ROTATION, so
     * an app that implements the callback alone never registers on a fresh install -- and a
     * fresh install is exactly a phone that has just been paired and is about to be
     * backgrounded, which is the state push exists for.
     *
     * The token arrives in a listener, so the phone is built off the `onCreate` thread. That is
     * deliberate: `PhoneRuntime.phone()` reaches Keystore, the filesystem and the native
     * library, and doing that inside `Application.onCreate` is what turns a locked handset into
     * a process that dies before any screen exists to say why.
     */
    fun requestInitialToken(context: Context) {
        // GUARDED, and this is not defensive padding. FirebaseMessaging.getInstance() throws
        // IllegalStateException when no default FirebaseApp exists -- which is the DELIBERATE
        // state of this module: build.gradle.kts does not apply the google-services plugin and
        // says so, because there is no Firebase project here (PB-E2E-5, deferred).
        //
        // Unguarded, this call sits in Application.onCreate, so the throw is an uncaught
        // exception on the main thread at launch: the app dies before any screen exists, on
        // every start, for want of a push transport it can work perfectly well without. That is
        // exactly what PB-PUSH-5 forbids -- "missing/invalid credentials degrade gracefully and
        // loudly; the system works without push".
        //
        // Loudly, not silently: the failure is logged. Push is the only thing that stops
        // working, and the phone is fully usable while the user is in it.
        //
        // IllegalStateException and not Throwable. The catch is scoped to the ONE failure that
        // is a deliberate property of this build, so a genuine fault -- a Firebase library that
        // is broken rather than unconfigured -- still surfaces instead of being logged as "no
        // project configured" and explained away. Broadening it back is how the guard stops
        // being a guard.
        try {
            FirebaseMessaging.getInstance().token
                .addOnSuccessListener { token -> register(context, token) }
                // THE OTHER HALF OF "LOUDLY". getInstance() succeeding only means a FirebaseApp
                // exists; the fetch itself still fails -- no network at launch, a revoked
                // project, an unavailable Play Services -- and it fails ASYNCHRONOUSLY, so the
                // catch below cannot see it. Without this arm a CONFIGURED build loses its
                // token silently and the phone is simply unreachable by push until the next
                // launch retries. It heals, but PB-PUSH-5 asks for graceful AND loud.
                .addOnFailureListener { e ->
                    Log.w(TAG, "push token fetch failed; this launch registered no token and the " +
                        "phone will not receive background wakes until the next one", e)
                }
        } catch (e: IllegalStateException) {
            Log.w(TAG, "push unavailable: no Firebase project is configured for this build; " +
                "the phone works without push and will not receive background wakes", e)
        }
    }

    /**
     * Hand one provider token to the phone core.
     *
     * A phone that cannot be built is not an error to report anywhere: there is no user present
     * on this path and no screen to report to. The token is not lost either -- Firebase hands
     * the same one back at the next [requestInitialToken], and a rotation that was missed is
     * followed by another onNewToken.
     */
    fun register(context: Context, token: String) {
        val app = phoneOf(context) ?: return
        try {
            app.registerPushToken(token)
        } catch (refused: Exception) {
            // The durable half of the verb runs BEFORE anything that can refuse, so what is
            // caught here is the relay leg -- which is owed to the reconnect path anyway. It is
            // caught rather than allowed to propagate because both callers are background
            // callbacks with no user present: an exception out of onNewToken, or out of a
            // Firebase listener, takes the process down on an event the user did not cause.
            return
        }
    }

    /**
     * PB-PUSH-9's "deletion on revoke/disable", for DISABLE: the user turns notifications off.
     *
     * REVOKE does not come through here. `App.RevokeThisDevice` deletes this device's token
     * itself, in Go, because a phone that relies on the relay to clean up has no deletion on
     * revoke at all when the revoke is issued while the relay is unreachable -- and it goes on
     * holding a token in durable state that the next connection re-registers.
     *
     * The Go verb clears durable state BEFORE it tries the relay, and durable state is what
     * onConnected reconciles the relay to, so a deletion issued while backgrounded -- the normal
     * state under ADR-007 B16 -- is delivered on the next authenticated reconnect rather than
     * lost. Nothing here has to retry it.
     */
    fun disable(context: Context) {
        val app = phoneOf(context) ?: return
        app.deletePushToken()
    }

    // NOTE, recorded here rather than left to be discovered: [disable] has no caller yet. The
    // settings screen is where it belongs -- PB-APP-7's switches are the "disable" half of
    // "deletion on revoke/disable" -- and dev.swarm.phone.ui.SettingsScreen is a pure model with
    // no Activity to host it, because S16 shipped the screen MODELS and this module still
    // declares no Activity at all. Revoke does NOT depend on this: App.RevokeThisDevice deletes
    // the token in Go.

    /** The process-wide phone, or null while it cannot be built (see `PhoneStartup`). */
    private fun phoneOf(context: Context): App? {
        val application = context.applicationContext as? SwarmApplication ?: return null
        return (application.phoneRuntime.phone() as? PhoneStartup.Ready)?.app
    }
}
