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
 *  - that the sixth case is the absence of a word and not a sixth word: where this phone holds no
 *    record it claims none of the five, and the three facts that do not come from the transcript
 *    keep theirs;
 *  - that "working" is read from the OPEN TURN and not from a running tool (plan D.1) -- the
 *    difference between a header that goes idle while an agent thinks and one that reads working
 *    forever after a completion goes missing;
 *  - that the dot's Group is the roster's own word, that a roster that cannot answer does not take
 *    the screen down with it, and -- since H.8 -- that it does not answer for the roster either:
 *    no Group means no mark, never a stand-in that reads as finished work;
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

    /**
     * **THE FIXTURE MOVED AND THE EXPECTATION DID NOT, AND THE DIFFERENCE MATTERS.** This built
     * `panel()`, which takes the helper's defaulted `items = emptyList()` -- so a test named for
     * an IDLE session was asserting over a session this phone holds NO RECORD of, and passing for
     * the right words and the wrong reason. It now constructs the session it claims to describe.
     *
     * The expectation is untouched: an idle session still says `idle`. What changed is that the
     * fixture is now one, and the correct fixture was already in this file -- `closedTurn()`, used
     * at the patch-path test below with the comment that it does not change the state word at all.
     * This is a fixture drifting from its own file's practice, not a claim edited into agreement
     * with a fix. `SessionDetailComposerTest`'s `settledAgent` helper already carries the general
     * form of the argument: "a single helper doing both by accident is how a fixture comes to
     * assert the wrong state without saying so."
     */
    @Test
    fun `an idle session says idle and a session with an open turn says working`() {
        assertEquals(
            "an idle session's header says something other than \"idle\". No turn is open, which " +
                "is exactly the state the daemon matches by the EMPTY expected_turn",
            "Idle · $MACHINE",
            panel(items = closedTurn()).headerSubtitle,
        )
        assertEquals(
            "a session with an OPEN turn does not read as working, so the header cannot answer " +
                "the one question a reader opens it to check",
            "Working · $MACHINE",
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
            "Idle · $MACHINE",
            panel(items = strandedTool).headerSubtitle,
        )
    }

    @Test
    fun `the link outranks the turn and ended outranks everything`() {
        assertEquals(
            "a phone that cannot reach the machine still reported activity it is not receiving",
            "Not connected · $MACHINE",
            panel(online = false, items = openTurn()).headerSubtitle,
        )
        assertEquals(
            "an ended session read as something other than ended, though there is nothing to " +
                "type into whatever the link or the record says",
            "Ended · $MACHINE",
            panel(ended = true, online = false, items = openTurn()).headerSubtitle,
        )
    }

    @Test
    fun `a session blocked on the reader says needs you rather than working`() {
        assertEquals(
            "a session the agent is blocked on read as working. It is not working -- it is " +
                "stopped, waiting on this reader, and that is the fact worth the subtitle",
            "Needs you · $MACHINE",
            panel(group = "needs_input", items = openTurn()).headerSubtitle,
        )
    }

    /**
     * The fixture correction above, for the same reason: this names an idle session, so it builds
     * one. The expectation is untouched.
     */
    @Test
    fun `a session id that names no machine says the state alone`() {
        assertEquals(
            "the subtitle kept its separator over a machine half that does not exist, which is " +
                "punctuation claiming a fact",
            "Idle",
            panel(machineLabel = "", items = closedTurn()).headerSubtitle,
        )
    }

    /**
     * FAILING-FIRST for the ruling that follows H.8 one seam over: **where this phone holds no
     * record, the header says nothing about what the agent is doing.**
     *
     * WHAT WAS WRONG. `TranscriptScreen.openTurnOf` answers `""` for an empty item list AND for a
     * closed turn, so one value carried two facts -- "nothing is running" and "I hold no record"
     * -- and `headerStateFor` read the second as the first and claimed `idle`. It is not an edge
     * case: `PhoneSurface.backfillOnOpen` exists precisely because a cold-opened session has zero
     * items, and the panel is built before the backfill lands. Every cold open of a session this
     * phone holds nothing for read `idle`, on a session that may be mid-turn.
     *
     * WHY NOTHING RATHER THAN A SIXTH WORD. The drawing tables five `header.state` words and a
     * state not drawn there is a state this screen may not enter; the absence of a record is drawn
     * as the absence of a mark, which is the same answer the dot's Group got in H.8 and it costs a
     * reader nothing here, because the machine name still carries the line.
     *
     * WHAT IT DOES NOT WEAKEN, asserted so a later change cannot quietly trade one for the other:
     * the three facts that do not come from the transcript still outrank it. An ended session, an
     * unreachable one and one the ROSTER says is blocked on this reader all keep their word, and
     * the phone holding no items says nothing about any of the three.
     */
    @Test
    fun `a session this phone holds no record of says nothing about what the agent is doing`() {
        assertEquals(
            "a cold-opened session -- no item has reached this phone yet -- reported the agent " +
                "as idle. The only evidence was an empty open-turn value, which is also what an " +
                "empty transcript produces, so the header claimed a state from a record it does " +
                "not have",
            MACHINE,
            panel(items = emptyList()).headerSubtitle,
        )
        assertEquals(
            "an ENDED session lost its word to a missing record. There is nothing to type into " +
                "whatever the record says, and `ended` is read from the session and not from the " +
                "transcript",
            "Ended · $MACHINE",
            panel(ended = true, items = emptyList()).headerSubtitle,
        )
        assertEquals(
            "an unreachable session lost its word to a missing record, though a phone that " +
                "cannot reach the machine knows exactly that much and owes the reader it",
            "Not connected · $MACHINE",
            panel(online = false, items = emptyList()).headerSubtitle,
        )
        assertEquals(
            "a session the ROSTER says is blocked on this reader lost its word to a missing " +
                "transcript. That fact comes off the roster row, not out of the items",
            "Needs you · $MACHINE",
            panel(group = "needs_input", items = emptyList()).headerSubtitle,
        )
    }

    /**
     * The two empty halves meeting, which is where a naive separator fix produces `" · "`.
     *
     * The separator rides with the MACHINE (`headerSubtitleFor`'s own paragraph), so an empty
     * state half would have hung it off the front -- a line opening on punctuation, promising a
     * word that was deliberately not said. Both halves empty is now reachable for the first time,
     * because until the ruling above the state was always one of five.
     */
    @Test
    fun `a subtitle with neither half draws nothing rather than a bare separator`() {
        assertEquals(
            "the header drew a separator with nothing on either side of it",
            "",
            panel(machineLabel = "", items = emptyList()).headerSubtitle,
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

    /**
     * FAILING-FIRST for plan H.8: **an unrecognised Group renders as unknown, never as
     * `completed`.**
     *
     * TWO INPUTS AND NOT ONE, because the case arrives on two paths and only the first was ever
     * argued. The empty string is the roster race `PhoneSurface.detailPanel` catches -- the row
     * is gone. A Group `internal/status` grew is the second, and nothing filters it out on the
     * way here: `FacadeBridge.sessionRow` reads `App.Session` directly rather than through
     * `TriageInbox.from`, whose loud check is the only thing that would have refused it, and the
     * one screen where that check does fire swallows the throw (`PhoneSurface.inboxScreen`
     * catches every Exception and falls back to an empty roster).
     *
     * WHAT THE ASSERTION IS AGAINST is a substitution, not a crash. `completed` renders every
     * correct pixel and is the phone INVENTING a Group -- the move `presenceDot`'s own KDoc
     * records as the defect rather than as the cheap implementation -- and it is not a neutral
     * one: grey is the triage inbox's FINISHED section, so a session this phone cannot place is
     * credited with having completed its work. The kit draws no mark for an empty Group
     * (ADR-009 D2's `.pdot.unknown`: the absence of a record drawn as the absence of a mark), so
     * what this panel owes it is the absence and not a stand-in.
     */
    @Test
    fun `a roster that cannot name the session draws no dot rather than a substituted one`() {
        for (unnamed in listOf("", "awaiting_review_v2")) {
            assertEquals(
                "the header substituted a Group the roster never sent for \"$unnamed\". " +
                    "`completed` is not the absence of a claim -- it is the claim that the agent " +
                    "FINISHED, and a phone with no Group has no evidence of that or of anything " +
                    "else",
                "",
                panel(group = unnamed).headerGroup,
            )
            assertTrue(
                "the header carries a Group `Kit.groupColour` can neither place nor skip, so a " +
                    "drill-down whose session the roster cannot name takes the whole screen down " +
                    "with it -- on the one surface a person is reading",
                panel(group = unnamed).headerGroup.let {
                    it.isEmpty() || it in TriageInbox.TRIAGE_ORDER
                },
            )
        }
    }

    /**
     * The same ruling stated as the thing a READER sees, which is the wave's rule 3: a
     * behavioural claim needs a behavioural test.
     *
     * The state word is computed from the link and the open turn and never from the Group
     * ([SessionDetailScreen.headerStateFor]), so the substitution could not be caught by asking
     * about either half alone -- it is the two halves TOGETHER that were wrong. A session with a
     * turn open drew the finished mark next to the word `working`: the dot contradicting the
     * sentence beside it, in the one direction that tells a reader to stop watching.
     */
    @Test
    fun `the dot never credits a working session with having finished`() {
        val working = panel(group = "awaiting_review_v2", items = openTurn())

        assertEquals(
            "the state word stopped being read from the open turn, so this no longer tests the " +
                "contradiction it was written for",
            "Working · $MACHINE",
            working.headerSubtitle,
        )
        assertFalse(
            "the header drew the recessive `completed` mark beside the word `working` -- one " +
                "session reported as both finished and running, on the screen a person opened to " +
                "find out which",
            working.headerGroup == "completed",
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

        assertEquals("the fixture does not change the state word at all", "Idle · $MACHINE", idle.headerSubtitle)
        assertEquals("Working · $MACHINE", working.headerSubtitle)
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
