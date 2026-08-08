package dev.swarm.phone

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Test

/**
 * FAILING-FIRST (TDD RED, GG-5) for agents-tracker-ksvb.3: the frame counter's own clock.
 *
 * **THE DIAGNOSTIC CAUSED A SMALL VERSION OF THE THING IT DIAGNOSES.** `QrScanner` reports every
 * analysed frame, which on a handset is roughly thirty a second, and the surface wrote each one
 * straight onto the notice under the viewfinder. A `TextView` re-lays out and re-antialiases its
 * whole line on every assignment, so a line whose job is to say "the camera IS looking" was
 * itself flickering thirty times a second, three centimetres under a viewfinder somebody was
 * holding steady over a QR code.
 *
 * TWO A SECOND IS THE RATE, AND BOTH CONDITIONS ARE REQUIRED. A count that only changed when the
 * NUMBER changed would still be thirty a second, because the number changes on every frame; a
 * count that only changed on a timer would rewrite the identical string forever once the camera
 * stalled -- which is the state this line exists to make visible, spent as a repaint. So a write
 * needs a new string AND a fresh interval.
 *
 * IT IS A CLASS AND NOT A PAIR OF FIELDS ON THE SURFACE, because `PairingSurface` reaches
 * `swarmmobile.App` and CameraX and is therefore unreachable on this JVM, while the decision --
 * which frames reach the screen -- is arithmetic on a string and a clock. The surface passes the
 * clock in for the same reason: a throttle that read the clock itself could only be tested by
 * sleeping.
 */
class ScanProgressThrottleTest {

    private val one = "1 frame analysed, no code found yet"

    private val two = "2 frames analysed, no code found yet"

    private val three = "3 frames analysed, no code found yet"

    @Test
    fun `the first frame reaches the screen at once`() {
        assertEquals(
            "the first count was withheld, so a camera that has just started looking says " +
                "nothing for half a second -- which is the silence the counter exists to end",
            one,
            ScanProgressThrottle().next(one, 8_000L),
        )
    }

    @Test
    fun `a newer count inside the interval is withheld`() {
        val throttle = ScanProgressThrottle()
        throttle.next(one, 8_000L)

        assertNull(
            "a second count 200 ms after the first reached the view, so the notice is rewritten " +
                "at the camera's frame rate and flickers under the viewfinder",
            throttle.next(two, 8_200L),
        )
        assertNull("and again at 499 ms", throttle.next(three, 8_499L))
    }

    @Test
    fun `a newer count after the interval reaches the screen`() {
        val throttle = ScanProgressThrottle()
        throttle.next(one, 8_000L)

        assertEquals(
            "a count half a second later was withheld, so the line freezes and reports a camera " +
                "that has stopped looking as one that is still going",
            three,
            throttle.next(three, 8_500L),
        )
    }

    @Test
    fun `the same count is never written twice, however long the wait`() {
        val throttle = ScanProgressThrottle()
        throttle.next(one, 8_000L)

        assertNull(
            "the identical string was written again, which is a repaint that says nothing -- and " +
                "a stalled analyser would go on paying for it forever",
            throttle.next(one, 20_000L),
        )
    }

    @Test
    fun `a restarted scan reports its first frame immediately`() {
        val throttle = ScanProgressThrottle()
        throttle.next(one, 8_000L)
        throttle.reset()

        assertEquals(
            "after the surface blanked the line for a new scan, the first count was held back by " +
                "an interval measured against the PREVIOUS scan",
            one,
            throttle.next(one, 8_100L),
        )
    }
}
