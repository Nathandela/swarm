package dev.swarm.phone.ui.screens

import dev.swarm.phone.ui.SessionRow
import dev.swarm.phone.ui.TriageInbox
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotEquals
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * FAILING-FIRST (TDD RED, GG-5) for PB-DS-9 over the triage inbox's SCREEN MODEL.
 *
 * WHY THERE IS A MODEL AT ALL, given [TriageInbox] already exists. [TriageInbox] answers what the
 * roster IS -- four Groups, their sections, whether the list is whole. This answers what the
 * SCREEN says about it: the heading each Group renders, what an empty section says, what the live
 * counter counts, what the badge counts, and which scope chips exist. Every one of those is copy
 * or arithmetic, both of which are the screen's (PB-DS-9), and putting them in a view file is how
 * this project has repeatedly ended up with strings no test can reach.
 *
 * THE ORDER IS [TriageInbox.TRIAGE_ORDER]'S AND NOT THIS FILE'S. Inventory C1 draws the artifact's
 * sections as `Needs you / Working / Ready for review / Done`; the model declares
 * `needs_input, ready_for_review, completed, working` and its KDoc argues why -- working is the one
 * Group that needs nothing from the user, so it goes last on a triage surface. The recorded copy is
 * the artifact's; the ORDER is the model's, and every assertion below reads it from the model
 * rather than restating it, so a change of mind there fails here rather than silently disagreeing.
 *
 * AN EMPTY SECTION IS THE POINT OF THIS FILE. Dropping it is the obvious implementation and it is
 * wrong for a triage surface: the sections then move under the user as sessions change group, and
 * "nothing is waiting on me" -- the most useful fact this screen can report -- becomes
 * indistinguishable from "that section scrolled away".
 */
class TriageInboxScreenTest {

    private fun row(
        id: String,
        group: String,
        need: String = "doing something",
        present: Boolean = true,
        agent: String = "claude",
    ) = SessionRow(
        id = id,
        title = id.substringAfter('/'),
        group = group,
        need = need,
        present = present,
        agent = agent,
    )

    private fun screenOf(
        rows: List<SessionRow>,
        stale: Boolean = false,
        scope: String? = null,
        selected: String? = null,
    ) = TriageInboxScreen.of(
        inbox = TriageInbox.from(rows, journalStale = stale),
        scope = scope,
        selectedSession = selected,
    )

    // ---- promotion --------------------------------------------------------

    /**
     * ADR-009 D4's promoted slab, as a fact this MODEL names rather than one the kit rediscovers.
     *
     * WHY IT IS THE SCREEN'S AND NOT THE COMPONENT'S. Which Group is the one blocked on the human
     * is a product decision, and this file already makes it twice -- [TriageInboxScreen] counts the
     * tab badge from `needs_input` and excludes it from nothing else. The kit made it a third time,
     * as `group == "needs_input"` inside `sessionRow`, and a third copy of a decision is how the
     * three come to disagree: a skin that promoted `ready_for_review` would move the slab and leave
     * the badge counting the other Group, with every test green because each was asked for exactly
     * what it drew.
     *
     * IT IS ALSO WHAT ADR-009 D5 WILL FIRE THE SWEEP FROM. The specular sweep runs once "at the
     * moment a session's Group becomes NeedsInput" -- that is this fact, changing -- so naming it
     * here is what stops O4 deriving the same Group a fourth time.
     *
     * THE STATE IS BUILT BY THE REAL RESOLVER AND NEVER HAND-FED, which is the recorded qx9m
     * lesson: a suite that constructed an `InboxRow` with `lit = true` and asserted it rendered
     * promoted would certify that the renderer reads its argument. What is interesting is the
     * MAPPING, so the rows go in as the wire describes them and come out through
     * `TriageInbox.from` and `TriageInboxScreen.of`.
     */
    @Test
    fun `the promoted row is the model's own fact and it is the Group the badge counts`() {
        val screen = screenOf(TriageInbox.TRIAGE_ORDER.map { group -> row("mbp/$group", group) })

        assertEquals(
            "the screen promotes a different set of Groups from the one it counts on the badge, " +
                "so the lit slab and the tab badge are answering to two different decisions",
            listOf("needs_input"),
            screen.sections.flatMap { it.rows }.filter { it.lit }.map { it.group },
        )
        assertEquals(
            "the badge counts a number of sessions other than the one promoted row",
            screen.sections.flatMap { it.rows }.count { it.lit },
            screen.tabs.sumOf { it.badgeCount },
        )
    }

