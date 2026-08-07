package dev.swarm.phone.ui.kit

import android.content.Context
import android.provider.Settings
import android.view.View
import androidx.test.core.app.ApplicationProvider
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner

/**
 * FAILING-FIRST (TDD RED, GG-5) for the obsidian migration plan's phase O6.3 -- the
 * predictive-back preview.
 *
 * WHAT THIS SUITE IS FOR, AND WHAT `android/gate/o6_predictiveback_test.go` IS FOR. The gate reads
 * SOURCE and the manifest: that the app opts in at all, that the Activity implements the three
 * progress members rather than only the commit, and that the three numbers are the plan's. It
 * cannot say what a frame LOOKS like. That is this file: the preview at rest, at the crossfade
 * threshold, at full drag, and put back after a cancelled gesture.
 *
 * THE PREVIEW IS A PURE FUNCTION OF PROGRESS AND THE VIEW'S OWN BOX, which is what makes it
 * assertable at all. A gesture cannot be replayed in a JVM; a frame can be asked for.
 *
 * REDUCED MOTION COLLAPSES IT TO NOTHING, and "nothing" is the operative word -- D5's own ruling
 * for the sweep, applied here for the same reason. The gesture still WORKS under reduced motion:
 * the back is still dispatched and the drill-down still closes. What does not happen is the
 * preview, so a user who asked the platform for stillness gets a screen that changes once rather
 * than one that follows their thumb.
 */
@RunWith(RobolectricTestRunner::class)
class PredictiveBackTest {

    private val context: Context
        get() = ApplicationProvider.getApplicationContext()

    private fun reducedMotion(on: Boolean) {
        Settings.Global.putFloat(
            context.contentResolver,
            Settings.Global.ANIMATOR_DURATION_SCALE,
            if (on) 0f else 1f,
        )
    }

    /** A laid-out view: the preview reads the box it is scaling, so an unmeasured one says nothing. */
    private fun drillDown(widthPx: Int = PHONE_WIDTH_PX, heightPx: Int = PHONE_HEIGHT_PX): View =
        View(context).apply { layout(0, 0, widthPx, heightPx) }

    @Before
    fun startFromOrdinaryMotion() {
        reducedMotion(false)
    }

    // ---- the frame --------------------------------------------------------

    @Test
    fun `at rest the drill-down is untouched`() {
        val view = drillDown()
        Motion.predictiveBack(context, view, 0f)
        assertEquals("a gesture that has not moved must not have moved anything", 1f, view.scaleX, 0f)
        assertEquals(1f, view.scaleY, 0f)
        assertEquals(1f, view.alpha, 0f)
    }

    @Test
    fun `at full drag the drill-down is at the plan's ninety percent`() {
        val view = drillDown()
        Motion.predictiveBack(context, view, 1f)
        assertEquals(
            "the plan states `scale to 90%`, and a preview that stops anywhere else is a gesture " +
                "that behaves differently from every other app on the handset",
            Motion.PREDICTIVE_BACK_SCALE,
            view.scaleX,
            TOLERANCE,
        )
        assertEquals("both axes scale together; a preview is not a squash", view.scaleX, view.scaleY, 0f)
    }

    @Test
    fun `the scale is interpolated across the drag rather than switched at the end`() {
        val view = drillDown()
        Motion.predictiveBack(context, view, HALF)
        val expected = 1f - HALF * (1f - Motion.PREDICTIVE_BACK_SCALE)
        assertEquals(
            "the preview must follow the thumb. A scale that jumps to its endpoint is a commit " +
                "wearing a gesture's clothes, and it tells the user nothing about what letting go " +
                "would do.",
            expected,
            view.scaleX,
            TOLERANCE,
        )
    }

    @Test
    fun `the eight-dp margin binds when ninety percent would not keep it`() {
        // A view narrow enough that a 10% inset is thinner than the margin the plan states.
        val marginPx = Motion.predictiveBackMarginPx(context)
        val narrow = (4 * marginPx).toInt()
        val view = drillDown(widthPx = narrow, heightPx = narrow)
        Motion.predictiveBack(context, view, 1f)
        val gapPx = narrow * (1f - view.scaleX) / 2f
        assertTrue(
            "the scaled preview sits ${gapPx}px from the edge and the plan states an 8dp margin. " +
                "On a phone the 90% scale already leaves more than that, which is exactly why " +
                "the margin has to be a FLOOR rather than a second way of writing the scale: on " +
                "any surface small enough for 10% to be thinner than 8dp, 90% is the wrong number.",
            gapPx >= marginPx - TOLERANCE,
        )
    }

    // ---- the crossfade ----------------------------------------------------

    @Test
    fun `nothing fades before the thirty-five percent threshold`() {
        val view = drillDown()
        listOf(0f, 0.1f, Motion.PREDICTIVE_BACK_CROSSFADE_AT - 0.01f).forEach { progress ->
            Motion.predictiveBack(context, view, progress)
            assertEquals(
                "at $progress the drill-down had already begun to fade. The threshold is what " +
                    "makes an abandoned gesture free: below it the user has been shown a shape " +
                    "moving, not a screen leaving.",
                1f,
                view.alpha,
                TOLERANCE,
            )
        }
    }

    @Test
    fun `past the threshold the drill-down fades out and is gone at the end`() {
        val view = drillDown()
        Motion.predictiveBack(context, view, Motion.PREDICTIVE_BACK_CROSSFADE_AT + 0.01f)
        val justPast = view.alpha
        assertTrue("the crossfade never started", justPast < 1f)

        Motion.predictiveBack(context, view, 1f)
        assertTrue("the crossfade did not deepen with the drag", view.alpha < justPast)
        assertEquals("a completed gesture leaves nothing of the screen it left", 0f, view.alpha, TOLERANCE)
    }

    // ---- cancelling -------------------------------------------------------

    @Test
    fun `an abandoned gesture puts the screen back exactly as it was`() {
        val view = drillDown()
        Motion.predictiveBack(context, view, 0.8f)
        Motion.clearPredictiveBack(view)
        assertEquals("a cancelled gesture left the screen scaled down", 1f, view.scaleX, 0f)
        assertEquals(1f, view.scaleY, 0f)
        assertEquals("a cancelled gesture left the screen faded", 1f, view.alpha, 0f)
    }

    // ---- reduced motion ---------------------------------------------------

    @Test
    fun `reduced motion collapses the preview to nothing`() {
        reducedMotion(true)
        val view = drillDown()
        Motion.predictiveBack(context, view, 1f)
        assertEquals(
            "a user who asked the platform for stillness got a screen following their thumb. D5's " +
                "ruling for the sweep is that reduced motion collapses it to NOTHING -- not to a " +
                "smaller movement -- and a preview is the same class of thing.",
            1f,
            view.scaleX,
            0f,
        )
        assertEquals(1f, view.alpha, 0f)
    }

    private companion object {

        /** A handset-sized drill-down, in px. Fixture input: the design states no screen size. */
        const val PHONE_WIDTH_PX = 1080

        const val PHONE_HEIGHT_PX = 2400

        /** Mid-drag. Fixture input: the plan states endpoints and a threshold, not a sample. */
        const val HALF = 0.5f

        const val TOLERANCE = 0.0005f
    }
}
