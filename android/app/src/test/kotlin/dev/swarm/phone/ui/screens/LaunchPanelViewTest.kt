package dev.swarm.phone.ui.screens

import android.content.Context
import android.view.View
import android.view.ViewGroup
import android.widget.TextView
import androidx.test.core.app.ApplicationProvider
import dev.swarm.phone.theme.SwarmTheme
import dev.swarm.phone.ui.LaunchRendering
import dev.swarm.phone.ui.LaunchResult
import dev.swarm.phone.ui.kit.kitFind
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertSame
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner

/**
 * FAILING-FIRST (TDD RED, GG-5) for PB-DS-6 and PB-APP-6 over the launch form AS DRAWN.
 *
 * WHY IT IS A VIEW TEST AND NOT MORE MODEL TESTS. ADR-007 B80 is the record of what happens
 * otherwise: `LaunchScreen` shipped with five green unit tests over a launch screen that did not
 * exist, the ledger said so, and the traceability table said PB-APP-6 was shipped. `PB-APP-6`'s
 * acceptance names a UI. `LaunchPanelScreenTest` asks what the form says; this asks whether the
 * boxes and the control are on screen and whether each field got the box meant for it.
 *
 * THE FIELD-IDENTITY TEST IS THE ONE THAT EARNS ITS PLACE. The plausible bug is a view that
 * resolves one box and hands the same one to every field, or one that pairs the working
 * directory's box with the agent's hint -- and both render as a perfectly ordinary form. The
 * daemon has no default for either required field, so a launch built from a transposed pair
 * starts an agent in the wrong place with no error anywhere.
 *
 * WHAT IS DELIBERATELY NOT ASSERTED: appearance. The section label's tracking is PB-DS-10's and is
 * asserted in `ui/kit`. The notice line HAS no appearance -- there is no notice component in the
 * kit -- and this suite says so by having nothing to claim about it rather than by asserting a
 * theme default as if it were a decision.
 */
@RunWith(RobolectricTestRunner::class)
class LaunchPanelViewTest {

    private val context: Context
        get() = SwarmTheme.applyTo(ApplicationProvider.getApplicationContext())

    /** Stand-ins for the views `PhoneSurface` owns: the three fields and the gated CTA. */
    private fun stub(): View = View(context)

    private fun view(
        panel: LaunchPanel,
        fields: MutableMap<LaunchFieldId, View> = mutableMapOf(),
        submit: View = stub(),
        below: View? = null,
    ): View = launchPanelView(
        context = context,
        panel = panel,
        fieldFor = { id -> fields.getOrPut(id) { stub() } },
        submit = submit,
        below = below,
    )

    private fun View.allTagged(tag: String): List<View> {
        val found = mutableListOf<View>()
        fun walk(v: View) {
            if (v.tag == tag) found += v
            if (v is ViewGroup) for (i in 0 until v.childCount) walk(v.getChildAt(i))
        }
        walk(this)
        return found
    }

    private fun textOf(view: View?): String = (view as? TextView)?.text?.toString().orEmpty()

    private fun rendering(
        result: LaunchResult = LaunchResult.LAUNCHED,
        reason: String = "",
        retryable: Boolean = false,
    ) = LaunchRendering(result = result, reason = reason, retryable = retryable)

    // ---- the composition ---------------------------------------------------

    @Test
    fun `the form is a section label, the model's fields, and the control that starts a session`() {
        val panel = LaunchPanelScreen.of()
        val root = view(panel)

        assertNotNull("the form has no heading", root.kitFind(LaunchTag.HEADING))
        assertEquals(panel.heading, textOf(root.kitFind(LaunchTag.HEADING)))
        assertEquals(
            "the form draws a different number of boxes from the number of fields the model asks " +
                "for, so one of them is collecting nothing",
            panel.fields.size,
            panel.fields.count { root.allTagged(LaunchTag.field(it.id)).size == 1 },
        )
        assertNotNull("nothing on the form starts a session", root.kitFind(LaunchTag.SUBMIT))
    }