    /**
     * Promotion follows the SESSION and not the section's position, which is the failure mode a
     * per-section flag would have.
     */
    @Test
    fun `promotion is per row, so a Group with several sessions promotes all of them`() {
        val screen = screenOf(
            listOf(
                row("mbp/one", "needs_input"),
                row("mbp/two", "needs_input"),
                row("mbp/three", "working"),
            ),
        )

        assertEquals(
            listOf(true, true, false),
            screen.sections.flatMap { it.rows }.map { it.lit },
        )
    }

    // ---- sections ---------------------------------------------------------

    @Test
    fun `every triage group is a section, in the model's own order`() {
        val screen = screenOf(listOf(row("mbp/one", "working")))

        assertEquals(
            "the screen renders a different set of sections from the one TriageInbox declares",
            TriageInbox.TRIAGE_ORDER,
            screen.sections.map { it.group },
        )
    }

    @Test
    fun `an empty section is still a section and says so`() {
        // One session, in one group. The other three sections have nothing in them and must
        // survive anyway -- with a heading a user can read and copy that says what the emptiness
        // means, which is the whole reason PB-DS-9 names this case.
        val screen = screenOf(listOf(row("mbp/one", "working")))

        val empty = screen.sections.filter { it.rows.isEmpty() }
        assertEquals(
            "sections vanished when they emptied: ${screen.sections.map { it.group }}",
            3,
            empty.size,
        )
        empty.forEach { section ->
            assertTrue(
                "the ${section.group} section is empty and carries no heading",
                section.heading.isNotBlank(),
            )
            assertTrue(
                "the ${section.group} section is empty and says nothing about it, so it renders " +
                    "as a bare heading over nothing",
                section.emptyCopy.isNotBlank(),
            )
        }
    }

    @Test
    fun `an empty roster still renders all four sections`() {
        val screen = screenOf(emptyList())

        assertEquals(TriageInbox.TRIAGE_ORDER, screen.sections.map { it.group })
        assertTrue(
            "a phone with no sessions shows nothing at all, so it cannot report the one fact it " +
                "is best placed to report -- that nothing is waiting on anybody",
            screen.sections.all { it.emptyCopy.isNotBlank() },
        )
    }

    @Test
    fun `the headings are the recorded copy`() {
        val screen = screenOf(emptyList())
        val headings = screen.sections.associate { it.group to it.heading }

        // Inventory C1: "Group labels in order: `Needs you` - `Working` - `Ready for review` -
        // `Done`". The words are the artifact's; only their order is ours.
        assertEquals(
            mapOf(
                "needs_input" to "Needs you",
                "ready_for_review" to "Ready for review",
                "completed" to "Done",
                "working" to "Working",
            ),
            headings,
        )
    }

    @Test
    fun `a session lands in its own group's section and nowhere else`() {
        val screen = screenOf(
            listOf(
                row("mbp/blocked", "needs_input"),
                row("mbp/busy", "working"),
                row("mbp/reviewable", "ready_for_review"),
                row("mbp/finished", "completed"),
            ),
        )

        assertEquals(
            mapOf(
                "needs_input" to listOf("mbp/blocked"),
                "ready_for_review" to listOf("mbp/reviewable"),
                "completed" to listOf("mbp/finished"),
                "working" to listOf("mbp/busy"),
            ),
            screen.sections.associate { section -> section.group to section.rows.map { it.id } },
        )
    }

    // ---- the row ----------------------------------------------------------

    @Test
    fun `a row carries the wire's own words and invents nothing`() {
        val screen = screenOf(listOf(row("mbp/api", "needs_input", need = "wants to run: git push")))
        val rendered = screen.sections.first { it.group == "needs_input" }.rows.single()

        assertEquals("mbp/api", rendered.id)
        assertEquals("api", rendered.project)
        assertEquals("wants to run: git push", rendered.need)
        // THE AGENT IS THE WIRE'S WORD, CARRIED THROUGH UNCHANGED. This assertion used to read
        // `assertEquals("", rendered.agent)` on the stated ground that "`swarmmobile.Session`
        // carries ID, Title, Group, Need and Present and no agent (mobile/types.go)". That stopped
        // being true at 5f45f34: mobile/types.go now documents Agent as "the agent identity the
        // machine reported for this session, verbatim from the wire". The old assertion pinned the
        // ABSENCE of the field, so it could not survive the field arriving; what it was really
        // protecting -- that this screen invents nothing -- is pinned in both directions now, here
        // and in the test below.
        assertEquals("claude", rendered.agent)
    }

