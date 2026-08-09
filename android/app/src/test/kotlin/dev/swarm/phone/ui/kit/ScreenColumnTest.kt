package dev.swarm.phone.ui.kit

import android.content.Context
import android.view.ViewGroup
import android.widget.LinearLayout
import androidx.test.core.app.ApplicationProvider
import dev.swarm.phone.theme.SwarmTheme
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotEquals
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import kotlin.math.roundToInt

/**
 * FAILING-FIRST (TDD RED, GG-5) for agents-tracker-nx44.1's first item: derivation row 18's
 * `.pair` padding -- `space_10` vertical x `space_24` horizontal -- which `swarm_space_24`
 * (dimens.xml) has carried since S22b with no caller. `ui/screens` is fenced against
 * `R.dimen`/`setPadding` (android/gate/s24_screens_test.go's PB-DS-6 scan), so a screen cannot
 * spend the row's own padding directly; this is the kit factory that does, on `sessionList`'s
 * precedent (SessionRow.kt: "a screen never types the 12 dp side padding or the gap between
 * rows").
 */
@RunWith(RobolectricTestRunner::class)
class ScreenColumnTest {

    private val context: Context
        get() = SwarmTheme.applyTo(ApplicationProvider.getApplicationContext())

    private fun dimenPx(name: String): Int {
        val id = context.resources.getIdentifier(name, "dimen", context.packageName)
        assertNotEquals("R.dimen.$name is not in the merged resource table", 0, id)
        return context.resources.getDimension(id).roundToInt()
    }

    /** Row 18: "padding `space_10` vertical x `space_24` horizontal". */
    @Test
    fun `the screen column spends row 18's own padding`() {
        val column = screenColumn(context)
        val vertical = dimenPx("swarm_space_10")
        val horizontal = dimenPx("swarm_space_24")

        val claims = listOf(
            Claim("row 18 padding-x (start)", horizontal, column.paddingStart),
            Claim("row 18 padding-x (end)", horizontal, column.paddingEnd),
            Claim("row 18 padding-y (top)", vertical, column.paddingTop),
            Claim("row 18 padding-y (bottom)", vertical, column.paddingBottom),
        )

        assertEquals(mismatches(claims).joinToString("\n"), emptyList<String>(), mismatches(claims))
    }

    @Test
    fun `the screen column is a vertical column that fills its screen's width`() {
        val column = screenColumn(context)

        assertEquals(LinearLayout.VERTICAL, column.orientation)
        assertEquals(ViewGroup.LayoutParams.MATCH_PARENT, column.layoutParams.width)
        assertEquals(ViewGroup.LayoutParams.WRAP_CONTENT, column.layoutParams.height)
    }
}
