package dev.swarm.phone.ui.screens

import android.content.Context
import android.widget.TextView
import androidx.test.core.app.ApplicationProvider
import dev.swarm.phone.ui.InteractionItem
import dev.swarm.phone.theme.SwarmTheme
import dev.swarm.phone.ui.kit.BubbleState
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner

/**
 * FAILING-FIRST (TDD RED, GG-5) for Wave H, H.4 — owner ruling **R6 has no mechanism**.
 * Plan: `docs/specifications/chat-surface-plan.md` §14. Bead: `agents-tracker-svph`.
 *
 * **WHAT THE READER SEES TODAY WHEN THEY PRESS SEND: nothing.**
 *
 * The draft is cleared on the daemon's acceptance (`verdict.clearsDraft`), and a `user_message`
 * block enters the transcript only when the agent's own record ECHOES it back. Between those two
 * moments the message exists nowhere on screen — not in the composer it was just cleared from, and
 * not in the conversation it has not reached. Before this wave the composer at least drew the word
 * "Sent"; the honesty pass removed that word, correctly (the daemon's acceptance means bytes were
 * written into a PTY, not that the agent has them) and **made the visible behaviour worse**, because
 * nothing replaced it.
 *
 * If the echo never lands, the message is gone with no evidence it was ever typed.
 *
 * **THE FACT THAT FIXES IT ALREADY CROSSES THE BOUNDARY AND IS READ BY NOBODY.**
 * `InteractionItem.operationId` was mapped for exactly this and has zero readers in `main/`. R6 is
 * "a sent bubble is PENDING until the agent's own transcript echoes it back", and the echo is
 * recognised by the operation id the send was issued under — the daemon already stamps it
 * (`stampComposerEchoLocked`).
 *
 * So the message is drawn from the moment it is sent, as the reader's own words in their own
 * bubble, and it stops being provisional when the record catches up. Nothing is invented: the
 * pending bubble carries the text that was actually sent, and it is replaced by the wire's own item
 * rather than merged with it.
 */
@RunWith(RobolectricTestRunner::class)
class TranscriptPendingSendTest {

    private val context: Context
        get() = SwarmTheme.applyTo(ApplicationProvider.getApplicationContext())

    private fun agent(id: String, text: String) = InteractionItem(
        sessionId = "m/s1", itemId = id, cursor = 1, kind = "agent_message",
        text = text, body = """{"text":"$text"}""",
    )

    private fun echoed(id: String, text: String, operationId: String) = InteractionItem(
        sessionId = "m/s1", itemId = id, cursor = 2, kind = "user_message",
        text = text, body = """{"text":"$text"}""", operationId = operationId,
    )

    @Test
    fun `a sent message is on screen before the agent has echoed it`() {
        val panel = TranscriptScreen.of(
            listOf(agent("a1", "Running the suite.")),
            pendingSends = listOf(PendingSend(operationId = "op-1", text = "also check the relay pin")),
        )

        val last = panel.blocks.last()
        assertEquals(
            "the message the reader just sent is nowhere on screen. The composer was cleared on " +
                "the daemon's acceptance and the transcript will not carry it until the agent " +
                "echoes it, so between those two moments it exists nowhere at all",
            "also check the relay pin",
            last.line,
        )
        assertTrue("it is the reader's own words, so it is their bubble", last.bubble)
        assertEquals(
            "a message the agent has not echoed is drawn as delivered. The daemon's acceptance " +
                "means bytes reached a PTY, which is not the same claim (owner ruling R6)",
            BubbleState.PENDING,
            last.sendState,
        )
    }

    @Test
    fun `the echo replaces the pending bubble rather than doubling it`() {
        val panel = TranscriptScreen.of(
            listOf(
                agent("a1", "Running the suite."),
                echoed("u1", "also check the relay pin", operationId = "op-1"),
            ),
            pendingSends = listOf(PendingSend(operationId = "op-1", text = "also check the relay pin")),
        )

        assertEquals(
            "the reader's message is drawn twice -- once as the local pending copy and once as " +
                "the record's own item. The operation id is what tells them apart and it is the " +
                "reason the field was mapped across the facade",
            1,
            panel.blocks.count { it.line == "also check the relay pin" },
        )
        assertEquals(
            "the echoed message is still marked provisional after the record caught up. Settling " +
                "IS the acknowledgement -- the drawing gives it no tick and no label",
            BubbleState.SETTLED,
            panel.blocks.last().sendState,
        )
    }

