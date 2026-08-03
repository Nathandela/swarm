package dev.swarm.phone.scan

import android.util.Size
import android.view.View
import androidx.appcompat.app.AppCompatActivity
import androidx.camera.core.CameraSelector
import androidx.camera.core.ImageAnalysis
import androidx.camera.core.Preview
import androidx.camera.lifecycle.ProcessCameraProvider
import androidx.camera.core.resolutionselector.AspectRatioStrategy
import androidx.camera.core.resolutionselector.ResolutionSelector
import androidx.camera.core.resolutionselector.ResolutionStrategy
import androidx.camera.view.PreviewView
import dev.swarm.phone.ui.kit.scanReticle
import java.util.concurrent.ExecutorService
import java.util.concurrent.Executors

/**
 * Phase B slice S19 -- PB-PAIR-3's scanner, as ADR-007 B21 decided it: `com.google.zxing:core`
 * decodes and `androidx.camera` (CameraX) supplies the frames. ML Kit was named and rejected
 * there and is not re-litigated here.
 *
 * THE PROPERTY THAT CHOSE THIS PAIR, restated where the code is rather than only in the ADR:
 * everything that runs is inside the APK the release key signs. No Play Services, no downloaded
 * model, no dynamic code loading. The accepted cost is a weaker decoder with no auto-zoom, which
 * this product can afford -- the QR is on a screen about a metre away, and PB-PAIR-2's
 * manual-entry fallback doubles as the fallback for a code that will not decode.
 *
 * IT DECODES AND NOTHING ELSE. No facade verb is reachable from this file: it hands the decoded
 * payload to its caller and that caller -- [dev.swarm.phone.PairingSurface] -- is the one that
 * decides what to do with it. The reason is PB-PAIR-6: a scanner that began a pairing itself
 * would be joining a destination the user has not seen, which is the exact defect BeginPairing
 * was split apart to close. The payload crossing this seam has been READ, not acted on.
 *
 * WHAT IT IS NOT EVIDENCE FOR. PB-E2E-5 is deferred. A decode against an emulator's virtual
 * scene proves this pipeline decodes; it establishes nothing about a physical camera, its
 * autofocus, or its behaviour in the dark. An emulator is not a handset.
 */
class QrScanner(private val activity: AppCompatActivity) {

    /**
     * The viewfinder. It is a real preview and not an ornament: ZXing has no auto-zoom, so
     * aiming is the user's job and they cannot do it blind.
     *
     * AND AIMING IS THE USER'S JOB, SO IT CARRIES THE MARK THAT SAYS WHERE. `scanReticle` is
     * derivation §4's framing square, and it is the preview's FOREGROUND for two reasons that are
     * both about this class rather than about the drawing. It cannot take a touch, so nothing over
     * the live camera image can swallow one (PB-SEC-12 clause 1). And it cannot outlive the
     * preview: [stop] hides this view, which takes the reticle with it in the same statement --
     * where a sibling view in the pairing screen's scanner host would have been a second thing to
     * hide, and a green frame over a released camera is worse than no frame at all.
     */
    val view: PreviewView = PreviewView(activity).apply { foreground = scanReticle(activity) }

    /**
     * The decode runs off the main thread -- a QR decode on a 1080p frame is tens of
     * milliseconds and this is called for every frame the camera produces.
     */
    private var frames: ExecutorService? = null

    private var provider: ProcessCameraProvider? = null

    /** Set once a payload has been handed on, so one code is not scanned twenty times. */
    private var handedOn = false

    /**
     * Start the camera and call [onPayload] on the MAIN thread with the first payload decoded.
     *
     * The caller is responsible for having the CAMERA permission; PB-PAIR-2's three scanner
     * states are [dev.swarm.phone.ui.PairingFlow]'s to decide and this class is not the place
     * that asks for anything.
     */
    fun start(onPayload: (String) -> Unit) {
        if (frames != null) return
        val executor = Executors.newSingleThreadExecutor()
        frames = executor
        handedOn = false

        val future = ProcessCameraProvider.getInstance(activity)
        future.addListener({
            // The Activity can be gone by the time the provider arrives -- it is a static
            // initialisation on first use and takes a moment. Binding a use case to a destroyed
            // lifecycle throws; checking here is cheaper than catching it.
            if (frames !== executor) return@addListener
            val cameraProvider = future.get()
            provider = cameraProvider

            val preview = Preview.Builder().build()
            preview.setSurfaceProvider(view.surfaceProvider)

            val analysis = ImageAnalysis.Builder()
                // The newest frame, never a backlog: a queue of stale frames would decode a
                // code the user has already moved away from.
                .setBackpressureStrategy(ImageAnalysis.STRATEGY_KEEP_ONLY_LATEST)
                .setResolutionSelector(analysisResolution())
                .build()
            analysis.setAnalyzer(executor) { image ->
                val payload = try {
                    val plane = image.planes[0]
                    val luma = ByteArray(plane.buffer.remaining())
                    plane.buffer.get(luma)
                    decoder.payload(luma, plane.rowStride, image.width, image.height)
                } finally {
                    // Not closing an ImageProxy stalls the whole analysis pipeline after two
                    // frames, which looks exactly like a camera that does not work.
                    image.close()
                }
                if (payload != null && !handedOn) {
                    handedOn = true
                    activity.runOnUiThread { onPayload(payload) }
                }
            }

            cameraProvider.unbindAll()
            cameraProvider.bindToLifecycle(
                activity,
                CameraSelector.DEFAULT_BACK_CAMERA,
                preview,
                analysis,
            )
        }, activity.mainExecutor)
    }

    /**
     * Release the camera.
     *
     * Called when the pairing leaves the scan step and from the Activity's pause, because a
     * camera left bound is a camera light left on -- on a screen whose whole subject is a
     * handset its owner may not be holding.
     */
    fun stop() {
        provider?.unbindAll()
        provider = null
        frames?.shutdown()
        frames = null
        view.visibility = View.GONE
    }

    /**
     * The decode half, split out so it can be fed frames on a JVM (agents-tracker-v5qc): the
     * Y plane of a YUV_420_888 frame IS the luminance image ZXing wants, and [FrameDecoder]
     * takes it as bytes with no camera type in its signature.
     */
    private val decoder = FrameDecoder()

    companion object {
        /**
         * The analysis resolution, and it is the fix for the owner's handset scanning nothing
         * (agents-tracker-v5qc): CameraX's unconfigured ImageAnalysis default is a 640x480
         * bound, a quarter the area of the preview the user watches, which put a version-6/7
         * ECC-L pairing symbol at two or three pixels per module -- below what ZXing locks
         * onto, while the preview looked sharp. 1280x720 is Signal's number for the same job;
         * CLOSEST_HIGHER_THEN_LOWER so a sensor without 720p yields the next size up rather
         * than quietly down.
         *
         * The aspect strategy is set WITH the bound because it outranks it: CameraX sorts
         * candidates by aspect strategy first, and the default 4:3 strategy would steer the
         * pick away from the 16:9 size the bound names.
         */
        internal fun analysisResolution(): ResolutionSelector = ResolutionSelector.Builder()
            .setResolutionStrategy(
                ResolutionStrategy(
                    Size(1280, 720),
                    ResolutionStrategy.FALLBACK_RULE_CLOSEST_HIGHER_THEN_LOWER,
                ),
            )
            .setAspectRatioStrategy(AspectRatioStrategy.RATIO_16_9_FALLBACK_AUTO_STRATEGY)
            .build()
    }
}
