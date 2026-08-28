package dev.swarm.phone.ui.screens

import android.content.Context
import android.view.View
import android.view.ViewGroup
import android.widget.TextView
import androidx.test.core.app.ApplicationProvider
import dev.swarm.phone.theme.SwarmTheme
import dev.swarm.phone.ui.ApprovalDecision
import dev.swarm.phone.ui.ApprovalItem
import dev.swarm.phone.ui.InteractionItem
import dev.swarm.phone.ui.kit.KitTag
import dev.swarm.phone.ui.kit.kitFind
import dev.swarm.phone.ui.kit.kitRequire
import org.json.JSONObject
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner

/**
 * FAILING-FIRST (TDD RED, GG-5) for slice I1's EXIT as far as a JVM can reach it: a real Claude
 * Code turn READS AS CHAT, and the approval in it can be answered — rendered from the bytes the
 * real machine actually sent.
 *
 * ## Why this suite exists beside TranscriptPanelTest and TranscriptViewTest
 *
 * Those two are the right tests and they share one blind spot: **every item they render was
 * hand-written in Kotlin.** `TranscriptPanelTest` decides what a `tool_run` reads as from a body
 * its own author typed, and `ApprovalSheetPanelTest` labels its buttons from `decisions[]` its own
 * author typed. Nothing joined either to what `internal/adapter/claude` and the daemon's producer
 * actually emit, so the whole app-side half of the interaction contract was asserted against
 * itself. That is the defect class this repository has paid for most often and names in as many
 * words: "a beautifully-tested [TriageInbox] that nothing constructs from `swarmmobile.App`, with
 * the real screen reading the facade directly and disagreeing with every assertion"
 * (`FacadeBridge`), and "literals transcribed from the implementation, compared against the
 * implementation" (`android/app/build.gradle.kts`).
 *
 * **THE GAP IS STRUCTURAL, NOT AN OVERSIGHT.** `swarmmobile.App` is a gomobile class over .so
 * files cross-compiled for Android ABIs, so it cannot be constructed on the unit-test JVM — which
 * is exactly why `FacadeBridge` lifts its pure halves out of the instance. No Robolectric test can
 * call the facade. So the crossing is RECORDED instead:
 * `internal/skeleton/interaction_screen_golden_test.go` drives the recorded Claude Code corpus
 * through the real adapter, the real producer, a separate gateway process, the real relay and the
 * real phone core, reads it back through the real bound `App.ReadTranscript` and
 * `App.PendingApprovals`, taps `App.Approve` with the id the card itself offered, and pins both
 * sides of that resolution. This suite renders that file.
 *
 * Neither half can drift alone. A change in what the producer emits rewrites the golden and turns
 * the assertions below red; a screen that stops rendering it fails here while the Go side stays
 * green, which is the direction that used to be invisible.
 *
 * ## What it does NOT prove
 *
 * A HANDSET. Robolectric is a JVM sandbox: it measures no glass, runs no real compositor and holds
 * no real `.so`. Slice I1's exit claim is that a real session reads as chat ON A DEVICE, and that
 * remains PB-E2E-5's deferred physical gate. What this closes is narrower and was the actual hole:
 * the screen is no longer asserted only against fixtures written on its own side.
 *
 * THE TAP REACHING THE WIRE. The `App.Approve` call is `PhoneSurface.approvalAction`'s, and the
 * round trip it makes is proven in Go (`TestApproveRoundTripE2E_APhoneTapAnswersTheMachinesApproval`).
 * What is proven here is that the three flat strings a button hands that verb are the three the
 * facade accepted — which is the half that lives on this side.
 */
@RunWith(RobolectricTestRunner::class)
class TranscriptScreenGoldenTest {

    private val context: Context
        get() = SwarmTheme.applyTo(ApplicationProvider.getApplicationContext())

    // ---- the recording -------------------------------------------------------

    /**
     * The pinned crossing, read from the classpath.
     *
     * IT FAILS LOUDLY WHEN ABSENT, on `Pin`'s precedent one package over: a suite that treated a
     * missing recording as an empty one would pass by rendering nothing, which reads identical to
     * passing over the real thing.
     */
    private val golden: JSONObject by lazy {
        val stream = javaClass.classLoader?.getResourceAsStream(GOLDEN)
            ?: error(
                "$GOLDEN is not on the unit-test classpath. android/app/build.gradle.kts must " +
                    "stage internal/skeleton/testdata/$GOLDEN, or this suite silently stops " +
                    "checking the screen against the facade's own bytes",
            )
        JSONObject(stream.bufferedReader().use { it.readText() })
    }

