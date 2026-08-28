package dev.swarm.phone.ui

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotEquals
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * FAILING-FIRST (TDD RED, GG-5) for agents-tracker-ksvb.5 (d) -- a camera that will not start,
 * said in words, on the screen every user of this product meets first.
 *
 * **THE DEFECT.** `PairingSurface.scanFailed` handed a CameraX bind failure to
 * `ErrorRouter.route(failure.message.orEmpty())`. That router keys on the class token the Go
 * facade stamps, and a `java.lang.IllegalArgumentException` from `CameraSelector.select` carries
 * none -- so it landed on the RESERVED `UNKNOWN` row: "Something failed in a way the app does not
 * recognise. Try again, and report it if it keeps happening." Three things are wrong with that on
 * this screen. The app recognises it perfectly well -- `QrScanner.failToStart` catches it by name
 * and says so in its own KDoc. "Try again" is the one act that cannot work, because the camera is
 * held by another app or absent. And the row is reserved for messages the facade did NOT produce,
 * so spending it here makes the exhaustiveness sweep's one honest answer a bucket.
 *
 * **WHY THE COPY LIVES IN [PairingFlow] AND NOT IN THE SURFACE.** `PairingSurface` is only
 * reachable through `PhoneRuntime.phone()` answering Ready, and the phone core is a gomobile AAR
 * this unit-test JVM cannot load -- which is why there is no `PairingSurfaceTest` in this module
 * at all. `hasCameraHardware` was extracted for exactly this reason (agents-tracker-nz9h) and this
 * follows it: the sentence is a value on the flow, so it has a seam a test can reach, and
 * `android/gate/qx9m_camerareach_test.go`'s arrangement -- the screen ASKS the flow -- is the one
 * already established here.
 */
class PairingCameraCopyTest {

    @Test
    fun `a camera that will not start is named, with the two acts that get past it`() {
        val copy = PairingFlow.CAMERA_DID_NOT_START

        assertEquals(
            "Camera didn't start. Close other camera apps, or enter the code.",
            copy,
        )
    }

    /**
     * THE ASSERTION THAT MATTERS, stated against the row rather than against the string: the
     * reserved UNKNOWN sentence is what this replaces, and a copy edit that drifted back onto it
     * would restore the defect while every equality above went on passing.
     */
    @Test
    fun `the camera sentence is not the reserved unknown row`() {
        val unknown = ErrorRouter.route("java.lang.IllegalArgumentException: No available camera")

        assertEquals(
            "the fixture no longer lands on UNKNOWN, so this test is asserting nothing about the " +
                "row it exists to keep the camera off",
            ErrorState.UNKNOWN,
            unknown.state,
        )
        assertNotEquals(
            "the camera failure still reads as the reserved unrecognised-failure row",
            unknown.message,
            PairingFlow.CAMERA_DID_NOT_START,
        )
        assertFalse(
            "the camera sentence tells the user to report a bug about a busy camera",
            PairingFlow.CAMERA_DID_NOT_START.contains("report it"),
        )
    }

    /**
     * IT NAMES THE TYPED PATH, and that is PB-PAIR-2's "a denied camera must not be a dead end"
     * one step further out: [PairingFlow.offersManualEntry] answers true unconditionally
     * (agents-tracker-qun0) precisely so a camera that is live and useless still has a way
     * forward. A camera that never opened is the same dead end reached earlier, so the sentence
     * has to point at the same door.
     */
    @Test
    fun `the camera sentence points at the fallback the screen is already offering`() {
        assertTrue(
            "the sentence does not mention entering the code, which is the only path left when " +
                "the camera will not open",
            PairingFlow.CAMERA_DID_NOT_START.contains("enter the code"),
        )
        assertTrue(PairingFlow.offersManualEntry(ScannerState.SCANNING))
    }
}
