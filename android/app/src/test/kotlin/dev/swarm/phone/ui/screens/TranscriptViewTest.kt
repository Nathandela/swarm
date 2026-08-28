package dev.swarm.phone.ui.screens

import android.content.Context
import android.text.TextUtils
import android.view.View
import android.view.ViewGroup
import android.widget.TextView
import androidx.test.core.app.ApplicationProvider
import dev.swarm.phone.R
import dev.swarm.phone.theme.SwarmTheme
import dev.swarm.phone.ui.kit.Kit
import dev.swarm.phone.ui.kit.KitTag
import dev.swarm.phone.ui.kit.kitFind
import dev.swarm.phone.ui.kit.kitRequire
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner

/**
 * FAILING-FIRST (TDD RED, GG-5) for ADR-009-structured-chat-interaction (1) AS DRAWN, and for
 * PB-DS-6 over it.
 *
 * WHAT ONLY A VIEW TEST CAN CATCH, which is `SessionDetailViewTest`'s own reason: `TranscriptPanelTest`
 * says the model decides the right things, and this says the screen actually puts them on the glass,
 * in the recorded order, OUT OF THE KIT. "The model is beautiful and nothing renders it" is the
 * defect PB-DS-6 was recorded NOT MET over, and a transcript is the screen it would cost the most on
 * -- after ADR-009 (3) deletes the terminal well, this is the session's only content surface.
 *
 * NOT ONE VISUAL DECISION IS MADE HERE OR IN THE SCREEN. The transcript is `sectionLabel` over
 * `sessionList` over one `activityRow` per block, with `monoWell` for the blocks that carry a
 * machine-authored literal and `emptyState` for a conversation with nothing in it -- five factories
 * that already exist, which is ADR-009-obsidian-visual-direction's requirement restated as
 * arrangement: a new screen composes the vocabulary, it does not mint one.
 *
 * THE SIGNED DRAWING ADDED FOUR MORE OF THEM (2026-08-26) AND THE RULE IS UNCHANGED: `gapDivider`
 * for a proven tear, `fileChangeRow` for a change, `approvalSheet` for the decision drawn where it
 * was asked, and `notice` for the offer onto a screen of its own. Every one of them already exists
 * in `ui/kit/`; what this file checks is that the screen SPENDS them, on the blocks the model
 * marked, and mints nothing of its own -- which is the half `android/gate/s24_screens_test.go`
 * cannot see, because a screen that drew the right things out of the wrong components would pass
 * that fence and fail this suite.
 */
@RunWith(RobolectricTestRunner::class)
class TranscriptViewTest {

    private val context: Context
        get() = SwarmTheme.applyTo(ApplicationProvider.getApplicationContext())

    private fun block(
        itemId: String = "01ITEM",
        kind: String = "agent_message",
        line: String = "I pushed the branch.",
        emphasis: String? = null,
        well: String = "",
        approval: Boolean = false,
        running: Boolean = false,
        gap: Boolean = false,
        fileChange: FileChangeChip? = null,
        route: TranscriptRoute = TranscriptRoute.None,
        secondary: String = "",
    ) = TranscriptBlock(
        itemId = itemId,
        kind = kind,
        line = line,
        emphasis = emphasis,
        well = well,
        approval = approval,
        running = running,
        gap = gap,
        fileChange = fileChange,
        route = route,
        secondary = secondary,
    )

    private fun panel(blocks: List<TranscriptBlock> = listOf(block())) =
        TranscriptPanel(heading = "Conversation", blocks = blocks, emptyCopy = "Nothing yet.")

    private fun view(
        panel: TranscriptPanel = panel(),
        onApproval: ((String) -> Unit)? = null,
        onRepair: (() -> Unit)? = null,
        onOutput: ((String) -> Unit)? = null,
        onDiff: ((String) -> Unit)? = null,
    ): View = transcriptView(
        context = context,
        panel = panel,
        onApproval = onApproval,
        onRepair = onRepair,
        onOutput = onOutput,
        onDiff = onDiff,
    )

