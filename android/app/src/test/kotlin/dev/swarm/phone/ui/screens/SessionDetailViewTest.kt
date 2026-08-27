package dev.swarm.phone.ui.screens

import android.content.Context
import android.view.View
import android.view.ViewGroup
import android.widget.EditText
import android.widget.FrameLayout
import android.widget.TextView
import androidx.test.core.app.ApplicationProvider
import dev.swarm.phone.R
import dev.swarm.phone.theme.SwarmTheme
import dev.swarm.phone.ui.CommandVerdict
import dev.swarm.phone.ui.InteractionItem
import dev.swarm.phone.ui.OperationOutcome
import dev.swarm.phone.ui.SessionDetail
import dev.swarm.phone.ui.SessionLease
import dev.swarm.phone.ui.UndeliveredInput
import dev.swarm.phone.ui.UndeliveredLedger
import dev.swarm.phone.ui.kit.ComposerModel
import dev.swarm.phone.ui.kit.Kit
import dev.swarm.phone.ui.kit.KitTag
import dev.swarm.phone.ui.kit.SendState
import dev.swarm.phone.ui.kit.kitFind
import dev.swarm.phone.ui.kit.kitRequire
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertSame
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner

/**
 * FAILING-FIRST (TDD RED, GG-5) for PB-APP-3 and PB-DS-6 over inventory C2 AS DRAWN.
 *
 * WHAT ONLY A VIEW TEST CAN CATCH, which is why this exists beside `SessionDetailPanelTest`. That
 * suite says the model decides the right things; this says the screen actually puts them on screen,
 * in the recorded order, from the kit. "The model is beautiful and nothing renders it" is the
 * defect PB-DS-6 was recorded NOT MET over, and this screen is the eighth and last chance to repeat
 * it.
 *
 * THE CONTROLS ARE SLOTS AND NOT CONSTRUCTIONS. Take control, Stop and Kill reach facade verbs,
 * carry PB-SEC-12 clause 1's touch filter, and must survive a redraw, so `PhoneSurface` owns them
 * and hands them in -- the arrangement `peekPanelView` already used for `[Take control]` before that
 * screen was deleted. What this file asserts is that the screen PLACES them, not that pressing one
 * does anything.
 *
 * THE TRANSCRIPT IS ONE SLOT NOW, NOT FOUR. `docs/adr/ADR-009-structured-chat-interaction.md`
 * deletes the terminal well and its own stale mark, and replaces the per-session journal's heading,
 * rows and empty state with [transcriptView]'s single composition -- so where this file used to
 * assert `DetailTag.SECTION_LABEL`, `.ROW`, `.EMPTY`, `.SNAPSHOT` and `.SNAPSHOT_STALE` one at a
 * time, it now asserts `DetailTag.TRANSCRIPT` is placed, and leaves what is INSIDE it to
 * `TranscriptViewTest` -- the suite built for exactly that surface.
 */
@RunWith(RobolectricTestRunner::class)
class SessionDetailViewTest {

    private val context: Context
        get() = SwarmTheme.applyTo(ApplicationProvider.getApplicationContext())

    private fun panel(
        leaseHeld: Boolean = true,
        online: Boolean = true,
        journalStale: Boolean = false,
        stopNotSent: Boolean = false,
        verdict: CommandVerdict = CommandVerdict.UNANSWERED,
        undelivered: UndeliveredLedger = UndeliveredLedger.EMPTY,
    ): SessionDetailPanel {
        val detail = SessionDetail(
            sessionId = "mbp/api",
            online = online,
            journalStale = journalStale,
            stopNotSent = stopNotSent,
        )
        return SessionDetailScreen.of(
            detail,
            TranscriptScreen.of(emptyList()),
            SessionLease(sessionId = detail.sessionId, online = detail.online),
            capabilities = SessionCapabilityFacts(structuredChat = true),
            undelivered = undelivered,
        )
    }

    /** One unit of input this phone took and could not deliver, in the shape the wire carries. */
    private fun lost(reason: String = "the link dropped") = UndeliveredLedger(
        entries = listOf(
            UndeliveredInput(
                sessionId = "mbp/api",
                bytes = 4,
                reason = reason,
                atMillis = 1_700_000_000_000L,
            ),
        ),
        dropped = 0,
    )