    /**
     * The other direction, and it is the one that matters.
     *
     * `mobile/types.go` states that an empty Agent means "the session's records carried none" and
     * that it is never derived on-device. So a screen that filled the gap -- with the project, the
     * id, or a word like "unknown" -- would render a fabricated identity in the one cell a reader
     * trusts to name the agent, and it would be indistinguishable from a real one (ADR-007 B135).
     * The Go side pins the same pair; this is its other end.
     */
    @Test
    fun `a session whose records carried no agent gets none invented for it`() {
        val screen = screenOf(listOf(row("mbp/api", "working", agent = "")))
        val rendered = screen.sections.first { it.group == "working" }.rows.single()

        assertEquals(
            "the agent slot was filled with something the wire never sent",
            "",
            rendered.agent,
        )
    }

    @Test
    fun `the dot's announcement is the section's own heading`() {
        val screen = screenOf(listOf(row("mbp/api", "needs_input")))
        val section = screen.sections.first { it.group == "needs_input" }

        // The 7dp mark is the only thing distinguishing the four Groups -- four hues, no text --
        // so a screen reader user gets nothing from it unless the screen supplies the words.
        assertEquals(section.heading, section.rows.single().stateDescription)
    }

    @Test
    fun `the selected session is the one the screen was told about`() {
        val screen = screenOf(
            listOf(row("mbp/one", "working"), row("mbp/two", "working")),
            selected = "mbp/two",
        )
        val rows = screen.sections.first { it.group == "working" }.rows

        assertEquals(listOf(false, true), rows.map { it.selected })
    }

    // ---- the counters -----------------------------------------------------

    @Test
    fun `the live counter counts needs_input plus working`() {
        // Derivation table 8.1: the artifact renders `3 LIVE` over 1 NeedsInput + 2 Working + 1
        // Done, and omits ReadyForReview entirely -- so its recommendation, NeedsInput + Working,
        // reproduces the artifact's arithmetic exactly. A session waiting on a human is not
        // running; a finished one is not either.
        val screen = screenOf(
            listOf(
                row("mbp/blocked", "needs_input"),
                row("mbp/busy", "working"),
                row("mbp/also-busy", "working"),
                row("mbp/reviewable", "ready_for_review"),
                row("mbp/finished", "completed"),
            ),
        )

        assertEquals("3 LIVE", screen.live)
    }

    @Test
    fun `nothing in flight shows no counter at all`() {
        val screen = screenOf(listOf(row("mbp/finished", "completed")))

        assertNull("a `0 LIVE` counter is a number nobody needs", screen.live)
    }

    /**
     * FAILING-FIRST (TDD RED, GG-5) for agents-tracker-e6mi.
     *
     * A HOLED ROSTER LABELLED `LIVE` IS THE ONE CLAIM THIS SCREEN MUST NOT MAKE. The list is
     * rendered from the journal stream, and [TriageInbox.stale] says that stream has an unrepaired
     * hole -- so a session, an exit or a needs_input may be missing, and the counter over it is an
     * arithmetic result presented as a fact. The stale notice said so at the BOTTOM of the column
     * while `.pnav .live` asserted `3 LIVE` at the top of the same screen, which is the two halves
     * of one screen disagreeing.
     *
     * IT IS QUALIFIED RATHER THAN SUPPRESSED, which is [TriageInbox]'s own rule about an empty
     * section applied to a number: dropping it makes "nothing is in flight" indistinguishable from
     * "we are not sure", and the count is still the most useful thing the screen has. The mark is
     * the model's copy, read from the model here rather than transcribed, so a change of wording
     * fails at the source rather than silently disagreeing with the screen.
     */
    @Test
    fun `a counter over a holed roster is not presented as a whole one`() {
        val rows = listOf(
            row("mbp/blocked", "needs_input"),
            row("mbp/busy", "working"),
            row("mbp/also-busy", "working"),
        )

        val whole = screenOf(rows, stale = false).live
        val holed = screenOf(rows, stale = true).live

        assertEquals("3 LIVE", whole)
        assertNotEquals(
            "the counter reads the same over a roster the phone knows is incomplete as over one " +
                "it knows is whole, so the screen asserts a liveness it cannot have counted",
            whole,
            holed,
        )
        assertNotNull("the counter vanished rather than being qualified", holed)
        assertTrue(
            "the qualified counter no longer carries the count, so the screen traded a claim it " +
                "could not support for no information at all: \"$holed\"",
            holed!!.contains("3"),
        )
    }

