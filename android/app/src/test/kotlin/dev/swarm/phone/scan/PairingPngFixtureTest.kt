package dev.swarm.phone.scan

import dev.swarm.phone.ui.PairingFlow
import java.util.zip.Inflater
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * THE CROSS-LANGUAGE FENCE: Go writes the pairing PNG, Kotlin decodes it, and no toolchain sees
 * both sides.
 *
 * WHY THE TWO HALVES CANNOT BE ONE TEST. `writePairingPNG` is Go, [FrameDecoder] is Kotlin over
 * ZXing, and the only thing that ever joins them is a person holding a phone in front of a
 * screen. Every test on either side until now built its own subject: the Go side asserts about
 * the symbol it just encoded, and [FrameDecoderTest] asserts about a `QRCodeWriter` symbol it
 * rendered itself. Both can be green while the artifact the product actually emits decodes on
 * nothing -- a quiet zone, a module scale, a polarity, an ECC level or a version bump moves in
 * one encoder and no test in the repository is looking at the join.
 *
 * SO THE ARTIFACT ITSELF IS COMMITTED and each side is pinned to the same bytes.
 * `cmd/swarm/pairpng_fixture_test.go` is the other half: it asserts the committed PNG is exactly
 * what the writer produces for a fixed payload, and regenerating it is deliberate
 * (-update-pairing-fixture). This half asserts the same file decodes to the same string, and the
 * expected payload is READ FROM THE SIDECAR rather than transcribed, so neither side carries a
 * copy of the other's constant. A drift in either encoder fails here, on a JVM, in seconds,
 * instead of on a handset in a field report.
 *
 * THIS IS THE PATH THE PRODUCT PROMISES. `swarm remote pair` prints "Or scan the QR image at:
 * <path>" because the terminal symbol depends on font metrics this product does not control
 * (agents-tracker-av7k): the PNG is the scan target the owner is pointed at. A red here means
 * that promise is broken.
 *
 * WHAT IT STILL CANNOT SAY is what every test in this package cannot say: anything about optics.
 * This is one encoder's output read back through the decoder with no camera, no focus, no
 * monitor and no hand shake in between. PB-E2E-5 stays deferred.
 */
class PairingPngFixtureTest {

    /** The payload the Go fixture recorded beside the image. Never re-spelled here. */
    private val expected: String get() = String(resource("pairing-qr-fixture.payload.txt")).trim()

    private fun resource(name: String): ByteArray {
        val stream = javaClass.getResourceAsStream("/$name")
        assertNotNull(
            "$name is not on the test classpath. It is the committed artifact `swarm remote pair` " +
                "emits, and without it this fence has no subject. It is written by " +
                "`go test ./cmd/swarm/ -run TestPairingPNGFixture -update-pairing-fixture`.",
            stream,
        )
        return stream!!.use { it.readBytes() }
    }

    @Test
    fun the_committed_pairing_png_decodes_to_the_payload_go_encoded() {
        val png = PngGray(resource("pairing-qr-fixture.png"))

        // Native size, width == stride: the image as the file holds it, so a red here is about
        // the SYMBOL and never about how this test packed it into a buffer.
        assertEquals(
            "the PNG `swarm remote pair` tells the user to scan does not decode on this phone's " +
                "decoder. The two encoders have drifted and the product's promised scan target " +
                "is unreadable by the app it exists for",
            expected,
            FrameDecoder().payload(png.pixels, png.width, png.width, png.height),
        )
    }

    @Test
    fun it_decodes_on_the_first_attempt_because_the_symbol_is_dark_on_light() {
        // Polarity, pinned. The inverted retry exists for a TERMINAL whose theme remaps the
        // ground (FrameDecoderTest.decodes_an_inverted_symbol); this artifact is an image the
        // writer paints itself, so it must never need that retry. If it starts needing one the
        // file is being written light-on-dark, and every scanner that does not retry -- which is
        // most of them, including every stock camera app -- has stopped reading it.
        val png = PngGray(resource("pairing-qr-fixture.png"))
        val decoder = FrameDecoder()

        assertEquals(expected, decoder.payload(png.pixels, png.width, png.width, png.height))
        assertEquals(1, decoder.decodedOnAttempt)
    }