    /** A machine that refused this screen's own take_control, in the machine's own words. */
    private fun refusedLease(): CommandVerdict = CommandVerdict.of(
        OperationOutcome(operationId = "op-1", code = "kill_switch", message = "remote control is disabled"),
        "op-1",
        accepted = "lease",
    )

    /**
     * A panel whose conversation carries one unresolved `approval_request`.
     *
     * IT IS BUILT FROM AN ITEM AND NOT FROM A HAND-MADE BLOCK, because the property under test is a
     * whole path: the item decodes (`ApprovalItem`), the fold marks it answerable
     * ([TranscriptScreen]), the transcript draws it as its own tag, and THIS screen has to have
     * handed the listener down for the tap to reach anything.
     */
    private fun panelWithApproval(): SessionDetailPanel {
        val detail = SessionDetail(
            sessionId = "mbp/api",
            online = true,
            journalStale = false,
        )
        return SessionDetailScreen.of(
            detail,
            TranscriptScreen.of(
                listOf(
                    InteractionItem(
                        sessionId = detail.sessionId,
                        itemId = "i-approve-1",
                        cursor = 1,
                        kind = "approval_request",
                        body = """
                            {"summary":"Run the test suite?",
                             "action":{"command":"go test ./..."},
                             "decisions":[{"id":"accept","label":"Allow"}]}
                        """.trimIndent(),
                    ),
                ),
            ),
            SessionLease(sessionId = detail.sessionId, online = true),
            capabilities = SessionCapabilityFacts(structuredChat = true),
        )
    }

    private fun view(
        panel: SessionDetailPanel = panel(),
        outcome: String = "",
        resync: View = TextView(context),
        acknowledge: View = TextView(context),
        approval: View = FrameLayout(context),
        onApproval: ((String) -> Unit)? = null,
    ): View = sessionDetailView(
        context = context,
        panel = panel,
        resync = resync,
        acknowledge = acknowledge,
        approval = approval,
        outcome = outcome,
        onApproval = onApproval,
    )

    private fun textOf(view: View?): String = (view as? TextView)?.text?.toString().orEmpty()

