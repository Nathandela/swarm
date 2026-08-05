package dev.swarm.phone.scan

import com.google.zxing.BinaryBitmap
import com.google.zxing.ChecksumException
import com.google.zxing.DecodeHintType
import com.google.zxing.FormatException
import com.google.zxing.LuminanceSource
import com.google.zxing.NotFoundException
import com.google.zxing.PlanarYUVLuminanceSource
import com.google.zxing.common.HybridBinarizer
import com.google.zxing.qrcode.QRCodeReader

/**
 * One luminance frame through ZXing: bytes in, payload or null out, no camera anywhere.
 *
 * This is [QrScanner]'s decode half, split out (agents-tracker-v5qc) so it has a test subject:
 * while it lived inside the analyzer callback, no test ever fed it a frame, and the first
 * evidence the pipeline produced was the owner's handset decoding nothing.
 *
 * Every frame gets two attempts, normal then inverted -- budiyev/code-scanner's pattern. The
 * machine paints the symbol's light ground itself (internal/remote/qrterm), but that guarantee
 * ends at the terminal: a theme that remaps ANSI colours, or a binarizer thrown by a monitor's
 * backlight, hands the camera light-on-dark. QRCodeReader cannot be hinted around this --
 * ALSO_INVERTED is honoured only by MultiFormatReader -- so the retry is by hand. It costs a
 * second decode only on frames the first attempt already failed, which the analysis pipeline
 * spends waiting for the next frame anyway.
 *
 * NOT thread-safe: the reader is reused across calls and reset between them. One analyzer
 * executor owns one instance.
 */
class FrameDecoder {

    private val reader = QRCodeReader()

    /**
     * How many ZXing decodes the LAST frame cost: 0 when the geometry was refused before any,
     * 1 when the first attempt read a symbol, 2 otherwise.
     *
     * IT DESCRIBES THE FRAME IN HAND AND NOT THE RUN (agents-tracker-av7k). A running total is
     * the scanner's to keep, because it is the scanner that knows when a scan began; what this
     * answers is what the frame just decoded actually cost, which is the number that pairs with
     * [decodedOnAttempt].
     */
    var attempts: Int = 0
        private set

    /**
     * Which attempt read the payload -- 1 normal, 2 inverted -- or 0 when none did.
     *
     * ONE INTEGER SETTLES A HYPOTHESIS. The field report is a scanner that never locks on
     * (agents-tracker-av7k), and the second attempt succeeding says the terminal handed the
     * camera a light-on-dark symbol, which is a different defect from the seam and the exposure
     * ones and has a different fix. Without it the log can only say "decoded" and the polarity
     * question stays open.
     */
    var decodedOnAttempt: Int = 0
        private set

    /**
     * Decode the Y plane of a YUV_420_888 frame. [stride] rather than [width] is the data
     * width, and that is not a detail: the camera pads rows to a hardware alignment, so a
     * source built with `width` reads the padding of row n as the start of row n+1 and decodes
     * nothing at all on the devices that pad (bitwarden/android 7097 shipped exactly that).
     */
    fun payload(luma: ByteArray, stride: Int, width: Int, height: Int): String? {
        attempts = 0
        decodedOnAttempt = 0
        if (stride <= 0 || width > stride) return null
        val rows = minOf(height, luma.size / stride)
        if (rows <= 0) return null
        val source = PlanarYUVLuminanceSource(luma, stride, rows, 0, 0, width, rows, false)
        val plain = attempt(source) ?: attempt(source.invert())
        if (plain != null) {
            plainFailureStreak = 0
            return plain
        }

        // THE LADDER (agents-tracker-v4xs), and it is evidence, not caution: the owner's own
        // capture -- a structurally perfect symbol in VS Code -- never decodes plain, because
        // the terminal leaves a ~3px unpainted strip between line boxes that slices every
        // vertical finder cross-check. A morphological CLOSE fills it: radius 1 at camera
        // scale, radius 2 at native scale, complementary windows measured on those bytes
        // before this was built (a close of radius r fills gaps up to 2r and over-closes
        // once 2r reaches the module pitch, so neither radius may substitute for the other).
        //
        // ESCALATION BY FAILURE STREAK (committee ruling): radius 1 joins from the second
        // consecutive plain failure, radius 2 from the third. The common no-symbol frame
        // stays two cheap attempts, and a streak is what "not locking on" IS. Per-polarity
        // deliberately: an inverted seamed symbol needs invert-then-close -- closing dark
        // seams in the normal luma does nothing for light ones -- so each rung runs the
        // close on both polarities of the ORIGINAL, never on its own output.
        val streak = plainFailureStreak
        if (streak >= 2) {
            val compact = compactRows(luma, stride, width, rows)
            val deepest = if (streak >= 3) 2 else 1
            for (radius in 1..deepest) {
                val closed = closeDark(compact, width, rows, radius)
                attempt(PlanarYUVLuminanceSource(closed, width, rows, 0, 0, width, rows, false))
                    ?.let { plainFailureStreak = 0; return it }
                val inverted = ByteArray(compact.size) { (255 - (compact[it].toInt() and 0xFF)).toByte() }
                attempt(PlanarYUVLuminanceSource(closeDark(inverted, width, rows, radius), width, rows, 0, 0, width, rows, false))
                    ?.let { plainFailureStreak = 0; return it }
            }
        }
        plainFailureStreak++
        return null
    }

