package dev.swarm.phone.ui.screens

import android.content.Context
import android.view.View
import android.widget.HorizontalScrollView
import android.widget.ScrollView
import android.widget.TextView
import androidx.test.core.app.ApplicationProvider
import dev.swarm.phone.theme.SwarmTheme
import dev.swarm.phone.ui.kit.KitTag
import dev.swarm.phone.ui.kit.kitFind
import dev.swarm.phone.ui.kit.kitRequire
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner

/**
 * FAILING-FIRST (TDD RED, GG-5) for owner ruling R8's other half: **expanded tool output opens in
 * place up to a bound, then on its own screen.**
 *
 * WHAT THE SCREEN IS FOR, and it is not "more room". `TranscriptScreen` bounds an opened card at
 * twenty lines and keeps the WHOLE body on the block's route -- one tap away, never dropped -- so
 * this screen is where that body is finally drawn. Without it the bound is a truncation the phone
 * invented, which is what IS-TOOL-3 forbids one field over: the item "SHALL NOT claim to hold the
 * underlying output".
 *
 * **IT AUTHORS NOTHING.** The drawing gives it one row -- "one tool run's whole output, in the mono
 * well the app already has, scrollable both ways" -- and tables no copy for it, so every string on
 * it arrives from the caller and is the machine's own. The title is the sentence the transcript
 * already drew for the row; the body is the route's own text; the one word this file spends is the
 * back control's, which is `terminalFallbackView`'s and is kept verbatim rather than retyped.
 *
 * **SCROLLABLE BOTH WAYS IS A CORRECTNESS CLAIM AND NOT A COMFORT.** `monoWell` sets
 * `setHorizontallyScrolling(true)`, which tells it to lay a line out whole rather than wrapping
 * it -- and says nothing about where the part past the visible edge goes. With no scroller it goes
 * nowhere and is silently clipped (agents-tracker-ksvb.7): unreachable content wearing a
 * typography flag's costume, on the one screen whose entire purpose is that the body is reachable.
 */
@RunWith(RobolectricTestRunner::class)
class OutputScreenTest {

    private val context: Context
        get() = SwarmTheme.applyTo(ApplicationProvider.getApplicationContext())

    private val body = (1..214).joinToString("\n") { "=== RUN   TestNumber$it" }

    private fun screen(
        title: CharSequence = "Bash go test ./...",
        output: CharSequence = body,
        onBack: (() -> Unit)? = null,
    ): View = outputScreen(context = context, title = title, output = output, onBack = onBack)

    private fun textOf(view: View?): String = (view as? TextView)?.text?.toString().orEmpty()

    /**
     * THE WHOLE BODY AND NOT THE REMAINDER. A screen that began at line twenty-one would open on a
     * page whose first line is mid-sentence, and would make the two renderings of one body
     * disagree about what it is.
     */
    @Test
    fun `the whole of the output is on the screen`() {
        assertEquals(
            "the screen redraws the head the flow already showed, or drops the part the reader " +
                "opened it for",
            body,
            textOf(screen().kitRequire(OutputTag.WELL)),
        )
    }

    /**
     * Both axes, and each has its own defect behind it: a long line is clipped without the
     * horizontal scroller, and a long body is unreachable below the fold without the vertical one.
     */
    @Test
    fun `the output scrolls both ways`() {
        val well = screen().kitRequire(OutputTag.WELL)

        val sideways = well.parent
        assertTrue(
            "a line wider than the viewport is silently clipped, with nothing below it wide " +
                "enough to reach the rest (agents-tracker-ksvb.7)",
            sideways is HorizontalScrollView,
        )
        assertTrue(
            "the screen does not scroll down, so a 214-line body ends at the fold on the one " +
                "screen that exists to show the whole of it",
            (sideways as View).parent is ScrollView,
        )
    }

    /**
     * `navHeaderDrill(back = null)`'s ruling: a screen with nowhere to go draws no control rather
     * than a chevron that does not act (agents-tracker-2yb).
     */
    @Test
    fun `the back control acts, and a screen with nowhere to go draws none`() {
        var went = 0
        val wired = screen(onBack = { went++ })

        assertEquals("Bash go test ./...", textOf(wired.kitRequire(KitTag.DRILL_TITLE)))
        wired.kitRequire(KitTag.DRILL_BACK).performClick()
        assertEquals("the back control does not act, so the reader is stranded on the output", 1, went)

        assertNotNull(
            "the screen has no header at all, so nothing on it says which tool run this is",
            screen().kitFind(OutputTag.NAV),
        )
        assertNull(
            "the screen draws a back control with no destination behind it",
            screen().kitFind(KitTag.DRILL_BACK),
        )
    }

    /**
     * The title is the caller's, and the caller's is the machine's.
     *
     * This screen never composes a sentence about a tool run: the transcript already wrote one for
     * the row, out of the wire's own `tool` and `target`, and a second phrasing here would be the
     * same fact in two voices -- the rule that produced five wordings of one moved turn.
     */
    @Test
    fun `the title is the sentence the conversation already drew`() {
        assertEquals(
            "Read /Users/Nathan/spike-sb-work/edit-target3.txt",
            textOf(screen(title = "Read /Users/Nathan/spike-sb-work/edit-target3.txt").kitRequire(KitTag.DRILL_TITLE)),
        )
    }
}
