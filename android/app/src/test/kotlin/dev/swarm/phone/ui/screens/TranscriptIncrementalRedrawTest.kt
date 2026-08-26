package dev.swarm.phone.ui.screens

import android.content.Context
import android.view.View
import android.view.ViewGroup
import android.widget.FrameLayout
import android.widget.TextView
import androidx.test.core.app.ApplicationProvider
import dev.swarm.phone.theme.SwarmTheme
import dev.swarm.phone.ui.InteractionItem
import dev.swarm.phone.ui.SessionDetail
import dev.swarm.phone.ui.SessionLease
import dev.swarm.phone.ui.kit.KitTag
import dev.swarm.phone.ui.kit.kitRequire
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotSame
import org.junit.Assert.assertSame
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner

/**
 * FAILING-FIRST (TDD RED, GG-5) for Mirror M2.3's VIEW half -- the incremental redraw keyed by
 * `item_id`, replacing the whole-transcript rebuild. Bead agents-tracker-hggx.7.
 *
 * `TranscriptIncremental` decides WHICH rows changed and had no caller: `sessionDetailRedraw`
 * still did `slot.removeAllViews()` and recomposed the entire conversation on every journal
 * event. `StreamingRedrawTest` pins what that patch already protects -- the header, the notices
 * and the control slots survive -- and records what it could not yet promise: "a conversation is
 * a LIST, so there is no `.text` to set and the rows are recomposed by design".
 *
 * THAT SENTENCE IS THE SUBJECT HERE. Recomposing every row at the agent's output rate is the
 * esed defect one layer in: a `TextView` rebuilt per event re-measures and re-antialiases, so
 * the conversation shimmers exactly while it is being read, and every row loses its selection,
 * its accessibility focus and any pending touch. The rows that did not change must be the SAME
 * VIEWS afterwards -- which is what the mutation list exists to make possible, and what this
 * suite asserts by identity.
 *
 * The negative controls matter as much: a row whose facts changed MUST rebind (a status flip
 * that did not reach the screen is worse than a shimmer), and a redraw that is handed a
 * different chrome must still refuse outright, because a screen states what is on it by
 * composing it (PB-DS-9).
 */
@RunWith(RobolectricTestRunner::class)
class TranscriptIncrementalRedrawTest {

    private val context: Context
        get() = SwarmTheme.applyTo(ApplicationProvider.getApplicationContext())

    private val session = "mbp/quanthome"

    private fun item(id: String, text: String, status: String = "completed") = InteractionItem(
        sessionId = session, itemId = id, cursor = id.hashCode().toLong(),
        kind = "agent_message", status = status, text = text, turnId = "turn-a",
    )

    private fun panelOf(vararg items: InteractionItem): SessionDetailPanel = SessionDetailScreen.of(
        SessionDetail(
            sessionId = session, online = true,
            journalStale = false, title = "api refactor",
        ),
        TranscriptScreen.of(items.toList()),
        SessionLease(sessionId = session, online = true),
        capabilities = SessionCapabilityFacts(structuredChat = true),
    )

