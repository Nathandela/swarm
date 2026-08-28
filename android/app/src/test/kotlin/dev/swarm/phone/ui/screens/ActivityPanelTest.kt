package dev.swarm.phone.ui.screens

import dev.swarm.phone.ui.JournalPageView
import dev.swarm.phone.ui.JournalRow
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * FAILING-FIRST (TDD RED, GG-5) for PB-DS-9 over the ACTIVITY screen's model.
 *
 * **THIS SUITE IS WHERE THE HONESTY OF THIS SCREEN IS ENFORCED, AND IT IS THE ONLY PLACE.** The Go
 * gate compares files against the design; `ActivityPanelViewTest` compares a view against the
 * kit. Neither can fail because a screen renders a fact the wire does not carry -- a panel that
 * split its rows under `While you were away` and `Informative`, stamped each with an `HH:MM`
 * formatted out of the cursor, and emphasised an invented project name would satisfy every fence
 * in `android/gate` and every appearance claim in `ui/kit`. It would also be the exact defect
 * ADR-007 B135 and §8.8 of the design doc are about: a screen that is pixel-accurate to its
 * drawing and lying.
 *
 * So three of the tests below assert that something the mock draws is NOT on screen, and each
 * names the field that would have to exist on `swarmmobile.JournalEntry` for it to be. They are
 * the tests to delete the day the wire grows one -- not before.
 */
class ActivityPanelTest {

    private fun page(
        rows: List<JournalRow> = listOf(
            JournalRow(cursor = 1, sessionId = "quanthome", type = "launched", group = "", tsUnixMs = 0L),
            JournalRow(cursor = 2, sessionId = "blog", type = "group_transition", group = "working", tsUnixMs = 0L),
            JournalRow(cursor = 3, sessionId = "swarm", type = "group_transition", group = "needs_input", tsUnixMs = 0L),
        ),
        stale: Boolean = false,
    ) = JournalPageView(rows = rows, nextCursor = rows.maxOfOrNull { it.cursor } ?: 0, stale = stale)

    /**
     * W7.4: a fixed "now", built in the default zone at NOON so "today" and "yesterday" are facts
     * about the fixture in every zone the JVM might run in, never about the wall clock.
     */
    private val NOW: Long = java.util.Calendar.getInstance().apply {
        set(2026, java.util.Calendar.AUGUST, 28, 12, 0, 0)
        set(java.util.Calendar.MILLISECOND, 0)
    }.timeInMillis
    private val HOUR = 60 * 60_000L
    private val DAY = 24 * HOUR

    private fun panel(
        rows: List<JournalRow> = page().rows,
        stale: Boolean = false,
    ) = ActivityPanelScreen.of(page(rows, stale), nowUnixMs = NOW)

    private fun stamped(cursor: Long, session: String, type: String, group: String, ts: Long) =
        JournalRow(cursor = cursor, sessionId = session, type = type, group = group, tsUnixMs = ts)

    private val ActivityPanel.only: ActivitySection get() = sections.single()

    // ---- what the mock draws and the journal cannot supply ---------------------

    /**
     * The mock's `While you were away` / `Informative` split.
     *
     * `swarmmobile.JournalEntry` is `(Cursor, SessionID, Type, Group)`: no seen-ness, no
     * acknowledgement, no severity. The first heading is a claim about when the user last looked
     * and the second is a claim about importance, and nothing on the wire computes either.
     */
    @Test
    fun `one section per day`() {
        // W7.4: the sections are DAYS, from the daemon's own record stamp. Three records on one
        // day are one section; the mock's two headings are still claims about seen-ness and
        // salience that nothing on the wire supports, and are still not reproduced.
        val sections = panel(
            rows = listOf(
                stamped(1, "quanthome", "launched", "", NOW - 3 * HOUR),
                stamped(2, "blog", "group_transition", "working", NOW - 2 * HOUR),
                stamped(3, "swarm", "group_transition", "needs_input", NOW - HOUR),
            ),
        ).sections

        assertEquals(
            "three records stamped on one day were split into ${sections.map { it.heading }}",
            listOf("Today"),
            sections.map { it.heading },
        )
        listOf("While you were away", "Informative").forEach { heading ->
            assertFalse(
                "the panel reproduces the mock's `$heading` heading. There is no field behind it",
                sections.any { it.heading == heading },
            )
        }
    }