    /** Every string a `TextView` under [this] carries, in traversal order. */
    private fun View.wordsIn(): List<String> {
        val out = mutableListOf<String>()
        fun walk(v: View) {
            if (v is TextView) out += v.text?.toString().orEmpty()
            if (v is ViewGroup) for (i in 0 until v.childCount) walk(v.getChildAt(i))
        }
        walk(this)
        return out
    }

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

    private fun bodiesOf(root: View): List<String> =
        root.allTagged(TranscriptTag.BLOCK).map { textOf(it.kitRequire(KitTag.ACTIVITY_BODY)) }

    @Test
    fun `the transcript is composed of the parts its recorded composition names`() {
        val root = view()

        listOf(
            TranscriptTag.SECTION_LABEL to "the heading over the conversation",
            TranscriptTag.BLOCK to "one interaction item",
        ).forEach { (tag, what) ->
            assertNotNull("the transcript renders nothing for $what", root.kitFind(tag))
        }
        assertEquals("Conversation", textOf(root.kitFind(TranscriptTag.SECTION_LABEL)))
    }

    @Test
    fun `every block is drawn, in the order the panel put them in`() {
        val root = view(
            panel(
                listOf(
                    block(itemId = "01A", kind = "user_message", line = "You · run the tests"),
                    block(itemId = "01B", line = "Running them now."),
                    block(itemId = "01C", kind = "tool_run", line = "Bash go test ./..."),
                ),
            ),
        )

        assertEquals(
            listOf("You · run the tests", "Running them now.", "Bash go test ./..."),
            bodiesOf(root),
        )
    }

    @Test
    fun `a conversation with nothing in it draws its copy, not an empty area`() {
        val root = view(panel(blocks = emptyList()))

        assertEquals("Nothing yet.", textOf(root.kitFind(TranscriptTag.EMPTY)))
        assertNull("an empty transcript still drew a block", root.kitFind(TranscriptTag.BLOCK))
        assertNotNull(
            "the heading went with the rows, so an empty conversation is a gap where a section " +
                "used to be rather than a section saying it is empty",
            root.kitFind(TranscriptTag.SECTION_LABEL),
        )
    }

    /**
     * §2's reuse rule, over the one thing on this screen that is a machine-authored literal.
     *
     * `monoWell` is documented as "the one factory for every mono block in the app". A tool's
     * captured output is exactly that, and a transcript that laid it out as body copy would
     * silently re-wrap column-aligned text -- which misreports what the machine printed. It is
     * `terminal = false`: this is not a VT grid, and ADR-009 (1) leaves no grid anywhere in the app.
     *
     * **THE FIXTURE MOVED FROM `file_change` TO `tool_run`, AND THAT IS OWNER RULING R9 REACHING A
     * TEST.** The pair this used to build -- a `file_change` carrying a well -- is now unreachable
     * in production: `TranscriptScreen`'s `FILE_CHANGE` arm fills [TranscriptBlock.fileChange] and
     * routes the diff to its own screen, and never fills [TranscriptBlock.well] at all. A fixture
     * that goes on building it would be this suite proving the well works on a shape the screen
     * can no longer be handed, which is how a test outlives the thing it was protecting. The
     * SUBJECT is unchanged: a block that carries a machine literal draws it in the well.
     */
    @Test
    fun `a block carrying a machine literal draws it in the mono well`() {
        val output = view(panel(listOf(block(kind = "tool_run", well = "@@ -1 +1 @@"))))
        val prose = view(panel(listOf(block())))

        assertEquals("@@ -1 +1 @@", textOf(output.kitFind(TranscriptTag.WELL)))
        assertNull(
            "a well is drawn under a block that carries no literal, which is an empty recessed " +
                "box saying \"we have nothing\" in the shape of \"the machine printed nothing\"",
            prose.kitFind(TranscriptTag.WELL),
        )
    }

    @Test
    fun `the approval block is the one a tap can answer, and it names what it answers`() {
        var answered = ""
        val root = view(
            panel(listOf(block(itemId = "01APPROVAL", kind = "approval_request", approval = true))),
            onApproval = { id -> answered = id },
        )

        val card = root.kitRequire(TranscriptTag.APPROVAL)
        assertTrue("the approval card is drawn and cannot be tapped", card.isClickable)
        card.performClick()

        assertEquals(
            "the tap reported nothing, or reported something other than the interaction_id the " +
                "signed ActionApprove has to name (IS-APR-1)",
            "01APPROVAL",
            answered,
        )
    }

