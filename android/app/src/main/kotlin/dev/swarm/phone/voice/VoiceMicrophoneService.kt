package dev.swarm.phone.voice

import android.Manifest
import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.app.Service
import android.content.Context
import android.content.Intent
import android.content.pm.PackageManager
import android.content.pm.ServiceInfo
import android.media.AudioAttributes
import android.media.AudioFocusRequest
import android.media.AudioManager
import android.os.IBinder
import android.net.Uri
import org.json.JSONObject
import java.util.concurrent.Executors
import java.util.concurrent.atomic.AtomicReference

internal class VoiceMicrophoneRequest(
    val id: String,
    val finalize: (ByteArray) -> String,
    val end: () -> Unit,
) {
    val capture = VoiceCapture()
    val phase = AtomicReference(VoiceMicPhase.ARMED)
}

internal enum class VoiceMicPhase { ARMED, RECORDING, STOPPING, FINALIZING, CANCELED }

internal object VoiceMicrophoneSession {
    val current = AtomicReference<VoiceMicrophoneRequest?>()
    @Volatile var lastOutcome: String? = null
    private val closer = Executors.newSingleThreadExecutor()

    @Synchronized fun arm(request: VoiceMicrophoneRequest): Boolean {
        if (!current.compareAndSet(null, request)) return false
        lastOutcome = null
        return true
    }

    fun send(id: String) {
        val request = current.get()?.takeIf { it.id == id } ?: return
        if (request.phase.compareAndSet(VoiceMicPhase.RECORDING, VoiceMicPhase.STOPPING)) request.capture.send()
    }

    fun discard(id: String) {
        val request = current.get()?.takeIf { it.id == id } ?: return
        val previous = request.phase.getAndSet(VoiceMicPhase.CANCELED)
        request.capture.discard()
        if (previous == VoiceMicPhase.ARMED) finish(request, "Recording discarded. Nothing was sent.")
        else close(request)
    }

    @Synchronized fun finish(request: VoiceMicrophoneRequest, outcome: String) {
        if (current.compareAndSet(request, null)) lastOutcome = outcome
        close(request)
    }

    private fun close(request: VoiceMicrophoneRequest) {
        closer.execute { runCatching { request.end() } }
    }
}

/** Only the visible Activity can arm an exact user-started microphone request. */
class VoiceMicrophoneService : Service() {
    private val worker = Executors.newSingleThreadExecutor()
    private var owned: VoiceMicrophoneRequest? = null
    private val focus = AtomicReference<AudioFocusRequest?>()
    private lateinit var audio: AudioManager

