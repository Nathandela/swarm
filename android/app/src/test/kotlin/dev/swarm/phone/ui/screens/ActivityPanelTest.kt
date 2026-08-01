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
            JournalRow(cursor = 1, type = "launched", group = ""),
            JournalRow(cursor = 2, type = "group_transition", group = "working"),
            JournalRow(cursor = 3, type = "group_transition", group = "needs_input"),
        ),
        stale: Boolean = false,
    ) = JournalPageView(rows = rows, nextCursor = rows.maxOfOrNull { it.cursor } ?: 0, stale = stale)

    private fun panel(
        rows: List<JournalRow> = page().rows,
        stale: Boolean = false,
    ) = ActivityPanelScreen.of(page(rows, stale))

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
    fun `the panel renders one section, because nothing supports the mock's two`() {
        val sections = panel().sections

        assertEquals(
            "the panel split its rows into ${sections.size} sections. The journal carries no " +
                "seen-ness and no salience, so any split is a grouping invented to match a " +
                "drawing -- which is worse than one honest section",
            1,
            sections.size,
        )
        listOf("While you were away", "Informative").forEach { heading ->
            assertFalse(
                "the panel reproduces the mock's `$heading` heading. There is no field behind it",
                sections.any { it.heading == heading },
            )
        }
    }

    /**
     * The mock's `HH:MM` gutter.
     *
     * `internal/journal.Record` HAS a `TS time.Time` and `protocol.JournalRecord` -- the form the
     * phone is served -- drops it. The `Cursor` beside it is a monotonic sequence number, and the
     * defect this forecloses is formatting one as a clock: a real field's value presented as a
     * fact it is not.
     */
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
     * The mock's emphasised project name.
     *
     * `swarmmobile.JournalEntry` DOES carry `SessionID` and `FacadeBridge.journal` drops it on the
     * way into `JournalRow`, so the emphasis has no project to be. What it is instead is the
     * Group, verbatim -- and on the records that carry no Group it is nothing, rather than the
     * type promoted for want of anything better.
     */
    @Test
    fun `the emphasis is the record's own Group, or nothing`() {
        val rows = panel().only.rows.associateBy { it.cursor }

        assertEquals("needs_input", rows.getValue(3L).emphasis)
        assertEquals("working", rows.getValue(2L).emphasis)
        assertNull(
            "a record with no Group was emphasised anyway. The five record types that carry no " +
                "Group have nothing to put the eye on, and emphasising the type would put it on " +
                "the one word every row shares",
            rows.getValue(1L).emphasis,
        )
    }

    // ---- what it does render ---------------------------------------------------

    @Test
    fun `each row is its record's Type and Group, verbatim`() {
        val rows = panel().only.rows.associateBy { it.cursor }

        assertEquals("launched", rows.getValue(1L).body)
        assertEquals("group_transition · needs_input", rows.getValue(3L).body)
    }

    /**
     * The emphasis has to be findable IN the body, because `activityRow` spans a substring of it
     * and fails loudly on one it cannot find. A model that produced the two independently could
     * pass every claim above and crash the screen.
     */
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
