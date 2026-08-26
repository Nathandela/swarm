package dev.swarm.phone.ui.screens

import android.content.Context
import android.view.View
import android.widget.FrameLayout
import android.widget.TextView
import androidx.test.core.app.ApplicationProvider
import dev.swarm.phone.theme.SwarmTheme
import dev.swarm.phone.ui.InteractionItem
import dev.swarm.phone.ui.SessionDetail
import dev.swarm.phone.ui.SessionLease
import dev.swarm.phone.ui.TriageInbox
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner

/**
 * FAILING-FIRST (TDD RED, GG-5) for the conversation header's own facts, and for the patch-path
 * decision they forced. Plan: docs/specifications/chat-surface-plan.md §5 and D.2. Bead:
 * agents-tracker-tbpm.1.
 *
 * **WHY THE HEADER IS TESTED AS A MODEL AND NOT THROUGH THE APP.** `PhoneRuntime.phone()` answers
 * `PhoneStartup.Unavailable` on every JVM run -- the phone core is a gomobile AAR of `.so` files
 * cross-compiled for Android ABIs -- so no session row exists, no drill-down can be opened, and
 * the whole conversation is out of reach of an `ActivityScenario` (the argument in full is
 * `android/gate/pbapp6_pbinput2_surface_test.go`'s, and it is why the session detail's reachability
 * is fenced by a Go source scan rather than by a Robolectric test). What IS reachable is every
 * decision the header rests on, because each of them is a pure function of a panel.
 *
 * WHAT EACH ASSERTION IS FOR:
 *
 *  - the five state words, and their PRECEDENCE, which is five rulings rather than a `when`;
 *  - that "working" is read from the OPEN TURN and not from a running tool (plan D.1) -- the
 *    difference between a header that goes idle while an agent thinks and one that reads working
 *    forever after a completion goes missing;
 *  - that the dot's Group is the roster's own word, and that a roster that cannot answer does not
 *    take the screen down with it;
 *  - that the patch path SURVIVES a state-word change and still refuses a composition change,
 *    which is the D.2 decision the plan requires be taken in the RED phase.
 */
@RunWith(RobolectricTestRunner::class)
class SessionDetailHeaderTest {

    private val context: Context
        get() = SwarmTheme.applyTo(ApplicationProvider.getApplicationContext())

    private fun item(
        kind: String,
        itemId: String,
        turnId: String = "",
        status: String = "",
        body: String = "{}",
    ) = InteractionItem(
        sessionId = SESSION,
        itemId = itemId,
        cursor = 1,
        kind = kind,
        status = status,
        turnId = turnId,
        body = body,
    )

    /**
     * A CLOSED turn: IS-ENV-1 opens one on a `user_message` and closes it on a terminal
     * `agent_message`, which is the state an idle session is in and the value the daemon matches
     * an idle session by (the empty `expected_turn`).
     */
    private fun closedTurn() = listOf(
        item("user_message", "i-1", turnId = TURN, body = """{"text":"check the relay logs"}"""),
        item("agent_message", "i-2", turnId = TURN, status = "completed", body = """{"text":"done"}"""),
    )

    /** The same conversation with a NEW turn opened on top of it. */
    private fun openTurn() = closedTurn() +
        item("user_message", "i-3", turnId = NEXT_TURN, body = """{"text":"and the relay pin"}""")

    private fun panel(
        online: Boolean = true,
        ended: Boolean = false,
        group: String = "working",
        machineLabel: String = MACHINE,
        items: List<InteractionItem> = emptyList(),
        structuredChat: Boolean = true,
    ): SessionDetailPanel = SessionDetailScreen.of(
        SessionDetail(
            sessionId = SESSION,
            online = online,
            journalStale = false,
            ended = ended,
            title = "claude-NewLatexCV",
            group = group,
            machineLabel = machineLabel,
        ),
        TranscriptScreen.of(items),
        SessionLease(sessionId = SESSION, online = online),
        capabilities = SessionCapabilityFacts(structuredChat = structuredChat),
    )

    // ---- the five words, and the order they outrank each other in ------------

    @Test
    fun `an idle session says idle and a session with an open turn says working`() {
        assertEquals(
            "an idle session's header says something other than \"idle\". No turn is open, which " +
                "is exactly the state the daemon matches by the EMPTY expected_turn",
            "idle · $MACHINE",
            panel().headerSubtitle,
        )
        assertEquals(
            "a session with an OPEN turn does not read as working, so the header cannot answer " +
                "the one question a reader opens it to check",
            "working · $MACHINE",
            panel(items = openTurn()).headerSubtitle,
        )
    }

    /**
     * Plan D.1, as the assertion that separates the right source from the plausible one.
     *
     * `SessionDetailPanel` derives its PLACEHOLDER from `blocks.any { it.running }` today, and
     * that is the reading this test refuses for the header: a `tool_run` that is still
     * `in_progress` is not the unit of work. An agent that is only THINKING has no open tool and
     * would read idle while it types; a tool whose completion never arrived would read working
     * forever, on a header no event can clear.
     */
    @Test
    fun `working is read from the open turn and not from a running tool`() {
        // THE MISSED COMPLETION, EXACTLY, AND THE ORDER IS THE FIXTURE. `openTurnOf` walks the
        // items and a terminal `agent_message` is what CLOSES a turn, so the stranded tool has to
        // sit INSIDE the turn that closed over it -- which is what a completion that never arrived
        // actually looks like on the wire. `blocks.any { it.running }` answers true here forever;
        // the open turn answers "", which is the truth.
        val strandedTool = listOf(
            item("user_message", "i-1", turnId = TURN, body = """{"text":"run the tests"}"""),
            item("tool_run", "i-tool", turnId = TURN, status = "in_progress"),
            item("agent_message", "i-2", turnId = TURN, status = "completed", body = """{"text":"done"}"""),
        )

        assertEquals(
            "the header read \"a tool is running\" instead of the open turn, so a completion " +
                "that never arrived leaves this session reading working for the rest of its life",
            "idle · $MACHINE",
            panel(items = strandedTool).headerSubtitle,
        )
    }

