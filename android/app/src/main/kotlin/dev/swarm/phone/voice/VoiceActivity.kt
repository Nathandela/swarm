package dev.swarm.phone.voice

import android.Manifest
import android.content.pm.PackageManager
import android.os.Bundle
import android.os.Handler
import android.os.Looper
import android.text.InputFilter
import android.widget.EditText
import android.widget.LinearLayout
import android.widget.ScrollView
import android.widget.TextView
import androidx.activity.result.contract.ActivityResultContracts
import androidx.appcompat.app.AlertDialog
import androidx.appcompat.app.AppCompatActivity
import dev.swarm.phone.scan.QrScanner
import dev.swarm.phone.ui.kit.CtaKind
import dev.swarm.phone.ui.kit.ctaButton
import dev.swarm.phone.ui.kit.navHeader
import dev.swarm.phone.ui.kit.notice
import dev.swarm.phone.ui.kit.screenAir
import dev.swarm.phone.ui.kit.sectionLabel
import dev.swarm.phone.ui.kit.textField
import org.json.JSONArray
import org.json.JSONObject
import swarmmobile.VoiceDevice
import java.util.Base64
import java.util.UUID
import java.util.concurrent.Executors
import java.util.concurrent.atomic.AtomicReference

/** Explicit, foreground voice surface. It never reads launch intents or dashboard state. */
class VoiceActivity : AppCompatActivity() {
    private val requests = Executors.newSingleThreadExecutor()
    private val transport = AtomicReference<VoiceDevice?>()
    private lateinit var store: VoiceStore
    private var state: VoiceState? = null
    private val micPoll = Handler(Looper.getMainLooper())
    private val micUI = object : Runnable {
        override fun run() {
            if (!active) return
            renderMicrophone()
            micPoll.postDelayed(this, 250)
        }
    }
    private var lastMicOutcome: String? = null
    private var importedInvitation: VoiceInvitation? = null
    private var scannerDialog: AlertDialog? = null
    @Volatile private var generation = 0
    @Volatile private var active = false
    private var initialized = false
    private var busy = false