    @Test
    fun `no row renders its cursor, because a cursor is not a time`() {
        panel().only.rows.forEach { entry ->
            assertFalse(
                "row ${entry.cursor} renders its own cursor in `${entry.body}`. A cursor is a " +
                    "sequence number; the wire carries no timestamp at all, so anything that " +
                    "looks like a time on this screen was manufactured on the handset",
                entry.body.contains(entry.cursor.toString()),
            )
        }
    }

    /**
     * The mock's emphasised project name, which is now the session.
     *
     * This test asserted the `Group` until `JournalRow` grew `sessionId`. The facade always
     * carried `SessionID` and `FacadeBridge.journal` dropped it, so the feed could report that a
     * session launched and not WHICH -- and the emphasis had no subject to be, only the Group
     * standing in for one. It has a subject now.
     *
     * The fallback chain is asserted rather than assumed, because each step means something
     * different: a record with a session emphasises it; a record with only a Group emphasises
     * that, so the eye still lands on a wire token in a monospace span; a record with neither
     * emphasises NOTHING, rather than promoting the type and putting the eye on the one word
     * every row shares.
     */
    @Test
    fun `the emphasis is the session, or nothing`() {
        val rows = panel().only.rows.associateBy { it.cursor }

        assertEquals(
            "a record carrying a session did not emphasise it. This is the whole point of the " +
                "sessionId field: without it the row cannot say which session it is about",
            "swarm",
            rows.getValue(3L).emphasis,
        )
        assertEquals("blog", rows.getValue(2L).emphasis)
        assertNull(
            "a record with a Group and no session was emphasised. W7.4's body is `session · " +
                "word` and the Group is folded into the word, so a Group emphasis would name a " +
                "span the body no longer holds -- which `activityRow` refuses",
            panel(rows = listOf(JournalRow(cursor = 9, sessionId = "", type = "x", group = "working", tsUnixMs = 0L)))
                .only.rows.single().emphasis,
        )
        assertNull(
            "a record with neither a session nor a Group was emphasised anyway. There is nothing " +
                "on it to put the eye on, and emphasising the type would put it on the one word " +
                "every row shares",
            panel(rows = listOf(JournalRow(cursor = 8, sessionId = "", type = "x", group = "", tsUnixMs = 0L)))
                .only.rows.single().emphasis,
        )
    }

    // ---- what it does render ---------------------------------------------------

    @Test
    fun `each row is its session and the W5 word`() {
        // W7.4: `session · word`, where the word is W5's vocabulary for what happened --
        // `started`, `finished`, `needs you`, `connection lost` -- and a group_transition reads
        // by the Group it names. A type this build does not know renders verbatim, the inbox's
        // own rule, so a server that adds one cannot make this screen lie.
        val rows = panel().only.rows.associateBy { it.cursor }

        assertEquals("quanthome · started", rows.getValue(1L).body)
        assertEquals("blog · working", rows.getValue(2L).body)
        assertEquals("swarm · needs you", rows.getValue(3L).body)
        assertEquals(
            listOf("api · finished", "api · connection lost", "api · a_future_type"),
            panel(
                rows = listOf(
                    JournalRow(cursor = 1, sessionId = "api", type = "exited", group = "", tsUnixMs = 0L),
                    JournalRow(cursor = 2, sessionId = "api", type = "lost", group = "", tsUnixMs = 0L),
                    JournalRow(cursor = 3, sessionId = "api", type = "a_future_type", group = "", tsUnixMs = 0L),
                ),
            ).only.rows.sortedBy { it.cursor }.map { it.body },
        )
    }

    // ---- W7.4: by day, with a time ---------------------------------------------
    //
    // FAILING-FIRST (TDD RED, GG-5). `JournalRow.tsUnixMs` is the daemon's own record stamp,
    // 0 where the wire carried none; the screen groups by day, newest day first, and a row
    // carries the time `ToolCard.timestampLabel` formats -- "" for 0, so the epoch is never drawn.

