package dev.swarm.phone.ui.screens

import android.content.Context
import android.view.View
import android.widget.TextView
import androidx.test.core.app.ApplicationProvider
import dev.swarm.phone.theme.SwarmTheme
import dev.swarm.phone.ui.InteractionItem
import dev.swarm.phone.ui.SessionDetail
import dev.swarm.phone.ui.SessionLease
import dev.swarm.phone.ui.kit.ComposerAvailability
import dev.swarm.phone.ui.kit.ComposerModel
import dev.swarm.phone.ui.kit.SendState
import dev.swarm.phone.ui.kit.kitFind
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner

/**
 * FAILING-FIRST (TDD RED, GG-5) for Mirror M2.4/M2.5 and M3.1's affordance, on the SCREEN rather
 * than in a model nothing calls. Bead agents-tracker-hggx.7; Wave R6 review finding B8.
 *
 * `ComposerModel` had every state M2.4 asks for -- the availability gate, the visible
 * pending/sent/refused lifecycle, the gentle `stale_turn` copy, the status-driven placeholder --
 * and NOTHING CALLED IT. The session detail's send control still rode the lease input plane
 * (`App.SendInput`, raw PTY bytes behind a lease) while `App.ComposerSend` sat in
 * android/unbound-verbs.tsv. So the composer's states were a table nobody read, and the wave's
 * headline verb was unreachable from the app a user installs.
 *
 * This suite pins what the PANEL decides, because that is what a screen shows and what a redraw
 * is guarded on. Three things it will not let go of:
 *
 *  - **ADR-017's structural absence.** A session whose structured record tore has NO message
 *    sink. The composer must be gone, not greyed: a greyed control promises a verb that will
 *    come back, and this one never does (the degrade is one-way).
 *  - **The draft survives every refusal.** A composer that eats the user's words punishes them
 *    for the machine's answer, and `stale_turn` is the ORDINARY case -- the conversation moved
 *    on between render and tap.
 *  - **Both halves of the race carry the same precondition.** Send and Stop are tapped under one
 *    race, so the screen has to hand both the turn it DREW them against (review finding B7).
 */
@RunWith(RobolectricTestRunner::class)
class SessionDetailComposerTest {

    private val context: Context
        get() = SwarmTheme.applyTo(ApplicationProvider.getApplicationContext())

    private val session = "mbp/quanthome"

    private fun agent(text: String, turn: String = "turn-a") = InteractionItem(
        sessionId = session, itemId = "a-${text.hashCode()}", cursor = 1,
        kind = "agent_message", text = text, turnId = turn,
    )

    /**
     * An agent message that CLOSED its turn, which is what a settled reply is (IS-ENV-1).
     *
     * IT IS A SECOND HELPER AND NOT A DEFAULT ON [agent], because the two are used for opposite
     * purposes in this file: B7's `expectedTurn` fixture needs a turn that is still OPEN, and the
     * placeholder's idle case needs one that is closed. A single helper doing both by accident is
     * how a fixture comes to assert the wrong state without saying so.
     */
    private fun settledAgent(text: String, turn: String = "turn-a") =
        agent(text, turn).copy(status = "completed")

    private fun runningTool() = InteractionItem(
        sessionId = session, itemId = "t-run", cursor = 2, kind = "tool_run",
        status = "in_progress", toolKind = "execute", turnId = "turn-a",
        body = """{"tool":"Bash","action":{"command":"go test ./..."}}""",
    )

    private fun gap() = InteractionItem(
        sessionId = session, itemId = "structured_gap:1", cursor = 3,
        kind = "structured_gap", status = "completed", text = "hook spool gap at seq 41",
    )

    /**
     * WAVE R8 (ADR-017 T2 rule 3): the composer's availability now comes from the MACHINE's
     * capability record, so the fixture supplies one.
     *
     * It used to come from the shape of the transcript -- `!transcript.structureTorn` -- and that
     * is the inference the rule names by example. Nothing this file asserts has changed; what
     * changed is WHERE the fact comes from, so the torn case below now sets `structuredChat =
     * false` (which is exactly what the daemon's one-way degrade writes into the record after a
     * proven structured gap) instead of relying on a gap element in the item list to be read as a
     * capability.
     */
    private fun panel(
        items: List<InteractionItem> = listOf(agent("hello")),
        online: Boolean = true,
        sendState: SendState? = null,
        refusal: String = "",
        structuredChat: Boolean = true,
    ): SessionDetailPanel = SessionDetailScreen.of(
        SessionDetail(
            sessionId = session,
            online = online,
            journalStale = false,
            title = "api refactor",
            composerState = sendState,
            composerRefusal = refusal,
        ),
        TranscriptScreen.of(items),
        SessionLease(sessionId = session, online = online),
        capabilities = SessionCapabilityFacts(structuredChat = structuredChat),
    )

