package dev.swarm.phone.scan

import android.os.Looper
import androidx.test.core.app.ActivityScenario
import dev.swarm.phone.PhoneActivity
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.Shadows.shadowOf

/**
 * FAILING-FIRST (TDD RED, GG-5) for agents-tracker-nz9h.
 *
 * ROBOLECTRIC'S OWN ENVIRONMENT HAS NO CAMERA, which is exactly the field report's condition and
 * not a simulation of it: `ProcessCameraProvider.bindToLifecycle(activity,
 * CameraSelector.DEFAULT_BACK_CAMERA, ...)` on this JVM throws the SAME
 * `IllegalArgumentException("No available camera can be found")` a camera-less handset throws,
 * from the same unguarded `future.addListener` callback `QrScanner.start` posts to the main
 * executor (QrScanner.kt:190-195). Before this fix nothing there caught it, so it reached the
 * main thread uncaught -- a crash on a real handset, and a Robolectric test failure here.
 *
 * WHAT THIS DOES NOT CLAIM. This is not evidence about a physical camera (PB-E2E-5 stays
 * deferred) -- it is evidence that a bind CameraX itself refuses is routed to the caller
 * instead of crashing the process, which is the one behaviour this fix changes.
 */
@RunWith(RobolectricTestRunner::class)
class QrScannerCameraFailureTest {

    @Test
    fun `a camera that will not bind reaches onError instead of crashing`() {
        ActivityScenario.launch(PhoneActivity::class.java).use { scenario ->
            scenario.onActivity { activity ->
                val scanner = QrScanner(activity)
                var failure: Exception? = null

                scanner.start(onPayload = {}, onError = { failure = it })

                // ProcessCameraProvider's own initialisation hands off to a background executor
                // Robolectric's scheduler does not drive, so the listener that reports the
                // failure lands on the main looper on ITS OWN schedule -- poll rather than a
                // single idle().
                val deadline = System.currentTimeMillis() + 10_000
                while (failure == null && System.currentTimeMillis() < deadline) {
                    shadowOf(Looper.getMainLooper()).idle()
                    Thread.sleep(20)
                }

                assertTrue(
                    "a camera-less handset's bindToLifecycle failure never reached onError, so " +
                        "it either crashed the main thread or was silently swallowed. Got: $failure",
                    failure is IllegalArgumentException,
                )
            }
        }
    }
}