    /**
     * One recorded `swarmmobile.TranscriptItem`, as `FacadeBridge.transcript` builds it.
     *
     * THE KEYS ARE THE BOUND FIELD NAMES. The golden is `json.Marshal` of the Go struct, so
     * `"SessionID"` is `getSessionID()`, `"Truncated"` is `getTruncated()`, and so on. This mapping
     * and `FacadeBridge.transcript`'s are two spellings of one thing, and they are joined by
     * `android/gate/i1_screengolden_test.go` — which set-compares the getters the bridge calls
     * against the keys this file reads and fails in either direction. Without that gate a getter
     * dropped from the bridge would leave this suite green over a field the app no longer reads.
     */
    private fun itemFrom(o: JSONObject) = InteractionItem(
        sessionId = o.getString("SessionID"),
        itemId = o.getString("ItemID"),
        cursor = o.getLong("Cursor"),
        kind = o.getString("Kind"),
        status = o.getString("Status"),
        text = o.getString("Text"),
        body = o.getString("Body"),
        truncated = o.getBoolean("Truncated"),
        degraded = o.getBoolean("Degraded"),
        resolved = o.getBoolean("Resolved"),
        // Wave R6 (M2.2/M2.4): the five additive facts FacadeBridge now maps -- read off the
        // recorded golden so the suite renders exactly what the shipped bridge reads
        // (android/gate/i1_screengolden_test.go's two-way field join).
        detail = o.getBoolean("Detail"),
        toolKind = o.getString("ToolKind"),
        turnId = o.getString("TurnID"),
        tsUnixMs = o.getLong("TSUnixMs"),
        source = o.getString("Source"),
        // Owner ruling R6's own field (lane B): WHICH of this phone's sends the agent echoed
        // back. It is read here for the reason every other key in this function is -- the gate
        // set-compares the getters `FacadeBridge` calls against the keys this body reads, in
        // BOTH directions, so a field the app reads in production and the recorded-bytes suite
        // does not is a field whose rendering is asserted nowhere.
        operationId = o.getString("OperationID"),
    )

    private fun itemsOf(side: String, field: String = "items"): List<InteractionItem> {
        val array = golden.getJSONObject(side).getJSONArray(field)
        return (0 until array.length()).map { itemFrom(array.getJSONObject(it)) }
    }

    private fun tap(side: String): JSONObject = golden.getJSONObject(side).getJSONObject("tap")

    // ---- walking what was drawn ----------------------------------------------

    /** Every row the transcript drew, in document order, as (tag, sentence). */
    private fun rowsOf(root: View): List<Pair<String, String>> {
        val rows = mutableListOf<Pair<String, String>>()
        fun walk(v: View) {
            val tag = v.tag
            // A BUBBLE IS A ROW OF THE CONVERSATION and must stay in this list, or the reader's
            // own message disappears from the recorded turn and the golden silently stops
            // asserting the one thing it exists for: that a session READS as a conversation, in
            // order. It carries its text directly rather than through an activity row's body.
            if (tag == TranscriptTag.BUBBLE) {
                rows += (tag as String) to textOf(v)
            }
            if (tag == TranscriptTag.BLOCK) {
                rows += (tag as String) to textOf(v.kitRequire(KitTag.ACTIVITY_BODY))
            }
            // THE DECISION IS A CARD AND THE CHANGE IS A CHIP (owner rulings R4/R7 and R9), so
            // neither is an `activityRow` and neither carries an ACTIVITY_BODY. What each one
            // SAYS is read off the words it draws -- but not the same way, because they are not
            // the same shape:
            //
            //  - A CARD LEADS WITH ITS QUESTION and then draws the literal and one to eight CLI
            //    labels. Its row entry is that LEADING line, so this list keeps meaning "what
            //    each row of the conversation says" and the entry stays the value the row used
            //    to draw. Joining every word under the card would put the command and the
            //    buttons into the conversation's own transcript, and the assertion would then
            //    pass over a card that drew its labels in the wrong order, or the literal twice.
            //    `approvalSheet` composes context, question, well, actions in that order and the
            //    context line is empty here, which is why the first NON-EMPTY text is the
            //    question rather than the first text.
            //  - A CHIP IS THREE CELLS and says all three: verb, path, counts. Joined with the
            //    separator the model itself uses, so the entry is the same sentence
            //    `TranscriptScreen` writes into `TranscriptBlock.line` -- which is the point: the
            //    chip must not quietly say something different from the model's own record of it.
            if (tag == TranscriptTag.APPROVAL) {
                rows += (tag as String) to wordsIn(v).firstOrNull().orEmpty()
            }
            if (tag == TranscriptTag.FILE_CHANGE) {
                rows += (tag as String) to wordsIn(v).joinToString(" · ")
            }
            if (v is ViewGroup) for (i in 0 until v.childCount) walk(v.getChildAt(i))
        }
        walk(root)
        return rows
    }