    /**
     * Consecutive frames on which the PLAIN attempts read nothing. It is the decoder's own
     * definition of "this scan is not locking on", and the ladder's gate: escalation keyed on
     * anything else would make a hand jitter or a walk-in frame pay six decodes.
     */
    private var plainFailureStreak = 0

    /** The luminance with its row padding stripped: morphology must never smear padding
     *  bytes into the image on the devices whose stride exceeds their width. */
    private fun compactRows(luma: ByteArray, stride: Int, width: Int, rows: Int): ByteArray {
        if (stride == width) return luma
        val out = ByteArray(width * rows)
        for (y in 0 until rows) {
            System.arraycopy(luma, y * stride, out, y * width, width)
        }
        return out
    }

    /**
     * Grey-scale morphological close for DARK features: dilate darkness (windowed min), then
     * erode it back (windowed max), separable and isotropic. Fills light gaps up to 2*radius
     * wide on BOTH axes -- the capture's seams arrive horizontal from a landscape hold and
     * vertical from a portrait one, because nothing rotates the sensor-native buffer.
     */
    private fun closeDark(src: ByteArray, width: Int, rows: Int, radius: Int): ByteArray {
        val a = scratch(0, width * rows)
        val b = scratch(1, width * rows)
        minPass(src, a, width, rows, radius)
        maxPass(a, b, width, rows, radius)
        return b
    }

    private fun minPass(src: ByteArray, dst: ByteArray, width: Int, rows: Int, radius: Int) =
        separable(src, dst, width, rows, radius) { x, y -> minOf(x, y) }

    private fun maxPass(src: ByteArray, dst: ByteArray, width: Int, rows: Int, radius: Int) =
        separable(src, dst, width, rows, radius) { x, y -> maxOf(x, y) }

    private inline fun separable(
        src: ByteArray,
        dst: ByteArray,
        width: Int,
        rows: Int,
        radius: Int,
        pick: (Int, Int) -> Int,
    ) {
        // Horizontal pass into dst, vertical pass back over dst in place via a row buffer.
        for (y in 0 until rows) {
            val row = y * width
            for (x in 0 until width) {
                var v = src[row + x].toInt() and 0xFF
                for (d in 1..radius) {
                    if (x - d >= 0) v = pick(v, src[row + x - d].toInt() and 0xFF)
                    if (x + d < width) v = pick(v, src[row + x + d].toInt() and 0xFF)
                }
                dst[row + x] = v.toByte()
            }
        }
        val col = ByteArray(rows)
        for (x in 0 until width) {
            for (y in 0 until rows) col[y] = dst[y * width + x]
            for (y in 0 until rows) {
                var v = col[y].toInt() and 0xFF
                for (d in 1..radius) {
                    if (y - d >= 0) v = pick(v, col[y - d].toInt() and 0xFF)
                    if (y + d < rows) v = pick(v, col[y + d].toInt() and 0xFF)
                }
                dst[y * width + x] = v.toByte()
            }
        }
    }

    private val scratchBuffers = arrayOfNulls<ByteArray>(2)

    private fun scratch(i: Int, size: Int): ByteArray {
        val have = scratchBuffers[i]
        if (have != null && have.size >= size) return have
        val fresh = ByteArray(size)
        scratchBuffers[i] = fresh
        return fresh
    }

    private fun attempt(source: LuminanceSource): String? = try {
        attempts++
        reader.decode(BinaryBitmap(HybridBinarizer(source)), HINTS).text
            .also { decodedOnAttempt = attempts }
    } catch (absent: NotFoundException) {
        // No code in this frame. The overwhelmingly common case: every frame before the
        // user has the code in shot lands here.
        null
    } catch (damaged: ChecksumException) {
        null
    } catch (malformed: FormatException) {
        null
    } finally {
        // QRCodeReader keeps per-decode state; a reader that is not reset returns the
        // previous frame's result on the next call.
        reader.reset()
    }

    private companion object {
        /**
         * TRY_HARDER, because the alternative here is the user retyping a 133-character payload
         * by hand. It costs decode time on a frame that has no code in it, which is time the
         * analysis pipeline was going to spend waiting for the next frame anyway.
         */
        val HINTS: Map<DecodeHintType, Any> = mapOf(DecodeHintType.TRY_HARDER to true)
    }
}