    private lateinit var address: EditText
    private lateinit var user: EditText
    private lateinit var pin: EditText
    private lateinit var endpoint: EditText
    private lateinit var place: EditText
    private lateinit var reference: EditText
    private lateinit var invitation: EditText
    private lateinit var message: EditText
    private lateinit var operation: EditText
    private lateinit var output: TextView
    private lateinit var hint: TextView
    private lateinit var publicKeys: TextView
    private lateinit var start: TextView
    private lateinit var stop: TextView
    private lateinit var setup: TextView
    private val microphone = registerForActivityResult(ActivityResultContracts.RequestPermission()) {
        if (active) hint.text = if (it) "Microphone allowed. Connect the saved grant before recording." else "Microphone denied. You can still send text."
    }
    private val notices = registerForActivityResult(ActivityResultContracts.RequestPermission()) {
        if (active) hint.text = if (it) "Notification allowed. Connect the saved grant before recording." else "An ongoing notification is required. You can still send text."
    }
    private val camera = registerForActivityResult(ActivityResultContracts.RequestPermission()) {
        if (active) {
            if (it) showScanner() else hint.text = "Camera denied. Paste an invitation or enter it manually."
        }
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        store = VoiceStore(applicationContext)
        val stack = LinearLayout(this).apply { orientation = LinearLayout.VERTICAL }
        stack.addView(navHeader(this, "Voice manager", null))
        fun item(view: android.view.View) { stack.addView(view.screenAir()) }
        fun field(label: String, max: Int, mono: Boolean = false) = textField(this, label, mono).apply {
            filters = arrayOf(InputFilter.LengthFilter(max))
            isSaveEnabled = false
            importantForAutofill = android.view.View.IMPORTANT_FOR_AUTOFILL_NO
        }.also { item(it) }
        fun button(label: String, action: () -> Unit) = ctaButton(this, label, CtaKind.MORE, bloom = false).apply {
            filterTouchesWhenObscured = true
            setOnClickListener { if (active) action() }
        }.also { item(it) }

        item(sectionLabel(this, "SSH profile"))
        setup = button("Generate voice keys", ::generate)
        publicKeys = notice(this, "").apply { setTextIsSelectable(true) }
        item(publicKeys)
        address = field("SSH address and port", 512)
        user = field("SSH user", 256)
        pin = field("SSH host key SHA256 fingerprint", 128, mono = true)
        endpoint = field("Remote voice loopback and port", 128)
        item(sectionLabel(this, "Enrollment"))
        invitation = field("Paste invitation JSON", 2048)
        button("Use pasted invitation") { applyInvitation(invitation.text.toString()) }
        button("Scan enrollment QR") {
            if (checkSelfPermission(Manifest.permission.CAMERA) == PackageManager.PERMISSION_GRANTED) showScanner()
            else camera.launch(Manifest.permission.CAMERA)
        }
        place = field("Place ID", 128)
        reference = field("One-use reference", 64, mono = true)
        button("Enroll", ::enroll)
        button("Connect saved grant", ::connect)
        item(sectionLabel(this, "Request"))
        message = field("Message for the manager", 16_384)
        button("Send new message", ::sendText)
        start = button("Start recording", ::beginRecording)
        stop = button("Stop and send recording") { VoiceMicrophoneSession.current.get()?.let { VoiceMicrophoneSession.send(it.id) }; stop.isEnabled = false }
        stop.isEnabled = false
        operation = field("Request ID for receipt", 128, mono = true)
        button("Check receipt and updates", ::readReceipt)
        button("End voice connection") {
            VoiceMicrophoneSession.current.get()?.let { VoiceMicrophoneSession.discard(it.id) }
            endTransport()
            hint.text = "Voice connection ended. Check the request ID if finalization had started."
        }
        hint = notice(this, "Generate two keys to begin. Install the SSH public key on the selected computer.")
            .apply { accessibilityLiveRegion = android.view.View.ACCESSIBILITY_LIVE_REGION_POLITE }
        item(hint)
        output = notice(this, "").apply { setTextIsSelectable(true) }
        item(output)
        setContentView(ScrollView(this).apply { addView(stack) })
    }

    override fun onResume() {
        super.onResume()
        active = true
        micPoll.post(micUI)
        if (!initialized) work("Loading voice profile…", { store.load() }) { saved ->
            initialized = true
            if (saved != null) show(saved)
            hint.text = if (saved == null) "Generate voice keys to begin." else "Connect the saved grant or enroll a new invitation."
        }
    }

    override fun onPause() {
        active = false
        micPoll.removeCallbacks(micUI)
        endTransport()
        scannerDialog?.dismiss()
        super.onPause()
    }

    override fun onDestroy() {
        requests.shutdownNow()
        super.onDestroy()
    }

    private fun endTransport() {
        generation++
        busy = false
        stop.isEnabled = false
        start.isEnabled = true
        transport.getAndSet(null)?.let { client -> closeLane.execute { runCatching { client.end() } } }
    }

    private fun <T> work(label: String, task: () -> T, result: (T) -> Unit) {
        if (!active || busy) return
        busy = true
        val ticket = generation
        hint.text = label
        requests.execute {
            val reply = runCatching(task)
            runOnUiThread {
                if (!active || ticket != generation) return@runOnUiThread
                busy = false
                reply.fold(result, onFailure = {
                    hint.text = if (it is VoiceKeysUnavailable) {
                        "Voice keys are unavailable. This device must support Android Keystore Ed25519."
                    } else {
                        "Voice operation unavailable. Verify permission, SSH pin and host grant. Check the same request ID after an uncertain result."
                    }
                })
            }
        }
    }

    private fun show(saved: VoiceState) {
        state = saved
        setup.isEnabled = false
        publicKeys.text = "Install this SSH public key on the trusted computer:\n${saved.keys.sshPublic}\n\nEnrollment public key:\n${saved.keys.enrollmentPublic}"
        saved.profile?.let {
            address.setText(it.address); user.setText(it.user); pin.setText(it.pin); endpoint.setText(it.endpoint)
        }
        operation.setText(saved.operation)
    }