    @Test
    fun it_survives_the_row_padding_a_real_analysis_buffer_adds() {
        // THE REAL ARTIFACT THROUGH THE REAL BUFFER SHAPE. `FrameDecoderTest` fences stride
        // handling against a symbol it renders itself; this asks the same question of the file
        // the user actually scans, because the padded Y plane is what reaches the decoder when
        // they do (bitwarden/android 7097 shipped a decoder that read the padding as image).
        //
        // The frame keeps the image's own dimensions rather than being centred in a 1280x720
        // one: at 784 px square the artifact does not fit that buffer, and inventing a frame
        // size no camera delivers would be dressing the test up as something it is not.
        val png = PngGray(resource("pairing-qr-fixture.png"))
        val stride = png.width + 64
        val frame = ByteArray(stride * png.height)
        for (y in 0 until png.height) {
            png.pixels.copyInto(
                destination = frame,
                destinationOffset = y * stride,
                startIndex = y * png.width,
                endIndex = (y + 1) * png.width,
            )
            // Padding is noise and not background, so a decoder that reads it sees torn rows.
            for (x in png.width until stride) frame[y * stride + x] = 0x55
        }

        assertEquals(expected, FrameDecoder().payload(frame, stride, png.width, png.height))
    }

    /**
     * The negative control, and this fence needs one more than most: it was GREEN ON THE FIRST
     * RUN, which is correct for an artifact that already ships and is also exactly how a test
     * that asserts nothing looks.
     *
     * Two ways it could be hollow, both closed here. The reader could be producing something the
     * decoder reads by luck, in which case defacing the symbol would still "decode" -- so a
     * finder pattern is painted out and the decode must go to null. And the sidecar could be
     * empty or junk, in which case the equality above compares two nothings -- so the expected
     * text is put to the app's own question, `PairingFlow.entryCarriesItsOwnRelay`, which is what
     * the pairing screen uses to route a payload to `DecodeQR` rather than to the short-code path.
     *
     * THE PERTURBATION IS IN MEMORY. Nothing here writes to the fixture: a control that edits the
     * committed artifact would leave the next run asserting about the damage.
     */
    @Test
    fun the_fixture_assertions_can_actually_fail() {
        val png = PngGray(resource("pairing-qr-fixture.png"))
        val defaced = png.pixels.copyOf()
        // ZXing locks onto the three finder patterns before it reads a single module. The
        // top-left one sits inside the first 200 px: four quiet-zone modules and seven finder
        // modules at 16 px each.
        for (y in 0 until 200) {
            for (x in 0 until 200) defaced[y * png.width + x] = 0xF0.toByte()
        }

        assertNull(
            "a symbol with its finder pattern painted out still decoded, so the assertions above " +
                "are not reading this file",
            FrameDecoder().payload(defaced, png.width, png.width, png.height),
        )
        assertTrue(
            "the recorded payload is not something this app would route to DecodeQR, so the " +
                "equality above may be comparing two empty strings",
            PairingFlow.entryCarriesItsOwnRelay(expected),
        )
    }
}

/**
 * The committed PNG as a luminance plane.
 *
 * WHY THIS IS HAND-ROLLED AND NOT `ImageIO`. There is no `javax.imageio` and no `java.awt` on an
 * Android unit test's classpath -- `android.jar` supplies the `java.*` surface and Android has
 * never had AWT -- so the obvious three lines do not compile. The alternative was a Robolectric
 * runner with native graphics for `BitmapFactory`, which is a heavier dependency and a slower
 * test for a file this simple to read.
 *
 * AND IT IS SIMPLE BY CONSTRUCTION, WHICH IS THE OTHER HALF OF THE ARGUMENT. `writePairingPNG`
 * builds an `image.NewGray` and hands it to Go's `png.Encode`: 8-bit greyscale, one byte per
 * pixel, no palette, no interlace. That IS a luminance plane, so there is no colour conversion
 * to choose or to get wrong -- the pixel byte is the Y sample the decoder wants. Everything below
 * is the PNG container: inflate the IDATs and undo the per-scanline filters.
 *
 * IT REFUSES ANYTHING ELSE BY NAME. A fixture regenerated as RGB, paletted, 16-bit or interlaced
 * fails here with what it actually is, rather than being read as garbage that happens not to
 * decode -- which would send the next reader looking for a drift in the QR encoder.
 */
