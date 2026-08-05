package dev.swarm.phone.scan

import com.google.zxing.BarcodeFormat
import com.google.zxing.EncodeHintType
import com.google.zxing.common.BitMatrix
import com.google.zxing.qrcode.QRCodeWriter
import com.google.zxing.qrcode.decoder.ErrorCorrectionLevel
import java.util.Locale
import org.junit.Assert.assertEquals
import org.junit.Test

/**
 * A capability bench for [FrameDecoder]: where exactly does the ZXing seam stop decoding?
 *
 * The field symptom is that a stock phone scanner reads the terminal symbol and ours never
 * does, at any distance. [FrameDecoderTest] already proves the decoder reads a CLEAN symbol,
 * so the clean case is not the question. The question is what the terminal actually puts on
 * the glass, and the suspect is line leading: internal/remote/qrterm draws one character cell
 * per TWO module rows with half-block glyphs, so if the glyph ink stops short of the cell box
 * a thin LIGHT seam runs through every dark module at every second module row. ZXing's finder
 * scan counts 1:1:3:1:1 run lengths down a column with no morphological closing; extra
 * transitions inside a run are not something it forgives.
 *
 * This class is a BENCH, not a gate. It sweeps px/module against seam width against vertical
 * stretch, prints the resulting capability map, and asserts exactly one cell -- the clean
 * 4px/module frame the existing test already covers -- so a shifted ZXing tolerance shows up
 * as a changed table rather than as a red build nobody can act on.
 *
 * What it cannot say: anything about optics. Focus, motion blur, moire against the panel's
 * pixel grid and the camera's own binarization are all outside a synthetic frame. If the map
 * below shows seams NOT breaking the decoder, that is the finding, and it moves the
 * investigation to the camera pipeline instead.
 *
 * Seam phase is fixed: module row 0 is assumed to be the top half of a character cell, which
 * is what an even quiet zone (qrterm draws 2 or 4) produces. An odd quiet zone shifts every
 * seam by one module row.
 */
class FrameDecoderCapabilityBenchTest {

    /** The shape of a real pairing payload: qr.go's 133-character ceiling, base64url charset. */
    private val payload = buildString {
        while (length < 133) append("swarm-pair_A9zXq7-")
    }.take(133)

    /**
     * The raw module grid, one pixel per module: MARGIN 0 drops the quiet zone and a 1x1
     * request makes QRCodeWriter fall back to the symbol's own size rather than scaling it.
     * Scaling is done here instead, because px/module is the axis under test.
     */
    private val modules: BitMatrix = QRCodeWriter().encode(
        payload,
        BarcodeFormat.QR_CODE,
        1,
        1,
        mapOf(
            EncodeHintType.ERROR_CORRECTION to ErrorCorrectionLevel.L,
            EncodeHintType.MARGIN to 0,
        ),
    )

    private data class Cell(val px: Int, val gap: Int, val stretch: Double)

    private class Frame(val data: ByteArray, val width: Int, val height: Int)

    private val pxPerModule = listOf(2, 3, 4, 5, 6)
    private val gaps = listOf(0, 1, 2, 3)
    private val stretches = listOf(1.0, 1.15)

    @Test
    fun capability_map_and_mitigation_rescue() {
        val cells = stretches.flatMap { s -> pxPerModule.flatMap { p -> gaps.map { g -> Cell(p, g, s) } } }
        val frames = cells.associateWith { render(it) }

        println("")
        println("FrameDecoder capability bench")
        println(
            "  symbol ${modules.width} modules, ECC L, ${payload.length}-char payload, " +
                "frames ${FRAME_WIDTH}x$FRAME_HEIGHT, polarity normal",
        )
        println("  seam: light rows cut through the symbol at every 2-module (character cell) boundary")

        val variants = listOf<Pair<String, (Frame) -> Frame>>(
            "baseline" to { f -> f },
            "vmin r=2" to { f -> verticalMin(f, 2) },
            "down 2x2" to { f -> downscale2x2(f) },
            "vmin + down" to { f -> downscale2x2(verticalMin(f, 2)) },
            "vclose r=1" to { f -> verticalMax(verticalMin(f, 1), 1) },
            "vclose r=2" to { f -> verticalMax(verticalMin(f, 2), 2) },
            "vclose r=1+down" to { f -> downscale2x2(verticalMax(verticalMin(f, 1), 1)) },
            "vclose r=2+down" to { f -> downscale2x2(verticalMax(verticalMin(f, 2), 2)) },
        )

        val maps = LinkedHashMap<String, Map<Cell, Boolean>>()
        for ((name, transform) in variants) {
            val started = System.currentTimeMillis()
            val result = cells.associateWith { cell ->
                val f = transform(frames.getValue(cell))
                FrameDecoder().payload(f.data, f.width, f.width, f.height) == payload
            }
            maps[name] = result
            printMap(name, result, System.currentTimeMillis() - started)
        }

        val baseline = maps.getValue("baseline")
        println("")
        println("  rescue summary (against baseline)")
        for ((name, result) in maps) {
            if (name == "baseline") continue
            val rescued = cells.filter { !baseline.getValue(it) && result.getValue(it) }
            val broken = cells.filter { baseline.getValue(it) && !result.getValue(it) }
            println(
                String.format(
                    Locale.ROOT,
                    "  %-14s rescued %2d of %2d failing, broke %2d of %2d passing",
                    name,
                    rescued.size,
                    cells.count { !baseline.getValue(it) },
                    broken.size,
                    cells.count { baseline.getValue(it) },
                ),
            )
            if (rescued.isNotEmpty()) println("      rescues: " + rescued.joinToString(" ") { label(it) })
            if (broken.isNotEmpty()) println("      breaks:  " + broken.joinToString(" ") { label(it) })
        }
        println("")

        // The only assertion: the floor FrameDecoderTest already holds. Everything above is
        // measurement, and measurement that fails the build stops being measurement.
        assertEquals(true, baseline.getValue(Cell(px = 4, gap = 0, stretch = 1.0)))
    }