    /**
     * `navHeaderDrill(back = null)`'s precedent, and the defect it was written against
     * (agents-tracker-2yb: "the chevron therefore looks like a control and does not act").
     *
     * AMENDED BY THE SIGNED DRAWING, AND THE SUBJECT IS UNCHANGED. The block is a decision CARD
     * now (owner rulings R4 and R7) rather than a row pointing at a sheet, so what a host with no
     * destination leaves behind is a card with no buttons and no tap -- not a missing question.
     * The question is still drawn, because the conversation is not allowed to hide the thing the
     * machine is blocked on; what is absent is the control. The assertion moved from the row's
     * body cell to the card's own words for exactly that reason: `approvalSheet` carries no
     * `ACTIVITY_BODY`, and a test that went on asking for one would fail this card for a reason
     * that has nothing to do with whether the question is on screen.
     */
    @Test
    fun `an approval block with nowhere to go is drawn and is not a dead control`() {
        val root = view(
            panel(listOf(block(kind = "approval_request", line = "Run the tests?", approval = true))),
            onApproval = null,
        )

        val card = root.kitRequire(TranscriptTag.APPROVAL)
        assertFalse(
            "the approval block is offered as tappable with no destination behind it, which is a " +
                "control that looks like a control and does not act",
            card.isClickable,
        )
        assertTrue(
            "the approval block was dropped along with its destination, so the one item the " +
                "machine is blocked on is missing from the conversation about it",
            card.wordsIn().contains("Run the tests?"),
        )
    }

    /**
     * agents-tracker-dwwv.1.2: the running marker, at the level only a view test can catch --
     * `TranscriptPanelTest` says the model names the block running; this says the row that reads
     * as a live tool is actually a DIFFERENT view from an ordinary one, the way `TranscriptTag.APPROVAL`
     * already is for the one block a tap can answer, and that the word its own mono line carries
     * reaches the glass.
     */
    @Test
    fun `a running tool's row is tagged distinctly and its mono line carries the word`() {
        val root = view(
            panel(
                listOf(
                    block(kind = "tool_run", line = "Bash go test ./...", well = "running", running = true),
                ),
            ),
        )

        assertNotNull(
            "a running tool_run is drawn as an ordinary block, so nothing on screen -- or in a " +
                "test -- can tell a live tool from a finished one",
            root.kitFind(TranscriptTag.RUNNING),
        )
        assertNull(
            "the running block is ALSO tagged as an ordinary one, so a test finding either tag " +
                "would see the same view twice",
            root.kitFind(TranscriptTag.BLOCK),
        )
        assertEquals("running", textOf(root.kitFind(TranscriptTag.WELL)))
    }

    @Test
    fun `a completed tool's row is an ordinary block, not tagged running`() {
        val root = view(panel(listOf(block(kind = "tool_run", line = "Bash go test ./...", running = false))))

        assertNotNull(root.kitFind(TranscriptTag.BLOCK))
        assertNull(
            "a finished tool is tagged as though it were still running",
            root.kitFind(TranscriptTag.RUNNING),
        )
    }

    @Test
    fun `the secondary line is one line and ellipsised`() {
        // phone-refit-playbook W6.1: one grey line under the verb -- the timestamp cell's ink,
        // never wrapping, ending in the platform's own mark -- and no line at all when the
        // model has nothing to put there.
        val root = view(panel(listOf(block(kind = "tool_run", line = "Ran a command", secondary = "go test ./..."))))
        val grey = root.kitRequire(KitTag.ACTIVITY_SECONDARY) as TextView
        assertEquals("go test ./...", grey.text.toString())
        assertEquals(1, grey.maxLines)
        assertEquals(TextUtils.TruncateAt.END, grey.ellipsize)
        assertEquals(Kit.colour(context, R.color.swarm_text_tertiary), grey.currentTextColor)
        assertEquals("the verb is still the row's body", "Ran a command", textOf(root.kitRequire(KitTag.ACTIVITY_BODY)))
        assertNull(
            "a row with nothing under its verb draws no grey line",
            view(panel(listOf(block(kind = "tool_run", line = "Used a tool")))).kitFind(KitTag.ACTIVITY_SECONDARY),
        )
    }