private class PngGray(file: ByteArray) {

    val width: Int
    val height: Int
    val pixels: ByteArray

    init {
        require(file.size > 8 && file.copyOfRange(0, 8).contentEquals(SIGNATURE)) {
            "the pairing fixture is not a PNG"
        }
        var width = 0
        var height = 0
        val deflated = mutableListOf<ByteArray>()
        var offset = 8
        while (offset + 8 <= file.size) {
            val length = file.int(offset)
            val type = String(file, offset + 4, 4, Charsets.US_ASCII)
            val data = file.copyOfRange(offset + 8, offset + 8 + length)
            when (type) {
                "IHDR" -> {
                    width = data.int(0)
                    height = data.int(4)
                    require(data[8].toInt() == 8 && data[9].toInt() == 0) {
                        "the pairing fixture is bit depth ${data[8]}, colour type ${data[9]}; this " +
                            "reader handles 8-bit greyscale, which is what image.NewGray writes"
                    }
                    require(data[12].toInt() == 0) { "the pairing fixture is interlaced" }
                }

                "IDAT" -> deflated += data
            }
            offset += 12 + length
            if (type == "IEND") break
        }
        require(width > 0 && height > 0) { "the pairing fixture declares no image size" }

        this.width = width
        this.height = height
        this.pixels = defilter(inflate(deflated.reduce(ByteArray::plus)), width, height)
    }

    private fun inflate(compressed: ByteArray): ByteArray {
        val inflater = Inflater()
        inflater.setInput(compressed)
        // One filter byte per scanline, then one byte per pixel.
        val out = ByteArray((width + 1) * height)
        var written = 0
        while (written < out.size && !inflater.finished()) {
            val n = inflater.inflate(out, written, out.size - written)
            if (n == 0) break
            written += n
        }
        inflater.end()
        require(written == out.size) {
            "the pairing fixture inflated to $written bytes, expected ${out.size}"
        }
        return out
    }

    /**
     * Undo the per-scanline filters (RFC 2083 section 6). Go's encoder picks one PER ROW, so all
     * five arms are reachable on a file nobody chose the filtering of -- and a reader missing one
     * produces a plausible-looking image that decodes to nothing.
     *
     * The byte offset to the pixel on the left is 1 for 8-bit greyscale.
     */
    private fun defilter(raw: ByteArray, width: Int, height: Int): ByteArray {
        val out = ByteArray(width * height)
        for (y in 0 until height) {
            val filter = raw[y * (width + 1)].toInt()
            val row = y * (width + 1) + 1
            for (x in 0 until width) {
                val value = raw[row + x].toInt() and 0xFF
                val left = if (x > 0) out[y * width + x - 1].toInt() and 0xFF else 0
                val up = if (y > 0) out[(y - 1) * width + x].toInt() and 0xFF else 0
                val upLeft = if (x > 0 && y > 0) out[(y - 1) * width + x - 1].toInt() and 0xFF else 0
                val restored = when (filter) {
                    0 -> value
                    1 -> value + left
                    2 -> value + up
                    3 -> value + (left + up) / 2
                    4 -> value + paeth(left, up, upLeft)
                    else -> throw IllegalArgumentException("unknown PNG row filter $filter")
                }
                out[y * width + x] = restored.toByte()
            }
        }
        return out
    }

    private fun paeth(left: Int, up: Int, upLeft: Int): Int {
        val estimate = left + up - upLeft
        val dl = kotlin.math.abs(estimate - left)
        val du = kotlin.math.abs(estimate - up)
        val dul = kotlin.math.abs(estimate - upLeft)
        return if (dl <= du && dl <= dul) left else if (du <= dul) up else upLeft
    }

    private fun ByteArray.int(at: Int): Int =
        ((this[at].toInt() and 0xFF) shl 24) or ((this[at + 1].toInt() and 0xFF) shl 16) or
            ((this[at + 2].toInt() and 0xFF) shl 8) or (this[at + 3].toInt() and 0xFF)

    private companion object {
        val SIGNATURE = byteArrayOf(
            0x89.toByte(), 'P'.code.toByte(), 'N'.code.toByte(), 'G'.code.toByte(),
            0x0D, 0x0A, 0x1A, 0x0A,
        )
    }
}
