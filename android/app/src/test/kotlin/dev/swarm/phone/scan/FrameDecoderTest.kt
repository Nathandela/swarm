package dev.swarm.phone.scan

import com.google.zxing.BarcodeFormat
import com.google.zxing.EncodeHintType
import com.google.zxing.common.BitMatrix
import com.google.zxing.qrcode.QRCodeWriter
import com.google.zxing.qrcode.decoder.ErrorCorrectionLevel
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Test

/**
 * agents-tracker-v5qc -- the camera pipeline's decoder finally has a test subject.
 *
 * Until this file, no test anywhere fed a frame through the scan path: the instrumented smoke
 * deliberately takes the typed fallback, so the owner's handset was the first thing ever to
 * point the decoder at a real symbol -- and it decoded nothing. These tests render the same
 * class of symbol the machine prints (version 6-7, ECC level L, the compact shape
 * internal/remote/pairing/qr.go chose to fit a terminal) into a luminance frame the analyzer
 * would deliver, and assert the seam decodes it.
 *
 * The frames are built by hand here because that is the point: the decoder's contract is bytes
 * in, payload out, with no camera anywhere. What these tests cannot say is anything about
 * optics -- focus, moire, screen flicker. PB-E2E-5 stays deferred and the handset remains the
 * only evidence for the glass.
 */
class FrameDecoderTest {

    /** The shape of a real pairing payload: qr.go's 133-character ceiling, base64url charset. */
    private val payload = buildString {
        while (length < 133) append("swarm-pair_A9zXq7-")
    }.take(133)

    private fun symbol(): BitMatrix {
        val hints = mapOf(
            // ECC L is what EncodeQR uses -- the least redundancy a symbol can carry, so the
            // decoder is tested against the least forgiving shape the machine will print.
            EncodeHintType.ERROR_CORRECTION to ErrorCorrectionLevel.L,
            EncodeHintType.MARGIN to 4,
        )
        return QRCodeWriter().encode(payload, BarcodeFormat.QR_CODE, 220, 220, hints)
    }

    /**
     * A luminance frame as the analyzer sees one: the symbol centred on a light ground, rows
     * laid out at [stride] bytes with anything past [width] being padding the decoder must
     * never read as image.
     */
    private fun frame(
        symbol: BitMatrix,
        width: Int = 1280,
        height: Int = 720,
        stride: Int = width,
        inverted: Boolean = false,
    ): ByteArray {
        val light: Byte = if (inverted) 0x10 else 0xF0.toByte()
        val dark: Byte = if (inverted) 0xF0.toByte() else 0x10
        val data = ByteArray(stride * height)
        for (y in 0 until height) {
            for (x in 0 until width) data[y * stride + x] = light
            // Padding bytes are noise, not background: a decoder that reads them as image
            // sees every row torn.
            for (x in width until stride) data[y * stride + x] = 0x55
        }
        val left = (width - symbol.width) / 2
        val top = (height - symbol.height) / 2
        for (y in 0 until symbol.height) {
            for (x in 0 until symbol.width) {
                if (symbol.get(x, y)) data[(top + y) * stride + (left + x)] = dark
            }
        }
        return data
    }

    @Test
    fun decodes_a_pairing_density_symbol_from_an_analysis_frame() {
        val s = symbol()
        assertEquals(payload, FrameDecoder().payload(frame(s), 1280, 1280, 720))
    }

    @Test
    fun decodes_an_inverted_symbol() {
        // The machine paints the light ground itself (qrterm.go), but that guarantee ends at
        // the terminal: a theme that remaps ANSI colours, or a binarizer thrown by a monitor's
        // backlight, hands the camera a light-on-dark symbol. Production scanners retry the
        // inverted frame (budiyev/code-scanner's decodeLuminanceSource); plain QRCodeReader
        // never does -- it even ignores the ALSO_INVERTED hint, which only MultiFormatReader
        // honours.
        val s = symbol()
        assertEquals(payload, FrameDecoder().payload(frame(s, inverted = true), 1280, 1280, 720))
    }

