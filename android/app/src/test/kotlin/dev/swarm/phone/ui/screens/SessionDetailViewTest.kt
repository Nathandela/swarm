package dev.swarm.phone.ui.screens

import android.content.Context
import android.view.View
import android.view.ViewGroup
import android.widget.TextView
import androidx.test.core.app.ApplicationProvider
import dev.swarm.phone.theme.SwarmTheme
import dev.swarm.phone.ui.JournalRow
import dev.swarm.phone.ui.SessionDetail
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
 * FAILING-FIRST (TDD RED, GG-5) for PB-APP-3 and PB-DS-6 over inventory C2 AS DRAWN.
 *
 * WHAT ONLY A VIEW TEST CAN CATCH, which is why this exists beside `SessionDetailPanelTest`. That
 * suite says the model decides the right things; this says the screen actually puts them on screen,
 * in the recorded order, from the kit. "The model is beautiful and nothing renders it" is the
 * defect PB-DS-6 was recorded NOT MET over, and this screen is the eighth and last chance to repeat
 * it.
 *
 * THE CONTROLS ARE SLOTS AND NOT CONSTRUCTIONS. Stop and Kill reach facade verbs, carry PB-SEC-12
 * clause 1's touch filter, and must survive a redraw, so `PhoneSurface` owns them and hands them in
 * -- the arrangement `peekPanelView` already uses for `[Take control]`. What this file asserts is
 * that the screen PLACES them, not that pressing one does anything.
 */
@RunWith(RobolectricTestRunner::class)
class SessionDetailViewTest {

    private val context: Context
        get() = SwarmTheme.applyTo(ApplicationProvider.getApplicationContext())

    private fun record(cursor: Long, type: String, group: String = "") =
        JournalRow(cursor = cursor, sessionId = "mbp/api", type = type, group = group)

    private fun panel(
        journal: List<JournalRow> = listOf(record(1, "launched")),
        snapshotText: String = "$ git push",
        leaseHeld: Boolean = true,
        online: Boolean = true,
        journalStale: Boolean = false,
        stopNotSent: Boolean = false,
        snapshotStale: Boolean = false,
    ) = SessionDetailScreen.of(
        SessionDetail(
            sessionId = "mbp/api",
            journal = journal,
            snapshotText = snapshotText,
            leaseHeld = leaseHeld,
            online = online,
            journalStale = journalStale,
            stopNotSent = stopNotSent,
            snapshotStale = snapshotStale,
        ),
    )

    private fun view(
        panel: SessionDetailPanel = panel(),
        outcome: String = "",
        onBack: () -> Unit = {},
    ): View = sessionDetailView(
        context = context,
        panel = panel,
        stop = TextView(context).apply { text = panel.stopLabel },
        kill = TextView(context).apply { text = panel.killLabel },
        outcome = outcome,
        onBack = onBack,
    )

    private fun View.allTagged(tag: String): List<View> {
        val found = mutableListOf<View>()
        fun walk(v: View) {
            if (v.tag == tag) found += v
            if (v is ViewGroup) for (i in 0 until v.childCount) walk(v.getChildAt(i))
        }
        walk(this)
        return found
    }

    private fun textOf(view: View?): String = (view as? TextView)?.text?.toString().orEmpty()

