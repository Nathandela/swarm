package dev.swarm.phone.ui.kit

import android.content.Context
import android.view.View
import android.widget.LinearLayout
import android.widget.TextView
import androidx.test.core.app.ApplicationProvider
import dev.swarm.phone.theme.SwarmTheme
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotEquals
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertSame
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import kotlin.math.roundToInt

/**
 * FAILING-FIRST (TDD RED, GG-5) for the ONE component the guided pairing screen needs and the kit
 * does not have (agents-tracker-qx9m follow-up: the owner installed the build, found the pairing
 * screen, and it gave them a bare text field with no camera and no instructions).
 *
 * WHY A COMPONENT AND NOT THREE VIEWS IN THE SCREEN. `android/gate/s24_screens_test.go` fences the
 * screens package against `R.dimen.`, `setTextAppearance` and `setTextColor`, so a numbered step
 * built there could carry no gutter, no type and no ink -- it would render as two unstyled
 * TextViews and the fence would pass, because the fence is what stops a screen from choosing. The
 * step is a visual element, PB-DS-6 says every visual element is one factory, and this is it.
 *
 * IT CITES ROW 18 AND CLAIMS NOTHING ROW 18 DOES NOT SAY. The pairing scaffold's cell specifies the
 * body copy (`Body.Message` / `--p-ink2`) and the steps this component spends (`space_18` under a
 * block, `space_8` between a title and what follows it). What row 18 does NOT enumerate is an
 * ORDINAL -- the artifact draws one pairing step at a time and never numbers them -- so the ordinal
 * takes the body's own style rather than a second one invented for it. A gutter with its own type
 * would be a type decision no design source could be asked about.
 *
 * `maxEms = 30` IS DELIBERATELY NOT SPENT HERE, and the omission is recorded rather than silent.
 * Row 18 states it for the pairing BODY, which is `PairingSurface.message` and not this component;
 * spending it here would put the literal 30 in the kit, where
 * `TestPBDS7_EveryNumberInTheKitIsAccountedFor` requires every number to be a `KitMetrics` constant
 * whose `derived:` annotation the metric reader can find in the row. Row 18 writes it as
 * `maxEms=30` inside a prose cell, which that reader does not parse. Reported rather than worked
 * around.
 */
@RunWith(RobolectricTestRunner::class)
class PairingStepRowTest {

    private val context: Context
        get() = SwarmTheme.applyTo(ApplicationProvider.getApplicationContext())

    private fun dimenPx(name: String): Int {
        val id = context.resources.getIdentifier(name, "dimen", context.packageName)
        assertNotEquals("R.dimen.$name is not in the merged resource table", 0, id)
        return context.resources.getDimension(id).roundToInt()
    }

    private fun View.find(tag: String): View? {
        if (this.tag == tag) return this
        if (this !is LinearLayout) return null
        for (i in 0 until childCount) getChildAt(i).find(tag)?.let { return it }
        return null
    }

    // ---- the two cells -----------------------------------------------------

    @Test
    fun `a step carries its ordinal and its line`() {
        val step = pairingStep(context, "1", "On your computer, run")

        assertEquals(
            "1",
            (step.find(KitTag.STEP_ORDINAL) as TextView).text.toString(),
        )
        assertEquals(
            "On your computer, run",
            (step.find(KitTag.STEP_LINE) as TextView).text.toString(),
        )
    }

    @Test
    fun `both cells take row 18's body ink`() {
        // ONE INK AND NOT TWO. A recessive ordinal is the obvious embellishment and it is the one
        // this component must not make: `--p-ink3` is 3.17 to 3.50:1 on every surface in this
        // product, under the 4.5:1 text floor, and the ordinal is the part that says WHICH step a
        // user is on. Row 18's body ink is `--p-ink2` and both cells are body copy.
        val step = pairingStep(context, "2", "It shows a QR code. Scan it below.")

        assertEquals(
            KitOrigin.token("--p-ink2"),
            (step.find(KitTag.STEP_ORDINAL) as TextView).currentTextColor,
        )
        assertEquals(
            KitOrigin.token("--p-ink2"),
            (step.find(KitTag.STEP_LINE) as TextView).currentTextColor,
        )
    }

