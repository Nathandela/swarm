package dev.swarm.phone.ui.screens

import android.content.Context
import android.view.View
import android.view.ViewGroup
import androidx.test.core.app.ApplicationProvider
import dev.swarm.phone.ui.ApprovalDecision
import dev.swarm.phone.ui.InteractionItem
import dev.swarm.phone.ui.SessionDetail
import dev.swarm.phone.ui.SessionLease
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner

/**
 * FAILING-FIRST (TDD RED, GG-5) for Wave H, H.3 — owner ruling **R4 was never delivered**.
 * Plan: `docs/specifications/chat-surface-plan.md` §14. Bead: `agents-tracker-ryuk`.
 *
 * **THE INLINE DECISION CARD HAS NO BUTTONS.** `decisionCard` computes
 * `val answer = if (block.approval) onDecision else null`, and `onDecision` was not a parameter of
 * `sessionDetailView` or `sessionDetailRedraw` and was never passed by `PhoneSurface`. So `answer`
 * is always null, `actions` is `emptyList()`, and the card falls back to being wholly tappable —
 * a POINTER to the old sheet rather than the question answered where it was asked.
 *
 * R4 is "the question is a message in the stream carrying its own buttons". The stream got the
 * question. The buttons stayed behind.
 *
 * **WHY THIS TEST GOES THROUGH `sessionDetailView` AND NOT `transcriptView`.** `TranscriptView`'s
 * own tests pass `onDecision` directly and are green — the card draws its choices correctly the
 * moment it is handed a handler. Nothing was wrong one level down, which is exactly why nothing
 * caught this: every test asserted the component it was written for. The defect lives in the seam,
 * so the assertion has to cross it.
 *
 * **AND WHY IT MATTERS THAT THIS LANDS BEFORE THE SHEET IS DELETED.** Deleting the old sheet while
 * the card has no buttons makes every approval unanswerable from the phone, and IS-LIFE-2's
 * exactly-once resolution would then never arrive from this side. That was attempted on
 * 2026-08-26 and reverted. Wiring first, deletion second, and this test is the gate between them.
 */
@RunWith(RobolectricTestRunner::class)
class SessionDetailDecisionWiringTest {

    private val context = ApplicationProvider.getApplicationContext<Context>()

    private fun panelWithApproval(
        extra: List<InteractionItem> = emptyList(),
        summary: String = "Run the test suite?",
    ) = SessionDetailScreen.of(
        SessionDetail(sessionId = "mbp/api", online = true, journalStale = false),
        TranscriptScreen.of(
            listOf(
                InteractionItem(
                    sessionId = "mbp/api",
                    itemId = "i-approve-1",
                    cursor = 1,
                    kind = "approval_request",
                    body = """
                        {"summary":"$summary",
                         "action":{"command":"go test ./..."},
                         "decisions":[{"id":"accept","label":"Yes"},
                                      {"id":"accept_always","label":"Yes, and do not ask again"},
                                      {"id":"refuse","label":"No, and tell it what to do instead"}]}
                    """.trimIndent(),
                ),
            ) + extra,
        ),
        SessionLease(sessionId = "mbp/api", online = true),
        capabilities = SessionCapabilityFacts(structuredChat = true),
    )

    private fun View.everyTagged(tag: String): List<View> {
        val found = mutableListOf<View>()
        fun walk(v: View) {
            if (v.tag == tag) found += v
            if (v is ViewGroup) for (i in 0 until v.childCount) walk(v.getChildAt(i))
        }
        walk(this)
        return found
    }

