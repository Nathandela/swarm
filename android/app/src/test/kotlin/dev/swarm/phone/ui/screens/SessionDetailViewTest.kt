package dev.swarm.phone.ui.screens

import android.content.Context
import android.view.View
import android.view.ViewGroup
import android.widget.TextView
import androidx.test.core.app.ApplicationProvider
import dev.swarm.phone.theme.SwarmTheme
import dev.swarm.phone.ui.CommandVerdict
import dev.swarm.phone.ui.InteractionItem
import dev.swarm.phone.ui.OperationOutcome
import dev.swarm.phone.ui.SessionDetail
import dev.swarm.phone.ui.SessionLease
import dev.swarm.phone.ui.kit.KitTag
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
    ): SessionDetailPanel {
        val detail = SessionDetail(
            sessionId = "mbp/api",
            leaseHeld = leaseHeld,
            online = online,
            journalStale = journalStale,
            stopNotSent = stopNotSent,
        )
        return SessionDetailScreen.of(
            detail,
            TranscriptScreen.of(emptyList()),
            SessionLease(sessionId = detail.sessionId, leaseHeld = detail.leaseHeld, online = detail.online),
            verdict,
        )
    }

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
            leaseHeld = true,
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
            SessionLease(sessionId = detail.sessionId, leaseHeld = true, online = true),
        )
    }

    private fun view(
        panel: SessionDetailPanel = panel(),
        outcome: String = "",
        onBack: () -> Unit = {},
        takeControl: View = TextView(context),
        onApproval: ((String) -> Unit)? = null,
    ): View = sessionDetailView(
        context = context,
        panel = panel,
        takeControl = takeControl,
        stop = TextView(context).apply { text = panel.stopLabel },
        kill = TextView(context).apply { text = panel.killLabel },
        outcome = outcome,
        onBack = onBack,
        onApproval = onApproval,
    )

    private fun textOf(view: View?): String = (view as? TextView)?.text?.toString().orEmpty()

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
            DetailTag.NAV to "C2.1 -- the drill-down header, per derivation section 4",
            DetailTag.TRANSCRIPT to "C2.3 -- the conversation, composed by transcriptView",
            DetailTag.STOP to "PB-APP-3's persistent Stop",
            DetailTag.KILL to "the escalation, behind its own confirmation",
        ).forEach { (tag, what) ->
            assertNotNull("the session detail renders nothing for $what", root.kitFind(tag))
        }
    }

    @Test
    fun `the header comes first and names the session`() {
        val root = view()
        val nav = root.kitRequire(DetailTag.NAV)

        assertEquals("mbp/api", textOf(nav.kitRequire(KitTag.DRILL_TITLE)))
    }

    @Test
    fun `the back control navigates, rather than being a chevron that does nothing`() {
        var went = false
        val root = view(onBack = { went = true })

        root.kitRequire(DetailTag.NAV).kitRequire(KitTag.DRILL_BACK).performClick()

        assertTrue(
            "the drill-down chevron is drawn and wired to nothing, which is the defect " +
                "agents-tracker-2yb reports: a control that looks like a control and does not act",
            went,
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
    fun `the parts are drawn in the order the recorded composition names`() {
        val root = view(
            panel = panel(
                leaseHeld = false,
                journalStale = true,
                online = false,
                stopNotSent = true,
                verdict = refusedLease(),
            ),
            outcome = "Your machine refused that.",
        )

        val order = mutableListOf<String>()
        fun walk(v: View) {
            (v.tag as? String)?.let { if (it in DetailTag.COMPOSITION) order += it }
            if (v is ViewGroup) for (i in 0 until v.childCount) walk(v.getChildAt(i))
        }
        walk(root)

        assertEquals(DetailTag.COMPOSITION.toList(), order)
    }

    // ---- PB-INPUT-2 AS DRAWN, RE-HOMED FROM THE DELETED PEEK ---------------
    //
    // THE FOUR TESTS BELOW ARE `PeekPanelViewTest`'S. That file is deleted with the terminal peek
    // (ADR-009 (3)) and these assertions are not: the peek held PB-INPUT-2's sentence and the Take
    // control button only because the peek was where the keyboard was, (5) keeps the input substrate
    // "exactly as decided", and this is the screen a session is read on now. `PeekTag.TAKE_CONTROL`
    // and `PeekTag.LEASE` become `DetailTag.TAKE_CONTROL` and `DetailTag.LEASE`; nothing else about
    // what they assert changed.

    @Test
    fun `take control is on screen exactly while the model offers it`() {
        // PB-INPUT-2's recorded failure mode is that this control looked identical in both states.
        // The inverse defect is the one that nearly shipped elsewhere: drawing it unconditionally,
        // which tells a user who already holds the lease to take it.
        val offered = panel(leaseHeld = false)
        assertTrue("the model does not offer the control, so this asserts nothing", offered.offersTakeControl)
        assertEquals(1, view(offered).allTagged(DetailTag.TAKE_CONTROL).size)

        val held = panel(leaseHeld = true)
        assertTrue("the model still offers the control, so this asserts nothing", !held.offersTakeControl)
        assertEquals(
            "the Take control button is on screen for a session whose lease the machine has " +
                "already confirmed",
            0,
            view(held).allTagged(DetailTag.TAKE_CONTROL).size,
        )
    }

    @Test
    fun `the control on screen is the one the surface supplied`() {
        val supplied = TextView(context)
        val root = view(panel(leaseHeld = false), takeControl = supplied)

        assertSame(supplied, root.allTagged(DetailTag.TAKE_CONTROL).single())
    }

    @Test
    fun `a control re-composed after a redraw is not refused for having a parent`() {
        // The panel is rebuilt whenever the conversation changes, which is every interaction item.
        // A slot arriving at its next addView still claiming a discarded parent is refused by
        // Android outright, and the failure is a crash on a screen somebody is holding.
        val supplied = TextView(context)
        val panel = panel(leaseHeld = false)
        view(panel, takeControl = supplied)
        val second = view(panel, takeControl = supplied)

        assertSame(supplied, second.allTagged(DetailTag.TAKE_CONTROL).single())
    }

    @Test
    fun `the lease sentence on screen is the one the model chose for that state`() {
        listOf(true, false).forEach { held ->
            val panel = panel(leaseHeld = held)
            assertEquals(panel.leaseNotice, textOf(view(panel).kitRequire(DetailTag.LEASE)))
        }
        assertTrue(
            "the two lease states put the same sentence on screen, which is the state PB-INPUT-2 " +
                "was recorded NOT MET in -- a user could not tell until a keystroke vanished",
            textOf(view(panel(leaseHeld = true)).kitRequire(DetailTag.LEASE)) !=
                textOf(view(panel(leaseHeld = false)).kitRequire(DetailTag.LEASE)),
        )
    }

    /**
     * FAILING-FIRST (TDD RED, GG-5) for agents-tracker-ksvb.10: the machine's own words are a
     * SECOND VIEW, drawn only where there are words.
     *
     * They used to be spliced into the sentence above -- `Your machine refused this phone control
     * of the session: <a Go error>.` -- so the wire string was drawn in the notice's own type and
     * ink, and nothing on screen said which half this product had written. The kit's `noticeDetail`
     * is the `.sheet2 .ctx` cell and `NoticeTest` argues what it looks like; what only this suite
     * can say is that the screen composes one at all, and skips it when the machine sent nothing.
     */
    @Test
    fun `the machine's own reason is drawn under the lease sentence, and only when there is one`() {
        val refused = panel(leaseHeld = false, verdict = refusedLease())

        assertEquals(
            "the machine's reason reaches no view, so a refusal on screen names no cause at all",
            refused.leaseDetail,
            textOf(view(refused).kitRequire(DetailTag.LEASE_DETAIL)),
        )
        assertNull(
            "an empty detail is drawn as a blank mono line -- a cell reserved for a reply the " +
                "machine never sent, which is the call the outcome and stale notices already make",
            view(panel(leaseHeld = false)).kitFind(DetailTag.LEASE_DETAIL),
        )
        assertTrue(
            "the sentence swallowed the machine's words again, so the demotion is undone on screen " +
                "whatever the model decided",
            !textOf(view(refused).kitRequire(DetailTag.LEASE)).contains(refused.leaseDetail),
        )
    }

    // ---- the way to the sheet ---------------------------------------------

    /**
     * The tap that opens the approval sheet, asserted AT THIS SCREEN because this screen is the only
     * thing between the surface's callback and the block that carries it.
     *
     * WHAT `TranscriptViewTest` CANNOT SEE. That suite hands `transcriptView` a listener directly and
     * proves the block calls it; it says nothing about whether the session detail passed one DOWN.
     * `sessionDetailView`'s own contract is "passed straight through", and the failure mode is silent
     * in exactly the way the kit's own rule names: null draws the block and no control, so a screen
     * that dropped the argument renders an approval that looks identical and does nothing -- the
     * dead-chevron defect (agents-tracker-2yb) one surface over.
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
}