    @Test
    fun `the marked span is the one the model named`() {
        val root = view(panel(listOf(block(kind = "tool_run", line = "Read src/main.rs", emphasis = "src/main.rs"))))

        // `activityRow` FAILS LOUDLY on an emphasis its body does not contain -- its own words:
        // "a caller that names a span not in the sentence has a copy bug". Composing the row at all
        // is therefore the assertion; what this adds is that the sentence survived intact.
        assertEquals("Read src/main.rs", bodiesOf(root).single())
    }

    // ---- the signed drawing's new parts -------------------------------------

    /**
     * The tear, as the drawing draws it: a rule with a word on it, and the word is the repair.
     *
     * **WHAT IT REPLACES IS A PARAGRAPH STANDING IN THE READING PATH.** `notice(ERROR)` is a
     * full-width sentence, and a tear drawn as one is something a reader has to finish before they
     * can carry on reading -- between two rows of a conversation, on the screen the owner
     * photographed with roughly 150 dp left for the messages. `gapDivider` is the same statement
     * in the same voice at a fraction of the height, and it is the component's own recorded
     * derivation ("the error notice, minus the paragraph").
     *
     * THE TYPE IS THE ASSERTION AND NOT A DETAIL. `notice` returns a `TextView` and `gapDivider`
     * returns a `LinearLayout` of two rules around a label; a screen that kept the old factory
     * would still carry the tag, still carry the words, and still be the paragraph. So this asks
     * what was actually composed rather than what it happens to say.
     */
    @Test
    fun `a proven tear is drawn as a divider and not as a paragraph`() {
        val root = view(panel(listOf(block(kind = "structured_gap", line = "records missing · repair", gap = true))))

        val tear = root.kitRequire(TranscriptTag.GAP)
        assertTrue(
            "the tear is still the full-width notice paragraph. The drawing reduces it to a rule " +
                "with a word on it, in position, and a paragraph between two messages is the " +
                "notice this whole slice was filed to remove",
            tear is ViewGroup,
        )
        assertTrue(
            "the tear no longer says the records are missing, or no longer carries its own " +
                "repair -- and a tear a reader cannot act on where they found it is the notice " +
                "standing above the conversation again",
            tear.wordsIn().contains("records missing · repair"),
        )
    }

    /**
     * The repair rides on the divider, and only where there is one to ride to.
     *
     * `gapDivider`'s own row: "the whole line is the control", which is why the label carries the
     * word rather than a span nothing can size. And `navHeaderDrill(back = null)`'s ruling, spent
     * for the fourth time in this file: a control with no destination behind it is worse than no
     * control at all (agents-tracker-2yb).
     */
    @Test
    fun `the repair is on the tear, and a tear with nowhere to go is not a control`() {
        var repaired = 0
        val torn = panel(listOf(block(kind = "structured_gap", line = "records missing · repair", gap = true)))

        val wired = view(torn, onRepair = { repaired++ })
        wired.kitRequire(TranscriptTag.GAP).performClick()
        assertEquals("the tear carries no repair, so the one place a reader meets the gap is the one place they cannot act on it", 1, repaired)

        assertFalse(
            "the tear is offered as tappable with no repair behind it, which is a control that " +
                "looks like a control and does not act",
            view(torn).kitRequire(TranscriptTag.GAP).isClickable,
        )
    }