    private fun generate() {
        if (state != null) return
        work("Generating voice keys…", { store.load() ?: VoiceState(VoiceKeys.open(create = true)).also { store.save(it) } }) {
            show(it)
            hint.text = "Keys ready. Install the SSH public key and request an invitation for the enrollment public key."
        }
    }

    private fun applyInvitation(value: String) {
        runCatching { VoiceInvitation.parse(value) }.onSuccess {
            importedInvitation = it
            place.setText(it.place); reference.setText(it.reference); endpoint.setText(it.endpoint)
            invitation.text.clear()
            hint.text = "Invitation loaded. Confirm the SSH host fingerprint before enrolling."
        }.onFailure { hint.text = "Invalid invitation. Confirm the public enrollment JSON or enter it manually." }
    }

    private fun showScanner() {
        if (!active || scannerDialog != null || VoiceMicrophoneSession.current.get() != null) return
        val qr = QrScanner(this)
        val dialog = AlertDialog.Builder(this).setTitle("Scan voice enrollment").setView(qr.view)
            .setNegativeButton("Cancel", null).create()
        scannerDialog = dialog
        dialog.setOnDismissListener { qr.stop(); scannerDialog = null }
        dialog.show()
        qr.start(onPayload = { if (active) applyInvitation(it); dialog.dismiss() },
            onError = { hint.text = "Scanner unavailable. Paste or enter the invitation."; dialog.dismiss() })
    }

    private fun enroll() {
        val saved = state ?: return tell("Generate voice keys first.")
        if (VoiceMicrophoneSession.current.get() != null) return
        val profile = VoiceProfile(address.text.toString().trim(), user.text.toString().trim(), pin.text.toString().trim(), endpoint.text.toString().trim())
        val placeID = place.text.toString().trim()
        val invite = reference.text.toString().trim()
        val imported = importedInvitation?.takeIf { it.reference == invite }
        if (imported != null && (imported.place != placeID || imported.endpoint != profile.endpoint)) {
            return tell("Place and loopback must match the imported invitation.")
        }
        work("Enrolling over pinned SSH…", {
            val device = open(profile, saved.keys)
            val grant = device.enroll(placeID, invite, profile.endpoint)
            saved.copy(profile = profile, grant = grant, operation = "").also { store.save(it) }
        }) {
            show(it)
            reference.text.clear()
            importedInvitation = null
            hint.text = "Enrolled. You can send a text request or start a recording."
        }
    }

    private fun connect() {
        val saved = state ?: return tell("Generate voice keys first.")
        val profile = saved.profile ?: return tell("Enroll a voice invitation first.")
        if (VoiceMicrophoneSession.current.get() != null) return
        work("Connecting over pinned SSH…", {
            JSONObject(open(profile, saved.keys).status(saved.grant))
        }) { result ->
            hint.text = if (result.optBoolean("ready")) "Manager ready." else "Connected. Manager is not ready for native input yet."
        }
    }

    private fun open(profile: VoiceProfile, keys: VoiceKeys): VoiceDevice {
        val ticket = generation
        check(active)
        val device = profile.open(keys)
        transport.getAndSet(device)?.end()
        if (!active || ticket != generation) {
            transport.compareAndSet(device, null)
            device.end()
            error("voice surface left")
        }
        return device
    }

    private fun sendText() {
        val device = transport.get() ?: return tell("Connect the voice grant first.")
        val saved = state ?: return
        val text = message.text.toString()
        if (text.isBlank() || VoiceMicrophoneSession.current.get() != null || busy) return
        val next = saved.copy(operation = UUID.randomUUID().toString())
        state = next
        operation.setText(next.operation)
        work("Submitting text…", {
            store.save(next)
            JSONObject(device.submitText(next.grant, next.operation, text))
        }) {
            message.text.clear()
            showReceipt(it)
        }
    }

