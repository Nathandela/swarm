package dev.swarm.phone.ui.kit

import android.content.Context
import android.content.res.Configuration
import android.graphics.Color
import android.view.View
import android.view.ViewGroup
import android.widget.ImageView
import android.widget.TextView
import androidx.core.widget.TextViewCompat
import androidx.test.core.app.ApplicationProvider
import dev.swarm.phone.R
import dev.swarm.phone.theme.SwarmTheme
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import kotlin.math.roundToInt

/** Failing-first contract for the two Signal Field primitives approved for frames one and three. */
@RunWith(RobolectricTestRunner::class)
class SignalFieldTest {

    private val context: Context
        get() = SwarmTheme.applyTo(ApplicationProvider.getApplicationContext())

    private fun View.flatten(): List<View> = when (this) {
        is ViewGroup -> listOf(this) + (0 until childCount).flatMap { getChildAt(it).flatten() }
        else -> listOf(this)
    }

    @Test
    fun `the welcome mark is the approved atmospheric raster and remains decorative`() {
        val mark = signalFieldMark(context)

        assertEquals(ImageView.ScaleType.FIT_CENTER, mark.scaleType)
        assertEquals(150f, mark.layoutParams.height / context.resources.displayMetrics.density, 0.01f)
        assertEquals(0.72f, mark.alpha, 0.001f)
        assertNull("the atmospheric mark must float on the window ground", mark.background)
        assertEquals(
            "the welcome installed a drawable other than the approved atmospheric raster",
            context.getDrawable(R.drawable.swarm_atmospheric_mark)?.constantState,
            mark.drawable?.constantState,
        )
        assertFalse(mark.isClickable)
        assertFalse(mark.isFocusable)
        assertEquals(View.IMPORTANT_FOR_ACCESSIBILITY_NO, mark.importantForAccessibility)
    }

    @Test
    fun `the trust sequence is six ordered beacons on one static path`() {
        val symbols = listOf("owl", "microscope", "galaxy", "anchor", "lock", "key")
        val sequence = sasSequence(context, symbols)
        val descendants = sequence.flatten()
        val tiles = descendants.filterIsInstance<TextView>()

        assertEquals(symbols, tiles.map { it.text.toString() })
        assertEquals("Verification symbols, in order: ${symbols.joinToString(", ")}", sequence.contentDescription)
        assertEquals(View.IMPORTANT_FOR_ACCESSIBILITY_YES, sequence.importantForAccessibility)
        val path = descendants.mapNotNull { it.background as? SignalPathDrawable }.single()
        assertEquals(0, Color.alpha(path.clearInk))
        assertEquals(Color.red(path.spec.ink), Color.red(path.clearInk))
        assertEquals(Color.green(path.spec.ink), Color.green(path.clearInk))
        assertEquals(Color.blue(path.spec.ink), Color.blue(path.clearInk))
        assertTrue("one or more symbol tiles paint no card surface", tiles.all { it.background is SubstrateSurface })
        assertTrue("the six tiles should be decorative children of one spoken sequence", tiles.all {
            it.importantForAccessibility == View.IMPORTANT_FOR_ACCESSIBILITY_NO
        })
        assertTrue(descendants.none { it.isClickable || it.isFocusable })
    }

    @Test
    fun `six trust beacons stay inside a narrow handset`() {
        val largeText = SwarmTheme.applyTo(
            ApplicationProvider.getApplicationContext<Context>().createConfigurationContext(
                Configuration(context.resources.configuration).apply { fontScale = 2f },
            ),
        )
        val symbols = listOf("🐶", "🐱", "🌙", "🔑", "✈️", "🌸")
        val sequence = sasSequence(largeText, symbols)
        // A 320 dp handset leaves 272 dp after the screen's 24 dp side cells.
        val width = (272f * largeText.resources.displayMetrics.density).roundToInt()
        val widthSpec = View.MeasureSpec.makeMeasureSpec(width, View.MeasureSpec.EXACTLY)
        val heightSpec = View.MeasureSpec.makeMeasureSpec(0, View.MeasureSpec.UNSPECIFIED)

        sequence.measure(widthSpec, heightSpec)
        sequence.layout(0, 0, sequence.measuredWidth, sequence.measuredHeight)

        val tiles = sequence.flatten().filterIsInstance<TextView>()
        assertEquals(6, tiles.size)
        assertTrue(tiles.all { it.left >= 0 && it.right <= width && it.measuredWidth > 0 })
        assertTrue("one or more trust glyphs were measured too small", tiles.all {
            it.measuredState and View.MEASURED_STATE_TOO_SMALL == 0
        })
        assertTrue("large-text trust glyphs clip horizontally", tiles.all {
            it.paint.measureText(it.text.toString()) <= it.measuredWidth - it.paddingLeft - it.paddingRight
        })
        assertTrue("large-text trust glyphs clip vertically", tiles.all {
            it.paint.fontMetrics.run { bottom - top } <= it.measuredHeight - it.paddingTop - it.paddingBottom
        })
        assertTrue("the security symbols do not opt into bounded auto-sizing", tiles.all {
            TextViewCompat.getAutoSizeTextType(it) == TextViewCompat.AUTO_SIZE_TEXT_TYPE_UNIFORM
        })
    }

    @Test
    fun `the trust trajectory crosses the centre of every beacon`() {
        val sequence = sasSequence(context, listOf("🐶", "🐱", "🌙", "🔑", "✈️", "🌸"))
        val width = (272f * context.resources.displayMetrics.density).roundToInt()
        sequence.measure(
            View.MeasureSpec.makeMeasureSpec(width, View.MeasureSpec.EXACTLY),
            View.MeasureSpec.makeMeasureSpec(0, View.MeasureSpec.UNSPECIFIED),
        )
        sequence.layout(0, 0, sequence.measuredWidth, sequence.measuredHeight)

        val descendants = sequence.flatten()
        val path = descendants.single { it.background is SignalPathDrawable }
        val tile = descendants.filterIsInstance<TextView>().first()
        val tileRow = tile.parent as View
        assertEquals(
            "the trajectory misses the centre of the trust beacons",
            tileRow.top + tile.top + tile.height / 2,
            path.top + path.height / 2,
        )
    }
}
