package dev.swarm.phone.scan

import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * FAILING-FIRST (TDD RED, GG-5) for agents-tracker-av7k's frame dump.
 *
 * WHY THIS FILE EXISTS AT ALL, WHICH IS AN ARGUMENT ABOUT EVIDENCE RATHER THAN ABOUT CODE. The
 * investigation into the scanner that never locks on has four live hypotheses -- half-block
 * seams, exposure bloom off the monitor, undersampling, and CameraX granting a size nobody
 * asked for -- and every one of them is a claim about WHAT THE ANALYSIS BUFFER ACTUALLY HOLDS.
 * Nothing in this repository has ever seen one. [FrameDecoderCapabilityBenchTest] sweeps
 * SYNTHETIC frames and says what the decoder can survive; it cannot say which of them the
 * handset produces. One real frame on disk answers that in a way no further reasoning can.
 *
 * PGM (P5) IS THE FORMAT BECAUSE THE FRAME IS ALREADY THIS. The Y plane of a YUV_420_888 buffer
 * IS an 8-bit greyscale raster, so a P5 header in front of the rows is a lossless copy with no
 * encoder, no dependency and nothing that could quietly resample the evidence -- and every image
 * viewer and every Python line that would look at it reads P5.
 *
 * THE STRIDE IS THE WHOLE OF THE WORK. A camera pads its rows to a hardware alignment, and a
 * dump that copied the buffer verbatim would produce an image sheared by the padding -- which
 * looks exactly like the seam artefact this dump exists to test for. That is the worst possible
 * failure for a diagnostic: not an absent answer but a convincing wrong one.
 */
class FrameDumpTest {

    /** The P5 header as a reader parses it: magic, width, height, maxval, then the raster. */
    private fun header(pgm: ByteArray): String = String(pgm.copyOfRange(0, pgm.raster()))

    /** Where the raster starts: after the third newline of the P5 header. */
    private fun ByteArray.raster(): Int {
        var seen = 0
        forEachIndexed { i, b ->
            if (b == '\n'.code.toByte()) {
                seen++
                if (seen == 3) return i + 1
            }
        }
        return 0
    }

    @Test
    fun writes_a_P5_header_naming_the_frame_the_decoder_was_given() {
        val pgm = FrameDump.pgm(ByteArray(6 * 2), stride = 6, width = 4, height = 2)

        assertEquals("P5\n4 2\n255\n", header(pgm))
    }

    @Test
    fun drops_the_row_padding_rather_than_writing_a_sheared_image() {
        // Rows of four image bytes in a six-byte stride. A verbatim copy would put two padding
        // bytes into every row and shift the image by two pixels per line -- a shear that reads
        // as exactly the diagonal artefact this dump is meant to rule in or out.
        val luma = ByteArray(6 * 3)
        for (y in 0 until 3) {
            for (x in 0 until 4) luma[y * 6 + x] = (10 * y + x).toByte()
            for (x in 4 until 6) luma[y * 6 + x] = 0x55
        }

        val pgm = FrameDump.pgm(luma, stride = 6, width = 4, height = 3)

        assertEquals("P5\n4 3\n255\n", header(pgm))
        assertArrayEquals(
            byteArrayOf(0, 1, 2, 3, 10, 11, 12, 13, 20, 21, 22, 23),
            pgm.copyOfRange(pgm.raster(), pgm.size),
        )
    }

    @Test
    fun a_short_buffer_shortens_the_image_rather_than_the_truth() {
        // The same guard `FrameDecoder` applies: what is on disk must be what was decoded. A
        // header claiming rows the buffer never held produces a file that fails to open, or
        // worse, one a lenient reader pads with garbage and a human reads as sensor noise.
        val luma = ByteArray(6 * 2)

        val pgm = FrameDump.pgm(luma, stride = 6, width = 4, height = 720)

        assertEquals("P5\n4 2\n255\n", header(pgm))
        assertEquals(4 * 2, pgm.size - pgm.raster())
    }

    @Test
    fun geometry_that_cannot_be_an_image_writes_no_file_at_all() {
        // Refused rather than thrown: this runs on the analysis executor, beside the decode, and
        // a debug affordance that can take the scanner down is worse than one that declines.
        // An empty result is the caller's signal to write nothing and say so.
        assertEquals(0, FrameDump.pgm(ByteArray(16), stride = 0, width = 4, height = 4).size)
        assertEquals(0, FrameDump.pgm(ByteArray(16), stride = 2, width = 4, height = 4).size)
        assertEquals(0, FrameDump.pgm(ByteArray(0), stride = 4, width = 4, height = 4).size)
    }

    @Test
    fun a_real_analysis_geometry_round_trips_byte_for_byte() {
        // 1280x720 with the 64-byte padding a real Y plane carries (the shape
        // `FrameDecoderTest.reads_rows_by_stride_and_never_by_width` uses, because it is the
        // shape that shipped a bug on Motorola handsets).
        val stride = 1280 + 64
        val luma = ByteArray(stride * 720) { (it % 251).toByte() }

        val pgm = FrameDump.pgm(luma, stride = stride, width = 1280, height = 720)
        val raster = pgm.copyOfRange(pgm.raster(), pgm.size)

        assertEquals(1280 * 720, raster.size)
        assertTrue(
            "a row of the dumped raster is not the row the decoder read",
            (0 until 720).all { y ->
                raster.copyOfRange(y * 1280, (y + 1) * 1280)
                    .contentEquals(luma.copyOfRange(y * stride, y * stride + 1280))
            },
        )
    }
}
