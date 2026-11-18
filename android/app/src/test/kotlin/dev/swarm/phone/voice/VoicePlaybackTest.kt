package dev.swarm.phone.voice

import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertThrows
import org.junit.Assert.assertTrue
import org.junit.Test

class VoicePlaybackTest {
    @Test fun stopCancelsCurrentAndLateProviderGenerationExactlyOnce() {
        val old = VoicePlaybackAttempt()
        var oldStops = 0
        old.stop()
        old.attach { oldStops++ }
        old.stop()
        assertFalse(old.isActive())
        assertEquals(1, oldStops)

        val next = VoicePlaybackAttempt()
        var nextStops = 0
        next.attach { nextStops++ }
        old.stop()
        assertTrue(next.isActive())
        assertEquals(0, nextStops)
        next.stop()
        next.stop()
        assertEquals(1, nextStops)
    }

    @Test fun renderedProgressUsesPlaybackHeadRatherThanQueuedSamples() {
        var queued = 0
        var played = 0
        val progress = mutableListOf<Int>()
        val complete = pumpVoicePreview(4800, { true }, { offset, size ->
            assertEquals(queued, offset)
            queued += size
            size
        }, { played }, {
            if (queued == 4800 && played == 0) assertTrue(progress.isEmpty())
            if (queued == 4800) played = minOf(4800, played + 2400)
        }, { count, total ->
            assertEquals(4800, total)
            progress += count
        })
        assertTrue(complete)
        assertEquals(listOf(2400, 4800), progress)
    }

    @Test fun stopOrRouteLossNeverReportsQueuedAudioAsComplete() {
        var active = true
        var reported = false
        assertFalse(pumpVoicePreview(6, { active }, { _, length -> length }, { 0 },
            { active = false }, { _, _ -> reported = true }))
        assertFalse(reported)
        assertArrayEquals(shortArrayOf(1, -32768), decodeVoicePreview("AQAAgA=="))
        assertThrows(IllegalArgumentException::class.java) { decodeVoicePreview("AQ==") }
        assertThrows(IllegalArgumentException::class.java) { decodeVoicePreview("x".repeat(MAX_VOICE_PREVIEW * 4 / 3 + 1)) }
    }
}
