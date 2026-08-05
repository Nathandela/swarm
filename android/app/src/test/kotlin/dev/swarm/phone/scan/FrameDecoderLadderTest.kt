package dev.swarm.phone.scan

// agents-tracker-v4xs -- the attempt ladder, un-deferred by a real capture.
//
// THE FIXTURES ARE FIELD PIXELS, NOT SYNTHETICS. field-capture-vscode-680x674.luma is the
// owner's VS Code terminal showing a live pairing symbol (screenshot, 2026-08-05, converted to
// luminance); 170x168 is the same capture at quarter scale, which is the 3-4 px/module regime a
// phone camera actually delivers. The symbol in them is structurally perfect -- finders and
// timing patterns measure exact -- and the shipped decoder reads NEITHER, because VS Code
// leaves a ~3px unpainted strip between line boxes (28px cadence) that slices every vertical
// and diagonal finder cross-check. The terminal-side repaint cannot reach BETWEEN lines.
//
// The mitigation was measured on these exact bytes before it was built: a morphological close
// of radius 2 rescues the native scale (a 3px gap needs 2r >= 3), radius 1 rescues the quarter
// scale (where radius 2 over-closes 3.6px modules) -- complementary windows, exactly where the
// capability bench put them on synthetics. Plain decode runs first, always, so a frame the old
// decoder read costs exactly what it used to.
//
// ESCALATION IS DELIBERATE AND DECIDED HERE (committee ruling): morphology runs only after
// PLAIN has failed on consecutive frames -- radius 1 from the second consecutive failure,
// radius 2 from the third -- so the common no-symbol-in-shot frame stays two cheap attempts,
// and a hand jitter or a walk-in never pays the full ladder. The counter is the decoder's own:
// it is what the scanner means by "a scan that is not locking on".

import com.google.zxing.BarcodeFormat
import com.google.zxing.EncodeHintType
import com.google.zxing.qrcode.QRCodeWriter
import com.google.zxing.qrcode.decoder.ErrorCorrectionLevel
import java.io.File
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Test

class FrameDecoderLadderTest {

    private fun capture(name: String): ByteArray =
        File("android/app/src/test/resources/$name").let { direct ->
            if (direct.isFile) direct.readBytes()
            else File("src/test/resources/$name").readBytes()
        }

    private fun blank() = ByteArray(1280 * 720) { 0xF0.toByte() }

    /** A clean, seamless symbol in a 1280x720 frame, FrameDecoderTest's shape. */
    private fun cleanFrame(): ByteArray {
        val payload = buildString { while (length < 133) append("swarm-pair_A9zXq7-") }.take(133)
        val symbol = QRCodeWriter().encode(
            payload, BarcodeFormat.QR_CODE, 220, 220,
            mapOf(
                EncodeHintType.ERROR_CORRECTION to ErrorCorrectionLevel.L,
                EncodeHintType.MARGIN to 4,
            ),
        )
        val data = ByteArray(1280 * 720) { 0xF0.toByte() }
        val left = (1280 - symbol.width) / 2
        val top = (720 - symbol.height) / 2
        for (y in 0 until symbol.height) {
            for (x in 0 until symbol.width) {
                if (symbol.get(x, y)) data[(top + y) * 1280 + (left + x)] = 0x10
            }
        }
        return data
    }

    @Test
    fun the_field_capture_decodes_at_camera_scale_once_the_ladder_escalates() {
        val luma = capture("field-capture-vscode-170x168.luma")
        val d = FrameDecoder()
        // Two failing frames build the escalation the third one spends.
        assertNull(d.payload(luma, 170, 170, 168))
        assertNull(d.payload(luma, 170, 170, 168))
        val got = d.payload(luma, 170, 170, 168)
        assertNotNull(
            "the owner's capture at camera scale never decodes: the ladder is not running " +
                "or radius 1 is not in it",
            got,
        )
        assertEquals(
            "the capture decoded before morphology, so this fixture no longer exercises " +
                "the ladder and the fixture or the escalation gate has drifted",
            3,
            d.decodedOnAttempt,
        )
    }

    @Test
    fun the_field_capture_decodes_at_native_scale_on_the_deep_rung() {
        val luma = capture("field-capture-vscode-680x674.luma")
        val d = FrameDecoder()
        assertNull(d.payload(luma, 680, 680, 674))
        assertNull(d.payload(luma, 680, 680, 674))
        assertNull("radius 1 filled a 3px gap, which a radius-1 close cannot do; if this " +
            "now decodes the fixture has changed", d.payload(luma, 680, 680, 674))
        val got = d.payload(luma, 680, 680, 674)
        assertNotNull(
            "the owner's native capture never decodes: radius 2 is not in the ladder",
            got,
        )
        assertEquals(5, d.decodedOnAttempt)
    }

    @Test
    fun a_clean_frame_still_costs_one_attempt_and_no_morphology() {
        // The regress-nothing property: plain runs first, so everything that decoded before
        // the ladder decodes identically with it.
        val d = FrameDecoder()
        // Build the escalation state a failing scan would have, then hand it a clean frame.
        assertNull(d.payload(blank(), 1280, 1280, 720))
        assertNull(d.payload(blank(), 1280, 1280, 720))
        assertNull(d.payload(blank(), 1280, 1280, 720))
        assertNotNull(d.payload(cleanFrame(), 1280, 1280, 720))
        assertEquals("a clean symbol paid for morphology it did not need", 1, d.decodedOnAttempt)
    }

    @Test
    fun the_first_failing_frame_stays_cheap() {
        val d = FrameDecoder()
        assertNull(d.payload(blank(), 1280, 1280, 720))
        assertEquals(
            "a single failing frame already paid the ladder: the escalation gate is not " +
                "gating, and the common no-symbol frame costs six decodes instead of two",
            2,
            d.attempts,
        )
    }

    @Test
    fun a_success_resets_the_escalation() {
        val d = FrameDecoder()
        assertNull(d.payload(blank(), 1280, 1280, 720))
        assertNull(d.payload(blank(), 1280, 1280, 720))
        assertNull(d.payload(blank(), 1280, 1280, 720))
        assertNotNull(d.payload(cleanFrame(), 1280, 1280, 720))
        assertNull(d.payload(blank(), 1280, 1280, 720))
        assertEquals(
            "the failure streak survived a success, so one lost frame after a decode pays " +
                "the whole ladder",
            2,
            d.attempts,
        )
    }
}