    @Test
    fun `the link outranks the turn and ended outranks everything`() {
        assertEquals(
            "a phone that cannot reach the machine still reported activity it is not receiving",
            "not connected · $MACHINE",
            panel(online = false, items = openTurn()).headerSubtitle,
        )
        assertEquals(
            "an ended session read as something other than ended, though there is nothing to " +
                "type into whatever the link or the record says",
            "ended · $MACHINE",
            panel(ended = true, online = false, items = openTurn()).headerSubtitle,
        )
    }

    @Test
    fun `a session blocked on the reader says needs you rather than working`() {
        assertEquals(
            "a session the agent is blocked on read as working. It is not working -- it is " +
                "stopped, waiting on this reader, and that is the fact worth the subtitle",
            "needs you · $MACHINE",
            panel(group = "needs_input", items = openTurn()).headerSubtitle,
        )
    }

    @Test
    fun `a session id that names no machine says the state alone`() {
        assertEquals(
            "the subtitle kept its separator over a machine half that does not exist, which is " +
                "punctuation claiming a fact",
            "idle",
            panel(machineLabel = "").headerSubtitle,
        )
    }

    // ---- the dot's Group: read, and survivable ------------------------------

    @Test
    fun `the dot draws the roster's own Group, so the header agrees with the row that was tapped`() {
        for (group in TriageInbox.TRIAGE_ORDER) {
            assertEquals(
                "the header derived a Group of its own for \"$group\" instead of rendering the " +
                    "roster's. A session must read the same here as on the row the reader tapped",
                group,
                panel(group = group).headerGroup,
            )
        }
    }

    @Test
    fun `a roster that cannot name the session still draws a header`() {
        val orphan = panel(group = "")

        assertTrue(
            "the header carries a Group `Kit.groupColour` cannot place, so a drill-down whose " +
                "session left the roster between two draws takes the whole screen down with it " +
                "-- on the one surface a person is reading",
            orphan.headerGroup in TriageInbox.TRIAGE_ORDER,
        )
        assertEquals(
            "the substituted Group ASSERTS something. A phone whose roster cannot name the " +
                "session has no evidence the agent is working, waiting or ready; the recessive " +
                "grey is the only mark in the design that claims no live activity",
            "completed",
            orphan.headerGroup,
        )
    }

    // ---- D.2: what the patch path had to accept, and what it must still refuse ----

    /**
     * THE DECISION THE PLAN REQUIRES BE TAKEN IN THE RED PHASE (D.2), stated as behaviour.
     *
     * The header's subtitle carries the state word and the state word is read from the open turn,
     * so it flips at every turn boundary -- as often as the transcript moves on a working session.
     * If `sessionDetailRedraw` refused a panel differing only in it, the conversation would be
     * fully rebuilt at that rate: the reader's place lost, every row re-measured, on the one screen
     * whose purpose is continuous reading. Admitting it is safe because the header is not in the
     * column the patch walks at all -- it is the scaffold's fixed region, redrawn on the surface's
     * own clock, which is the whitelist's own stated test.
     */
    @Test
    fun `a turn opening patches the conversation instead of rebuilding it`() {
        // BOTH FIXTURES HOLD ITEMS, deliberately: `offersLoadEarlier` is derived from whether
        // there is anything to page before, and it ADDS OR REMOVES a child of the column, so a
        // fixture that went from empty to non-empty would refuse the patch for a reason that has
        // nothing to do with the header.
        val idle = panel(items = closedTurn())
        val working = panel(items = openTurn())
        val host = column(idle)

        assertEquals("the fixture does not change the state word at all", "idle · $MACHINE", idle.headerSubtitle)
        assertEquals("working · $MACHINE", working.headerSubtitle)
        assertTrue(
            "the redraw refused a panel that differs only in the transcript and the header's own " +
                "two fields, so the conversation is rebuilt every time a turn opens or closes -- " +
                "the reader's place thrown away at exactly the rate the agent works",
            sessionDetailRedraw(host, idle, working),
        )
    }

    @Test
    fun `losing the composer still forces a rebuild, because it changes the composition`() {
        val whole = panel(items = closedTurn())
        val host = column(whole)

        assertFalse(
            "the redraw patched across a change that ADDS OR REMOVES children -- a session that " +
                "lost its message sink keeps the pinned bar and loses the sentence that says why",
            sessionDetailRedraw(host, whole, panel(items = closedTurn(), structuredChat = false)),
        )
    }

    /** The scrolling column alone, which is all `sessionDetailRedraw` is ever handed. */
    private fun column(panel: SessionDetailPanel): View = sessionDetailView(
        context = context,
        panel = panel,
        resync = TextView(context),
        acknowledge = TextView(context),
        approval = FrameLayout(context),
        outcome = "",
    )

    private companion object {
        const val SESSION = "ep-9f2a/NewLatexCV"
        const val MACHINE = "ep-9f2a"
        const val TURN = "t-1"
        const val NEXT_TURN = "t-2"
    }
}