    override fun onBind(intent: Intent?): IBinder? = null

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        val id = intent?.getStringExtra(EXTRA_ID) ?: run { stopSelf(startId); return START_NOT_STICKY }
        if (intent.action == SEND) {
            VoiceMicrophoneSession.send(id)
            if (owned == null) stopSelf(startId)
            return START_NOT_STICKY
        }
        if (intent.action == DISCARD) {
            VoiceMicrophoneSession.discard(id)
            if (owned == null) stopSelf(startId)
            return START_NOT_STICKY
        }
        val request = VoiceMicrophoneSession.current.get()?.takeIf { it.id == id && it.phase.get() == VoiceMicPhase.ARMED }
        if (intent.action != START || request == null || owned != null) {
            if (owned == null) stopSelf(startId)
            return START_NOT_STICKY
        }
        owned = request
        try {
            check(canRecord())
            createChannel(this)
            startForeground(NOTIFICATION_ID, notification(request, "Preparing microphone…"), ServiceInfo.FOREGROUND_SERVICE_TYPE_MICROPHONE)
            audio = getSystemService(AudioManager::class.java)
            val requested = AudioFocusRequest.Builder(AudioManager.AUDIOFOCUS_GAIN_TRANSIENT_EXCLUSIVE)
                .setAudioAttributes(AudioAttributes.Builder().setUsage(AudioAttributes.USAGE_VOICE_COMMUNICATION)
                    .setContentType(AudioAttributes.CONTENT_TYPE_SPEECH).build())
                .setOnAudioFocusChangeListener { change ->
                    if (change < 0 && request.phase.get() != VoiceMicPhase.FINALIZING) VoiceMicrophoneSession.discard(request.id)
                }.build()
            focus.set(requested)
            check(audio.requestAudioFocus(requested) == AudioManager.AUDIOFOCUS_REQUEST_GRANTED)
            worker.execute { record(request) }
        } catch (_: Exception) {
            VoiceMicrophoneSession.finish(request, "Microphone could not start. Nothing was sent.")
            stopSelf()
        }
        return START_NOT_STICKY
    }

    private fun record(request: VoiceMicrophoneRequest) {
        var outcome = "Recording ended without sending audio."
        try {
            val pcm = request.capture.read(
                stillAllowed = { canRecord() && request.phase.get() != VoiceMicPhase.CANCELED },
                onStarted = {
                    if (request.phase.compareAndSet(VoiceMicPhase.ARMED, VoiceMicPhase.RECORDING)) {
                        getSystemService(NotificationManager::class.java)
                            .notify(NOTIFICATION_ID, notification(request, "Recording · Stop and send, or discard"))
                    }
                },
            )
            if (pcm != null) {
                try {
                    if (request.phase.compareAndSet(VoiceMicPhase.STOPPING, VoiceMicPhase.FINALIZING)) {
                        releaseFocus()
                        outcome = "Recording outcome uncertain. Check its request ID."
                        getSystemService(NotificationManager::class.java)
                            .notify(NOTIFICATION_ID, notification(request, "Finalizing recording…"))
                        val receipt = JSONObject(request.finalize(pcm))
                        outcome = "Request #${receipt.optLong("sequence")} · ${receipt.optString("phase", "accepted")}"
                    }
                } finally { pcm.fill(0) }
            }
        } catch (_: Exception) {
            if (request.phase.get() == VoiceMicPhase.FINALIZING) {
                outcome = "Recording outcome uncertain. Check its request ID."
            }
        } finally {
            VoiceMicrophoneSession.finish(request, outcome)
            stopSelf()
        }
    }

    private fun canRecord(): Boolean =
        checkSelfPermission(Manifest.permission.RECORD_AUDIO) == PackageManager.PERMISSION_GRANTED &&
            notificationsAllowed(this)

    private fun notification(request: VoiceMicrophoneRequest, detail: String): Notification {
        val builder = Notification.Builder(this, CHANNEL).setSmallIcon(android.R.drawable.ic_btn_speak_now)
            .setContentTitle("Swarm voice").setContentText(detail).setOnlyAlertOnce(true).setOngoing(true)
            .setContentIntent(PendingIntent.getActivity(this, 0, Intent(this, VoiceActivity::class.java),
                PendingIntent.FLAG_IMMUTABLE))
        fun action(label: String, verb: String, code: Int) {
            val command = Intent(this, VoiceMicrophoneService::class.java).setAction(verb)
                .setData(Uri.Builder().scheme("swarm-voice").authority("microphone").appendPath(request.id).build())
                .putExtra(EXTRA_ID, request.id)
            builder.addAction(Notification.Action.Builder(null, label,
                PendingIntent.getService(this, code, command, PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT)).build())
        }
        if (request.phase.get() == VoiceMicPhase.RECORDING) action("Stop and send", SEND, 1)
        action("Discard", DISCARD, 2)
        return builder.build()
    }

    override fun onTaskRemoved(rootIntent: Intent?) {
        owned?.let { VoiceMicrophoneSession.discard(it.id) }
        super.onTaskRemoved(rootIntent)
    }

    override fun onDestroy() {
        owned?.takeIf { VoiceMicrophoneSession.current.get() === it }?.let {
            VoiceMicrophoneSession.discard(it.id)
            VoiceMicrophoneSession.finish(
                it, "Voice capture stopped. Check the request ID if it was already being finalized.",
            )
        }
        releaseFocus()
        worker.shutdownNow()
        stopForeground(STOP_FOREGROUND_REMOVE)
        super.onDestroy()
    }

    private fun releaseFocus() { focus.getAndSet(null)?.let { audio.abandonAudioFocusRequest(it) } }

    companion object {
        private const val CHANNEL = "swarm_voice_microphone"
        private const val NOTIFICATION_ID = 742
        private const val EXTRA_ID = "dev.swarm.phone.voice.REQUEST"
        private const val START = "dev.swarm.phone.voice.START"
        private const val SEND = "dev.swarm.phone.voice.SEND"
        private const val DISCARD = "dev.swarm.phone.voice.DISCARD"

        internal fun start(context: Context, request: VoiceMicrophoneRequest) {
            val intent = Intent(context, VoiceMicrophoneService::class.java).setAction(START).putExtra(EXTRA_ID, request.id)
            context.startForegroundService(intent)
        }

        internal fun notificationsAllowed(context: Context): Boolean {
            val manager = context.getSystemService(NotificationManager::class.java)
            return context.checkSelfPermission(Manifest.permission.POST_NOTIFICATIONS) == PackageManager.PERMISSION_GRANTED &&
                manager.areNotificationsEnabled() &&
                manager.getNotificationChannel(CHANNEL)?.importance != NotificationManager.IMPORTANCE_NONE
        }

        private fun createChannel(context: Context) {
            context.getSystemService(NotificationManager::class.java).createNotificationChannel(
                NotificationChannel(CHANNEL, "Voice microphone", NotificationManager.IMPORTANCE_LOW),
            )
        }
    }
}
