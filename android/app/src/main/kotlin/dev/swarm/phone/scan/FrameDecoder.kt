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
        return attempt(source) ?: attempt(source.invert())
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
