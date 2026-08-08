package dev.swarm.phone.ui.kit

import android.content.Context
import android.view.View
import android.view.ViewGroup
import android.widget.TextView
import androidx.test.core.app.ApplicationProvider
import dev.swarm.phone.R
import dev.swarm.phone.theme.SwarmTheme
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotEquals
import org.junit.Assert.assertSame
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.GraphicsMode

/**
 * FAILING-FIRST (TDD RED, GG-5) for the obsidian migration plan's phase O6.1 -- the pull-quote
 * approval sheet.
 *
 * THE WHOLE DERIVATION IS AN ORDERING, which is why the first assertion in this file is about
 * order rather than about paint. Substrate's `.sheet2` put an `h4` question first and the
 * machine/project context under it; ADR-009's maquette reverses them and grows the question into a
 * pull-quote. Three parts before, three parts after -- no new information, and the reordering is
 * the deliverable. A test that only checked colours would pass on the old arrangement.
 *
 * IT IS THE FIRST CALLER `sheetSurface` HAS EVER HAD. O3 built the recipe -- ADR-009 D4.4's one
 * vertical gradient, the heaviest material in the app, reserved for the moment of decision -- and
 * recorded in as many words that it had no screen: "this is a recipe waiting for its screen, not a
 * rendered surface" (docs/verification/obsidian-o3-evidence.md, item 2). This is the screen.
 */
@RunWith(RobolectricTestRunner::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
class ApprovalSheetTest {

    private val context: Context
        get() = SwarmTheme.applyTo(ApplicationProvider.getApplicationContext())

    private fun dimenPx(name: String): Int {
        val id = context.resources.getIdentifier(name, "dimen", context.packageName)
        assertNotEquals("R.dimen.$name is not in the merged resource table", 0, id)
        return context.resources.getDimension(id).toInt()
    }

    private fun sheet(
        well: View? = null,
        actions: List<View> = emptyList(),
    ): ViewGroup = approvalSheet(
        context = context,
        contextLine = CONTEXT_LINE,
        question = QUESTION,
        well = well,
        actions = actions,
    ) as ViewGroup

    private fun textAt(sheet: ViewGroup, index: Int) = sheet.getChildAt(index) as TextView

    /** What one type-scale style resolves to in px, so a size is compared to a STYLE, not a number. */
    private fun styleSize(styleId: Int): Float =
        TextView(context).apply { setTextAppearance(styleId) }.textSize

    // ---- the ordering, which is the deliverable ---------------------------

    @Test
    fun `the question comes after the context line and before the well`() {
        val well = monoWell(context, COMMAND)
        val allow = ctaButton(context, "Allow", CtaKind.APPROVE)
        val view = sheet(well = well, actions = listOf(allow))

        assertEquals(
            "the context line is first: a person reading top-down meets the machine and the " +
                "project, then the question. Substrate drew these two the other way round and " +
                "O6.1 is the reversal.",
            CONTEXT_LINE.uppercase(),
            textAt(view, 0).text.toString().uppercase(),
        )
        assertEquals(
            "the question is the HEADLINE. It is the blocking question itself, typeset as the " +
                "pull-quote, and everything under it is evidence for answering it.",
            QUESTION,
            textAt(view, 1).text.toString(),
        )
        assertSame("the literal command stays in the well, below the question", well, view.getChildAt(2))
    }

    @Test
    fun `a sheet with no well still reads in order`() {
        val view = sheet()
        assertEquals(
            "a sheet without a command must not leave a hole where the well would be, and must " +
                "not promote the actions into the question's slot",
            2,
            view.childCount,
        )
    }

    @Test
    fun `the actions come last and share the width`() {
        val allow = ctaButton(context, "Allow", CtaKind.APPROVE)
        val deny = ctaButton(context, "Deny", CtaKind.DENY)
        val view = sheet(well = monoWell(context, COMMAND), actions = listOf(allow, deny))
        val row = view.getChildAt(view.childCount - 1) as ViewGroup
        assertSame("the approve action is the first of the pair", allow, row.getChildAt(0))
        assertSame("the deny action is the second", deny, row.getChildAt(1))
        assertEquals("two actions, and nothing invented beside them", 2, row.childCount)
    }

    // ---- the type, which is what "pull-quote" means -----------------------

    @Test
    fun `the question is the display style and the context line is mono`() {
        val view = sheet()
        val question = textAt(view, 1)
        val ctx = textAt(view, 0)

        assertEquals(
            "the question must be set in the app's Display style at --p-display-wt",
            styleSize(R.style.TextAppearance_Swarm_Display_NavTitle),
            question.textSize,
            SP_TOLERANCE,
        )
        assertTrue(
            "the question must be LARGER than Title.Sheet, which is what Substrate's `.sheet2 h4` " +
                "spent at 15.5 sp. `larger type` is half of what the plan asks for, and a " +
                "pull-quote the same size as the heading it replaces is not a pull-quote.",
            question.textSize > styleSize(R.style.TextAppearance_Swarm_Title_Sheet),
        )
        assertTrue(
            "the context line must be smaller than the question it sits over. The hierarchy IS " +
                "the change.",
            ctx.textSize < question.textSize,
        )
        assertTrue(
            "the context line is uppercased by the COMPONENT (isAllCaps) and not by the copy, " +
                "so the accessibility tree still reads a phrase rather than a run of letters",
            ctx.isAllCaps,
        )
    }

    @Test
    fun `the question is the primary ink and the context line the tertiary`() {
        val view = sheet()
        assertEquals(
            "the question is what the user is being asked, so it takes --p-ink",
            Kit.colour(context, R.color.swarm_text_primary),
            textAt(view, 1).currentTextColor,
        )
        assertEquals(
            "the context line is --p-ink3: it is the section label of this sheet, and the tertiary " +
                "ink is never the sole carrier of anything (ADR-009 D8.1's named deviation)",
            Kit.colour(context, R.color.swarm_text_tertiary),
            textAt(view, 0).currentTextColor,
        )
    }

    // ---- the material -----------------------------------------------------

    @Test
    fun `the sheet is the one vertical gradient and not a card`() {
        val spec = (sheet().background as SubstrateSurface).spec
        assertEquals(
            "the approval sheet takes ADR-009 D4.4's gradient top stop. It is the heaviest " +
                "material in the app and it is reserved for the moment of decision -- a card here " +
                "would make the decision look like a row.",
            Kit.colour(context, R.color.swarm_sheet_gradient_top),
            spec.fill,
        )
        assertEquals(
            Kit.colour(context, R.color.swarm_sheet_gradient_bottom),
            spec.fillBottom,
        )
        assertEquals(
            "the sheet radius, which nothing else in the app spends",
            Kit.dimen(context, R.dimen.swarm_radius_sheet),
            spec.radiusPx,
            0f,
        )
    }

    @Test
    fun `the sheet pads on the ledger's step`() {
        val view = sheet()
        val want = dimenPx("swarm_space_14")
        assertEquals("the docked sheet's own padding, reused rather than re-derived", want, view.paddingStart)
        assertEquals(want, view.paddingEnd)
        assertEquals(want, view.paddingTop)
        assertEquals(want, view.paddingBottom)
    }

    private companion object {

        /** Fixture input: the machine and the project, as the maquette's frame 2 writes them. */
        const val CONTEXT_LINE = "swarm · claude · mbp-m1"

        const val QUESTION = "Claude wants to push the release commit to main."

        const val COMMAND = "$ git push origin main"

        const val SP_TOLERANCE = 0.5f
    }
}