    @Test
    fun `an unresolved decision draws the agent's own choices as pressable controls`() {
        val view = sessionDetailView(
            context = context,
            panel = panelWithApproval(),
            resync = View(context),
            acknowledge = View(context),
            approval = View(context),
            outcome = "",
            onApproval = {},
            onDecision = { _, _, _ -> },
        )

        val choices = view.everyTagged(TranscriptTag.DECISION_CHOICE)
        assertEquals(
            "the decision is drawn with no buttons at all, so the question reaches the reader and " +
                "the answer does not. R4 is `the question is a message in the stream carrying its " +
                "own buttons` -- the stream got the question and the buttons stayed behind",
            3,
            choices.size,
        )
        assertTrue(
            "the choices are drawn but cannot be pressed, which is a control that looks like one " +
                "and does nothing -- `navHeaderDrill`'s dead-chevron defect on the most " +
                "privileged surface in the app",
            choices.all { it.isEnabled && it.hasOnClickListeners() },
        )
    }

    @Test
    fun `pressing a choice reaches the host with the item and the decision it named`() {
        val seen = mutableListOf<Pair<String, String>>()
        val view = sessionDetailView(
            context = context,
            panel = panelWithApproval(),
            resync = View(context),
            acknowledge = View(context),
            approval = View(context),
            outcome = "",
            onApproval = {},
            onDecision = { _, itemId, decision -> seen += itemId to decision.id },
        )

        view.everyTagged(TranscriptTag.DECISION_CHOICE).first().performClick()

        assertEquals(
            "the press did not reach the host, so the card answers nothing. The id pair is what " +
                "the facade's `approve(session, itemId, decisionId)` is built from, and this " +
                "surface may not author a verdict of its own (IS-APR-4)",
            listOf("i-approve-1" to "accept"),
            seen,
        )
    }

    /**
     * The patch path carries the handler too, or a redraw silently un-answers the question.
     *
     * `sessionDetailRedraw`'s own KDoc records why: two of the transcript's offers are drawn only
     * when there is somewhere to send them, so a patch that rebuilds a block WITHOUT a handler the
     * composition WAS given produces a different view for the same block. A decision that loses its
     * buttons on the first incremental update is worse than one that never had them, because the
     * reader watched them disappear.
     */
    @Test
    fun `a redraw keeps the choices pressable`() {
        val drawn = panelWithApproval()
        val host = android.widget.FrameLayout(context).apply {
            addView(
                sessionDetailView(
                    context = context,
                    panel = drawn,
                    resync = View(context),
                    acknowledge = View(context),
                    approval = View(context),
                    outcome = "",
                    onApproval = {},
                    onDecision = { _, _, _ -> },
                ),
            )
        }

        // THE DECISION BLOCK ITSELF MUST REBIND, or this proves nothing.
        //
        // Two earlier versions of this test passed while `insertRun` dropped the handler on the
        // floor. The first handed `sessionDetailRedraw` an IDENTICAL panel, so the mutation list
        // was empty and `patchConversation` returned before rebuilding anything. The second added
        // an agent message, which produces an INSERT of that message -- and left the decision
        // block untouched, so it kept the views the composition had already given it, handlers and
        // all. Neither ever ran a decision through `insertRun`, which is the only path that can
        // lose the buttons.
        //
        // Changing the question's own text is what forces a REBIND of that block: the patch
        // removes its views and rebuilds them, which is exactly the moment a dropped handler
        // becomes a dead button. Proven by perturbation -- with `insertRun` put back to passing
        // `null`, this fails; the two earlier versions did not.
        val after = panelWithApproval(summary = "Run the test suite, with -race?")

        sessionDetailRedraw(host, drawn, after, onDecision = { _, _, _ -> })
        val choices = host.everyTagged(TranscriptTag.DECISION_CHOICE)
        assertTrue(
            "the redraw dropped the decision's buttons, so the question stops being answerable " +
                "the moment the agent writes one more line -- and the reader watched them go",
            choices.isNotEmpty(),
        )
        assertTrue(
            "the rebuilt choices are drawn but dead: `insertRun` rebuilt the block without the " +
                "handler the column was composed with",
            choices.all { it.hasOnClickListeners() },
        )
    }
}