    private fun view(p: SessionDetailPanel): View =
        sessionDetailView(
            context = context,
            panel = p,
            resync = TextView(context),
            acknowledge = TextView(context),
            loadEarlier = TextView(context),
            approval = TextView(context),
            outcome = "",
        )

    // ---- M2.4: the composer exists, or honestly does not --------------------

    /**
     * MOVED, WITH THE BAR ITSELF (chat-surface-plan §5). These two asserted `DetailTag.COMPOSER`
     * inside this column; the bar is `conversationScaffoldView`'s pinned region now, and what
     * decides whether a session gets one is [SessionDetailPanel.composerIsBar] -- ONE predicate,
     * read by this column to decide whether to draw the sentence and by `PhoneSurface` to decide
     * whether to pin the bar. So the fact under test is unchanged and its subject is the
     * predicate rather than a tag, which is what these two assert now. That the pinned region
     * actually appears is `PhoneSurfaceConversationHostTest`'s.
     */
    @Test
    fun `an online session with a structured record offers the composer`() {
        assertEquals(ComposerAvailability.AVAILABLE, panel().composerAvailability)
        assertTrue(
            "a healthy session is not offered the composer bar at all",
            panel().composerIsBar,
        )
        assertNull(
            "the column draws the no-composer sentence over a session that HAS a composer, so " +
                "the reader is told they cannot type into a session they can type into",
            view(panel()).kitFind(DetailTag.COMPOSER_ABSENT),
        )
    }

    @Test
    fun `an offline session keeps the composer and says the send will not go`() {
        val p = panel(online = false)
        assertEquals(ComposerAvailability.OFFLINE, p.composerAvailability)
        assertTrue(
            "an offline composer was removed. The draft is still worth typing -- the link comes " +
                "back -- and a control that vanishes teaches the user the feature is gone",
            p.composerIsBar,
        )
        assertNull(
            "an offline session drew the no-composer sentence, which is the removal above " +
                "wearing a paragraph",
            view(p).kitFind(DetailTag.COMPOSER_ABSENT),
        )
        // **D.10, AT THE LEVEL A JVM CAN SEE IT.** The bar staying is only half the state: the
        // two sentences `ComposerModel` computes for OFFLINE were read by NOTHING, because the
        // hint came from `placeholderFor(working)` and `composerShut` was discarded on this arm.
        // The second of them is the one fact on the sheet a reader cannot deduce from a greyed
        // field. `PhoneSurface.drawComposerRegion` is what spends them -- the field's hint and the
        // line under the bar -- and this is the fact it spends.
        assertNotNull(
            "an offline session has no shut copy at all, so the bar it keeps has nothing true to " +
                "say inside it",
            p.composerShut,
        )
        assertTrue(
            "the offline copy does not say that nothing is held. A composer that looks live and " +
                "says only \"not connected\" invites a message the reader believes will go when " +
                "the link returns, and it never will",
            p.composerShut!!.detail.isNotEmpty(),
        )
    }

    @Test
    fun `a torn session has no composer at all and says why`() {
        val p = panel(items = listOf(agent("before"), gap()), structuredChat = false)
        assertEquals(
            "ADR-017 T2 rule 2: structured_chat=false means there is NO message sink, so a " +
                "composer over one is a message that goes in and can never be shown",
            // MOVED: this fixture holds a `gap()` element, so the state it lands in is the
            // one that names what actually happened -- the record tore -- rather than the
            // catch-all that also covered "no record was ever authored".
            ComposerAvailability.TORN,
            p.composerAvailability,
        )
        val root = view(p)
        assertTrue(
            "the composer bar is still offered over a session whose structured record tore, so " +
                "the surface pins a control promising a verb the session structurally lacks",
            !p.composerIsBar,
        )
        assertNotNull(
            "the composer vanished with no explanation, which reads as a bug rather than as a " +
                "capability the machine lost",
            root.kitFind(DetailTag.COMPOSER_ABSENT),
        )
    }

    // ---- M2.5: the placeholder is status-driven ------------------------------

    /**
     * **THE SOURCE OF "working" CHANGED UNDER THIS TEST AND THE FIXTURE MOVED WITH IT** (plan
     * D.1). The placeholder read `blocks.any { it.running }`; it reads the OPEN TURN now, which is
     * the same value the header word, the working line and Stop read. So the idle case is a turn
     * that CLOSED rather than a list with no running tool in it, and the working case is a turn
     * that is still open rather than a tool that happens to be in progress. What is asserted is
     * unchanged; what it is asserted over is now the fact the daemon itself matches on.
     */
    @Test
    fun `the placeholder invites a message when idle and feedback while working`() {
        assertEquals(
            "a session whose last turn is over still invites feedback into it, so the composer " +
                "reads as though the agent were working with nothing running",
            ComposerModel.placeholderFor(false),
            panel(items = listOf(settledAgent("hello"))).composerPlaceholder,
        )
        assertEquals(
            "a working agent still takes input, as feedback into the running turn, and the " +
                "placeholder must not imply the composer is closed",
            ComposerModel.placeholderFor(true),
            panel(items = listOf(settledAgent("hello"), runningTool())).composerPlaceholder,
        )
    }