    private fun beginRecording() {
        val device = transport.get() ?: return tell("Connect the voice grant first.")
        val saved = state ?: return
        if (VoiceMicrophoneSession.current.get() != null || busy) return
        if (checkSelfPermission(Manifest.permission.RECORD_AUDIO) != PackageManager.PERMISSION_GRANTED) {
            microphone.launch(Manifest.permission.RECORD_AUDIO)
            return
        }
        if (checkSelfPermission(Manifest.permission.POST_NOTIFICATIONS) != PackageManager.PERMISSION_GRANTED) {
            notices.launch(Manifest.permission.POST_NOTIFICATIONS)
            return
        }
        if (!VoiceMicrophoneService.notificationsAllowed(this)) return tell("Enable the voice notification before recording.")
        val next = saved.copy(operation = UUID.randomUUID().toString())
        state = next
        operation.setText(next.operation)
        work("Reserving recording…", {
            store.save(next)
            require(JSONObject(device.beginPTT(next.grant, next.operation)).optString("phase") == "capturing")
        }) {
            if (!transport.compareAndSet(device, null)) return@work
            val request = VoiceMicrophoneRequest(next.operation, finalize = { pcm ->
                device.finalizePTT(next.grant, next.operation, Base64.getEncoder().encodeToString(pcm))
            }, end = { device.end() })
            if (!VoiceMicrophoneSession.arm(request)) {
                closeLane.execute { runCatching { device.end() } }
                return@work tell("A voice recording is already active.")
            }
            try {
                VoiceMicrophoneService.start(this, request)
                hint.text = "Preparing microphone…"
                renderMicrophone()
            } catch (_: Exception) {
                VoiceMicrophoneSession.finish(request, "Microphone could not start. Nothing was sent.")
                renderMicrophone()
            }
        }
    }

    private fun renderMicrophone() {
        val current = VoiceMicrophoneSession.current.get()
        val phase = current?.phase?.get()
        stop.isEnabled = phase == VoiceMicPhase.RECORDING
        start.isEnabled = current == null
        if (current != null) {
            if (operation.text.toString() != current.id) operation.setText(current.id)
            val line = when (phase) {
                VoiceMicPhase.RECORDING -> "Recording · Stop and send, or End to discard."
                VoiceMicPhase.STOPPING, VoiceMicPhase.FINALIZING -> "Finalizing recording…"
                VoiceMicPhase.CANCELED -> "Stopping voice recording…"
                else -> "Preparing microphone…"
            }
            if (hint.text.toString() != line) hint.text = line
        } else {
            val outcome = VoiceMicrophoneSession.lastOutcome
            if (outcome != null && outcome != lastMicOutcome) {
                lastMicOutcome = outcome
                hint.text = outcome
            }
        }
    }

    private fun readReceipt() {
        val device = transport.get() ?: return tell("Connect the voice grant first.")
        val saved = state ?: return
        val id = operation.text.toString().trim()
        if (id.isEmpty() || VoiceMicrophoneSession.current.get() != null) return
        work("Checking the exact request…", {
            val receipt = JSONObject(device.receipt(saved.grant, id))
            val publications = runCatching { JSONArray(device.publications(saved.grant, id)) }
            receipt to publications
        }) { (receipt, publications) ->
            showReceipt(receipt)
            output.text = publications.fold(
                onSuccess = { items -> (0 until items.length()).joinToString("\n\n") { items.getJSONObject(it).optString("text") } },
                onFailure = {
                    if (it.message?.contains("voice publication history exceeds handset capacity") == true) {
                        "This request has more publication history than the phone can show."
                    } else "Canonical updates unavailable."
                },
            )
        }
    }

    private fun showReceipt(receipt: JSONObject) {
        hint.text = "Request #${receipt.optLong("sequence")} · ${receipt.optString("phase", "accepted").replace('_', ' ')}"
    }
    private fun tell(line: String) { hint.text = line }

    companion object { private val closeLane = Executors.newSingleThreadExecutor() }
}