    private fun host(panel: SessionDetailPanel): FrameLayout = FrameLayout(context).apply {
        addView(
            sessionDetailView(
                context = context,
                panel = panel,
                stop = TextView(context),
                kill = TextView(context),
                resync = TextView(context),
                acknowledge = TextView(context),
                composer = TextView(context),
                approval = TextView(context),
                outcome = "",
                onBack = {},
            ),
        )
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

    private fun rowsIn(root: View): List<View> = root.allTagged(TranscriptTag.BLOCK)

    private fun spokenIn(root: View): List<String> = rowsIn(root)
        .map { (it.kitRequire(KitTag.ACTIVITY_BODY) as TextView).text.toString() }

    @Test
    fun `an appended item keeps every row that did not change`() {
        val drawn = panelOf(item("a", "Running the suite."), item("b", "Two packages left."))
        val host = host(drawn)
        val before = rowsIn(host)
        assertEquals(2, before.size)

        assertTrue(
            sessionDetailRedraw(
                host,
                drawn,
                panelOf(
                    item("a", "Running the suite."),
                    item("b", "Two packages left."),
                    item("c", "All green."),
                ),
            ),
        )

        val after = rowsIn(host)
        assertEquals(
            listOf("Running the suite.", "Two packages left.", "All green."),
            spokenIn(host),
        )
        assertSame(
            "the first row was rebuilt for an item appended below it, so every row re-measures " +
                "and re-antialiases at the rate the agent writes",
            before[0],
            after[0],
        )
        assertSame("the second row was rebuilt too", before[1], after[1])
    }

    @Test
    fun `a streamed growth rebinds its own row and no other`() {
        val drawn = panelOf(item("a", "Running the suite."), item("b", "The fix"))
        val host = host(drawn)
        val before = rowsIn(host)

        assertTrue(
            sessionDetailRedraw(
                host,
                drawn,
                panelOf(item("a", "Running the suite."), item("b", "The fix is a one-liner")),
            ),
        )

        assertEquals(
            listOf("Running the suite.", "The fix is a one-liner"),
            spokenIn(host),
        )
        assertSame(
            "the untouched row was rebuilt because its neighbour grew",
            before[0],
            rowsIn(host)[0],
        )
    }

    @Test
    fun `a load-earlier page arrives above the conversation and not below it`() {
        val drawn = panelOf(item("live", "All green."))
        val host = host(drawn)

        assertTrue(
            sessionDetailRedraw(
                host,
                drawn,
                panelOf(item("old", "Started the run."), item("live", "All green.")),
            ),
        )

        assertEquals(
            "the history ADR-014 pages BACKWARDS landed under the message the reader was " +
                "reading, so the phone reordered a conversation it did not have",
            listOf("Started the run.", "All green."),
            spokenIn(host),
        )
    }

    @Test
    fun `a row the read no longer holds leaves the screen`() {
        val drawn = panelOf(item("evicted", "The oldest thing."), item("kept", "All green."))
        val host = host(drawn)

        assertTrue(sessionDetailRedraw(host, drawn, panelOf(item("kept", "All green."))))

        assertEquals(
            "the retention bound trimmed the head and the row stayed on screen, over a " +
                "transcript that no longer holds it",
            listOf("All green."),
            spokenIn(host),
        )
    }

    @Test
    fun `a row whose facts changed really is rebound`() {
        val drawn = panelOf(item("a", "The fix"))
        val host = host(drawn)

        assertTrue(sessionDetailRedraw(host, drawn, panelOf(item("a", "The fix is a one-liner"))))

        // READ OFF THE SCREEN AND NOT OFF A VIEW HELD FROM BEFORE. A rebind recomposes the row
        // it names -- that is what "rebind" means here, a conversation being a list rather than a
        // string (`sessionDetailRedraw`'s own KDoc) -- so a reference kept across the patch names
        // a view that is no longer on screen, and asserting on it would test the wrong object.
        assertEquals(
            "the row kept its old words, so an incremental redraw that keeps views has stopped " +
                "delivering the ones that changed",
            listOf("The fix is a one-liner"),
            spokenIn(host),
        )
    }

    @Test
    fun `the composer and the controls are still never re-parented`() {
        val drawn = panelOf(item("a", "Running the suite."))
        val host = host(drawn)
        val composer = host.kitRequire(DetailTag.COMPOSER)
        val stop = host.kitRequire(DetailTag.STOP)

        assertTrue(
            sessionDetailRedraw(
                host,
                drawn,
                panelOf(item("a", "Running the suite."), item("b", "All green.")),
            ),
        )

        assertSame(composer, host.kitRequire(DetailTag.COMPOSER))
        assertSame(stop, host.kitRequire(DetailTag.STOP))
    }

    @Test
    fun `a patch that would change the chrome is still refused outright`() {
        val drawn = panelOf(item("a", "Running the suite."))
        val host = host(drawn)
        val torn = SessionDetailScreen.of(
            SessionDetail(
                sessionId = session, online = true,
                journalStale = false, title = "api refactor",
            ),
            TranscriptScreen.of(listOf(item("a", "Running the suite."))),
            SessionLease(sessionId = session, online = true),
            // WAVE R8 (ADR-017 T2 rule 3): a TORN session is now the RECORD saying
            // structured_chat=false -- which is exactly what the daemon's one-way degrade writes
            // after a proven structured gap -- rather than a gap element in the item list being
            // read as a capability. The assertion below is unchanged.
            capabilities = SessionCapabilityFacts(structuredChat = false),
        )
        assertTrue(
            "a lease change was patched in place, so the controls go on saying what they said " +
                "before the machine answered",
            !sessionDetailRedraw(host, drawn, torn),
        )
        assertNotSame(
            "a refusal must leave the caller to rebuild, and this one reported false having " +
                "already changed the screen",
            "",
            spokenIn(host).first(),
        )
    }
}
