package dev.swarm.phone.ui

import dev.swarm.phone.keys.ConnectionState
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * FAILING-FIRST (TDD RED, GG-5) for agents-tracker-e6mi -- PB-APP-8 and PB-APP-11's persistent
 * half, as a MODEL rather than as a string built at a call site.
 *
 * THE DEFECT THIS FILE IS ABOUT IS A `joinToString(" ")`. `PhoneSurface.renderReady` concatenated
 * the connection banner, the machine's freshness verdict and the roster's stale notice into one
 * line, so a user whose link had dropped while their machine was also silent read:
 *
 *	Lost the link to your machine; reconnecting. Not heard from your machine since 09:14. This
 *	list may be incomplete: some of your machine's activity has not arrived yet.
 *
 * as a single run-on paragraph. They are THREE DIFFERENT FACTS from three different authorities --
 * the transport's opinion, the machine's own clock, and the journal stream's completeness
 * (`ConnectionUi.kt` argues at length that the second is not the first) -- and a reader cannot tell
 * where one ends and the next begins. The join is also what made "hide it when there is nothing to
 * say" un-expressible: an empty middle fact left two sentences with two spaces between them.
 *
 * SO THE MODEL IS THE THREE, SEPARATELY, and [StatusBanner.lines] is the only place the order and
 * the emptiness rule live. A view that assembled its own lines could disagree with this, which is
 * how the run-on happened the first time.
 *
 * WHY IT IS A MODEL AND NOT A VIEW TEST. The interesting behaviour is a mapping from what the
 * phone knows onto what a person reads, which is `TriageInbox`'s own argument for existing, and it
 * needs no Robolectric to state. The composition -- one line per fact, above the tab scaffold's
 * content -- is `ScaffoldBannerTest`'s.
 */
class StatusBannerTest {

    private fun banner(state: ConnectionState) = ConnectionBanner.of(state)

    private val silentMachine = MachineFreshness(silent = true, lastHeardUnixMs = 0L)

    private val liveMachine = MachineFreshness(silent = false, lastHeardUnixMs = 0L)

    /** The machine's verdict as the surface renders it: a formatter it owns, or null. */
    private fun verdict(freshness: MachineFreshness) = freshness.notice { "09:14" }

    // ---- the three are three ----------------------------------------------

    @Test
    fun `each fact is its own line, in the order a reader meets them`() {
        val model = StatusBanner.of(
            connection = banner(ConnectionState.RECONNECTING),
            freshness = verdict(silentMachine),
            staleNotice = TriageInbox.from(emptyList(), journalStale = true).staleNotice,
        )

        assertEquals(
            "the three facts are not three lines. They came from three different authorities -- " +
                "the transport, the machine's own clock and the journal stream -- and a reader " +
                "handed them as one sentence cannot tell which is which",
            listOf(
                banner(ConnectionState.RECONNECTING).text,
                verdict(silentMachine),
                TriageInbox.from(emptyList(), journalStale = true).staleNotice,
            ),
            model.lines,
        )
    }

    @Test
    fun `no line is a run-on of another`() {
        val model = StatusBanner.of(
            connection = banner(ConnectionState.OFFLINE),
            freshness = verdict(silentMachine),
            staleNotice = TriageInbox.from(emptyList(), journalStale = true).staleNotice,
        )

        for (line in model.lines) {
            assertEquals(
                "\"$line\" carries more than one of the three facts, so the join that produced " +
                    "the run-on paragraph is still there under a different name",
                1,
                model.lines.count { line.contains(it) },
            )
        }
    }

    // ---- a fact with nothing to say says nothing --------------------------

    @Test
    fun `an online link contributes no line at all`() {
        // ConnectionUi's own doctrine: "ONLINE IS THE ONLY QUIET STATE". `ConnectionBanner.visible`
        // has said so since S16 and nothing read it -- the surface spent `.text` unconditionally,
        // so every phone with a working link carried a permanent "Connected to your machine."
        // where its warnings go. A banner that is always up is a banner nobody reads.
        val model = StatusBanner.of(
            connection = banner(ConnectionState.ONLINE),
            freshness = null,
            staleNotice = "",
        )

        assertEquals("", model.connection)
        assertEquals(emptyList<String>(), model.lines)
        assertTrue("a banner with nothing to say does not report itself silent", model.silent)
    }

    @Test
    fun `a machine inside its budget contributes no line`() {
        val model = StatusBanner.of(
            connection = banner(ConnectionState.OFFLINE),
            freshness = verdict(liveMachine),
            staleNotice = "",
        )

        assertEquals("", model.freshness)
        assertEquals(listOf(banner(ConnectionState.OFFLINE).text), model.lines)
    }

    @Test
    fun `a whole roster contributes no line`() {
        val model = StatusBanner.of(
            connection = banner(ConnectionState.ONLINE),
            freshness = verdict(silentMachine),
            staleNotice = TriageInbox.from(emptyList(), journalStale = false).staleNotice,
        )

        assertEquals("", model.stale)
        assertEquals(listOf(verdict(silentMachine)), model.lines)
        assertFalse("a banner carrying a verdict reports itself silent", model.silent)
    }

    @Test
    fun `the roster's verdict survives a link that is perfectly healthy`() {
        // The adversary ADR-007 D9 declares does not break the connection: it answers every poll
        // and withholds the newest frames. So "online" and "this list may be incomplete" are the
        // NORMAL pairing rather than a contradiction, and a banner that showed only the transport's
        // opinion would render exactly what that adversary wants.
        val model = StatusBanner.of(
            connection = banner(ConnectionState.ONLINE),
            freshness = verdict(silentMachine),
            staleNotice = TriageInbox.from(emptyList(), journalStale = true).staleNotice,
        )

        assertEquals(2, model.lines.size)
        assertTrue(model.lines.contains(TriageInbox.from(emptyList(), journalStale = true).staleNotice))
    }

    @Test
    fun `the empty banner is silent and has no lines`() {
        assertTrue(StatusBanner.NONE.silent)
        assertEquals(emptyList<String>(), StatusBanner.NONE.lines)
    }
}
