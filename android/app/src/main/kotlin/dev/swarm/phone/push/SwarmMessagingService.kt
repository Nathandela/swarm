package dev.swarm.phone.push

import android.app.NotificationManager
import com.google.firebase.messaging.FirebaseMessagingService
import com.google.firebase.messaging.RemoteMessage
import dev.swarm.phone.PhoneStartup
import dev.swarm.phone.SwarmApplication

/**
 * Phase B slice S17 -- the OS entry point for push, and the only way an FCM message reaches
 * this app.
 *
 * The relay's sender emits a DATA-ONLY message (internal/remote/push/fcm.go). That is not an
 * implementation detail: a `notification` block would be rendered by the SYSTEM, on the lock
 * screen, from text the PROVIDER composed, which is precisely the rendering PB-PUSH-4 puts
 * under this app's control. Android does not render a data-only message itself -- it hands it
 * to this service, and without the service the phone receives every wake and acts on none.
 *
 * IT DECIDES NOTHING. Kotlin does no base64, no parsing and no crypto: the payload crosses to
 * the Go core unchanged, because only the core holds the epoch wake key that says the wake is
 * genuine and the persisted replay coordinate (PB-PUSH-3) that says it is new. A notification
 * raised without that call is raised on the say-so of the push provider and the relay, neither
 * of which holds a wake key and neither of which is therefore distinguishable from an attacker
 * -- and the relay handles every wake and can re-deliver one, which would be a button that puts
 * a notification on the owner's lock screen whenever it likes.
 *
 * AND IT FETCHES NOTHING. No content verb is reachable from [onMessageReceived], which is
 * PB-PUSH-4's real requirement rather than a property of the string it renders: the wake
 * arrives with no user present, so "renders a content-free notification unless the user has
 * authenticated" is satisfied by an app that reads the roster, is refused, and renders the
 * generic line anyway -- and that app decrypts session content the moment it runs on a handset
 * the user unlocked five minutes ago.
 */
class SwarmMessagingService : FirebaseMessagingService() {

    /**
     * FCM rotated this install's token. It does not ask, and it fires on reinstall, on app data
     * restore and on a token TTL expiry -- any of them while the app is backgrounded and
     * therefore, under ADR-007 B16, disconnected. A phone that does not notice is silently
     * unreachable by push forever, with nothing on either side reporting it.
     */
    override fun onNewToken(token: String) {
        PushTokens.register(this, token)
    }

    /**
     * One wake, as the provider delivered it.
     *
     * "e" is the ONE key internal/remote/push/fcm.go marshalMessage puts in the data block, and
     * it is spelled here rather than hidden behind a constant because a rename on either side is
     * a phone that receives every wake and opens none -- a failure neither side's own tests can
     * see. Every additional key would be provider-visible metadata, and PB-PUSH-3 concedes the
     * provider the token, the timing and the size only.
     */
    override fun onMessageReceived(message: RemoteMessage) {
        val payload = message.data["e"] ?: return
        renderWake(payload)
    }

    /**
     * Authenticate, then render exactly what the core allows.
     *
     * A REFUSED WAKE RENDERS NOTHING, which is why the failure path returns rather than falling
     * through to a generic notification: an unparseable payload, a forgery, a replay and a wake
     * past its TTL are all cases where the only party that could have told this app to notify
     * the user did not.
     *
     * A phone that cannot be built renders nothing either. There is no user present and no
     * screen to report to, and the state is transient by construction -- the commonest cause is
     * a custody refusal that the next authentication clears.
     */
    private fun renderWake(payload: String) {
        val startup = (application as SwarmApplication).phoneRuntime.phone()
        if (startup !is PhoneStartup.Ready) return
        val alert = try {
            startup.app.handlePushWake(payload)
        } catch (refused: Exception) {
            return
        }
        WakeNotifications.ensureChannel(this)
        getSystemService(NotificationManager::class.java).notify(
            WakeNotifications.NOTIFICATION_ID,
            WakeNotifications.build(this, alert.getText(), alert.getContentReady()),
        )
    }
}