    /**
     * Every non-empty text a component draws, in the order it draws it.
     *
     * NON-EMPTY, because absent is not empty on this surface and several kit components draw a
     * cell they were given nothing for -- `approvalSheet`'s context line here. A list with a blank
     * at the front would make "the first thing this card says" the empty string.
     */
    private fun wordsIn(root: View): List<String> {
        val words = mutableListOf<String>()
        fun walk(v: View) {
            if (v is TextView) textOf(v).takeIf { it.isNotEmpty() }?.let { words += it }
            if (v is ViewGroup) for (i in 0 until v.childCount) walk(v.getChildAt(i))
        }
        walk(root)
        return words
    }

    private fun wellsOf(root: View): List<String> {
        val wells = mutableListOf<String>()
        fun walk(v: View) {
            if (v.tag == TranscriptTag.WELL) wells += textOf(v)
            if (v is ViewGroup) for (i in 0 until v.childCount) walk(v.getChildAt(i))
        }
        walk(root)
        return wells
    }

    private fun textOf(view: View?): String = (view as? TextView)?.text?.toString().orEmpty()

    private fun render(
        items: List<InteractionItem>,
        onApproval: ((String) -> Unit)? = null,
        expanded: Set<String> = emptySet(),
    ): View = transcriptView(context, TranscriptScreen.of(items, expanded = expanded), onApproval)

    // ---- the conversation ----------------------------------------------------

    /**
     * The whole recorded turn, in the machine's own order and the screen's own words.
     *
     * ONE ASSERTION OVER THE WHOLE LIST rather than six. The subject is that a session READS as a
     * conversation, and reading is sequential: an assertion per row would pass over a transcript
     * whose rows were shuffled, which is the one defect a chat surface cannot survive.
     *
     * THE APPROVAL SITS THIRD FROM THE TOP AND THAT IS THE MACHINE'S DOING, not a sort here. The
     * recorded turn raised the permission at `PreToolUse` — before either tool call completed — so
     * the fold's ascending-cursor order puts the question above the work it was blocking.
     * `TranscriptScreen` keeps the wire's order precisely so the phone never reorders a
     * conversation it did not have.
     */
    @Test
    fun `the recorded Claude Code turn reads as a conversation`() {
        val rows = rowsOf(render(itemsOf("pending")))

        assertEquals(
            listOf(
                // THE READER'S OWN MESSAGE, now a bubble and no longer prefixed. It read
                // "You · Using the Edit tool..." because a row could only say who spoke in
                // words; the bubble says it by which side of the screen it is on.
                TranscriptTag.BUBBLE to
                    "Using the Edit tool, change the text 'line two' to 'line TWO EDITED' in edit-target3.txt",
                TranscriptTag.APPROVAL to "Edit /Users/Nathan/spike-sb-work/edit-target3.txt",
                TranscriptTag.BLOCK to "Read edit-target3.txt",
                TranscriptTag.BLOCK to "Edited edit-target3.txt",
                TranscriptTag.FILE_CHANGE to "modify · /Users/Nathan/spike-sb-work/edit-target3.txt · +1 -1",
                TranscriptTag.BLOCK to "Done. Changed 'line two' to 'line TWO EDITED' in edit-target3.txt.",
            ),
            rows,
        )
    }

