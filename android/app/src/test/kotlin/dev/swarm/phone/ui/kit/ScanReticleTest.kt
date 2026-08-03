package dev.swarm.phone.ui.kit

import android.content.Context
import android.graphics.Bitmap
import android.graphics.Canvas
import android.graphics.Rect
import android.graphics.RectF
import android.util.TypedValue
import android.view.MotionEvent
import android.widget.FrameLayout
import androidx.test.core.app.ApplicationProvider
import dev.swarm.phone.theme.SwarmTheme
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotEquals
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.GraphicsMode

/**
 * FAILING-FIRST (TDD RED, GG-5) for PB-DS-10 over §4's `Scanner reticle` row.
 *
 * THE EXPECTED INK IS READ OUT OF THE ROW, not written here, for FocusRingTest's reason: Substrate
 * draws no scanner at all -- row 6's `.qr` is the code TILE, not a viewfinder pointed at one -- so
 * that row is the only place this component is specified, and a suite that transcribed `--p-hero`
 * would agree with itself forever.
 *
 * THE OTHER THREE CLAIMS ARE THE ONES A COLOUR CHECK CANNOT MAKE, and each is a way this component
 * could be exactly right in every value and still be wrong on a handset:
 *
 *   - **It is OPEN.** Four corners are an aiming mark; a closed rectangle is a border. A reticle
 *     that painted the whole frame would satisfy every metric below.
 *   - **It takes no touch.** A view laid over a live camera preview that could take a tap is
 *     PB-SEC-12 clause 1's tapjacking surface, and the row's answer is that this is a `Drawable`
 *     and not a view. That is only true if the thing wearing it still refuses the touch.
 *   - **It fits.** The frame is a fixed 180 dp and the preview is the screen's width, so on a
 *     narrow handset the frame is the larger of the two -- and a reticle drawn outside the preview
 *     is a green line across the rest of the screen.
 */
