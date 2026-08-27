package dev.swarm.phone.ui.screens

import dev.swarm.phone.ui.SessionDetail
import dev.swarm.phone.ui.SessionLease
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * FAILING-FIRST (TDD RED, GG-5) for owner ruling R2's destination: the conversation header's
 * overflow menu. Plan: docs/specifications/chat-surface-plan.md §5 item 4 and §6.
 *
 * **WHAT R2 ACTUALLY BOUGHT, AND WHY IT IS WORTH A TEST.** Take control, Stop and Kill shipped as
 * three stacked full-width CTAs between the transcript and the composer -- about 160 dp on a
 * viewport with roughly 150 dp left for the conversation. Two of the three leave the product; the
 * third moves into a 48 dp mark in the header. This file asserts the part of that move that is a
 * DECISION rather than a layout: which rows the menu has, and the two it refuses.
 *
 * IT IS A PURE FUNCTION AND THAT IS DELIBERATE. `PhoneRuntime.phone()` answers Unavailable on every
 * JVM run, so the menu as a view is out of reach here; the rows are copy and arrangement, both
 * assigned to the screen model by PB-DS-9, so they are decided where a test can read them.
 */
class SessionDetailMenuTest {

    private fun panel(
        atFloor: Boolean = false,
        online: Boolean = true,
        ended: Boolean = false,
    ): SessionDetailPanel = SessionDetailScreen.of(
        SessionDetail(
            sessionId = "ep-9f2a/NewLatexCV",
            online = online,
            journalStale = false,
            ended = ended,
            title = "claude-NewLatexCV",
            group = "working",
            machineLabel = "nathan-mbp",
        ),
        TranscriptScreen.of(
            listOf(
                dev.swarm.phone.ui.InteractionItem(
                    sessionId = "ep-9f2a/NewLatexCV",
                    itemId = "i-1",
                    cursor = 1,
                    kind = "user_message",
                    body = """{"text":"check the relay logs"}""",
                ),
            ),
            atFloor = atFloor,
        ),
        SessionLease(sessionId = "ep-9f2a/NewLatexCV", online = online),
        capabilities = SessionCapabilityFacts(structuredChat = true),
    )

    @Test
    fun `kill is in the menu, marked destructive, and is the only destructive row`() {
        val rows = SessionDetailScreen.menuChoicesFor(panel())
        val kill = rows.single { it.id == SessionDetailScreen.MENU_KILL }

        assertEquals(
            "the menu's kill row does not carry the shipped question's own label, so the control " +
                "in the header and the control that was in the CTA stack are two different things",
            panel().killLabel,
            kill.label,
        )
        assertTrue(
            "the one act on this menu that cannot be undone is drawn like a route. `--p-ink` is " +
                "a place you go and `--p-err` is a thing that ends, and the row directly above a " +
                "mis-aimed tap on Kill is an ordinary navigation",
            kill.destructive,
        )
        assertEquals(
            "more than one row claims to be destructive, which spends the one mark that means it",
            1,
            rows.count { it.destructive },
        )
    }

    @Test
    fun `there is no terminal-view route and no second repair`() {
        val labels = SessionDetailScreen.menuChoicesFor(panel()).map { it.label.toString().lowercase() }

        assertFalse(
            "the menu offers a route to the raw terminal. ADR-017:60-65 forbids one on a session " +
                "with a structured record, so the row would be a door onto a room that is not there",
            labels.any { it.contains("terminal") },
        )
        assertFalse(
            "the menu offers a repair. The tear has a POSITION and the repair is drawn at it, " +
                "inside the conversation -- two affordances for one live-only operation are two " +
                "pending states competing over which of them is in flight",
            labels.any { it.contains("repair") },
        )
    }

    @Test
    fun `load earlier is offered only while there is something older to fetch`() {
        assertTrue(
            "the page is not offered at all, so the only way to read the start of a conversation " +
                "is the chip at the head of a list the reader has to scroll to",
            SessionDetailScreen.menuChoicesFor(panel())
                .any { it.id == SessionDetailScreen.MENU_LOAD_EARLIER },
        )
        assertFalse(
            "the page is still offered after the machine declared the floor, so the row is a tap " +
                "that can only ever come back empty -- the dead-chevron defect wearing a page",
            SessionDetailScreen.menuChoicesFor(panel(atFloor = true))
                .any { it.id == SessionDetailScreen.MENU_LOAD_EARLIER },
        )
    }

    @Test
    fun `a row is answered by its id and never by the words on it`() {
        val rows = SessionDetailScreen.menuChoicesFor(panel())

        assertEquals(
            "two rows share an id, so what comes back through onChoose cannot say which one was " +
                "pressed",
            rows.size,
            rows.map { it.id }.distinct().size,
        )
        assertTrue(
            "a row's id is its own visible copy, so the menu re-routes itself the day " +
                "\"Kill session\" becomes \"End session\"",
            rows.none { it.id == it.label.toString() },
        )
    }
}
