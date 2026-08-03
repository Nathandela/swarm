package dev.swarm.phone.scan

import android.view.MotionEvent
import android.view.View
import androidx.test.core.app.ActivityScenario
import dev.swarm.phone.PhoneActivity
import dev.swarm.phone.ui.kit.ScanReticleDrawable
import org.junit.Assert.assertFalse
import org.junit.Assert.assertSame
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner

/**
 * FAILING-FIRST (TDD RED, GG-5) for §4's `Scanner reticle` where it meets the camera.
 *
 * ScanReticleTest asserts what the reticle IS. This asserts the two things that are facts about
 * the seam rather than about the drawing, and both are ways a correct reticle ships broken:
 *
 *   - **It is on the preview, and it is a foreground.** Anything else -- a sibling view in
 *     `PairingSurface`'s host, a child added to the preview -- is a view over a live camera image,
 *     which is the tapjacking surface PB-SEC-12 clause 1 exists for, and it is one `isClickable`
 *     away from being one. It also has to be the foreground of the PREVIEW rather than of the host
 *     around it, because of the second claim.
 *   - **It cannot outlive the preview.** `stop()` sets `view.visibility = GONE`, and a green frame
 *     hanging over a dead camera is worse than no frame at all. Being the preview's own foreground
 *     is what makes that structural rather than something a caller has to remember: there is no
 *     ordering of `stop()` and a render in which the reticle is visible and the preview is not.
 *
 * WHAT THIS DOES NOT CLAIM. No camera is opened here -- `start()` is never called, because
 * Robolectric has no camera and `ProcessCameraProvider` would be answering about nothing. What is
 * asserted is the preview's own state, which is what `stop()` actually changes.
 */
@RunWith(RobolectricTestRunner::class)
class QrScannerReticleTest {

    @Test
    fun the_preview_wears_the_reticle_as_a_foreground() {
        ActivityScenario.launch(PhoneActivity::class.java).use { scenario ->
            scenario.onActivity { activity ->
                val scanner = QrScanner(activity)

                assertTrue(
                    "the preview carries no reticle: its foreground is " +
                        "${scanner.view.foreground}. A viewfinder with no framing mark is the " +
                        "screen the owner used on a handset -- a live image and nothing saying " +
                        "where to point it.",
                    scanner.view.foreground is ScanReticleDrawable,
                )
                assertFalse(
                    "the preview is clickable, so the thing over the camera image can take a " +
                        "tap that was aimed at what is behind it",
                    scanner.view.isClickable,
                )
            }
        }
    }

    @Test
    fun the_reticle_goes_when_the_scanner_stops() {
        ActivityScenario.launch(PhoneActivity::class.java).use { scenario ->
            scenario.onActivity { activity ->
                val scanner = QrScanner(activity)
                val reticle = scanner.view.foreground

                scanner.stop()

                assertTrue(
                    "stop() left the preview visible, so nothing here is evidence about the " +
                        "reticle either",
                    scanner.view.visibility == View.GONE,
                )
                assertSame(
                    "the reticle is no longer the foreground of the view stop() hides, so it is " +
                        "carried by something with a different lifetime and can be left on " +
                        "screen over a released camera",
                    reticle,
                    scanner.view.foreground,
                )
            }
        }
    }

    @Test
    fun the_preview_does_not_consume_a_touch() {
        ActivityScenario.launch(PhoneActivity::class.java).use { scenario ->
            scenario.onActivity { activity ->
                val scanner = QrScanner(activity)
                scanner.view.layout(0, 0, SIDE, SIDE)

                val down = MotionEvent.obtain(
                    0L, 0L, MotionEvent.ACTION_DOWN, SIDE / 2f, SIDE / 2f, 0,
                )
                val consumed = try {
                    scanner.view.dispatchTouchEvent(down)
                } finally {
                    down.recycle()
                }

                assertFalse(
                    "the preview consumed a touch. PB-SEC-12 clause 1's filter is applied to the " +
                        "controls a screen names, and a preview that swallowed taps would be an " +
                        "unfiltered one nobody had listed.",
                    consumed,
                )
            }
        }
    }

    /** Any laid-out size at all; the touch only has to land inside the view. */
    private companion object {
        const val SIDE = 400
    }
}
