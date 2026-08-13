package dev.swarm.phone.ui.screens

import android.content.Context
import android.view.View
import android.view.ViewGroup
import android.widget.TextView
import androidx.test.core.app.ApplicationProvider
import dev.swarm.phone.theme.SwarmTheme
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
    ) = TranscriptBlock(
        itemId = itemId,
        kind = kind,
        line = line,
        emphasis = emphasis,
        well = well,
        approval = approval,
        running = running,
    )

    private fun panel(blocks: List<TranscriptBlock> = listOf(block())) =
        TranscriptPanel(heading = "Conversation", blocks = blocks, emptyCopy = "Nothing yet.")

    private fun view(
        panel: TranscriptPanel = panel(),
        onApproval: ((String) -> Unit)? = null,
    ): View = transcriptView(context = context, panel = panel, onApproval = onApproval)

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
     * `monoWell` is documented as "the one factory for every mono block in the app". A tool's output
     * and a unified diff are exactly that, and a transcript that laid them out as body copy would
     * silently re-wrap column-aligned text -- which misreports what the machine printed. It is
     * `terminal = false`: this is not a VT grid, and ADR-009 (1) leaves no grid anywhere in the app.
     */
    @Test
    fun `a block carrying a machine literal draws it in the mono well`() {
        val diff = view(panel(listOf(block(kind = "file_change", well = "@@ -1 +1 @@"))))
        val prose = view(panel(listOf(block())))

        assertEquals("@@ -1 +1 @@", textOf(diff.kitFind(TranscriptTag.WELL)))
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
        assertTrue("the approval block is drawn as an ordinary line and cannot be tapped", card.isClickable)
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
     * The sheet that answers an approval is a separate screen with a separate owner. Until a caller
     * hands this one a destination, the block is still DRAWN -- the conversation is not allowed to
     * hide the thing the machine is blocked on -- and it is not offered as a control.
     */
    @Test
    fun `an approval block with nowhere to go is drawn and is not a dead control`() {
        val root = view(
            panel(listOf(block(kind = "approval_request", approval = true))),
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
            textOf(card.kitRequire(KitTag.ACTIVITY_BODY)).isNotEmpty(),
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
    fun `the marked span is the one the model named`() {
        val root = view(panel(listOf(block(kind = "tool_run", line = "Read src/main.rs", emphasis = "src/main.rs"))))

        // `activityRow` FAILS LOUDLY on an emphasis its body does not contain -- its own words:
        // "a caller that names a span not in the sentence has a copy bug". Composing the row at all
        // is therefore the assertion; what this adds is that the sentence survived intact.
        assertEquals("Read src/main.rs", bodiesOf(root).single())
    }
}