    @Test
    fun `a holed roster with nothing in flight still shows no counter`() {
        // The qualification does not resurrect the counter the model already refuses: a `0 LIVE`
        // is a number nobody needs, and a qualified zero is that number with a mark on it.
        assertNull(screenOf(listOf(row("mbp/finished", "completed")), stale = true).live)
    }

    @Test
    fun `the badge counts needs_input only`() {
        val screen = screenOf(
            listOf(
                row("mbp/blocked", "needs_input"),
                row("mbp/also-blocked", "needs_input"),
                row("mbp/busy", "working"),
            ),
        )
        val inbox = screen.tabs.single { it.selected }

        // Derivation table 1.4: the two counters ship together only because they count different
        // things -- the header says how much is in flight, the badge says what needs YOU and is
        // the only one that survives leaving this screen.
        assertEquals(2, inbox.badgeCount)
        // Row 3 states the form of the announcement.
        assertEquals("2 sessions need you", inbox.badgeDescription)
    }

    @Test
    fun `one session blocked is announced in the singular`() {
        val screen = screenOf(listOf(row("mbp/blocked", "needs_input")))

        // The recorded form is "N sessions need you", and at the count this badge spends most of
        // its life at that produces "1 sessions need you" -- read aloud, in place of a number
        // whose whole job is to be understood.
        assertEquals("1 session needs you", screen.tabs.single { it.selected }.badgeDescription)
    }

    @Test
    fun `no session needs anyone and there is no badge`() {
        val screen = screenOf(listOf(row("mbp/busy", "working")))

        assertEquals(
            "a badge reading zero is an alarm about nothing, on the one signal this product " +
                "promises means something",
            0,
            screen.tabs.single { it.selected }.badgeCount,
        )
        assertNull(screen.tabs.single { it.selected }.badgeDescription)
    }

    // ---- the tab bar ------------------------------------------------------

    @Test
    fun `the tab bar is the recorded four, with the inbox selected`() {
        val screen = screenOf(emptyList())

        assertEquals(
            listOf("Inbox", "Machines", "Activity", "Settings"),
            screen.tabs.map { it.label },
        )
        assertEquals(listOf(true, false, false, false), screen.tabs.map { it.selected })
    }

    // ---- the scope bar ----------------------------------------------------

    @Test
    fun `the scope bar is All machines plus one chip per machine in the roster`() {
        val screen = screenOf(
            listOf(
                row("nathans-mbp/one", "working"),
                row("nathans-mbp/two", "completed"),
                row("mac-studio/three", "working"),
            ),
        )

        // Sorted, not roster order. TriageInbox has already grouped the roster by the time this
        // screen sees it, so "roster order" would really be "order of first appearance walking
        // the Groups" -- and the chips would swap places when a session changed group, under the
        // finger of whoever was reaching for one.
        assertEquals(
            listOf("All machines", "mac-studio", "nathans-mbp"),
            screen.scopes.map { it.label },
        )
        assertEquals(
            "the All machines chip names no machine, so it has no presence to report",
            null,
            screen.scopes.first().present,
        )
        assertEquals(listOf(true, false, false), screen.scopes.map { it.selected })
    }

    @Test
    fun `the chips keep their places when a session changes group`() {
        val before = screenOf(
            listOf(row("nathans-mbp/one", "working"), row("mac-studio/two", "needs_input")),
        )
        // The same two machines, with the two sessions' Groups swapped -- which is the ordinary
        // event on this screen and must not move a filter control.
        val after = screenOf(
            listOf(row("nathans-mbp/one", "needs_input"), row("mac-studio/two", "working")),
        )

        assertEquals(before.scopes.map { it.label }, after.scopes.map { it.label })
    }