    @Test
    fun `the ordinal leads the line rather than trailing it`() {
        // The order is the reading order, and it is asserted because a horizontal LinearLayout
        // will happily draw "On your computer, run  1" and look like a component that works.
        val step = pairingStep(context, "1", "On your computer, run")

        assertEquals(LinearLayout.HORIZONTAL, step.orientation)
        assertEquals(KitTag.STEP_ORDINAL, step.getChildAt(0).tag)
    }

    // ---- the detail, which is what makes step 1 a step ----------------------

    @Test
    fun `the detail is hosted under the line and not beside the ordinal`() {
        // THE INDENT IS THE WHOLE REASON THIS PARAMETER EXISTS. The command well belongs to step 1
        // and has to sit inside step 1's text column -- a well hung off the step row itself would
        // start under the ordinal, and the list would stop reading as a list at exactly the point
        // it carries the thing the user has to type.
        val well = monoWell(context, "swarm remote pair")
        val step = pairingStep(context, "1", "On your computer, run", detail = well)

        val line = step.find(KitTag.STEP_LINE)
        assertNotNull("the step lost its line when it acquired a detail", line)

        val column = line!!.parent as LinearLayout
        assertSame(
            "the detail is not in the line's own column, so it is not indented under the step",
            column,
            well.parent,
        )
        assertTrue(
            "the detail was drawn above the line it belongs to",
            column.indexOfChild(well) > column.indexOfChild(line),
        )
    }

    @Test
    fun `a step with no detail draws no empty column beneath its line`() {
        val step = pairingStep(context, "2", "It shows a QR code. Scan it below.")
        val column = step.find(KitTag.STEP_LINE)!!.parent as LinearLayout

        assertEquals(
            "a step with nothing to show under its line drew a slot for one anyway, which is the " +
                "gap that reads as a missing command",
            1,
            column.childCount,
        )
    }

    // ---- spacing, which is row 18's and not this file's ---------------------

    @Test
    fun `the step spends row 18's own steps and no others`() {
        val well = monoWell(context, "swarm remote pair")
        val step = pairingStep(context, "1", "On your computer, run", detail = well)
        // THE GUTTER IS THE COLUMN'S AND NOT THE LINE'S, which is what the first draft of this
        // test got wrong and is worth stating rather than quietly correcting: the gap has to inset
        // everything beside the ordinal, and the command well is the half that matters. Read off
        // the line, the assertion would pass over an implementation that indented the sentence and
        // left the well hanging under the number.
        val column = step.find(KitTag.STEP_LINE)!!.parent as View

        // Row 18: "body margin-bottom `space_18`". One step is one block of body copy, so the air
        // under it is the air the row puts under a body block.
        assertEquals(
            dimenPx("swarm_space_18"),
            (step.layoutParams as LinearLayout.LayoutParams).bottomMargin,
        )
        // Row 18: "title margins ... `space_8` bottom" -- the step between a line and the thing it
        // introduces. The ordinal-to-line gutter is the same step, because it is the same relation
        // turned ninety degrees and a second value here would be one nothing could be asked about.
        assertEquals(
            dimenPx("swarm_space_8"),
            (column.layoutParams as LinearLayout.LayoutParams).marginStart,
        )
        assertEquals(
            dimenPx("swarm_space_8"),
            (well.layoutParams as LinearLayout.LayoutParams).topMargin,
        )
    }

    @Test
    fun `the line takes the width the ordinal leaves, so a long step wraps rather than clipping`() {
        // PB-DS-12 requires the layout to survive a 1.3x font scale. A wrap-content line beside a
        // wrap-content ordinal is measured against the parent's whole width and clips at the right
        // edge; the weight is what makes the second cell a column rather than a run of text.
        val step = pairingStep(context, "1", "On your computer, run")
        val column = step.find(KitTag.STEP_LINE)!!.parent as View

        assertEquals(0, (column.layoutParams as LinearLayout.LayoutParams).width)
        assertEquals(1f, (column.layoutParams as LinearLayout.LayoutParams).weight, 0f)
    }

    /** The negative control: the finder these assertions depend on can actually miss. */
    @Test
    fun `the step assertions can actually fail`() {
        val step = pairingStep(context, "1", "On your computer, run")

        assertNull(
            "the tag finder answers for a tag no cell carries, so every assertion above could " +
                "be reading whatever view it happened to reach first",
            step.find("no cell carries this"),
        )
    }
}
