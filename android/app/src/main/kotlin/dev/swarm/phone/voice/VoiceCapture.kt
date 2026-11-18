package dev.swarm.phone.voice

import android.annotation.SuppressLint
import android.media.AudioFormat
import android.media.AudioRecord
import android.media.MediaRecorder
import android.os.SystemClock
import java.util.concurrent.atomic.AtomicReference

internal const val VOICE_RATE = 24_000
internal const val MAX_VOICE_PCM = VOICE_RATE * 2 * 120

internal class VoiceCapture {
    private enum class Decision { RECORD, SEND, DISCARD }
    private val decision = AtomicReference(Decision.RECORD)

    fun send() { decision.compareAndSet(Decision.RECORD, Decision.SEND) }
    fun discard() { decision.set(Decision.DISCARD) }

    @SuppressLint("MissingPermission") // Activity checks RECORD_AUDIO; AudioRecord also enforces it.
    fun read(stillAllowed: () -> Boolean = { true }, onStarted: () -> Unit): ByteArray? {
        val channels = AudioFormat.CHANNEL_IN_MONO
        val format = AudioFormat.ENCODING_PCM_16BIT
        val minimum = AudioRecord.getMinBufferSize(VOICE_RATE, channels, format)
        require(minimum in 1..MAX_VOICE_PCM)
        val record = AudioRecord(MediaRecorder.AudioSource.VOICE_RECOGNITION, VOICE_RATE, channels, format, maxOf(minimum, 4096))
        val pcm = ByteArray(MAX_VOICE_PCM)
        val samples = ShortArray(1024)
        var count = 0
        try {
            check(record.state == AudioRecord.STATE_INITIALIZED && record.sampleRate == VOICE_RATE && record.channelCount == 1)
            if (decision.get() != Decision.RECORD || !stillAllowed()) return null
            record.startRecording()
            check(record.recordingState == AudioRecord.RECORDSTATE_RECORDING)
            onStarted()
            val deadline = SystemClock.elapsedRealtime() + 120_000
            var route: Int? = null
            while (decision.get() == Decision.RECORD && count < pcm.size && SystemClock.elapsedRealtime() < deadline) {
                if (!stillAllowed() || record.activeRecordingConfiguration?.isClientSilenced == true) return null
                val currentRoute = record.routedDevice?.id
                if (route != null && route != currentRoute) return null
                route = currentRoute
                val n = record.read(samples, 0, minOf(samples.size, (pcm.size - count) / 2), AudioRecord.READ_NON_BLOCKING)
                check(n >= 0)
                count = appendVoicePCM(samples, n, pcm, count)
                if (n == 0) Thread.sleep(15)
            }
            // Reaching a duration or sample limit stops capture without sending.
            return if (decision.get() == Decision.SEND && count > 0) pcm.copyOf(count) else null
        } finally {
            try {
                if (record.recordingState == AudioRecord.RECORDSTATE_RECORDING) record.stop()
            } finally {
                record.release()
                samples.fill(0)
                pcm.fill(0)
            }
        }
    }
}

internal fun appendVoicePCM(samples: ShortArray, size: Int, pcm: ByteArray, offset: Int): Int {
    require(size in 0..samples.size && offset >= 0 && offset % 2 == 0 && size <= (pcm.size - offset) / 2)
    var at = offset
    for (i in 0 until size) {
        val value = samples[i].toInt()
        pcm[at++] = value.toByte()
        pcm[at++] = (value shr 8).toByte()
    }
    return at
}
