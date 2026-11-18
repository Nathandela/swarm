package dev.swarm.phone.voice

import java.util.concurrent.CountDownLatch
import java.util.concurrent.TimeUnit
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

class VoiceMicrophoneSessionTest {
    @Test fun cancellationBeforeServiceStartDisarmsWithoutFinalizing() {
        val ended = CountDownLatch(1)
        val request = VoiceMicrophoneRequest("owner", finalize = { error("must not send") }, end = { ended.countDown() })
        assertTrue(VoiceMicrophoneSession.arm(request))
        try {
            VoiceMicrophoneSession.send("owner")
            assertEquals(VoiceMicPhase.ARMED, request.phase.get())
            VoiceMicrophoneSession.discard("old-owner")
            assertEquals(VoiceMicPhase.ARMED, request.phase.get())
            VoiceMicrophoneSession.discard("owner")
            assertEquals(VoiceMicPhase.CANCELED, request.phase.get())
            assertNull(VoiceMicrophoneSession.current.get())
            assertTrue(ended.await(2, TimeUnit.SECONDS))
        } finally {
            VoiceMicrophoneSession.finish(request, "")
        }
    }

    @Test fun exactStopAndDiscardCannotControlAnotherRequest() {
        val first = VoiceMicrophoneRequest("old", finalize = { error("must not send") }, end = {})
        val second = VoiceMicrophoneRequest("current", finalize = { error("must not send") }, end = {})
        assertTrue(VoiceMicrophoneSession.arm(first))
        assertFalse(VoiceMicrophoneSession.arm(second))
        VoiceMicrophoneSession.finish(first, "")
        assertTrue(VoiceMicrophoneSession.arm(second))
        try {
            second.phase.set(VoiceMicPhase.RECORDING)
            VoiceMicrophoneSession.send("old")
            VoiceMicrophoneSession.discard("old")
            assertEquals(VoiceMicPhase.RECORDING, second.phase.get())
            VoiceMicrophoneSession.send("current")
            assertEquals(VoiceMicPhase.STOPPING, second.phase.get())
            VoiceMicrophoneSession.discard("current")
            assertEquals(VoiceMicPhase.CANCELED, second.phase.get())
        } finally {
            VoiceMicrophoneSession.finish(second, "")
        }
    }
}