    // ---- M2.4: the visible per-send lifecycle --------------------------------

    @Test
    fun `every send state is tellable apart on screen`() {
        assertEquals("", panel().composerStateLabel)
        for (state in SendState.values()) {
            assertEquals(
                ComposerModel.stateLabel(state),
                panel(sendState = state).composerStateLabel,
            )
        }
    }

    @Test
    fun `a stale turn is refused gently and the draft is kept`() {
        val p = panel(sendState = SendState.STALE_TURN, refusal = "STALE_TURN")
        assertEquals(ComposerModel.noticeFor("STALE_TURN").copy, p.composerNotice)
        assertTrue(
            "the composer threw away what the user typed because the conversation moved on, " +
                "which punishes them for the machine's answer",
            p.composerRetainsDraft,
        )
        assertNotNull(
            "the gentle refusal never reached the screen",
            view(p).kitFind(DetailTag.COMPOSER_NOTICE),
        )
    }

    @Test
    fun `any other refusal keeps the draft too and says the message was not delivered`() {
        val p = panel(sendState = SendState.REFUSED, refusal = "OFFLINE")
        assertEquals(ComposerModel.noticeFor("OFFLINE").copy, p.composerNotice)
        assertTrue(p.composerRetainsDraft)
    }

    /**
     * FAILING-FIRST for the owner's label/notice ruling: **a label names a state, a notice
     * explains one, and no state gets both.**
     *
     * WHAT IT IS AGAINST. `stateLabel(STALE_TURN)` is "Not sent - the conversation moved on" and
     * `noticeFor("STALE_TURN")` is the sentence that says the same thing and then says what to do
     * about it. Drawn together they are two near-duplicate wordings under one bubble, which reads
     * as two failures. The notice wins because it is the half that carries the remedy; the label
     * survives alone for `PENDING`, which names a state and explains nothing.
     */
    @Test
    fun `a refusal says one thing, not the same thing twice`() {
        val refused = view(panel(sendState = SendState.STALE_TURN, refusal = "STALE_TURN"))

        assertNotNull(
            "the refusal's remedy is not on screen at all",
            refused.kitFind(DetailTag.COMPOSER_NOTICE),
        )
        assertNull(
            "the send's state label is drawn ABOVE the notice that explains the same refusal, so " +
                "one failed send reports itself twice in two near-identical wordings",
            refused.kitFind(DetailTag.COMPOSER_STATE),
        )
        assertNotNull(
            "a send still crossing draws nothing at all, though \"Sending\" names a state and " +
                "explains none -- it is the one label with no notice to be duplicated by",
            view(panel(sendState = SendState.PENDING)).kitFind(DetailTag.COMPOSER_STATE),
        )
    }

    @Test
    fun `a healthy composer prints no notice`() {
        assertEquals("", panel().composerNotice)
        assertNull(view(panel()).kitFind(DetailTag.COMPOSER_NOTICE))
    }

    // ---- B7: one race, one precondition --------------------------------------

    @Test
    fun `the panel names the turn both Send and Stop are drawn against`() {
        val p = panel(items = listOf(agent("a", turn = "turn-a"), agent("b", turn = "turn-b")))
        assertEquals(
            "App.ComposerSend and App.Interrupt both REQUIRE the turn the screen rendered them " +
                "against, and the screen had none to give",
            "turn-b",
            p.expectedTurn,
        )
    }

    // ---- M3.1: load earlier ---------------------------------------------------

    @Test
    fun `the load-earlier control is placed above the conversation it extends`() {
        val p = panel()
        assertTrue(p.offersLoadEarlier)
        assertEquals(p.transcript.oldestItemId, p.loadEarlierBeforeItem)
        assertNotNull(
            "ADR-014's paged history has no way to be asked for on the handset",
            view(p).kitFind(DetailTag.LOAD_EARLIER),
        )
    }

    @Test
    fun `no control is drawn once the machine has declared the floor`() {
        val p = SessionDetailScreen.of(
            SessionDetail(
                sessionId = session, online = true,
                journalStale = false, title = "api refactor",
            ),
            TranscriptScreen.of(listOf(agent("oldest")), atFloor = true),
            SessionLease(sessionId = session, online = true),
        )
        assertFalse(p.offersLoadEarlier)
        assertNull(view(p).kitFind(DetailTag.LOAD_EARLIER))
    }
}