    /**
     * The match is on the OPERATION ID and not on the words.
     *
     * Two identical messages are ordinary — a reader repeating themselves because the first drew
     * no response is precisely the situation this defect creates. Matching on text would make the
     * second send settle against the first's echo and vanish.
     */
    @Test
    fun `an identical earlier message does not settle a later send`() {
        val panel = TranscriptScreen.of(
            listOf(echoed("u1", "ping", operationId = "op-1")),
            pendingSends = listOf(PendingSend(operationId = "op-2", text = "ping")),
        )

        assertEquals(
            "a second identical send was swallowed by the first one's echo, so the reader watched " +
                "their message disappear for the second time",
            2,
            panel.blocks.count { it.line == "ping" },
        )
        assertEquals(BubbleState.PENDING, panel.blocks.last().sendState)
    }

    @Test
    fun `a refused send keeps the words on screen`() {
        val panel = TranscriptScreen.of(
            emptyList(),
            pendingSends = listOf(PendingSend(operationId = "op-1", text = "rm the stale db", refused = true)),
        )

        assertEquals(
            "a refused message is dropped from the screen, so the reader loses what they typed " +
                "AND the reason. Nothing is ever silently swallowed",
            "rm the stale db",
            panel.blocks.last().line,
        )
        assertEquals(BubbleState.REFUSED, panel.blocks.last().sendState)
    }

    @Test
    fun `a refused send keeps its exact refusal copy and machine detail on its own block`() {
        val panel = TranscriptScreen.of(
            emptyList(),
            pendingSends = listOf(
                PendingSend(
                    operationId = "op-1",
                    text = "please continue",
                    refused = true,
                    notice = "Not sent. Finish typing on your computer first.",
                    detail = "input region is not empty",
                ),
                PendingSend(operationId = "op-2", text = "newer message"),
            ),
        )

        val refused = panel.blocks.first()
        assertEquals("Not sent. Finish typing on your computer first.", refused.sendNotice)
        assertEquals("input region is not empty", refused.sendNoticeDetail)
        assertEquals("", panel.blocks.last().sendNotice)
        assertEquals("", panel.blocks.last().sendNoticeDetail)
        assertFalse("the newer pending send inherited the older refusal", panel.blocks.last().sendState == BubbleState.REFUSED)

        val refusedViews = transcriptBlockViews(context, refused)
        assertEquals(3, refusedViews.size)
        assertEquals("Not sent. Finish typing on your computer first.", (refusedViews[1] as TextView).text.toString())
        assertEquals("input region is not empty", (refusedViews[2] as TextView).text.toString())
    }

    @Test
    fun `two locally sealed sends stay visible in their original order`() {
        val panel = TranscriptScreen.of(
            listOf(agent("a1", "Running the suite.")),
            pendingSends = listOf(
                PendingSend(operationId = "op-1", text = "first follow-up"),
                PendingSend(operationId = "op-2", text = "second follow-up"),
            ),
        )

        assertEquals(
            listOf("first follow-up", "second follow-up"),
            panel.blocks.takeLast(2).map { it.line },
        )
        assertTrue(panel.blocks.takeLast(2).all { it.sendState == BubbleState.PENDING })
    }

    @Test
    fun `each echo replaces only its own pending send`() {
        val panel = TranscriptScreen.of(
            listOf(echoed("u1", "first follow-up", operationId = "op-1")),
            pendingSends = listOf(
                PendingSend(operationId = "op-1", text = "first follow-up"),
                PendingSend(operationId = "op-2", text = "second follow-up"),
            ),
        )

        assertEquals(1, panel.blocks.count { it.line == "first follow-up" })
        assertEquals(1, panel.blocks.count { it.line == "second follow-up" })
        assertEquals(BubbleState.SETTLED, panel.blocks.first().sendState)
        assertEquals(BubbleState.PENDING, panel.blocks.last().sendState)
    }

    @Test
    fun `only a user-message echo settles a provisional send`() {
        val sameOperationTool = InteractionItem(
            sessionId = "m/s1",
            itemId = "t1",
            cursor = 2,
            kind = "tool_run",
            text = "working",
            operationId = "op-1",
        )
        val panel = TranscriptScreen.of(
            listOf(sameOperationTool),
            pendingSends = listOf(PendingSend(operationId = "op-1", text = "please continue")),
        )

        assertEquals("please continue", panel.blocks.last().line)
        assertEquals(BubbleState.PENDING, panel.blocks.last().sendState)
    }

    @Test
    fun `an ordinary block carries no send state at all`() {
        val panel = TranscriptScreen.of(listOf(agent("a1", "Running the suite.")))
        assertNull(
            "the agent's own prose was given a delivery state, which is a claim about a message " +
                "this phone never sent",
            panel.blocks.single().sendState,
        )
    }
}
