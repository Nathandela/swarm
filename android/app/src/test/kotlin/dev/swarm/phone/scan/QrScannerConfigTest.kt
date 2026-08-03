package dev.swarm.phone.scan

import android.util.Size
import androidx.camera.core.AspectRatio
import androidx.camera.core.resolutionselector.AspectRatioStrategy
import androidx.camera.core.resolutionselector.ResolutionStrategy
import org.junit.Assert.assertEquals
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner

/**
 * agents-tracker-v5qc, the half that is camera configuration rather than decoding.
 *
 * CameraX's ImageAnalysis default is a 640x480 bound -- an analysis frame a quarter the area
 * of the preview the user watches, which is how a scanner can look alive and decode nothing:
 * a version-6/7 ECC-L pairing symbol lands at two or three pixels per module and ZXing never
 * locks on. Nothing asserted the analysis resolution, so nothing failed.
 *
 * 1280x720 is Signal's number for exactly this job (CameraScreenViewModel.QR_ANALYSIS_RESOLUTION),
 * with CLOSEST_HIGHER_THEN_LOWER so a sensor without 720p yields the next size up rather than
 * quietly down. The aspect strategy must be pinned WITH it: AspectRatioStrategy outranks
 * ResolutionStrategy when CameraX sorts candidates, and the default 4:3 strategy would steer
 * the pick away from the 16:9 size the bound names.
 */
@RunWith(RobolectricTestRunner::class)
class QrScannerConfigTest {

    @Test
    fun analysis_frames_are_requested_at_720p_not_the_vga_default() {
        val selector = QrScanner.analysisResolution()
        val strategy = selector.resolutionStrategy
            ?: throw AssertionError("no ResolutionStrategy: analysis runs at CameraX's 640x480 default")
        assertEquals(Size(1280, 720), strategy.boundSize)
        assertEquals(
            ResolutionStrategy.FALLBACK_RULE_CLOSEST_HIGHER_THEN_LOWER,
            strategy.fallbackRule,
        )
    }

    @Test
    fun the_aspect_strategy_matches_the_bound_so_it_cannot_outvote_it() {
        val aspect = QrScanner.analysisResolution().aspectRatioStrategy
        assertEquals(AspectRatio.RATIO_16_9, aspect.preferredAspectRatio)
        assertEquals(AspectRatioStrategy.FALLBACK_RULE_AUTO, aspect.fallbackRule)
    }
}
