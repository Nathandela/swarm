package dev.swarm.phone.ui.kit

import android.view.Gravity
import android.widget.LinearLayout
import androidx.test.core.app.ApplicationProvider
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotEquals
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner

/**
 * The reader's own words (derivation row 26, owner ruling on the conversation surface).
 *
 * WHAT THIS COMPONENT IS FOR, stated as the thing it must not become: every sender used to share
 * one bordered `activityRow`, and that is what made the owner's screenshot read as a log rather
 * than a conversation. A row says "here is a record"; a bubble says "somebody said this". The
 * asymmetry is the design -- the agent gets no bubble at all -- so these assertions are as much
 * about what is absent as what is drawn.
 */
@RunWith(RobolectricTestRunner::class)
class MessageBubbleTest {

    private val context = ApplicationProvider.getApplicationContext<android.content.Context>()

    @Test
    fun `it carries the reader's own words and sits on their side`() {
        val bubble = messageBubble(context, "check the relay logs too")
        assertEquals("check the relay logs too", bubble.text.toString())
        val params = bubble.layoutParams as LinearLayout.LayoutParams
        assertEquals(
            "a bubble that laid out from the start would read as the agent's, which is the one " +
                "thing the asymmetry exists to prevent",
            Gravity.END,
            params.gravity,
        )
        assertEquals(
            "a bubble hugs its words. MATCH_PARENT would draw a full-width band, which is a row",
            LinearLayout.LayoutParams.WRAP_CONTENT,
            params.width,
        )
    }

    @Test
    fun `the three states are told apart by their surface, not by their words`() {
        val settled = messageBubble(context, "ship it", BubbleState.SETTLED).background
        val pending = messageBubble(context, "ship it", BubbleState.PENDING).background
        val refused = messageBubble(context, "ship it", BubbleState.REFUSED).background

        for (b in listOf(settled, pending, refused)) {
            assertTrue("every state draws the kit's own surface", b is SubstrateSurface)
        }
        val specs = listOf(settled, pending, refused).map { (it as SubstrateSurface).spec }
        assertEquals(
            "a settled bubble has NO border: the fill is the differentiation, and a hairline over " +
                "it would be a second statement of the same thing",
            0,
            specs[0].strokeWidthPx,
        )
        assertNotEquals("a pending bubble is bordered so the eye can find it", 0, specs[1].strokeWidthPx)
        assertNotEquals("a refused bubble is bordered", 0, specs[2].strokeWidthPx)
        assertNotEquals(
            "pending and refused share a border WIDTH and must not share its colour, or a message " +
                "the machine turned away looks exactly like one still in flight",
            specs[1].stroke,
            specs[2].stroke,
        )
        assertEquals(
            "every state is the same raised surface. Changing the FILL per state would make " +
                "delivery a property of the material rather than of the border",
            specs[0].fill,
            specs[2].fill,
        )
    }

    @Test
    fun `pending dims the ink and not the bubble`() {
        val settled = messageBubble(context, "ship it", BubbleState.SETTLED)
        val pending = messageBubble(context, "ship it", BubbleState.PENDING)
        assertNotEquals(
            "a pending message reads exactly like a delivered one, which is the delivery claim " +
                "the wire cannot back (owner ruling R6)",
            settled.currentTextColor,
            pending.currentTextColor,
        )
        assertEquals(
            "fading the whole bubble would fade the reader's own words, which are not in doubt -- " +
                "what is in doubt is whether the agent has them",
            1f,
            pending.alpha,
            0f,
        )
    }

    @Test
    fun `a slash command is drawn in the machine's own face`() {
        val prose = messageBubble(context, "check the logs", mono = false)
        val command = messageBubble(context, "/debug", mono = true)
        assertNotEquals(
            "a typed machine word drawn in body copy reads as something the reader said in " +
                "English, which is not what a slash command is",
            prose.typeface,
            command.typeface,
        )
    }

    @Test
    fun `no bubble draws a key light`() {
        val spec = (messageBubble(context, "x").background as SubstrateSurface).spec
        assertEquals(
            "the key light is the material's \"this is a surface you could pick up\"; a bubble is " +
                "a thing somebody said",
            null,
            spec.keyLight,
        )
    }
}