    /**
     * The machine-authored literals in the turn, in `monoWell` and nowhere else.
     *
     * These are the fields §2's reuse rule was written for: a tool's captured output and a unified
     * diff are column-aligned text that a body-copy layout would silently re-wrap, misreporting
     * what the machine printed. The `Edit` tool_run carries NO `output_excerpt` in the recording,
     * so it draws no well — absent is not empty, and the screen reading the field rather than
     * always drawing the box is what this pins.
     *
     * THIS TEST IS WHERE THE OLD DEFAULT'S ARGUMENT LIVED, and it is the honest place to record
     * that the default moved (owner ruling R3). `TranscriptScreen.of` used to argue for
     * open-by-default on exactly this evidence: a real recorded turn draws two mono blocks, and
     * "a collapsed default would silently stop drawing the first of them". That was true, and
     * the word that carried it was SILENTLY -- the closed line now names its own worst outcome
     * (TranscriptBlock.mark), so the record is not hidden, it is folded.
     *
     * AND THE DIFF LEFT THE FLOW ENTIRELY (owner ruling R9, 2026-08-26). MOVED, not deleted: the
     * recorded diff is still asserted byte-for-byte, on the block's own route to the diff screen
     * rather than in a well between two messages. Reading it off `wellsOf` is what would have
     * been silent, so the well list is now asserted EMPTY in the same test -- the recorded turn
     * draws no mono block at all until the reader opens the tool run.
     */
    @Test
    fun `the recorded turn folds its tool output and routes its diff`() {
        assertEquals(
            "the recorded turn still pours a unified diff into the reading column with nothing " +
                "opened, which is what made one session take two screenshots",
            emptyList<String>(),
            wellsOf(render(itemsOf("pending"))),
        )
        assertEquals(
            "a file change is never folded away to nothing: the diff is the only rendering of " +
                "what changed on disk, and the recorded turn must still be able to show it",
            listOf("@@ -1,3 +1,3 @@\n line one\n-line two\n+line TWO EDITED\n line three"),
            TranscriptScreen.of(itemsOf("pending")).blocks
                .mapNotNull { (it.route as? TranscriptRoute.Diff)?.text },
        )
    }

    @Test
    fun `the recorded output is the only mono block when opened`() {
        val everyItem = itemsOf("pending").map { it.itemId }.toSet()
        assertEquals(
            listOf("line one\nline two\nline three\n"),
            wellsOf(render(itemsOf("pending"), expanded = everyItem)),
        )
    }

    // ---- the approval --------------------------------------------------------

    /**
     * The card the machine is blocked on, and the id a tap reports.
     *
     * The reported id is compared against the golden's own `tap.item_id` — the value the Go side
     * passed to `App.Approve` and the facade accepted (IS-APR-1: the `item_id` IS D7's
     * `interaction_id`). A literal here would be this side agreeing with itself again.
     */
    @Test
    fun `the recorded approval is the row a tap answers, and it names what the facade accepted`() {
        var answered = ""
        val root = render(itemsOf("pending"), onApproval = { id -> answered = id })

        val card = root.kitRequire(TranscriptTag.APPROVAL)
        assertTrue(
            "the recorded approval was drawn as an ordinary line, so the one thing the machine is " +
                "blocked on is not reachable from the conversation about it",
            card.isClickable,
        )
        card.performClick()

        assertEquals(tap("pending").getString("item_id"), answered)
    }

    /**
     * The sheet, from the same recorded item — the question, the literal and the buttons.
     *
     * EVERY STRING BELOW IS READ OUT OF THE RECORDING. The question is §3.5's `summary` as the
     * adapter wrote it; the labels are `decisions[].label` in the adapter's own order, which for
     * this real Claude Code dialog are `Yes` and `No` and NOT `Allow`/`Deny` — the maquette's own
     * words and a fine illustration of why a phone-side table over this vocabulary would be wrong.
     */
    @Test
    fun `the sheet asks the machine's question and offers the machine's own decisions`() {
        val card = itemsOf("pending", "approvals").single()
        val item = requireNotNull(ApprovalItem.of(card.sessionId, card.itemId, card.body)) {
            "the recorded approval_request did not decode into a card. Either the producer stopped " +
                "sending `decisions[]` — which makes it unanswerable — or ApprovalItem stopped " +
                "reading the shape it sends"
        }
        val panel = ApprovalSheetScreen.of(item, row = null)

        assertEquals("Edit /Users/Nathan/spike-sb-work/edit-target3.txt", panel.question)
        assertEquals("/Users/Nathan/spike-sb-work/edit-target3.txt", panel.command)
        assertEquals(
            listOf(ApprovalDecision("allow", "Yes"), ApprovalDecision("deny", "No")),
            panel.actions,
        )
        // The two ids a signed ActionApprove names, as the sheet would pass them.
        assertEquals(tap("pending").getString("item_id"), panel.itemId)
        assertEquals(tap("pending").getString("session"), panel.sessionId)

        // ---- and pressing one hands back exactly what the facade took ---------
        var pressed = ""
        val sheet = approvalSheetView(
            context = context,
            panel = panel,
            actionFor = { decision ->
                TextView(context).apply {
                    text = decision.label
                    setOnClickListener { pressed = decision.id }
                }
            },
        )
        val actions = mutableListOf<View>()
        fun walk(v: View) {
            if (v.tag == SheetTag.ACTION) actions += v
            if (v is ViewGroup) for (i in 0 until v.childCount) walk(v.getChildAt(i))
        }
        walk(sheet)

        assertEquals(listOf("Yes", "No"), actions.map { textOf(it) })
        assertEquals(
            "the recorded literal is not in the sheet's mono well",
            "/Users/Nathan/spike-sb-work/edit-target3.txt",
            textOf(sheet.kitFind(SheetTag.WELL)),
        )
        actions.first().performClick()
        assertEquals(
            "the pressed button reported a decision id the facade never accepted",
            tap("pending").getString("decision_id"),
            pressed,
        )
    }