    /** The first editable field anywhere under this view, which on this screen is the composer's. */
    private fun View.firstFieldOrNull(): View? {
        if (this is EditText) return this
        if (this is ViewGroup) {
            for (i in 0 until childCount) getChildAt(i).firstFieldOrNull()?.let { return it }
        }
        return null
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

    @Test
    fun `the screen is composed of the parts its recorded composition names`() {
        val root = view()

        listOf(
            DetailTag.TRANSCRIPT to "C2.3 -- the conversation, composed by transcriptView",
            DetailTag.APPROVAL to "ADR-009 (4)'s card, in place under the block that points at it",
        ).forEach { (tag, what) ->
            assertNotNull("the session detail renders nothing for $what", root.kitFind(tag))
        }
    }

    /**
     * The other half of the move, and the half a diff cannot tell from a deleted assertion.
     *
     * THREE ASSERTIONS LEFT THIS FILE WITH THEIR SUBJECTS (chat-surface-plan §5): that the drill
     * header is composed here, that it comes FIRST and names the session, and that its chevron
     * navigates. All three are MOVED rather than deleted -- the header is
     * `conversationScaffoldView`'s fixed region now and `PhoneSurfaceConversationHostTest` makes
     * every one of them over the app the user actually opens, where the header is real.
     *
     * WHAT STANDS HERE IS THE CLAIM IN THE OTHER DIRECTION, because a part that merely stopped
     * being asserted is a part that can come back without anything failing: this column must
     * compose NO header, NO composer and NO destructive control. A second title inside the scroll,
     * under a header that already names the session, is the screen saying the same thing twice --
     * and the two would disagree the first time one was redrawn without the other.
     */
    @Test
    fun `the scrolling column composes no header, no composer and no destructive control`() {
        val root = view()

        assertNull(
            "the conversation column still composes a drill header. It scrolls, so the session " +
                "name and the way back slide off the top the moment a reader moves -- which is " +
                "the defect the fixed header exists to close, reintroduced one level down",
            root.kitFind(KitTag.DRILL_TITLE),
        )
        assertNull(
            "the column composes a back control of its own, so there are two ways out of this " +
                "screen and only one of them is where a reader looks for it",
            root.kitFind(KitTag.DRILL_BACK),
        )
        assertNull(
            "the column composes a text field, so the composer is inside the scroll again: " +
                "reachable only by scrolling past the whole transcript, and unable to stay above " +
                "the IME. The app has exactly one field on this screen and it is the composer's",
            root.firstFieldOrNull(),
        )
    }

    /**
     * "SOMETHING WAS NOT SENT" NOW MEANS A PRESS, and agents-tracker-4lta is why. The last line read:
     *
     *     assertTrue(textOf(offline.kitFind(DetailTag.NOT_SENT)).isNotEmpty())
     *
     * over `panel(online = false)`, a session nobody had pressed Stop on -- so the screen drew
     * "Stop did not reach your machine and was not held for later" the moment the link dropped, in
     * the past tense, about a Stop that was never pressed. That is the same warning-about-a-loss-
     * that-did-not-happen this test's own first assertion is against, one state over. Both
     * directions are asserted below: nothing while the link is merely down, and the notice once a
     * press has resolved NOT_SENT.
     */
    @Test
    fun `the not-sent notice appears only when something was not sent`() {
        val live = view(panel(online = true))
        val offline = view(panel(online = false))
        val pressed = view(panel(online = false, stopNotSent = true))

        assertNull(
            "a not-sent notice was drawn over a session whose link is up, which warns about a " +
                "loss that did not happen",
            live.kitFind(DetailTag.NOT_SENT),
        )
        assertNull(
            "a not-sent notice was drawn over a session whose link is down and whose Stop was " +
                "never pressed, which reports a failure the user did not cause",
            offline.kitFind(DetailTag.NOT_SENT),
        )
        assertTrue(textOf(pressed.kitFind(DetailTag.NOT_SENT)).isNotEmpty())
    }

    @Test
    fun `a holed journal says so above the conversation it is missing from`() {
        val whole = view(panel(journalStale = false))
        val holed = view(panel(journalStale = true))

        assertNull(whole.kitFind(DetailTag.STALE))
        assertTrue(textOf(holed.kitFind(DetailTag.STALE)).isNotEmpty())
    }

    /**
     * FAILING-FIRST for PB-APP-9 on the two controls this screen is the only home of.
     *
     * WHY IT IS THIS SCREEN'S PROBLEM AND NOT THE SURFACE'S. `PhoneSurface` reports every verb's
     * refusal on one routed line, and that line is a child of the unrecomposed column under the
     * INBOX -- so the moment the drill-down replaces the list, Stop and Kill are two controls that
     * reach a machine with nowhere on screen to say what it answered. That is the exact failure the
     * surface's own words name: "the user presses a control, something refuses, and the screen
     * looks identical either way".
     *
     * IT IS A SLOT FOR THE SAME REASON THE NOTICES ARE ONE, and not a second copy of the line: the
     * surface holds the routed sentence and hands it in, so the two renderings are never both on
     * screen and never disagree.
     */
    @Test
    fun `what the machine answered the screen's own controls is on the screen`() {
        val refused = view(outcome = "Your machine refused that.")

        assertEquals(
            "PB-APP-9: the session detail renders nothing for what Stop or Kill was answered, so " +
                "the two controls this screen is the only home of refuse in silence -- the " +
                "surface's routed line is a child of the inbox column, which is the view this " +
                "screen replaces",
            "Your machine refused that.",
            textOf(refused.kitRequire(DetailTag.OUTCOME)),
        )
    }

    @Test
    fun `a screen whose controls have answered nothing draws no line about it`() {
        assertNull(
            "an empty routed line is drawn as a blank notice, which is a report nobody wrote -- " +
                "the same call the stale and not-sent notices already make",
            view(outcome = "").kitFind(DetailTag.OUTCOME),
        )
    }

    /**
     * The ON-SCREEN ORDER, which is what [DetailTag.COMPOSITION] records and what nothing read.
     *
     * THE LEASE ROW AND TAKE CONTROL ARE PART OF THIS SCREEN'S RECORDED ORDER NOW. Both arrived
     * with the terminal peek's deletion: PB-INPUT-2's "visibly" sentence and the step that would
     * confirm a lease used to live on that screen, and this is the screen a session is read on now.
     * [leaseHeld] is set to `false` here, and only here among this file's tests, so that Take
     * control is actually offered and its tag appears for the walk below to find -- every other
     * fact this test varies (`journalStale`, `online`, `stopNotSent`) is independent of it.
     *
     * THE OUTCOME SITS WITH THE CONTROLS AND NOT WITH THE NOTICES. Every other notice on this
     * screen qualifies the CONTENT above which it is drawn -- the stale line qualifies the
     * transcript, the not-sent line qualifies what was typed -- and this one qualifies the two
     * controls, so it belongs immediately above them. A routed refusal at the top of a scrolling
     * transcript is a report the person who pressed the button is not looking at.
     *
     * THE VERDICT IS REFUSED FOR THE SAME REASON [leaseHeld] IS FALSE (agents-tracker-ksvb.10):
     * `detail.lease.detail` is drawn only where the machine sent words, so a fixture with no
     * refusal in it would walk past the one part whose POSITION this test is the only statement of
     * -- under the sentence it explains, and above the control that sentence offers.
     */
    @Test
    fun `an approval block tapped on this screen reaches the caller with its item id`() {
        var opened: String? = null
        val root = view(panelWithApproval(), onApproval = { opened = it })

        root.kitRequire(TranscriptTag.APPROVAL).performClick()

        assertEquals(
            "the session detail did not hand its onApproval down to the transcript, so the block " +
                "the machine is blocked on is drawn as a tappable row that reaches nothing -- and " +
                "the sheet that answers it has no way in",
            "i-approve-1",
            opened,
        )
    }

    @Test
    fun `a screen given no destination for an approval draws the block anyway`() {
        // `transcriptView`'s ruling, asserted through this screen: never hide what the machine is
        // waiting on, and never draw a tap with nothing behind it. The block is present; only the
        // listener is absent.
        assertNotNull(
            "the approval block vanished when no destination was supplied, which hides the one " +
                "thing in the conversation the machine is waiting on",
            view(panelWithApproval(), onApproval = null).kitFind(TranscriptTag.APPROVAL),
        )
    }

    /**
     * FAILING-FIRST (TDD RED, GG-5) for agents-tracker-dwwv.2.4: the sheet that ANSWERS a pending
     * approval is composed INSIDE this screen, beside the block that points at it -- not reached
     * by leaving it.
     *
     * WHAT THIS REPLACES. `PhoneSurface.openApproval` used to call `closeSessionDetail()`: the
     * only place the sheet was ever composed was under the inbox list, so answering a question
     * this very screen had just shown meant navigating away from the conversation to find the
     * card that answers it. `[approval]` is `PhoneSurface`'s `approvalHost` -- the SAME host the
     * inbox list places, re-parented here on the same "one component, two hosts" pattern
     * `statusSlot` already uses -- so there is no second composition, and no destination for a
     * tap on the transcript's approval block to reach: the answer is already on this screen.
     */
    @Test
    fun `the approval sheet is composed inside this screen -- there is nothing to navigate to`() {
        val sheet = TextView(context)

        val root = view(approval = sheet)

        assertSame(
            "the session detail does not place the approval host at all, so answering a pending " +
                "approval still has nowhere to go but out of this screen",
            sheet,
            root.kitFind(DetailTag.APPROVAL),
        )
    }

    /** The block that points at the sheet sits right above the sheet it points to. */
    @Test
    fun `the approval sheet sits directly under the block that points at it`() {
        val root = view(panelWithApproval(), approval = TextView(context))
        val order = root.compositionOrder()

        assertTrue(
            "the sheet is not on screen at all beside the block that names it",
            order.contains(DetailTag.APPROVAL),
        )
        assertEquals(
            "the sheet is the answer to the conversation that ends with the question -- it " +
                "belongs directly after the transcript, not somewhere a reader has to hunt for it",
            order.indexOf(DetailTag.TRANSCRIPT) + 1,
            order.indexOf(DetailTag.APPROVAL),
        )
    }

    /**
     * The re-parenting the tag above proves is only useful if the same View instance survives a
     * second composition -- `PhoneSurface` hands back the identical `approvalHost` on every draw,
     * the way it already does for `takeControl` (see "a control re-composed after a redraw is not
     * refused for having a parent").
     */
    @Test
    fun `a re-parented approval host is not refused for still having a parent`() {
        val host = TextView(context)
        val panel = panel()
        view(panel, approval = host)

        (host.parent as? ViewGroup)?.removeView(host)
        val second = view(panel, approval = host)

        assertSame(host, second.kitFind(DetailTag.APPROVAL))
    }

    // ---- the composer, and the ledger it ships with (agents-tracker-hxv) ---

    /**
     * MOVED, WITH ITS SUBJECT (chat-surface-plan §5). This test read "the composer is on the
     * screen a session is read and answered on" and asserted `DetailTag.COMPOSER` -- the defect
     * agents-tracker-nx44.6 reported, where this screen promised that what you type is sent live
     * while the app's only composer was parented at the bottom of the triage inbox.
     *
     * The composer is still on the screen a session is read on, and it is now PINNED there rather
     * than being the last child of a document: `ConversationScaffoldViewTest` asserts it is
     * outside the scroll and last in the composition, and `PhoneSurfaceConversationHostTest`
     * asserts the app the user opens actually pins it. What is left here is the negative claim, in
     * `the scrolling column composes no header, no composer and no destructive control` above --
     * because a promise this file merely stopped making is a promise that can be quietly broken
     * back.
     */

    /**
     * agents-tracker-hxv: the ledger "renders ABOVE the transcript, never over the composer -- it
     * concerns input already gone and must not cover the control the user is reaching for".
     */
    @Test
    fun `the undelivered ledger is above the transcript and the composer is below it`() {
        val root = view(panel(undelivered = lost()))
        val order = root.compositionOrder()

        assertTrue(
            "the ledger is not on screen at all, so a composer accepts input with nothing " +
                "reporting what it loses -- which is the state hxv's do-not-split ruling exists " +
                "to prevent",
            order.contains(DetailTag.UNDELIVERED),
        )
        assertTrue(
            "the ledger draws BELOW the transcript, so what was lost sits under a conversation " +
                "that grows while the user reads it",
            order.indexOf(DetailTag.UNDELIVERED) < order.indexOf(DetailTag.TRANSCRIPT),
        )
        // THE THIRD ASSERTION MOVED WITH THE COMPOSER. It read "the composer is not last, so a
        // report about input already gone can cover the control the user is reaching for" over
        // `order.indexOf(DetailTag.COMPOSER) == order.size - 1`. The composer cannot be covered by
        // anything in this column any more -- it is a sibling of the scroll, not its last child --
        // so the property holds by construction and is asserted where the construction is
        // (`ConversationScaffoldViewTest.the composer is pinned below the list and not inside it`).
        // What this file still owes is the half that is about THIS column: the ledger is above the
        // conversation, which is the two assertions above.
    }

    @Test
    fun `the ledger draws nothing at all for a session that lost nothing`() {
        val root = view(panel(undelivered = UndeliveredLedger.EMPTY))

        assertNull(
            "an empty ledger drew a notice anyway, which is a warning nobody wrote",
            root.kitFind(DetailTag.UNDELIVERED),
        )
        assertNull(
            "the acknowledgement is on screen with no backlog to acknowledge",
            root.kitFind(DetailTag.ACKNOWLEDGE),
        )
    }

    @Test
    fun `the machine's reason for a loss is drawn in the machine's own register`() {
        val root = view(panel(undelivered = lost("relay refused: swarm/offline")))

        assertEquals(
            "relay refused: swarm/offline",
            textOf(root.kitRequire(DetailTag.UNDELIVERED_DETAIL)),
        )
        assertNull(
            "a session that lost nothing drew an empty mono line, which is a cell reserved for " +
                "a reason that does not exist",
            view(panel(undelivered = UndeliveredLedger.EMPTY)).kitFind(DetailTag.UNDELIVERED_DETAIL),
        )
    }

    /** PB-INPUT-3's take_control_end: a lease this screen took can now be given back. */
    @Test
    fun `the repair control sits with the notice that reports the hole, and nowhere else`() {
        val resync = TextView(context)

        val stale = view(panel(journalStale = true), resync = resync)
        val whole = view(panel(journalStale = false), resync = TextView(context))
        val order = stale.compositionOrder()

        assertSame(resync, stale.kitFind(DetailTag.RESYNC))
        assertTrue(
            "the repair is drawn away from the sentence that explains what it repairs",
            order.indexOf(DetailTag.RESYNC) == order.indexOf(DetailTag.STALE) + 1,
        )
        assertNull(
            "a session whose chronology has no hole in it is offered a repair, which is a " +
                "control that spends a rate-bounded verb on nothing",
            whole.kitFind(DetailTag.RESYNC),
        )
    }

    /** The parts actually on screen, in the order the column drew them. */
    private fun View.compositionOrder(): List<String> {
        val found = mutableListOf<String>()
        fun walk(v: View) {
            (v.tag as? String)?.takeIf { it in DetailTag.COMPOSITION }?.let { found += it }
            if (v is ViewGroup) for (i in 0 until v.childCount) walk(v.getChildAt(i))
        }
        walk(this)
        return found
    }

    // DELETED with the lease UX they described (owner ruling R1):
    //   - `the parts are drawn in the order the recorded composition names`
    //   - `take control is on screen exactly while the model offers it`
    //   - `the control on screen is the one the surface supplied`
    //   - `a control re-composed after a redraw is not refused for having a parent`
    //   - `the lease sentence on screen is the one the model chose for that state`
    //   - `the machine's own reason is drawn under the lease sentence, and only when there is one`
    //   - `release is placed exactly while the lease is held and take control is not`
    // The controls, the tags and the notices they asserted are gone from the screen.

    // ---- W2.3: one refusal, said once (phone-refit-playbook §3) -----------------------------

    /** A refused send with the machine's words riding beside the refusal token. */
    private fun refusedPanel(detail: String): SessionDetailPanel {
        val refused = SessionDetail(
            sessionId = "mbp/api",
            online = true,
            journalStale = false,
            composerState = SendState.REFUSED,
            composerRefusal = "INPUT_BUSY",
            composerRefusalDetail = detail,
        )
        return SessionDetailScreen.of(
            refused,
            TranscriptScreen.of(emptyList()),
            SessionLease(sessionId = refused.sessionId, online = true),
            capabilities = SessionCapabilityFacts(structuredChat = true),
        )
    }

    private fun View.textViews(): List<TextView> {
        val found = mutableListOf<TextView>()
        fun walk(v: View) {
            if (v is TextView) found += v
            if (v is ViewGroup) for (i in 0 until v.childCount) walk(v.getChildAt(i))
        }
        walk(this)
        return found
    }

    /**
     * THE CONTRACT'S PREMISE, MADE TRUE. The composer-notice path carried only
     * `ComposerModel.noticeFor(state).copy`; the machine's words reached the reader through the
     * toast's mono suffix alone, and W2.3 deletes the toast. So the words ride as
     * `composerRefusalDetail` and are drawn as the kit's mono tertiary cell directly under the
     * sentence they qualify -- and not at all when the machine sent none.
     */
    @Test
    fun `the machine's words are drawn under the composer notice and absent when empty`() {
        val words = "session \"mbp/api\" had input on its line, so this message was not written"
        val root = view(refusedPanel(detail = words))
        val notice = root.kitRequire(DetailTag.COMPOSER_NOTICE)
        val cell = root.kitRequire(DetailTag.COMPOSER_NOTICE_DETAIL)
        assertEquals(words, textOf(cell))
        val column = notice.parent as ViewGroup
        assertSame(
            "the machine's words sit directly under the sentence they qualify",
            cell,
            column.getChildAt(column.indexOfChild(notice) + 1),
        )
        assertEquals(
            "the cell is the kit's mono tertiary cell (`noticeDetail`, `.sheet2 .ctx`): visibly the " +
                "machine talking, never this app's own body type",
            Kit.colour(context, R.color.swarm_text_tertiary),
            (cell as TextView).currentTextColor,
        )
        assertNull(
            "no words, no cell: an empty mono line is a cell reserved for a reason that does not exist",
            view(refusedPanel(detail = "")).kitFind(DetailTag.COMPOSER_NOTICE_DETAIL),
        )
    }

    /** Fence (W2.3): the sentence is on the view tree once, whatever else the screen draws. */
    @Test
    fun `a refused send says its sentence exactly once across the view tree`() {
        val sentence = ComposerModel.noticeFor("INPUT_BUSY").copy
        val root = view(refusedPanel(detail = "the machine said so"))
        assertEquals(
            "one refused send reports itself more than once on one screen",
            1,
            root.textViews().count { it.text.toString() == sentence },
        )
    }
}
