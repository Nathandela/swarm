package dev.swarm.phone.ui.screens

import android.content.Context
import android.view.View
import android.view.ViewGroup
import android.widget.TextView
import androidx.test.core.app.ApplicationProvider
import dev.swarm.phone.theme.SwarmTheme
import dev.swarm.phone.ui.CommandVerdict
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
 * asserted in `ui/kit`. This paragraph used to add that "the notice line HAS no appearance -- there
 * is no notice component in the kit"; it has one now (`§4 Notice line`, agents-tracker-ksvb.4) and
 * `NoticeTest` is where its type and its ink are read off the design source. The conclusion is
 * unchanged and is now true for the ordinary reason: appearance is the kit's, and a second opinion
 * about it here could disagree with the first.
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

    /**
     * agents-tracker-ksvb.10 moved the machine's words OUT of this line and into a second view, so
     * what this asserts is the pair: the form's own sentence with its retry clause, and the wire's
     * string beneath it in `.sheet2 .ctx`.
     *
     * THE RETRY CLAUSE IS NAMED RATHER THAN INFERRED FROM A PREFIX. It used to be asserted as "the
     * notice starts with the machine's reason and is longer than it", which was a reading of the
     * splice; `CommandVerdict.RETRY_HINT` is the value the model appends and is what the screen has
     * to be carrying.
     */
    @Test
    fun `the notice on screen is the form's sentence, with the machine's words beneath it`() {
        // The user's next step depends on which refusal it was: a kill-switch refusal told to a
        // user as "against policy" sends them to change a spec that was fine.
        val transient = LaunchPanelScreen.of(
            rendering(
                result = LaunchResult.REFUSED_TRANSIENTLY,
                reason = "The machine is still starting up.",
                retryable = true,
            ),
        )
        val root = view(transient)
        val drawn = textOf(root.allTagged(LaunchTag.NOTICE).single())

        assertEquals(transient.notice, drawn)
        assertTrue(
            "the retry clause the model appends for a refusal worth retrying did not reach the " +
                "screen:\n$drawn",
            drawn.endsWith(CommandVerdict.RETRY_HINT),
        )
        assertEquals(
            "the machine's own reason reaches no view, so a refused launch names no cause at all",
            listOf("The machine is still starting up."),
            root.allTagged(LaunchTag.NOTICE_DETAIL).map { textOf(it) },
        )
        assertEquals(
            "a detail is drawn under a launch nobody refused, which is a mono line reserved for a " +
                "reply the machine never sent",
            0,
            view(LaunchPanelScreen.of(rendering(result = LaunchResult.LAUNCHED)))
                .allTagged(LaunchTag.NOTICE_DETAIL).size,
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
