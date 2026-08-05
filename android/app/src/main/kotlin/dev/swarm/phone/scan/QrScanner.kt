package dev.swarm.phone.scan

import android.os.SystemClock
import android.util.Log
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
import java.io.File
import java.io.IOException
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
     * What this scan has actually done, for the log and for the line under the viewfinder
     * (agents-tracker-av7k).
     *
     * THE COUNTERS EXIST BECAUSE THE FIELD REPORT IS "NOTHING HAPPENS", and nothing is what a
     * camera that never opened, a pipeline delivering no frames and a symbol that will not decode
     * all look like from the outside. They are read and written on the analysis executor alone --
     * one executor per scan, created in [start] and shut down in [stop] -- so they need no
     * synchronisation and none is implied by their being on the object.
     */
    private var framesAnalysed = 0L
    private var decodeAttempts = 0L
    private var startedAtMillis = 0L

    /**
     * Where to write the next frame, set by [dumpNextFrame] and cleared by the analyzer that
     * honours it. Volatile because those are two different threads: the request comes from a
     * long press on the main thread and the write happens on the analysis executor.
     */
    @Volatile
    private var dumpTo: File? = null

    /**
     * Start the camera and call [onPayload] on the MAIN thread with the first payload decoded.
     *
     * The caller is responsible for having the CAMERA permission; PB-PAIR-2's three scanner
     * states are [dev.swarm.phone.ui.PairingFlow]'s to decide and this class is not the place
     * that asks for anything.
     *
     * @param onFrames called on the MAIN thread with the number of frames analysed so far, every
     *  [SCREEN_EVERY] frames. It is throttled here rather than at the screen because the caller
     *  cannot throttle what it is not told: at thirty frames a second an un-throttled callback is
     *  thirty main-thread posts a second for a line of text that a person reads at reading speed.
     */
    fun start(onPayload: (String) -> Unit, onFrames: (Long) -> Unit = {}) {
        if (frames != null) return
        val executor = Executors.newSingleThreadExecutor()
        frames = executor
        handedOn = false
        framesAnalysed = 0
        decodeAttempts = 0
        startedAtMillis = SystemClock.elapsedRealtime()

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
                    framesAnalysed++
                    if (framesAnalysed == 1L) {
                        // THE GEOMETRY THE PIPELINE ACTUALLY GRANTED, once, at the top of the
                        // scan. `analysisResolution` REQUESTS 1280x720 and CameraX is free to
                        // answer with the nearest size the sensor supports -- "CameraX granted a
                        // different resolution than we asked for" is one of the four live
                        // explanations for the scanner that never locks on, and it is the only
                        // one this line can settle outright. The rotation is here because a
                        // portrait hold delivers a sensor-native landscape buffer, which the
                        // decode ladder benched so far does not account for.
                        Log.i(
                            TAG,
                            "analysis ${image.width}x${image.height} stride=${plane.rowStride} " +
                                "rotation=${image.imageInfo.rotationDegrees}",
                        )
                    }
                    dumpIfAsked(luma, plane.rowStride, image.width, image.height)
                    decoder.payload(luma, plane.rowStride, image.width, image.height)
                } finally {
                    // Not closing an ImageProxy stalls the whole analysis pipeline after two
                    // frames, which looks exactly like a camera that does not work.
                    image.close()
                }
                decodeAttempts += decoder.attempts
                if (framesAnalysed % LOG_EVERY == 0L) {
                    // FRAMES AND ATTEMPTS ARE TWO NUMBERS BECAUSE THEY ANSWER TWO QUESTIONS. The
                    // frame count says the pipeline is alive; the attempt count says the decoder
                    // is being run on what it delivers, and the two would diverge if the geometry
                    // guard started refusing frames before ZXing ever saw one.
                    Log.i(
                        TAG,
                        "$framesAnalysed frames, $decodeAttempts decode attempts, " +
                            "${SystemClock.elapsedRealtime() - startedAtMillis} ms",
                    )
                }
                if (framesAnalysed % SCREEN_EVERY == 0L) {
                    val seen = framesAnalysed
                    activity.runOnUiThread { onFrames(seen) }
                }
                if (payload != null && !handedOn) {
                    handedOn = true
                    // WHICH ATTEMPT READ IT, never what it read. The second attempt succeeding
                    // says the terminal handed the camera a light-on-dark symbol, which is a
                    // different defect with a different fix -- and the payload itself is a
                    // pairing secret that must never reach a log buffer.
                    Log.i(
                        TAG,
                        "decoded on attempt ${decoder.decodedOnAttempt} at frame $framesAnalysed",
                    )
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
        dumpTo = null
        view.visibility = View.GONE
    }

    /**
     * Write the NEXT analysis frame into [dir] as a PGM, and log where it went
     * (agents-tracker-av7k).
     *
     * WHY THIS IS IN THE PRODUCT AND NOT IN A BRANCH. Four explanations for the scanner that
     * never locks on -- seams, exposure, undersampling, a resolution nobody asked for -- are all
     * claims about what the analysis buffer holds, and no bench can settle them because a bench
     * builds its own frames. The evidence has to come off the handset that fails, which is the
     * owner's, running an internal-testing build. A diagnostic that only exists in a developer's
     * checkout cannot be run on the phone that has the defect.
     *
     * ONE FRAME, ON REQUEST. Not a stream, not a ring buffer, and nothing at all until somebody
     * long-presses the viewfinder: the frames are camera images taken during a pairing, so the
     * cheapest thing that answers the question is also the least this can hold.
     */
    fun dumpNextFrame(dir: File) {
        dumpTo = dir
    }

    /**
     * Honour a pending [dumpNextFrame], on the analysis executor, before the decode.
     *
     * BEFORE THE DECODE, so what lands on disk is the frame the decoder is about to be given
     * rather than the next one -- the whole question is what the decoder is being handed.
     */
    private fun dumpIfAsked(luma: ByteArray, stride: Int, width: Int, height: Int) {
        val dir = dumpTo ?: return
        dumpTo = null
        val image = FrameDump.pgm(luma, stride, width, height)
        if (image.isEmpty()) {
            Log.w(TAG, "no analysis frame was written: this frame's geometry describes no image")
            return
        }
        val file = File(dir, "scan-frame-${System.currentTimeMillis()}.pgm")
        try {
            dir.mkdirs()
            file.writeBytes(image)
            Log.i(TAG, "wrote one analysis frame to ${file.absolutePath}")
        } catch (unwritable: IOException) {
            // Loud rather than silent: a debug affordance that appears to work and writes
            // nothing sends someone looking for a file that was never there.
            Log.w(TAG, "no analysis frame was written", unwritable)
        }
    }

    /**
     * The decode half, split out so it can be fed frames on a JVM (agents-tracker-v5qc): the
     * Y plane of a YUV_420_888 frame IS the luminance image ZXing wants, and [FrameDecoder]
     * takes it as bytes with no camera type in its signature.
     */
    private val decoder = FrameDecoder()

    companion object {

        /** The logcat tag. One scan's lines are one `adb logcat -s` filter. */
        private const val TAG = "QrScanner"

        /**
         * How often the running totals reach the log, in frames. Roughly every second and a half
         * at thirty frames a second: often enough that a stalled pipeline is visible as a line
         * that stops arriving, rare enough that the buffer is still readable afterwards.
         */
        private const val LOG_EVERY = 50L

        /**
         * How often the count reaches the SCREEN, in frames. It is ten times the log's rate
         * because the two are read differently: the log is read afterwards and the line under
         * the viewfinder is read while it changes, where a number that only moves every second
         * and a half looks stuck.
         */
        private const val SCREEN_EVERY = 5L

        /**
         * The analysis resolution, RAISED from the 640x480 floor CameraX applies when nobody
         * configures ImageAnalysis (agents-tracker-v5qc). That default is a quarter the area of
         * the preview the user watches, which put a version-6/7 ECC-L pairing symbol at two or
         * three pixels per module -- below what ZXing locks onto, while the preview looked
         * sharp. 1280x720 is Signal's number for the same job; CLOSEST_HIGHER_THEN_LOWER so a
         * sensor without 720p yields the next size up rather than quietly down.
         *
         * IT WAS CALLED "THE FIX FOR THE OWNER'S HANDSET" HERE AND THE FIELD RETEST FALSIFIED
         * THAT. The owner ran 0.2.2 and 0.2.3 with this setting and the scanner still never
         * locked onto the terminal symbol (agents-tracker-av7k), so what this removes is one
         * necessary condition among several rather than the cause. The remaining candidates --
         * half-block seams, exposure bloom off the monitor, the rotation of a portrait hold, and
         * whether CameraX grants what this asks for -- are what the instrumentation in this file
         * exists to separate. A comment that claims a defect is closed is worse than no comment
         * when the next reader is looking for what is still open.
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
