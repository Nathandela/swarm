package dev.swarm.phone.ui.screens

import android.content.Context
import android.view.View
import android.view.ViewGroup
import android.widget.LinearLayout
import android.widget.ScrollView
import android.widget.TextView
import androidx.test.core.app.ApplicationProvider
import dev.swarm.phone.bottomInsetPx
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertSame
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner

/**
 * FAILING-FIRST (TDD RED, GG-5) for the structural change the whole conversation surface rests
 * on. Plan: docs/specifications/chat-surface-plan.md §5. Bead: agents-tracker-tbpm.1.
 *
 * THE DEFECT IT EXISTS AGAINST IS MEASURABLE. `phoneScaffoldView` wraps whatever it is handed in
 * ONE ScrollView with the tab bar underneath, so a session's notices, its conversation and its
 * controls scrolled as a single document -- about 414 dp of fixed furniture before the first
 * message on a healthy session, and 640 to 720 dp in the state the owner photographed, which is
 * why one session needed two screenshots. The composer, being the last child of that document,
 * could only be reached by scrolling past the whole transcript and then past three buttons.
 *
 * A CONVERSATION IS THREE REGIONS AND ONLY ONE OF THEM MOVES.
 */
@RunWith(RobolectricTestRunner::class)
class ConversationScaffoldViewTest {

    private val context = ApplicationProvider.getApplicationContext<Context>()

    private fun label(text: String) = TextView(context).apply { this.text = text }

    private fun scaffold(
        header: View = label("claude-NewLatexCV"),
        content: View = label("the conversation"),
        composer: View? = label("Message"),
        status: View? = null,
    ) = conversationScaffoldView(context, header, content, composer, status)

    private fun View.find(tag: String): View? {
        if (this.tag == tag) return this
        if (this is ViewGroup) {
            for (i in 0 until childCount) getChildAt(i).find(tag)?.let { return it }
        }
        return null
    }

    @Test
    fun `only the message list scrolls`() {
        val root = scaffold()
        val scroll = root.find(ScaffoldTag.CONTENT)
        assertTrue("the content region is a scroll", scroll is ScrollView)

        val header = root.find(ScaffoldTag.HEADER)!!
        val composer = root.find(ScaffoldTag.COMPOSER)!!
        for ((name, part) in listOf("header" to header, "composer" to composer)) {
            var p = part.parent
            var insideAScroll = false
            while (p is View) {
                if (p is ScrollView) insideAScroll = true
                p = p.parent
            }
            assertTrue(
                "the $name is inside a scroll, so it slides away the moment a reader moves -- " +
                    "which is what makes today's session detail one long document",
                !insideAScroll,
            )
        }
    }

    @Test
    fun `the composer is pinned below the list and not inside it`() {
        val root = scaffold() as LinearLayout
        val order = (0 until root.childCount).map { root.getChildAt(it).tag }
        assertEquals(
            "the recorded order is header, list, composer: a header under the conversation is a " +
                "title below the fold, and a composer above it is a control in the reading path",
            listOf(ScaffoldTag.HEADER, ScaffoldTag.CONTENT, ScaffoldTag.COMPOSER),
            order,
        )
    }

    @Test
    fun `there is no tab bar`() {
        assertNull(
            "a conversation is a place you go INTO, and a bar here is an invitation to leave a " +
                "screen you just arrived at. Back returns to the inbox, which keeps its bar",
            scaffold().find(ScaffoldTag.TABS),
        )
    }

    @Test
    fun `the connection strip survives`() {
        val strip = label("Not connected to your machine")
        val root = scaffold(status = strip)
        assertNotNull(
            "dropping the strip with the bar would make the one screen where a person is TYPING " +
                "the one screen that cannot tell them the link is gone -- ScaffoldTag.STATUS's " +
                "own argument, and the reason the two are separate decisions",
            root.find(ScaffoldTag.STATUS) ?: strip.takeIf { it.parent != null },
        )
        assertSame("the strip is drawn above everything", strip, (root as LinearLayout).getChildAt(0))
    }

    @Test
    fun `a session with no composer draws none rather than a disabled one`() {
        val root = scaffold(composer = null) as LinearLayout
        assertNull(
            "a session with no message sink has no composer at all (ADR-017): a disabled bar " +
                "promises a verb the session structurally lacks",
            root.find(ScaffoldTag.COMPOSER),
        )
        assertEquals(
            listOf(ScaffoldTag.HEADER, ScaffoldTag.CONTENT),
            (0 until root.childCount).map { root.getChildAt(it).tag },
        )
    }

    @Test
    fun `the window's bottom inset is the keyboard when one is up`() {
        assertEquals(
            "a pinned composer over an open keyboard is the promise this app has never had to " +
                "keep; the inset was being dispatched and ignored",
            140,
            bottomInsetPx(barsBottomPx = 48, imeBottomPx = 140),
        )
        assertEquals(
            "with no keyboard the navigation bar is still the floor",
            48,
            bottomInsetPx(barsBottomPx = 48, imeBottomPx = 0),
        )
        assertEquals(
            "a MAX and not a sum: the keyboard is drawn OVER the navigation bar, so adding them " +
                "would inset the window by a strip the keyboard already occupies",
            140,
            bottomInsetPx(barsBottomPx = 140, imeBottomPx = 140),
        )
    }
}