    @Test
    fun `the composition is in the model's order, with the required fields first`() {
        val panel = LaunchPanelScreen.of()
        val order = mutableListOf<String>()
        fun walk(v: View) {
            (v.tag as? String)?.let { if (it in LaunchTag.COMPOSITION) order += it }
            if (v is ViewGroup) for (i in 0 until v.childCount) walk(v.getChildAt(i))
        }
        walk(view(panel))

        assertEquals(
            listOf(LaunchTag.HEADING) +
                panel.fields.map { LaunchTag.field(it.id) } +
                listOf(LaunchTag.SUBMIT),
            order,
        )
        // The model's own claim, restated where it is visible: the two the daemon refuses a
        // launch without come first, so a user who stops reading has still answered them.
        assertTrue(
            "an optional field is drawn before a required one",
            panel.fields.takeWhile { it.required }.size >= panel.fields.count { it.required },
        )
    }

    // ---- each field gets the box meant for it -------------------------------

    @Test
    fun `each field hosts the box the caller supplied for that field and no other`() {
        val supplied = LaunchFieldId.entries.associateWith { View(context) }
        val root = launchPanelView(
            context = context,
            panel = LaunchPanelScreen.of(),
            fieldFor = { id -> requireNotNull(supplied[id]) },
            submit = stub(),
        )

        LaunchFieldId.entries.forEach { id ->
            assertSame(
                "the box on screen for $id is not the one supplied for it, so what the user typed " +
                    "into one field is read back as another",
                supplied[id],
                root.allTagged(LaunchTag.field(id)).single(),
            )
        }
    }

    @Test
    fun `a box re-composed after a redraw is not refused for having a parent`() {
        // The panel is rebuilt when the notice changes. A slot arriving at its next addView still
        // claiming a discarded parent is refused by Android outright.
        val fields = LaunchFieldId.entries.associateWith { View(context) }.toMutableMap()
        val submit = stub()
        view(LaunchPanelScreen.of(), fields, submit)
        val second = view(LaunchPanelScreen.of(rendering()), fields, submit)

        assertSame(submit, second.allTagged(LaunchTag.SUBMIT).single())
        LaunchFieldId.entries.forEach { id ->
            assertSame(fields[id], second.allTagged(LaunchTag.field(id)).single())
        }
    }

    // ---- the notice ---------------------------------------------------------

    @Test
    fun `a notice is drawn only when the form has something to report`() {
        assertEquals(
            "a status line was drawn over a form that has launched nothing. It has not been " +
                "refused and has not succeeded, and a line reserved for an outcome nobody asked " +
                "for is a status about an operation that does not exist",
            0,
            view(LaunchPanelScreen.of()).allTagged(LaunchTag.NOTICE).size,
        )

        val answered = LaunchPanelScreen.of(rendering())
        assertEquals(
            listOf(answered.notice),
            view(answered).allTagged(LaunchTag.NOTICE).map { textOf(it) },
        )
    }

    @Test
    fun `the notice on screen is the machine's own words, retry clause and all`() {
        // The user's next step depends on which refusal it was: a kill-switch refusal told to a
        // user as "against policy" sends them to change a spec that was fine.
        val transient = LaunchPanelScreen.of(
            rendering(
                result = LaunchResult.REFUSED_TRANSIENTLY,
                reason = "The machine is still starting up.",
                retryable = true,
            ),
        )
        val drawn = textOf(view(transient).allTagged(LaunchTag.NOTICE).single())

        assertEquals(transient.notice, drawn)
        assertTrue(
            "the retry clause the model appends for a refusal worth retrying did not reach the " +
                "screen:\n$drawn",
            drawn.startsWith("The machine is still starting up.") && drawn.length >
                "The machine is still starting up.".length,
        )
    }

    @Test
    fun `what this slice has not recomposed is hosted under the panel, not instead of it`() {
        val trailing = View(context)
        val root = view(LaunchPanelScreen.of(), below = trailing) as ViewGroup

        assertSame(trailing, root.getChildAt(root.childCount - 1))
        assertNotNull("hosting the remainder dropped the form", root.kitFind(LaunchTag.HEADING))
    }
}
