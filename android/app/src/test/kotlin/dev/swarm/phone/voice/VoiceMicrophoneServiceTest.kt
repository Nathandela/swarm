package dev.swarm.phone.voice

import android.app.Application
import android.content.Intent
import android.app.Service
import java.util.concurrent.CountDownLatch
import java.util.concurrent.TimeUnit
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.Robolectric
import org.robolectric.RobolectricTestRunner
import org.robolectric.Shadows.shadowOf
import org.robolectric.annotation.Config

@RunWith(RobolectricTestRunner::class)
@Config(application = Application::class)
class VoiceMicrophoneServiceTest {
    @Test fun serviceRestartCannotRestoreMicrophoneOwnership() {
        val service = Robolectric.buildService(VoiceMicrophoneService::class.java).create().get()
        try {
            assertEquals(Service.START_NOT_STICKY, service.onStartCommand(null, 0, 1))
            assertTrue(shadowOf(service).isStoppedBySelf)
        } finally { service.onDestroy() }
    }

    @Test fun serviceRequiresPermissionEvenForArmedRequest() {
        val ended = CountDownLatch(1)
        val request = VoiceMicrophoneRequest("voice-test", finalize = { error("must not finalize") }, end = { ended.countDown() })
        val service = Robolectric.buildService(VoiceMicrophoneService::class.java).create().get()
        assertTrue(VoiceMicrophoneSession.arm(request))
        try {
            val intent = Intent(service, VoiceMicrophoneService::class.java).setAction("dev.swarm.phone.voice.START")
                .putExtra("dev.swarm.phone.voice.REQUEST", request.id)
            assertEquals(Service.START_NOT_STICKY, service.onStartCommand(intent, 0, 2))
            assertTrue(shadowOf(service).isStoppedBySelf)
            assertNull(VoiceMicrophoneSession.current.get())
            assertTrue(ended.await(2, TimeUnit.SECONDS))
        } finally {
            service.onDestroy()
            VoiceMicrophoneSession.finish(request, "")
        }
    }
}
