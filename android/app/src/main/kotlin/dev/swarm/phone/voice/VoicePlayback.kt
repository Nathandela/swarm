package dev.swarm.phone.voice

import android.content.Context
import android.media.AudioAttributes
import android.media.AudioFocusRequest
import android.media.AudioFormat
import android.media.AudioManager
import android.media.AudioTrack
import android.os.SystemClock
import java.nio.ByteBuffer
import java.nio.ByteOrder
import java.util.Base64
import java.util.concurrent.atomic.AtomicBoolean

internal const val MAX_VOICE_PREVIEW = VOICE_RATE * 2 * 6

internal class VoicePlaybackAttempt {
    private val stopped = AtomicBoolean()
    private var cancellation: (() -> Unit)? = null

    fun isActive(): Boolean = !stopped.get()

    @Synchronized fun attach(cancel: () -> Unit) {
        if (stopped.get()) cancel() else cancellation = cancel
    }

    fun stop() {
        val cancel = synchronized(this) {
            stopped.set(true)
            cancellation.also { cancellation = null }
        }
        cancel?.invoke()
    }
}

internal fun decodeVoicePreview(encoded: String): ShortArray {
    require(encoded.length in 1..(MAX_VOICE_PREVIEW * 4 / 3))
    val pcm = Base64.getDecoder().decode(encoded)
    try {
        require(pcm.size in 2..MAX_VOICE_PREVIEW && pcm.size % 2 == 0)
        return ShortArray(pcm.size / 2).also {
            ByteBuffer.wrap(pcm).order(ByteOrder.LITTLE_ENDIAN).asShortBuffer().get(it)
        }
    } finally { pcm.fill(0) }
}

internal fun playVoicePreview(
    context: Context, clip: ShortArray, attempt: VoicePlaybackAttempt, progress: (Int, Int) -> Unit,
): Boolean {
    if (!attempt.isActive()) return false
    val manager = context.getSystemService(AudioManager::class.java)
    val attributes = AudioAttributes.Builder().setUsage(AudioAttributes.USAGE_MEDIA)
        .setContentType(AudioAttributes.CONTENT_TYPE_SPEECH).build()
    val focus = AudioFocusRequest.Builder(AudioManager.AUDIOFOCUS_GAIN_TRANSIENT)
        .setAudioAttributes(attributes)
        .setOnAudioFocusChangeListener { change -> if (change < 0) attempt.stop() }
        .build()
    check(manager.requestAudioFocus(focus) == AudioManager.AUDIOFOCUS_REQUEST_GRANTED)
    try {
        if (!attempt.isActive()) return false
        val minimum = AudioTrack.getMinBufferSize(VOICE_RATE, AudioFormat.CHANNEL_OUT_MONO, AudioFormat.ENCODING_PCM_16BIT)
        check(minimum in 1..MAX_VOICE_PREVIEW)
        val track = AudioTrack.Builder().setAudioAttributes(attributes)
            .setAudioFormat(AudioFormat.Builder().setSampleRate(VOICE_RATE)
                .setChannelMask(AudioFormat.CHANNEL_OUT_MONO).setEncoding(AudioFormat.ENCODING_PCM_16BIT).build())
            .setTransferMode(AudioTrack.MODE_STREAM).setBufferSizeInBytes(maxOf(minimum, 4096)).build()
        try {
            check(track.state == AudioTrack.STATE_INITIALIZED && track.sampleRate == VOICE_RATE)
            if (!attempt.isActive()) return false
            track.setStartThresholdInFrames(1)
            track.play()
            val deadline = SystemClock.elapsedRealtime() + 9_000
            var route: Int? = null
            return pumpVoicePreview(
                clip.size,
                active = {
                    val now = track.routedDevice?.id
                    val routeChanged = route != null && route != now
                    route = now
                    attempt.isActive() && !routeChanged && track.playState == AudioTrack.PLAYSTATE_PLAYING
                },
                write = { offset, size -> track.write(clip, offset, size, AudioTrack.WRITE_NON_BLOCKING) },
                head = { track.playbackHeadPosition },
                tick = { check(SystemClock.elapsedRealtime() < deadline); Thread.sleep(10) },
                progress = progress,
            )
        } finally {
            try {
                if (track.playState == AudioTrack.PLAYSTATE_PLAYING) track.pause()
                track.flush()
            } finally { track.release() }
        }
    } finally { manager.abandonAudioFocusRequest(focus) }
}

internal fun pumpVoicePreview(
    frames: Int, active: () -> Boolean, write: (Int, Int) -> Int, head: () -> Int,
    tick: () -> Unit, progress: (Int, Int) -> Unit,
): Boolean {
    require(frames in 1..MAX_VOICE_PREVIEW / 2)
    var queued = 0
    var reported = 0
    while (active()) {
        if (queued < frames) {
            val length = minOf(frames - queued, 2048)
            val written = write(queued, length)
            check(written in 0..length)
            queued += written
        }
        val played = head().coerceIn(0, queued)
        if (played == frames || played >= reported + VOICE_RATE / 10) {
            reported = played
            progress(played, frames)
        }
        if (played == frames) return active()
        tick()
    }
    return false
}