    private fun label(c: Cell) = String.format(Locale.ROOT, "[%dpx/g%d/s%.2f]", c.px, c.gap, c.stretch)

    private fun printMap(name: String, result: Map<Cell, Boolean>, millis: Long) {
        println("")
        println(String.format(Locale.ROOT, "  %s  (%d ms for %d frames)", name, millis, result.size))
        println("    px/mod  stretch | " + gaps.joinToString("  ") { "gap=$it" })
        for (s in stretches) {
            for (p in pxPerModule) {
                val row = gaps.joinToString("  ") { g ->
                    if (result.getValue(Cell(p, g, s))) " ok  " else " --  "
                }
                println(String.format(Locale.ROOT, "    %5d   %5.2f  | %s", p, s, row))
            }
        }
    }

    /**
     * A luminance frame as the analyzer delivers one, with the symbol drawn at [Cell.px]
     * pixels per module, its height scaled by [Cell.stretch], and [Cell.gap] rows of
     * background cut through it at every character-cell boundary.
     */
    private fun render(cell: Cell): Frame {
        val n = modules.width
        val symbolWidth = n * cell.px
        val symbolHeight = (n * cell.px * cell.stretch).toInt()
        val data = ByteArray(FRAME_WIDTH * FRAME_HEIGHT) { LIGHT }
        val left = (FRAME_WIDTH - symbolWidth) / 2
        val top = (FRAME_HEIGHT - symbolHeight) / 2
        for (y in 0 until symbolHeight) {
            val moduleY = y * n / symbolHeight
            val rowBase = (top + y) * FRAME_WIDTH + left
            for (x in 0 until symbolWidth) {
                if (modules.get(x / cell.px, moduleY)) data[rowBase + x] = DARK
            }
        }
        if (cell.gap > 0) {
            var moduleRow = 2
            while (moduleRow < n) {
                // The cell boundary in frame rows, with the cut centred on it.
                val boundary = top + moduleRow * symbolHeight / n
                for (d in 0 until cell.gap) {
                    val y = boundary - cell.gap / 2 + d
                    if (y >= top && y < top + symbolHeight) {
                        java.util.Arrays.fill(data, y * FRAME_WIDTH + left, y * FRAME_WIDTH + left + symbolWidth, LIGHT)
                    }
                }
                moduleRow += 2
            }
        }
        return Frame(data, FRAME_WIDTH, FRAME_HEIGHT)
    }

    /**
     * Candidate (a): every pixel takes the darkest luminance within [radius] rows. Dark ink is
     * low luminance, so a vertical min-filter grows dark downward and upward and swallows a
     * seam thinner than 2*radius -- at the price of fattening every dark module by the same
     * amount, which is why it is measured rather than assumed.
     */
    private fun verticalMin(f: Frame, radius: Int) = vertical(f, radius, keepDarker = true)

    /**
     * The dilation's other half: taken after [verticalMin] it is a true morphological close,
     * which fills any light run up to 2*radius rows and leaves everything wider alone. The
     * radius is therefore load-bearing in BOTH directions -- once 2*radius reaches a module's
     * height in pixels the close stops distinguishing a seam from a genuine light module row
     * and eats the symbol, so it is swept rather than picked.
     */
    private fun verticalMax(f: Frame, radius: Int) = vertical(f, radius, keepDarker = false)

    private fun vertical(f: Frame, radius: Int, keepDarker: Boolean): Frame {
        val out = ByteArray(f.data.size)
        for (y in 0 until f.height) {
            val lo = maxOf(0, y - radius)
            val hi = minOf(f.height - 1, y + radius)
            for (x in 0 until f.width) {
                var v = f.data[lo * f.width + x].toInt() and 0xFF
                for (yy in lo + 1..hi) {
                    val p = f.data[yy * f.width + x].toInt() and 0xFF
                    v = if (keepDarker) minOf(v, p) else maxOf(v, p)
                }
                out[y * f.width + x] = v.toByte()
            }
        }
        return Frame(out, f.width, f.height)
    }

    /**
     * Candidate (b): a 2x2 box average. A one-pixel seam becomes a mid-grey the binarizer can
     * read as dark again, and the frame the detector walks shrinks to a quarter -- but so does
     * px/module, so it can only help where there is resolution to spend.
     */
    private fun downscale2x2(f: Frame): Frame {
        val w = f.width / 2
        val h = f.height / 2
        val out = ByteArray(w * h)
        for (y in 0 until h) {
            for (x in 0 until w) {
                val a = f.data[(2 * y) * f.width + 2 * x].toInt() and 0xFF
                val b = f.data[(2 * y) * f.width + 2 * x + 1].toInt() and 0xFF
                val c = f.data[(2 * y + 1) * f.width + 2 * x].toInt() and 0xFF
                val d = f.data[(2 * y + 1) * f.width + 2 * x + 1].toInt() and 0xFF
                out[y * w + x] = ((a + b + c + d) / 4).toByte()
            }
        }
        return Frame(out, w, h)
    }

    private companion object {
        const val FRAME_WIDTH = 1280
        const val FRAME_HEIGHT = 720
        const val LIGHT = 0xF0.toByte()
        const val DARK: Byte = 0x10
    }
}