    @Test
    fun `a machine chip reports the presence the wire gave its sessions`() {
        val screen = screenOf(
            listOf(
                row("nathans-mbp/one", "working", present = true),
                row("mac-studio/two", "working", present = false),
            ),
        )
        val chips = screen.scopes.associate { it.label to it.present }

        assertEquals(mapOf("All machines" to null, "nathans-mbp" to true, "mac-studio" to false), chips)
    }

    @Test
    fun `a machine chip announces its presence, which a dot cannot`() {
        val screen = screenOf(listOf(row("nathans-mbp/one", "working", present = false)))
        val chip = screen.scopes.single { it.machine == "nathans-mbp" }

        // The presence dot is a compound drawable and a drawable cannot be described, so the chip
        // is the only view that can speak for it.
        assertEquals("nathans-mbp, offline", chip.description)
        assertNull(
            "the All machines chip has no dot to describe, and a non-null description would be " +
                "read INSTEAD of its label",
            screen.scopes.first().description,
        )
    }

    @Test
    fun `choosing a machine narrows the rows and keeps every section`() {
        val rows = listOf(
            row("nathans-mbp/one", "working"),
            row("mac-studio/two", "working"),
            row("mac-studio/three", "needs_input"),
        )
        val screen = screenOf(rows, scope = "mac-studio")

        assertEquals(
            "the scope chip is decoration: it did not narrow the roster",
            listOf("mac-studio/three", "mac-studio/two"),
            screen.sections.flatMap { section -> section.rows.map { it.id } },
        )
        assertEquals(
            "narrowing the scope dropped the sections with nothing left in them",
            TriageInbox.TRIAGE_ORDER,
            screen.sections.map { it.group },
        )
        assertEquals(listOf(false, true, false), screen.scopes.map { it.selected })
    }

    @Test
    fun `the counters follow the scope`() {
        val rows = listOf(
            row("nathans-mbp/one", "needs_input"),
            row("nathans-mbp/two", "working"),
            row("mac-studio/three", "needs_input"),
        )

        assertEquals("3 LIVE", screenOf(rows).live)
        assertEquals("1 LIVE", screenOf(rows, scope = "mac-studio").live)
        assertEquals(1, screenOf(rows, scope = "mac-studio").tabs.single { it.selected }.badgeCount)
    }

    @Test
    fun `a session with no machine in its id contributes no chip`() {
        // `mobile/app.go` derives a session's title by cutting the id at the first "/", and falls
        // back to the whole id when there is none. A roster row in that shape belongs to no
        // machine this screen can name, so it gets no chip rather than a chip labelled "".
        val screen = screenOf(listOf(row("unnamespaced", "working")))

        assertEquals(listOf("All machines"), screen.scopes.map { it.label })
        assertEquals(
            "the row itself was dropped along with the chip it could not fill",
            1,
            screen.sections.sumOf { it.rows.size },
        )
    }

    // ---- staleness --------------------------------------------------------

    @Test
    fun `a holed journal is reported in the model's own words`() {
        val stale = screenOf(listOf(row("mbp/one", "working")), stale = true)
        val whole = screenOf(listOf(row("mbp/one", "working")), stale = false)

        // PB-APP-8 at this screen. The wording is TriageInbox's, so there is one sentence about a
        // holed roster rather than a second copy that can disagree with it.
        assertEquals(
            TriageInbox.from(listOf(row("mbp/one", "working")), journalStale = true).staleNotice,
            stale.staleNotice,
        )
        assertTrue(stale.staleNotice.isNotBlank())
        assertEquals("", whole.staleNotice)
    }

    // ---- the copy tables ---------------------------------------------------

    @Test
    fun `every group the model can carry has a heading and empty copy`() {
        // The reverse of the section assertions: not "the screen built four sections" but "no
        // status.Group the model places can reach this screen without copy of its own". A Group
        // added to TRIAGE_ORDER with no row here would otherwise render as a blank heading.
        TriageInbox.TRIAGE_ORDER.forEach { group ->
            assertNotNull(
                "no section heading for the status.Group $group",
                TriageInboxScreen.headingFor(group),
            )
            assertNotNull(
                "no empty copy for the status.Group $group",
                TriageInboxScreen.emptyCopyFor(group),
            )
        }
    }
}