    @Test
    fun reads_rows_by_stride_and_never_by_width() {
        // Row-padded Y planes are real hardware, not a synthetic case: bitwarden/android
        // PR 7097 fixed exactly this on Motorola G9s, where every row after the first was
        // misaligned and nothing ever decoded.
        val s = symbol()
        assertEquals(
            payload,
            FrameDecoder().payload(frame(s, stride = 1280 + 64), 1280 + 64, 1280, 720),
        )
    }

    @Test
    fun a_frame_with_no_symbol_is_null_not_an_exception() {
        val blank = ByteArray(1280 * 720) { 0xF0.toByte() }
        assertNull(FrameDecoder().payload(blank, 1280, 1280, 720))
    }

    /**
     * FAILING-FIRST (TDD RED, GG-5) for agents-tracker-av7k's instrumentation.
     *
     * WHAT THE FIELD REPORT LEAVES UNDECIDED. The owner hovers the camera over the terminal
     * symbol and nothing happens -- and "nothing" is the same observation for a pipeline
     * delivering no frames, one delivering frames the decoder never reads, and one reading
     * frames that simply do not carry a decodable symbol. The scanner cannot tell those apart
     * today because it counts nothing, so every log line it could write would say the same thing.
     *
     * THE ATTEMPT IS THE INTERESTING NUMBER AND NOT THE FRAME COUNT. Each frame is tried normal
     * and then inverted, so which of the two reads a symbol is a fact about the SCENE: the second
     * one succeeding says the terminal handed the camera light-on-dark, which is a different
     * defect with a different fix from the seam and the exposure hypotheses. It is one integer
     * and it settles a question the whole ladder rests on.
     */
    @Test
    fun reports_which_attempt_read_the_symbol_and_how_many_it_made() {
        val s = symbol()

        val normal = FrameDecoder()
        assertEquals(payload, normal.payload(frame(s), 1280, 1280, 720))
        assertEquals("a symbol on a light ground was not read on the first attempt", 1, normal.decodedOnAttempt)
        assertEquals("the inverted retry was spent on a frame the first attempt had read", 1, normal.attempts)

        val inverted = FrameDecoder()
        assertEquals(payload, inverted.payload(frame(s, inverted = true), 1280, 1280, 720))
        assertEquals(
            "an inverted symbol reports the attempt that read it as the first one, so the log " +
                "cannot distinguish a light-on-dark terminal from an ordinary decode",
            2,
            inverted.decodedOnAttempt,
        )
        assertEquals(2, inverted.attempts)

        val blank = FrameDecoder()
        assertNull(blank.payload(ByteArray(1280 * 720) { 0xF0.toByte() }, 1280, 1280, 720))
        assertEquals("a frame that decoded nothing still claims an attempt read it", 0, blank.decodedOnAttempt)
        assertEquals(
            "a failing frame did not spend both attempts, so the inverted retry is not running",
            2,
            blank.attempts,
        )
    }

    @Test
    fun the_counters_describe_the_last_frame_and_never_the_run() {
        // A running total would answer "how has this session gone", which is the SCANNER's
        // question and is counted there. What the decoder is asked is about the frame in hand,
        // so a decode that follows a failure must not report the failure's attempts as its own.
        val decoder = FrameDecoder()
        assertNull(decoder.payload(ByteArray(1280 * 720) { 0xF0.toByte() }, 1280, 1280, 720))
        assertEquals(payload, decoder.payload(frame(symbol()), 1280, 1280, 720))

        assertEquals(1, decoder.attempts)
        assertEquals(1, decoder.decodedOnAttempt)
    }

    @Test
    fun a_frame_refused_before_any_decode_reports_no_attempts() {
        // The geometry guard returns before ZXing is reached at all. Reporting an attempt here
        // would put a decode in the log that never happened, on the one path where the fault is
        // the pipeline's rather than the scene's.
        val decoder = FrameDecoder()
        assertNull(decoder.payload(ByteArray(16), stride = 0, width = 0, height = 4))

        assertEquals(0, decoder.attempts)
        assertEquals(0, decoder.decodedOnAttempt)
    }
}