    @Test
    fun `rows are grouped by day newest day first`() {
        val sections = panel(
            rows = listOf(
                stamped(1, "quanthome", "launched", "", NOW - DAY - HOUR),
                stamped(2, "blog", "group_transition", "working", NOW - 3 * HOUR),
                stamped(3, "swarm", "group_transition", "needs_input", NOW - HOUR),
                stamped(4, "api", "exited", "", NOW - DAY),
            ),
        ).sections

        assertEquals(listOf("Today", "Yesterday"), sections.map { it.heading })
        assertEquals(
            "within a day the feed still reads newest first",
            listOf(3L, 2L),
            sections[0].rows.map { it.cursor },
        )
        assertEquals(listOf(4L, 1L), sections[1].rows.map { it.cursor })
    }

    @Test
    fun `a stamped row carries its time`() {
        val ts = NOW - HOUR
        val row = panel(rows = listOf(stamped(1, "quanthome", "launched", "", ts))).sections.single().rows.single()

        assertEquals(dev.swarm.phone.ui.kit.ToolCard.timestampLabel(ts), row.time)
        assertTrue("the time is empty for a stamped row", row.time.isNotEmpty())
        assertFalse(
            "the time was spliced into the body; it is a cell of its own (`activityRow`'s " +
                "timestamp), so a cursor test on the body cannot mistake it for a number",
            row.body.contains(row.time),
        )
    }

    @Test
    fun `an absent stamp renders no time and no day heading`() {
        val sections = panel(rows = page().rows).sections

        assertEquals("zero-stamp rows fall into one trailing section", 1, sections.size)
        assertTrue(
            "an unstamped section was headed with a day (${sections.single().heading}); the " +
                "phone has no basis for one",
            sections.single().heading !in setOf("Today", "Yesterday"),
        )
        sections.single().rows.forEach { entry ->
            assertEquals("row ${entry.cursor} drew a time from a zero stamp", "", entry.time)
        }
    }

    @Test
    fun `zero-stamp rows trail the stamped days`() {
        val sections = panel(
            rows = listOf(
                JournalRow(cursor = 1, sessionId = "old", type = "launched", group = "", tsUnixMs = 0L),
                stamped(2, "swarm", "group_transition", "needs_input", NOW - HOUR),
            ),
        ).sections

        assertEquals(2, sections.size)
        assertEquals("Today", sections[0].heading)
        assertEquals(listOf(1L), sections[1].rows.map { it.cursor })
    }

    @Test
    fun `every emphasis occurs in the body it emphasises`() {
        panel().only.rows.forEach { entry ->
            entry.emphasis?.let {
                assertTrue(
                    "`$it` is not in `${entry.body}`, so `activityRow` would refuse this row",
                    entry.body.contains(it),
                )
            }
        }
    }

    @Test
    fun `the feed reads newest first`() {
        assertEquals(
            listOf(3L, 2L, 1L),
            panel().only.rows.map { it.cursor },
        )
    }

    @Test
    fun `the title is the mock's own`() {
        assertEquals("Activity", panel().title)
    }

    // ---- PB-APP-8 --------------------------------------------------------------

    /**
     * `JournalPage.Stale`'s own reason: the log renders as a chronology, "a shape that reads as
     * complete unless it says otherwise". The plausible wrong answer is an unconditional notice,
     * which is a warning over a healthy feed and trains the reader to skip it.
     */
    @Test
    fun `a holed stream says so, and a whole one says nothing`() {
        assertEquals("", panel(stale = false).staleNotice)

        val holed = panel(stale = true).staleNotice
        assertTrue("a stale journal page produced no notice at all", holed.isNotEmpty())
        assertTrue(
            "the notice does not say the history is INCOMPLETE. `stale` on its own leaves a " +
                "reader to guess whether the list is old or has records missing from it, and for " +
                "a chronology it is the second",
            holed.contains("missing") || holed.contains("complete"),
        )
    }

    // ---- the empty page --------------------------------------------------------

    /**
     * PB-DS-9's rule that an empty section is still a section, over the surface where the obvious
     * implementation is to render nothing at all.
     */
    @Test
    fun `an empty page still renders a section, with something to read in it`() {
        val empty = panel(rows = emptyList())

        assertEquals(1, empty.sections.size)
        assertEquals(emptyList<ActivityEntry>(), empty.only.rows)
        assertTrue("the empty section has no copy in it", empty.only.emptyCopy.isNotEmpty())
        assertFalse(
            "the empty copy claims nothing HAPPENED. This phone cannot know that -- it can only " +
                "say what has reached it, which is a different and smaller claim",
            empty.only.emptyCopy.contains("Nothing has happened"),
        )
    }
}
