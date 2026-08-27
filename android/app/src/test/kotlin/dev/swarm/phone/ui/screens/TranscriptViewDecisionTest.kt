package dev.swarm.phone.ui.screens

import android.content.Context
import android.view.View
import android.view.ViewGroup
import android.widget.TextView
import androidx.test.core.app.ApplicationProvider
import dev.swarm.phone.theme.SwarmTheme
import dev.swarm.phone.ui.ApprovalDecision
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
 * FAILING-FIRST (TDD RED, GG-5) for owner ruling R4 as the signed drawing draws it: **the question
 * the agent is blocked on is a message in the stream, carrying its own buttons.**
 *
 * WHY THIS IS ITS OWN SUITE AND NOT FOUR MORE TESTS IN `TranscriptViewTest`. That file's subject is
 * that the conversation is composed out of the kit; this one's is the single element on the screen
 * whose rendering can cost a person something irreversible. The committee's rules over it are
 * behavioural rather than compositional -- never collapse an unresolved decision, preserve every
 * label in wire order, lock all choices while one answer is in flight, never dead-end on a kind
 * this build cannot render -- and each of them names a defect that a test about ARRANGEMENT would
 * pass straight over.
 *
 * **THE ONE COPY RULE THAT OUTRANKS THE REST: THIS SURFACE MAY NOT AUTHOR A VERDICT.** IS-APR-4
 * keeps `allow | deny | other` machine-side and off the wire, and
 * `internal/skeleton/interaction_chain_e2e_test.go` fails the build if one ever rides beside
 * `id`/`label`. So "Allow" and "Deny" are precisely the two words this card may not draw, the
 * labels are the CLI's own at the count and in the order the CLI sent them, and the assertions
 * below compare against the fixture's own list rather than against anything spelled here twice.
 *
 * **AND THERE IS NO DISMISS CONTROL** (owner ruling, 2026-08-26). It was ruled out rather than
 * deferred: "Dismiss" implies the question goes away, and it is still waiting at the machine --
 * IS-LIFE-2 resolves a request exactly once, and a phone that hid the card would have resolved
 * nothing while telling the reader it had. The way out of a card this build cannot answer is the
 * tabled sentence, which names the two places it CAN be answered.
 */
@RunWith(RobolectricTestRunner::class)
class TranscriptViewDecisionTest {

    private val context: Context
        get() = SwarmTheme.applyTo(ApplicationProvider.getApplicationContext())

    /** §3.5's own labels, from the recorded Claude Code dialog: `Yes` and `No`, never Allow/Deny. */
    private val offered = listOf(
        ApprovalDecision(id = "d-yes", label = "Yes"),
        ApprovalDecision(id = "d-session", label = "Yes, and don't ask again this session"),
        ApprovalDecision(id = "d-no", label = "No, and tell Codex what to do differently"),
    )

    private val literal = "rm -rf ~/.swarm-relay/relay.db"

    private fun decision(
        itemId: String = "01ASK",
        line: String = "Run this command?",
        choices: List<ApprovalDecision> = offered,
        literal: String = this.literal,
        approval: Boolean = true,
        locked: Boolean = false,
        unrenderable: Boolean = false,
    ) = TranscriptBlock(
        itemId = itemId,
        kind = "approval_request",
        line = line,
        choices = choices,
        literal = literal,
        approval = approval,
        locked = locked,
        unrenderable = unrenderable,
    )

    private fun panel(blocks: List<TranscriptBlock>) =
        TranscriptPanel(heading = "Conversation", blocks = blocks, emptyCopy = "Nothing yet.")