    @Test
    fun `the screen is composed of the parts its recorded composition names`() {
        val root = view()

        listOf(
            DetailTag.NAV to "C2.1 -- the drill-down header, per derivation section 4",
            DetailTag.SNAPSHOT to "C2.2 -- the daemon-rendered grid in the mono well",
            DetailTag.SECTION_LABEL to "C2.3 -- the heading over the session's own journal",
            DetailTag.ROW to "C2.3 -- one record",
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

    @Test
    fun `the transcript renders one row per record, newest first, in the wire's words`() {
        val root = view(
            panel(
                journal = listOf(
                    record(1, "launched"),
                    record(2, "group_transition", group = "needs_input"),
                ),
            ),
        )

        assertEquals(
            listOf("group_transition · needs_input", "launched"),
            root.allTagged(DetailTag.ROW).map { textOf(it.kitRequire(KitTag.ACTIVITY_BODY)) },
        )
    }

    @Test
    fun `a session with no records draws its copy, not an empty area`() {
        val root = view(panel(journal = emptyList()))

        assertTrue(
            "an empty transcript renders nothing under its heading, which reads as a feed that " +
                "failed to load rather than a session with no records yet",
            textOf(root.kitFind(DetailTag.EMPTY)).isNotEmpty(),
        )
        assertNull("an empty transcript still drew a row", root.kitFind(DetailTag.ROW))
    }

    @Test
    fun `no snapshot means no card at all`() {
        val quiet = view(panel(snapshotText = ""))
        val printing = view(panel(snapshotText = "$ git push"))

        assertNull(
            "a session the machine has sent no frame for draws an empty terminal, which " +
                "presents \"we have not heard\" as \"the screen is blank\"",
            quiet.kitFind(DetailTag.SNAPSHOT),
        )
        assertNotNull(printing.kitFind(DetailTag.SNAPSHOT))
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

    /**
     * FAILING-FIRST for agents-tracker-0qe7 as DRAWN: the mark goes beside the card, and it is a
     * notice rather than a line inside the grid.
     *
     * THE WELL PRINTS THE MACHINE'S OUTPUT BYTE FOR BYTE. `monoWell(terminal = true)` renders
     * `swarmmobile.Snapshot.Text` as the daemon sanitized it, so a sentence written into the text
     * would be an English line in the machine's own register -- which is the defect this issue
     * reports on the peek, and there is no reason to repeat it here.
     */
    @Test
    fun `a stale snapshot is marked beside its card, not inside the grid`() {
        val frozen = view(panel(snapshotStale = true, snapshotText = "$ git push"))
        val fresh = view(panel(snapshotStale = false, snapshotText = "$ git push"))

        assertTrue(
            "the snapshot card carries no stale mark, so a grid the phone knows is out of date " +
                "reads as what the session is doing now",
            textOf(frozen.kitFind(DetailTag.SNAPSHOT_STALE)).isNotEmpty(),
        )
        assertEquals(
            "the stale sentence was written into the grid, in the well that prints the machine's " +
                "own output byte for byte",
            "$ git push",
            textOf(frozen.kitFind(DetailTag.SNAPSHOT)),
        )
        assertNull(
            "a stale mark is drawn over a snapshot the machine is still sending frames for",
            fresh.kitFind(DetailTag.SNAPSHOT_STALE),
        )
        assertNull(
            "a stale mark is drawn for a session that has sent no frame at all, which warns about " +
                "a card that is not on screen",
            view(panel(snapshotStale = true, snapshotText = "")).kitFind(DetailTag.SNAPSHOT_STALE),
        )
    }

    @Test
    fun `a holed journal says so above the records it is missing from`() {
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
     * THE OUTCOME SITS WITH THE CONTROLS AND NOT WITH THE NOTICES. Every other notice on this
     * screen qualifies the CONTENT above which it is drawn -- the stale line qualifies the
     * transcript, the not-sent line qualifies what was typed -- and this one qualifies the two
     * controls, so it belongs immediately above them. A routed refusal at the top of a scrolling
     * transcript is a report the person who pressed the button is not looking at.
     */
    @Test
    fun `the parts are drawn in the order the recorded composition names`() {
        // `stopNotSent` joins the state this draws from because the not-sent line is press-gated
        // (agents-tracker-4lta): a panel that is merely offline no longer draws it, and a part
        // this asserts the ORDER of has to be on screen for the order to mean anything.
        // `snapshotStale` joins it for the same reason, one part later (agents-tracker-0qe7).
        val root = view(
            panel = panel(
                journalStale = true,
                online = false,
                stopNotSent = true,
                snapshotStale = true,
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
}