@RunWith(RobolectricTestRunner::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
class ScanReticleTest {

    private val context: Context
        get() = SwarmTheme.applyTo(ApplicationProvider.getApplicationContext())

    private fun dp(value: Float): Float = TypedValue.applyDimension(
        TypedValue.COMPLEX_UNIT_DIP, value, context.resources.displayMetrics,
    )

    private fun dimen(name: String): Float {
        val id = context.resources.getIdentifier(name, "dimen", context.packageName)
        assertNotEquals("R.dimen.$name is not in the merged resource table", 0, id)
        return context.resources.getDimension(id)
    }

    /** §4's leading cell for this component. */
    private val ROW = "Scanner reticle"

    /** §4's ink cell: `brackets --p-hero`, and the row argues the alternatives out. */
    @Test
    fun `the brackets are the ink the row states`() {
        val stated = KitOrigin.tokenIn(KitOrigin.adjacentRow(ROW), ROW, "brackets")

        assertEquals(KitOrigin.token(stated), scanReticle(context).spec.ink)
    }

    /**
     * §4's geometry cell: `frame 180`, `stroke 2`, `arm 24`, radius `--p-card-r` 9.
     *
     * THE THREE LENGTHS ARE THE KIT'S CONSTANTS and the radius is not, which is the same split
     * FocusRingTest makes: `s23_kit_test.go` holds each constant to the field of the row it cites,
     * so reading them here is reading the row one indirection out -- while `--p-card-r` is a TOKEN
     * with a resource, so it is resolved off the merged table rather than converted from a number.
     */
    @Test
    fun `the reticle is the geometry the row states`() {
        val spec = scanReticle(context).spec
        val claims = listOf(
            Claim("the row's stroke", dp(KitMetrics.RETICLE_STROKE_DP), spec.strokePx),
            Claim("the row's frame", dp(KitMetrics.RETICLE_FRAME_DP), spec.framePx),
            Claim("the row's arm", dp(KitMetrics.RETICLE_ARM_DP), spec.armPx),
            Claim("row 6's `--p-card-r`", dimen("swarm_radius_card"), spec.radiusPx),
        )

        assertEquals(mismatches(claims).joinToString("\n"), emptyList<String>(), mismatches(claims))
    }

    /**
     * The row's "the middle of every edge stays open", as the thing that makes it a reticle.
     *
     * Asserted as ink PRESENT at the four corners and ABSENT at the four edge midpoints and at the
     * centre. No pixel's colour or position is the subject -- what is being asked is whether the
     * frame is closed, which is the one drawing mistake that passes every metric above.
     */
    @Test
    fun `the frame is four corners and not a box`() {
        val box = (dp(KitMetrics.RETICLE_FRAME_DP) * 2f).toInt()
        val painted = paint(box)
        val frame = frameIn(box)
        val arm = dp(KitMetrics.RETICLE_ARM_DP)

        val faults = mutableListOf<String>()
        corners(frame, arm).forEach { (where, probe) ->
            if (!hasInk(painted, probe)) faults += "the $where corner painted nothing at all"
        }
        edgeMidpoints(frame, arm).forEach { (where, probe) ->
            if (hasInk(painted, probe)) {
                faults += "the middle of the $where edge is painted, so this is a closed " +
                    "rectangle -- a border rather than an aiming mark"
            }
        }
        if (hasInk(painted, RectF(frame.centerX() - arm, frame.centerY() - arm, frame.centerX() + arm, frame.centerY() + arm))) {
            faults += "the middle of the frame is painted, so the reticle covers the code it frames"
        }

        assertEquals(faults.joinToString("\n"), emptyList<String>(), faults)
    }

    /**
     * The row's "clamped to the preview when the preview is the smaller of the two".
     *
     * A 180 dp frame does not fit inside every preview this can be given -- the preview is the
     * screen's width less the pairing scaffold's own padding, and on a narrow handset that is under
     * 180. Asserted from the BOUNDS' corners rather than the frame's: if the reticle had kept its
     * stated size it would have painted outside them, and the canvas would have clipped away the
     * evidence at the corners rather than at the middle.
     */
    @Test
    fun `the reticle shrinks to a preview smaller than its frame`() {
        val box = (dp(KitMetrics.RETICLE_FRAME_DP) / 2f).toInt()
        val painted = paint(box)
        val arm = dp(KitMetrics.RETICLE_ARM_DP)
        val edge = RectF(0f, 0f, box.toFloat(), box.toFloat())

        val faults = corners(edge, arm).mapNotNull { (where, probe) ->
            if (hasInk(painted, probe)) null else "the $where corner of a preview smaller than " +
                "the frame is unpainted, so the reticle was drawn off the preview and clipped"
        }

        assertEquals(faults.joinToString("\n"), emptyList<String>(), faults)
    }

    /**
     * The row's "it is not a control", measured rather than asserted about the type.
     *
     * A FOREGROUND DRAWABLE CANNOT CONSUME A TOUCH, and that is the whole reason the row specifies
     * a drawable: `PreviewView` is a `FrameLayout`, so this is the shape the reticle is actually
     * carried in, and a view added over the preview instead would have been one `isClickable` away
     * from the surface PB-SEC-12 clause 1 is about.
     */
    @Test
    fun `a preview wearing the reticle does not take the touch`() {
        val size = dp(KitMetrics.RETICLE_FRAME_DP).toInt()
        val host = FrameLayout(context)
        host.foreground = scanReticle(context)
        host.measure(size, size)
        host.layout(0, 0, size, size)

        val down = MotionEvent.obtain(
            0L, 0L, MotionEvent.ACTION_DOWN, size / 2f, size / 2f, 0,
        )
        val consumed = try {
            host.dispatchTouchEvent(down)
        } finally {
            down.recycle()
        }

        assertFalse(
            "the view carrying the reticle consumed a touch aimed at the preview under it",
            consumed,
        )
        assertFalse("the reticle made its host clickable", host.isClickable)
        assertFalse("the reticle made its host focusable", host.isFocusable)
    }

    /** The negative control PB-DS-10 requires, through the comparisons the claims above use. */
    @Test
    fun `the reticle assertions can actually fail`() {
        val ink = KitOrigin.token(KitOrigin.tokenIn(KitOrigin.adjacentRow(ROW), ROW, "brackets"))

        assertTrue(
            "an ink one unit from the row's passes the comparison",
            mismatches(listOf(Claim("ink", ink, ink + 1))).isNotEmpty(),
        )
        assertTrue(
            "a frame one pixel from the row's passes the comparison",
            mismatches(
                listOf(
                    Claim(
                        "frame",
                        dp(KitMetrics.RETICLE_FRAME_DP),
                        dp(KitMetrics.RETICLE_FRAME_DP) + 1f,
                    ),
                ),
            ).isNotEmpty(),
        )
        // The openness probe has to be able to see a closed frame, or the claim above passes for a
        // border as readily as for four corners. A rectangle drawn the obvious way is the control.
        val box = (dp(KitMetrics.RETICLE_FRAME_DP) * 2f).toInt()
        val frame = frameIn(box)
        val closed = Bitmap.createBitmap(box, box, Bitmap.Config.ARGB_8888)
        Canvas(closed).drawRect(frame, android.graphics.Paint().apply {
            style = android.graphics.Paint.Style.STROKE
            strokeWidth = dp(KitMetrics.RETICLE_STROKE_DP)
            color = ink
        })
        assertTrue(
            "the openness probe reports no ink at the middle of a closed rectangle's top edge, " +
                "so it could not tell a border from an aiming mark",
            edgeMidpoints(frame, dp(KitMetrics.RETICLE_ARM_DP)).any { hasInk(closed, it.second) },
        )
        // §4 rejects `--p-ink` on scannability: row 6 draws the symbol this frames as a white
        // tile. If the two resolved alike, the ink claim would accept a reticle nobody can see
        // against the thing it is pointed at.
        assertNotEquals(
            "`--p-hero` and `--p-ink` resolve to the same colour, so a white reticle over row " +
                "6's white QR tile would satisfy the row",
            KitOrigin.token("--p-ink"),
            ink,
        )
    }

    // ------------------------------------------------------------------
    // Reading the paint.
    // ------------------------------------------------------------------

    /** The reticle drawn into a square bitmap of [box] pixels a side. */
    private fun paint(box: Int): Bitmap {
        val drawn = scanReticle(context)
        drawn.bounds = Rect(0, 0, box, box)
        val bitmap = Bitmap.createBitmap(box, box, Bitmap.Config.ARGB_8888)
        drawn.draw(Canvas(bitmap))
        return bitmap
    }

    /**
     * Where the row says the frame is, computed here rather than read off the drawable.
     *
     * "frame 180, centred and clamped to the preview when the preview is the smaller of the two"
     * is the specification; asking the component where it drew would be the self-comparison this
     * package's scaffolding exists to prevent.
     */
    private fun frameIn(box: Int): RectF {
        val side = minOf(dp(KitMetrics.RETICLE_FRAME_DP), box.toFloat())
        val reach = side / 2f
        return RectF(box / 2f - reach, box / 2f - reach, box / 2f + reach, box / 2f + reach)
    }

    /** An arm-sized probe at each corner of [frame], named for the corner it sits on. */
    private fun corners(frame: RectF, arm: Float): List<Pair<String, RectF>> = listOf(
        "top-left" to RectF(frame.left, frame.top, frame.left + arm, frame.top + arm),
        "top-right" to RectF(frame.right - arm, frame.top, frame.right, frame.top + arm),
        "bottom-left" to RectF(frame.left, frame.bottom - arm, frame.left + arm, frame.bottom),
        "bottom-right" to RectF(frame.right - arm, frame.bottom - arm, frame.right, frame.bottom),
    )

    /** A probe at the middle of each edge of [frame], clear of both arms that share it. */
    private fun edgeMidpoints(frame: RectF, arm: Float): List<Pair<String, RectF>> {
        val reach = arm / 2f
        return listOf(
            "top" to RectF(frame.centerX() - reach, frame.top - reach, frame.centerX() + reach, frame.top + reach),
            "bottom" to RectF(frame.centerX() - reach, frame.bottom - reach, frame.centerX() + reach, frame.bottom + reach),
            "left" to RectF(frame.left - reach, frame.centerY() - reach, frame.left + reach, frame.centerY() + reach),
            "right" to RectF(frame.right - reach, frame.centerY() - reach, frame.right + reach, frame.centerY() + reach),
        )
    }

    /** Whether any pixel inside [probe] carries ink. Antialiasing makes the value unreadable; the
     * presence of one is not. */
    private fun hasInk(bitmap: Bitmap, probe: RectF): Boolean {
        val left = probe.left.toInt().coerceIn(0, bitmap.width - 1)
        val top = probe.top.toInt().coerceIn(0, bitmap.height - 1)
        val right = probe.right.toInt().coerceIn(left + 1, bitmap.width)
        val bottom = probe.bottom.toInt().coerceIn(top + 1, bitmap.height)
        for (x in left until right) {
            for (y in top until bottom) {
                if (bitmap.getPixel(x, y) != 0) return true
            }
        }
        return false
    }
}
