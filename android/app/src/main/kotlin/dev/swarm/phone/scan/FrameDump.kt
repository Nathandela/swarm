package dev.swarm.phone.scan

/**
 * One analysis frame as a file a human can open (agents-tracker-av7k).
 *
 * WHY THE PRODUCT CARRIES A DIAGNOSTIC AT ALL. The scanner does not lock onto the terminal
 * symbol on the owner's handset, and the investigation has four live explanations -- half-block
 * seams cutting every second module row, exposure bloom off the monitor, undersampling, and
 * CameraX granting a resolution nobody asked for. Every one of them is a claim about what the
 * analysis buffer HOLDS, and nothing in this repository has ever seen one:
 * [FrameDecoderCapabilityBenchTest] sweeps synthetic frames and can say what the decoder
 * survives, never which of them the handset produces. One real frame on disk decides between
 * four hypotheses that no further reasoning can separate.
 *
 * PGM (P5) BECAUSE THE FRAME ALREADY IS ONE. The Y plane of a YUV_420_888 buffer is an 8-bit
 * greyscale raster, so a fourteen-byte header in front of the rows is a LOSSLESS copy -- no
 * encoder, no new dependency, and nothing in the path that could resample the evidence on its
 * way to disk. A PNG would need a compressor and a colour model; a JPEG would answer the
 * question about seams and blur by introducing its own.
 *
 * THE STRIDE IS THE WHOLE OF THE WORK, and getting it wrong is worse here than anywhere else in
 * the pipeline. A camera pads its rows to a hardware alignment; a dump that copied the buffer
 * verbatim would shear the image by the padding, and a sheared image looks like exactly the
 * periodic artefact this file exists to test for. A diagnostic that produces a convincing wrong
 * answer is worse than one that produces none.
 */
object FrameDump {

    /**
     * The luminance plane as a P5 raster, or an EMPTY array where the geometry cannot describe
     * an image.
     *
     * REFUSED RATHER THAN THROWN. This runs on the analysis executor beside the decode, and a
     * debug affordance that can take the scanner down is worse than one that declines: the
     * caller writes nothing and logs that it wrote nothing.
     *
     * The height is clamped to the rows the buffer actually holds, which is [FrameDecoder]'s own
     * guard and is here for the same reason one layer on -- a header claiming rows the file does
     * not contain produces something a reader either rejects or pads with garbage a human then
     * reads as sensor noise.
     */
    fun pgm(luma: ByteArray, stride: Int, width: Int, height: Int): ByteArray {
        if (stride <= 0 || width <= 0 || width > stride) return ByteArray(0)
        val rows = minOf(height, luma.size / stride)
        if (rows <= 0) return ByteArray(0)

        val header = "P5\n$width $rows\n255\n".toByteArray(Charsets.US_ASCII)
        val out = ByteArray(header.size + width * rows)
        header.copyInto(out)
        for (y in 0 until rows) {
            luma.copyInto(
                destination = out,
                destinationOffset = header.size + y * width,
                startIndex = y * stride,
                endIndex = y * stride + width,
            )
        }
        return out
    }
}
