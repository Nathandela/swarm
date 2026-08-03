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
}