    private fun view(
        blocks: List<TranscriptBlock> = listOf(decision()),
        onApproval: ((String) -> Unit)? = null,
        onDecision: ((String, ApprovalDecision) -> Unit)? = null,
    ): View = transcriptView(
        context = context,
        panel = panel(blocks),
        onApproval = onApproval,
        onDecision = onDecision,
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

    /** Every non-empty string a `TextView` under [this] carries, in traversal order. */
    private fun View.wordsIn(): List<String> {
        val out = mutableListOf<String>()
        fun walk(v: View) {
            if (v is TextView) v.text?.toString()?.takeIf { it.isNotEmpty() }?.let { out += it }
            if (v is ViewGroup) for (i in 0 until v.childCount) walk(v.getChildAt(i))
        }
        walk(this)
        return out
    }

    private fun labelsOf(root: View): List<String> =
        root.allTagged(TranscriptTag.DECISION_CHOICE).map { (it as TextView).text.toString() }

    // ---- R4: the buttons are the agent's ------------------------------------

    /**
     * The card is answerable where it was asked, and the labels are the machine's own.
     *
     * ORDER IS AN ASSERTION AND NOT A TIDINESS. §3.5 permits one to eight CLI-defined labels and
     * says nothing about their meaning; the ORDER is the only thing that carries which one the CLI
     * considers first, and a surface that sorted, merged or re-labelled them would be deciding
     * what the reader is agreeing to. The expected list is the fixture's own, so this can never
     * degrade into the screen agreeing with a second copy of the labels written here.
     */
    @Test
    fun `the decision is answered where it was asked, with the CLI's own labels in wire order`() {
        val root = view(onDecision = { _, _ -> })

        assertNotNull(
            "the decision is still drawn as a row that POINTS at a sheet. Owner ruling R7 is one " +
                "renderer: the question is a message in the stream carrying its own buttons",
            root.kitFind(TranscriptTag.APPROVAL),
        )
        assertEquals(
            "the choices are missing, reordered, or fewer than the CLI offered -- and a surface " +
                "that drops one of eight labels is a surface deciding what the reader may agree to",
            offered.map { it.label },
            labelsOf(root),
        )
    }

    /**
     * IS-APR-4, as an assertion rather than as a comment: the two words this surface may not write.
     *
     * The build already fails if a verdict rides on the wire beside `id`/`label`
     * (`internal/skeleton/interaction_chain_e2e_test.go`). What that test cannot see is a PHONE
     * that draws its own pair of buttons over the machine's list, which is exactly what every
     * approval UI in this class of product does.
     */
    @Test
    fun `the card authors no verdict of its own`() {
        val drawn = view(onDecision = { _, _ -> }).kitRequire(TranscriptTag.APPROVAL).wordsIn()

        listOf("Allow", "Deny", "Approve", "Reject").forEach { verdict ->
            assertFalse(
                "the card draws `$verdict`, which is a verdict this side invented. IS-APR-4 keeps " +
                    "the verdict machine-side and the wire carries only {id, label}",
                drawn.contains(verdict),
            )
        }
        assertEquals(
            "the card draws something the machine did not say. What is on it is the question, the " +
                "literal and the labels -- nothing else, because everything else would be ours",
            listOf("Run this command?", literal) + offered.map { it.label },
            drawn,
        )
    }

    /**
     * There is no Dismiss, and this is the test that keeps one from being added back.
     *
     * A dismissal would be the phone claiming a resolution it has not got: IS-LIFE-2 resolves a
     * request exactly once, at the machine, and a card swiped away here is a question still
     * blocking an agent with nothing on any surface saying so.
     */
    @Test
    fun `there is no way to dismiss a decision, only to answer it`() {
        val card = view(onDecision = { _, _ -> }).kitRequire(TranscriptTag.APPROVAL)

        val controls = mutableListOf<View>()
        fun walk(v: View) {
            if (v.isClickable) controls += v
            if (v is ViewGroup) for (i in 0 until v.childCount) walk(v.getChildAt(i))
        }
        walk(card)

        assertTrue(
            "the card carries a control that is not one of the machine's own decisions -- a " +
                "dismissal, a `not now`, a second route to the same question. The only controls " +
                "on a decision are the answers the CLI offered",
            controls.all { it.tag == TranscriptTag.DECISION_CHOICE },
        )
        assertEquals(
            "the card offers a different number of answers than the machine did",
            offered.size,
            controls.size,
        )
    }

    @Test
    fun `answering names the item and the decision the machine offered`() {
        var answered: Pair<String, ApprovalDecision>? = null
        val root = view(onDecision = { id, choice -> answered = id to choice })

        root.allTagged(TranscriptTag.DECISION_CHOICE)[1].performClick()

        assertEquals(
            "the answer reported nothing, or reported something other than the interaction_id a " +
                "signed ActionApprove has to name (IS-APR-1) paired with the decision that was pressed",
            "01ASK" to offered[1],
            answered,
        )
    }

    // ---- the literal ---------------------------------------------------------

    /**
     * The literal is drawn INSIDE the card and it is drawn WHOLE.
     *
     * TWO CLAIMS AND BOTH ARE THE MODEL'S OWN RULING, checked here because only the view can break
     * them. A well UNDER the card would put `rm -rf ~/.swarm-relay/relay.db` in a block below the
     * card that is asking about it; a BOUNDED literal would ask a reader to approve the half of a
     * command they were shown, which is the one place R8's twenty-line bound may never reach.
     */
    @Test
    fun `the literal is drawn inside the card, whole, and never in a well below it`() {
        val long = (1..40).joinToString("\n") { "rm -rf /very/long/path/number/$it" }
        val root = view(listOf(decision(literal = long)), onDecision = { _, _ -> })

        assertEquals(
            "the command the reader is being asked to approve is truncated, or is drawn somewhere " +
                "other than inside the card that asks about it",
            long,
            (root.kitRequire(TranscriptTag.DECISION_LITERAL) as TextView).text.toString(),
        )
        assertNull(
            "the decision's literal is drawn in the transcript's own mono well, which puts the " +
                "command in a block BELOW the card that is asking about it",
            root.kitFind(TranscriptTag.WELL),
        )
    }

    @Test
    fun `a decision whose action names no literal draws no empty well`() {
        val root = view(listOf(decision(literal = "")), onDecision = { _, _ -> })

        assertNull(
            "a recessed box saying nothing is drawn in the shape of a command that is blank",
            root.kitFind(TranscriptTag.DECISION_LITERAL),
        )
    }

    // ---- the lock ------------------------------------------------------------

    /**
     * An answer in flight locks EVERY choice.
     *
     * A card that greyed only the tapped button would leave the other seven live over an answer
     * already sent -- two `ActionApprove`s for one `interaction_id`, and whichever the daemon
     * resolves first decides which of the reader's two answers the agent acted on.
     */
    @Test
    fun `an answer in flight locks every choice and not only the one that was tapped`() {
        var answered = 0
        val root = view(listOf(decision(locked = true)), onDecision = { _, _ -> answered++ })

        val choices = root.allTagged(TranscriptTag.DECISION_CHOICE)
        assertEquals(
            "the locked card dropped its choices, so it reflows under a descending thumb and the " +
                "reader loses the question they were reading",
            offered.size,
            choices.size,
        )
        choices.forEach { choice ->
            assertFalse(
                "a choice is still live while an answer is on the wire, so one question can be " +
                    "answered twice",
                choice.isEnabled,
            )
            choice.performClick()
        }
        assertEquals("a locked choice still sent an answer", 0, answered)
    }

    // ---- the kind this build cannot draw -------------------------------------

    /**
     * Never a dead end (committee rule, plan §8).
     *
     * An `approval_request` this side cannot decode used to fall to the neutral row, which prints
     * the wire's own kind name `approval_request` at a reader and offers nothing to tap: a session
     * parked behind a card that cannot be answered on any surface. The sentence is the model's and
     * is tabled in the drawing as `decision.unknown`; what the SCREEN owes is that it is drawn as
     * the card and not as a row, that it offers no control, and that it is not the tag a pill
     * would jump to.
     */
    @Test
    fun `a decision this build cannot draw says so and dead-ends nowhere`() {
        val sentence = "This version of swarm cannot show this question. Answer it at your " +
            "machine, or update the app."
        val root = view(
            listOf(
                decision(
                    line = sentence,
                    choices = emptyList(),
                    literal = "",
                    approval = false,
                    unrenderable = true,
                ),
            ),
            onDecision = { _, _ -> },
        )

        val card = root.kitRequire(TranscriptTag.UNRENDERABLE)
        assertEquals(
            "the unrenderable question does not say what it is, or says it in words this screen " +
                "invented rather than the tabled sentence",
            listOf(sentence),
            card.wordsIn(),
        )
        assertTrue("the card offers a control it cannot honour", labelsOf(root).isEmpty())
        assertFalse("the card is tappable, and there is nothing behind the tap", card.isClickable)
        assertNull(
            "the unanswerable card is ALSO tagged as an answerable decision, so a test -- or the " +
                "decision pill -- would send a reader to a card whose only sentence tells them to " +
                "go to their machine",
            root.kitFind(TranscriptTag.APPROVAL),
        )
    }

    // ---- the screen reader ---------------------------------------------------

    /**
     * The drawing's own rule: the decision announces itself when it arrives, and reads in order
     * with the message it belongs to.
     *
     * ORDER IS STRUCTURAL AND IS ASSERTED AS STRUCTURE. A card appended to the end of the column,
     * or hoisted above the conversation, would read correctly to a sighted reader scrolling and
     * put a screen reader in a different conversation from the one on the glass. It is the item's
     * own position in the list, which is the only arrangement that cannot drift.
     */
    @Test
    fun `the decision announces itself and reads in order with the message it belongs to`() {
        val root = view(
            listOf(
                TranscriptBlock(itemId = "01A", kind = "agent_message", line = "I need to clear the database."),
                decision(),
                TranscriptBlock(itemId = "01C", kind = "agent_message", line = "Standing by."),
            ),
            onDecision = { _, _ -> },
        )

        val card = root.kitRequire(TranscriptTag.APPROVAL)
        assertEquals(
            "the decision does not announce itself, so a reader who cannot see the screen finds " +
                "out an agent is blocked on them by scrolling",
            View.ACCESSIBILITY_LIVE_REGION_POLITE,
            card.accessibilityLiveRegion,
        )

        val list = root.kitRequire(TranscriptTag.LIST) as ViewGroup
        assertEquals(
            "the decision is drawn somewhere other than at its own item, so it reads out of order " +
                "with the message it belongs to",
            1,
            (0 until list.childCount).first { list.getChildAt(it) === card },
        )
    }

    // ---- the unwired host ----------------------------------------------------

    /**
     * `navHeaderDrill(back = null)`'s ruling, on the one card where a dead control would cost the
     * most: a host that has not wired an answer draws NO buttons rather than eight dead ones.
     *
     * IT KEEPS THE ROUTE IT HAS TODAY, and that is deliberate rather than transitional debt. The
     * sheet is still the surface that answers an approval until Wave F deletes it, and a card with
     * neither buttons nor a route would be the dead end this whole element exists to prevent.
     */
    @Test
    fun `a decision with no wired answer draws no dead buttons and keeps its route`() {
        var opened = ""
        val root = view(onApproval = { id -> opened = id }, onDecision = null)

        assertTrue(
            "the card drew the machine's choices with nothing behind them, so every button on the " +
                "one card that can cost something irreversible is a control that does not act",
            labelsOf(root).isEmpty(),
        )
        val card = root.kitRequire(TranscriptTag.APPROVAL)
        assertTrue("the card has no buttons and no route, which is a question with no way to answer it", card.isClickable)
        card.performClick()
        assertEquals("01ASK", opened)
    }
}