    // ---- after the answer ----------------------------------------------------

    /**
     * IS-LIFE-2 as the screen honours it: the request STAYS and stops being a decision.
     *
     * The pending set is empty on the answered side of the recording — which is what lifts
     * IS-LIFE-3's retention exemption — while the request itself is still in the transcript
     * marked `Resolved`. Both halves matter and they pull opposite ways: a transcript that erased
     * what it answered could not show what was decided, and one that kept the card tappable would
     * offer a second answer to a question that has none left.
     */
    @Test
    fun `the answered card stays in the conversation and stops being a decision`() {
        assertEquals(
            "the answered card is still in the phone's pending set, so the sheet would still be up",
            0,
            golden.getJSONObject("answered").getJSONArray("approvals").length(),
        )

        var answered = ""
        val root = render(itemsOf("answered"), onApproval = { id -> answered = id })
        val rows = rowsOf(root)

        assertNull(
            "a resolved approval_request is still drawn as an answerable card. IS-LIFE-2 spends " +
                "exactly one resolution per request, and a stale card must dismiss on every surface",
            root.kitFind(TranscriptTag.APPROVAL),
        )
        assertTrue(
            "the answered request left the conversation. It is what was asked, and a transcript " +
                "that erases what it answered cannot show what was decided",
            rows.any { it.second == "Edit /Users/Nathan/spike-sb-work/edit-target3.txt" },
        )
        // The resolution itself: §3.6's decision, verbatim, and where it was answered. AMENDED
        // (design-honesty finding, 2026-08-26) and MOVED rather than deleted -- `allowed · phone`
        // said both facts in a vocabulary written for a machine, and this is the same two facts
        // with the second one in the reader's language. The recorded bytes are what make this the
        // load-bearing assertion for that copy: `decision: allowed, by: phone` is a REAL pair the
        // daemon emitted, not a pair this side invented to render.
        //
        // `on this phone` is the whole claim, and it is the strongest one available here: IS-RES-1
        // classifies `allowed` from the verdict the daemon supplied, which happens only on the
        // phone path. The owner path resolves `answered_locally` and carries no verdict at all.
        assertEquals(
            listOf(TranscriptTag.BLOCK to "allowed · answered on this phone"),
            rows.takeLast(1),
        )
        assertEquals("no tap should have been possible", "", answered)
    }

    /**
     * The one screen-side half of IS-APR-2: the app renders and answers a card WITHOUT the binding
     * tuple.
     *
     * The recording carries `content_hash`, `expires_at` and `agent_instance` on the card, because
     * the machine sends them. Nothing on this side reads any of the three — `App.Approve` takes
     * three flat strings and reads the tuple off the item the Go core is already holding, so a
     * model that carried them would be one a screen could pass, and a screen that can pass them is
     * one that could compute them. This asserts the absence rather than trusting it.
     */
    @Test
    fun `the card the screen builds carries no part of the binding tuple`() {
        val card = itemsOf("pending", "approvals").single()
        val body = JSONObject(card.body)
        // The recording must still contain them, or this test proves nothing about the app.
        listOf("content_hash", "expires_at", "agent_instance").forEach { field ->
            assertTrue(
                "the recorded card no longer carries $field, so this assertion is vacuous",
                body.has(field),
            )
        }

        val rendered = requireNotNull(ApprovalItem.of(card.sessionId, card.itemId, card.body))
            .toString()
        listOf(
            body.getString("content_hash"),
            body.getString("expires_at"),
            body.getJSONObject("agent_instance").toString(),
        ).forEach { secret ->
            assertFalse(
                "the card model carries part of ADR-007 D7's binding tuple ($secret). IS-APR-2 " +
                    "makes the phone echo it through App.Approve off the item the core holds, and a " +
                    "model that carries it is a model a screen could compute",
                rendered.contains(secret),
            )
        }
    }

    private companion object {
        const val GOLDEN = "i1-transcript-screen.golden.json"
    }
}