    /**
     * Owner ruling R9: one tappable line per file, and never a screen of diff in the flow.
     *
     * **THE ROW IS A DIFFERENT COMPONENT AND SO IT IS A DIFFERENT TAG**, which is
     * [TranscriptTag.APPROVAL]'s reasoning in this file's own words: a single tag over a chip and
     * a row would let a test find either and assert the other's behaviour. `fileChangeRow` carries
     * three cells and no `ACTIVITY_BODY`, so a shared tag would not merely be confusing -- every
     * assertion written against the row would fail on the chip for the wrong reason.
     */
    @Test
    fun `a file change is a chip in the flow, carrying its own three cells`() {
        val root = view(
            panel(
                listOf(
                    block(
                        itemId = "01FILE",
                        kind = "file_change",
                        line = "modify · ui/kit/Composer.kt · +12 -24",
                        fileChange = FileChangeChip("modify", "ui/kit/Composer.kt", "+12 -24"),
                        route = TranscriptRoute.Diff("@@ -1 +1 @@"),
                    ),
                ),
            ),
        )

        val chip = root.kitRequire(TranscriptTag.FILE_CHANGE)
        assertEquals(
            "the chip does not carry the wire's own three cells, so a reader scanning a wide " +
                "refactor cannot see which files it touched or by how much",
            listOf("modify", "ui/kit/Composer.kt", "+12 -24"),
            chip.wordsIn(),
        )
        assertNull(
            "the file change is ALSO drawn as an ordinary row, so one change is two things in " +
                "the conversation",
            root.kitFind(TranscriptTag.BLOCK),
        )
        assertNull(
            "the unified diff is drawn in the flow under the chip. R9 moves it to a screen with " +
                "room to scroll sideways; a refactor touching nine files costs nine screens here",
            root.kitFind(TranscriptTag.WELL),
        )
    }

    @Test
    fun `the chip opens the diff, and a change with no diff is not a control`() {
        var opened = ""
        val changed = block(
            itemId = "01FILE",
            kind = "file_change",
            line = "modify · ui/kit/Composer.kt · +12 -24",
            fileChange = FileChangeChip("modify", "ui/kit/Composer.kt", "+12 -24"),
            route = TranscriptRoute.Diff("@@ -1 +1 @@"),
        )

        view(panel(listOf(changed)), onDiff = { id -> opened = id })
            .kitRequire(TranscriptTag.FILE_CHANGE).performClick()
        assertEquals("the chip reported nothing, or reported something other than the item whose diff it opens", "01FILE", opened)

        assertFalse(
            "a delete carries no diff_excerpt and the chip still offers a screen, which is a tap " +
                "onto an empty page",
            view(panel(listOf(changed.copy(route = TranscriptRoute.None))), onDiff = { })
                .kitRequire(TranscriptTag.FILE_CHANGE).isClickable,
        )
    }

    /**
     * Owner ruling R8's overflow, which is the state the model lane flagged the drawing does not
     * allow: an open card bounded at twenty lines, showing a head, with no way on.
     *
     * THE LABEL IS THE MODEL'S OWN STRING AND IS NOT RE-TEMPLATED HERE. `TranscriptScreen` tables
     * the singular and the plural separately -- because one plural template reads `1 more lines`
     * at exactly the count likeliest to occur -- and a screen that rebuilt the sentence from a
     * count would be a second place for English to be got wrong.
     */
    @Test
    fun `a bounded body says how much more there is and offers the whole of it`() {
        var opened = ""
        val bounded = block(
            itemId = "01TOOL",
            kind = "tool_run",
            line = "Bash go test ./...",
            well = "=== RUN   TestOne",
            route = TranscriptRoute.Output(text = "the whole body", label = "Open in full · 214 more lines"),
        )

        val root = view(panel(listOf(bounded)), onOutput = { id -> opened = id })
        val offer = root.kitRequire(TranscriptTag.ROUTE)
        assertEquals("Open in full · 214 more lines", textOf(offer))
        offer.performClick()
        assertEquals("the offer reported nothing, or reported something other than the item whose output it opens", "01TOOL", opened)

        assertNull(
            "a body that fits still offers a screen, so a reader is invited to tap through to " +
                "three lines they can already see",
            view(panel(listOf(bounded.copy(route = TranscriptRoute.None))), onOutput = { })
                .kitFind(TranscriptTag.ROUTE),
        )
        assertNull(
            "the offer is drawn with no screen behind it, which is the dead chevron wearing a route",
            view(panel(listOf(bounded))).kitFind(TranscriptTag.ROUTE),
        )
    }
}
