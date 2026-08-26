package dev.swarm.phone.ui.screens

import android.content.Context
import androidx.test.core.app.ApplicationProvider
import dev.swarm.phone.ui.InteractionItem
import dev.swarm.phone.ui.kit.kitFind
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner

/**
 * FAILING-FIRST (TDD RED, GG-5): the reader's own messages are drawn as bubbles, and the "You ·"
 * prefix goes with them. Plan: docs/specifications/chat-surface-plan.md §6/§8.
 * Bead: agents-tracker-tbpm.5.
 *
 * THE PREFIX AND THE BUBBLE ARE ONE DECISION. `TranscriptScreen` writes "You · hello" because
 * every sender shared one `activityRow` and the sentence was the only thing that could say who
 * spoke. A bubble says it in the layout -- the side of the screen and the raised surface -- so
 * keeping the prefix would state it twice, and the owner's screenshot shows exactly what twice
 * looks like: a column of identical bordered boxes each beginning with the same two words.
 *
 * THE AGENT KEEPS ITS PROSE ON THE GROUND. That asymmetry is the whole design (kit row 26), so
 * it is asserted here as well as in the component: a screen that gave both sides a bubble would
 * pass every test in MessageBubbleTest and still be wrong.
 */
@RunWith(RobolectricTestRunner::class)
class TranscriptBubbleTest {

    private val context = ApplicationProvider.getApplicationContext<Context>()

    private fun user(id: String, text: String) = InteractionItem(
        sessionId = "m/s1", itemId = id, cursor = 1L, kind = "user_message",
        status = "completed", text = text, turnId = "turn-A",
        body = """{"text":"$text"}""",
    )

    private fun agent(id: String, text: String) = InteractionItem(
        sessionId = "m/s1", itemId = id, cursor = 2L, kind = "agent_message",
        status = "completed", text = text, turnId = "turn-A",
        body = """{"text":"$text"}""",
    )

    private fun blocksOf(vararg items: InteractionItem) =
        TranscriptScreen.of(items.toList()).blocks

    @Test
    fun `the reader's message carries its own words and no attribution`() {
        val b = blocksOf(user("u1", "check the relay logs too")).single()
        assertEquals(
            "the line still names the speaker, so the screen says who spoke twice -- once in the " +
                "sentence and once in the layout",
            "check the relay logs too",
            b.line,
        )
        assertTrue("a bubble is drawn for the reader's own messages", b.bubble)
    }

    @Test
    fun `the agent's prose is not a bubble`() {
        val b = blocksOf(agent("a1", "I'll tail the relay log.")).single()
        assertFalse(
            "giving the agent a bubble makes this a chat between two strangers; one of the two " +
                "is the person holding the phone and the other is a machine reporting on their " +
                "own work",
            b.bubble,
        )
    }

    @Test
    fun `a slash command is marked as the machine word it is`() {
        assertTrue(blocksOf(user("u1", "/debug")).single().command)
        assertFalse(
            "an ordinary sentence drawn in the machine's face reads as something it is not",
            blocksOf(user("u2", "check the logs")).single().command,
        )
        assertFalse(
            "a slash inside a sentence is a path or a fraction, not a command",
            blocksOf(user("u3", "look at src/main.rs")).single().command,
        )
    }

    @Test
    fun `the view draws a bubble for the reader and prose for the agent`() {
        val panel = TranscriptScreen.of(
            listOf(user("u1", "check the relay logs too"), agent("a1", "Tailing it now.")),
        )
        val root = transcriptView(context, panel)
        assertNotNull(
            "the reader's message is not drawn as a bubble, so the conversation still reads as a " +
                "log of identical rows",
            root.kitFind(TranscriptTag.BUBBLE),
        )
        assertEquals(
            "the reader's own words did not reach the bubble",
            "check the relay logs too",
            (root.kitFind(TranscriptTag.BUBBLE) as android.widget.TextView).text.toString(),
        )
    }

    @Test
    fun `a bubble is not tagged as an ordinary block`() {
        val root = transcriptView(context, TranscriptScreen.of(listOf(user("u1", "hello"))))
        assertEquals(
            "a bubble found by the block tag would let a test assert a row's behaviour of a " +
                "bubble -- the reasoning TranscriptTag already gives for keeping APPROVAL and " +
                "RUNNING apart from BLOCK",
            null,
            root.kitFind(TranscriptTag.BLOCK),
        )
    }
}
