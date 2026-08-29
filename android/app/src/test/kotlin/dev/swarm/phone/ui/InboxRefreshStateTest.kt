package dev.swarm.phone.ui

import dev.swarm.phone.ui.screens.InboxScreen
import dev.swarm.phone.ui.screens.TriageInboxScreen
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class InboxRefreshStateTest {
    @Test
    fun `refresh is single-flight until a newer authoritative roster lands`() {
        val refresh = InboxRefreshState()
        assertTrue(refresh.begin(rosterRevision = 7))
        assertFalse("a second pull issued a duplicate resync", refresh.begin(rosterRevision = 7))
        assertTrue(refresh.refreshing)
        assertFalse("an unrelated redraw completed the refresh", refresh.observe(rosterRevision = 7))
        assertTrue(refresh.refreshing)
        assertTrue("the next authoritative roster did not complete the refresh", refresh.observe(rosterRevision = 8))
        assertFalse(refresh.refreshing)
    }

    @Test
    fun `a refused refresh returns to idle and permits retry`() {
        val refresh = InboxRefreshState()
        refresh.begin(rosterRevision = 4)
        refresh.refused()
        assertFalse(refresh.refreshing)
        assertFalse("the refusal was mistaken for a completed refresh", refresh.observe(5))
        assertTrue("a retry after failure was refused", refresh.begin(rosterRevision = 4))
    }

    @Test
    fun `a failed reread keeps the last drawn rows and a later success replaces them`() {
        fun screen(id: String?, refreshing: Boolean = false): InboxScreen =
            TriageInboxScreen.of(
                inbox = TriageInbox.from(
                    sessions = id?.let {
                        listOf(
                            SessionRow(
                                id = it,
                                title = it.substringAfter('/'),
                                group = "working",
                                need = "group_transition",
                                present = true,
                                agent = "claude",
                                stateSinceUnixMs = 0L,
                            ),
                        )
                    }.orEmpty(),
                    journalStale = false,
                ),
                refreshing = refreshing,
            )

        fun ids(screen: InboxScreen): List<String> =
            screen.sections.flatMap { it.rows }.map { it.id }

        val cache = InboxScreenCache()
        cache.remember(screen("mbp/old"))
        val failed = cache.fallback(screen(null, refreshing = true))
        assertEquals(listOf("mbp/old"), ids(failed))
        assertTrue(failed.refreshing)

        cache.remember(screen("mbp/new"))
        assertEquals(listOf("mbp/new"), ids(cache.fallback(screen(null))))
        cache.clear()
        assertTrue(ids(cache.fallback(screen(null))).isEmpty())
    }
}
