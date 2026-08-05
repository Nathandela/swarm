package dev.swarm.phone.ui

import dev.swarm.phone.keys.ConnectionState
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * FAILING-FIRST (TDD RED, GG-5) for agents-tracker-pxz8 -- **SYNCING is undetectable until an
 * action fails.**
 *
 * THE DEFECT. `StateSummary.Reconciled` crosses the boundary from `mobile/app.go:684` and, before
 * this fix, was read by no Kotlin at all -- `PhoneSurface.kt:793,879`, `SettingsSurface.kt:288`
 * and `PairingSurface.kt:788,801` read `.paired` and `.machine` off the same handle and stopped
 * there. So a phone sitting in PB-SYNC-7's fail-closed hold -- paired, online, reading fine, every
 * mutating op refused until the machine publishes its rollback authorities -- said nothing about
 * it. The user found out by pressing something, and the refusal read as THAT PRESS having failed
 * rather than as a state the phone was already in before the press.
 *
 * `mobile/error_taxonomy.tsv`'s row for `ErrClassUnreconciled` already promises "a banner and not
 * an error page"; this file is that promise reaching [StatusBanner], the model
 * `agents-tracker-e6mi` built for exactly this kind of proactive, always-on fact.
 *
 * WHY IT IS A FOURTH FACT AND NOT A REWORDING OF [ConnectionBanner]. Every state that type can
 * hold is either a healthy link or one this phone cannot use at all; the unreconciled hold is
 * neither -- reading works throughout, only writes are held -- so folding it into the transport's
 * banner would either hide a real condition behind "Connected to your machine." or invent a
 * connection problem that is not one. `StatusBannerTest` is the sibling suite for the three facts
 * this one joins; this file is scoped to the fourth alone.
 */
class StatusBannerSyncingTest {

    private fun banner(state: ConnectionState) = ConnectionBanner.of(state)

    private val taxonomyMessage = ErrorRouter.route(SwarmErrorTokens.SYNCING).message

    // ---- the taxonomy's own sentence, reused rather than re-typed -------------

    @Test
    fun `the syncing line is the taxonomy's own sentence for ErrClassUnreconciled`() {
        // mobile/error_taxonomy.tsv's copy for the row, pinned here so a rewording on either side
        // of the boundary is caught rather than silently drifting into two answers.
        assertEquals(
            "Waiting for your machine to publish its current state. Reading works throughout; " +
                "changes are held until it does.",
            taxonomyMessage,
        )
    }

    // ---- proactive: the hold shows before any press, not after one fails ------

    @Test
    fun `an unreconciled machine says so on a link that is otherwise perfectly healthy`() {
        // ONLINE contributes no line of its own (ConnectionUi's "the only quiet state"), so a
        // banner that stayed silent here is the exact defect this issue reports: a paired,
        // connected phone giving no sign that every mutating op it is sent will be refused.
        val model = StatusBanner.of(
            connection = banner(ConnectionState.ONLINE),
            freshness = null,
            staleNotice = "",
            reconciled = false,
        )

        assertEquals(taxonomyMessage, model.syncing)
        assertEquals(listOf(taxonomyMessage), model.lines)
        assertFalse("a phone with a hold to report is drawn as having nothing to say", model.silent)
    }

    @Test
    fun `a reconciled machine contributes no syncing line`() {
        val model = StatusBanner.of(
            connection = banner(ConnectionState.ONLINE),
            freshness = null,
            staleNotice = "",
            reconciled = true,
        )

        assertEquals("", model.syncing)
        assertEquals(emptyList<String>(), model.lines)
    }

    @Test
    fun `reconciled defaults to true, so every caller predating this fact stays silent on it`() {
        // StatusBannerTest builds `of(...)` without naming `reconciled` at all; a default of
        // `false` would put a permanent "waiting for your machine" line under every one of those
        // assertions and under this app's normal, fully-synced state besides.
        val model = StatusBanner.of(
            connection = banner(ConnectionState.ONLINE),
            freshness = null,
            staleNotice = "",
        )

        assertEquals("", model.syncing)
        assertTrue(model.silent)
    }

    // ---- distinct from connection state -----------------------------------

    @Test
    fun `the hold is its own line, never folded into the connection banner's text`() {
        val model = StatusBanner.of(
            connection = banner(ConnectionState.RECONNECTING),
            freshness = null,
            staleNotice = "",
            reconciled = false,
        )

        assertEquals(
            listOf(banner(ConnectionState.RECONNECTING).text, taxonomyMessage),
            model.lines,
        )
        for (line in model.lines) {
            assertEquals(
                "\"$line\" carries more than one fact, so the hold has been run on with the " +
                    "connection banner's own sentence",
                1,
                model.lines.count { line.contains(it) },
            )
        }
    }

    @Test
    fun `the syncing line sits second, after the link and before the machine's clock`() {
        val silentMachine = MachineFreshness(silent = true, lastHeardUnixMs = 0L)
        val model = StatusBanner.of(
            connection = banner(ConnectionState.RECONNECTING),
            freshness = silentMachine.notice { "09:14" },
            staleNotice = TriageInbox.from(emptyList(), journalStale = true).staleNotice,
            reconciled = false,
        )

        assertEquals(
            listOf(
                banner(ConnectionState.RECONNECTING).text,
                taxonomyMessage,
                silentMachine.notice { "09:14" },
                TriageInbox.from(emptyList(), journalStale = true).staleNotice,
            ),
            model.lines,
        )
    }

    // ---- a transport that has stopped retrying silences it too -------------

    @Test
    fun `a terminal link silences the hold the same way it silences freshness and staleness`() {
        // `RELAY_UNTRUSTED` is terminal and still paired (agents-tracker-agre): the transport has
        // stopped retrying, so a promise that resolves only once the machine is heard from again
        // is exactly as false as the freshness and staleness lines this same branch already
        // suppresses.
        val model = StatusBanner.of(
            connection = banner(ConnectionState.RELAY_UNTRUSTED),
            freshness = MachineFreshness(silent = true, lastHeardUnixMs = 0L).notice { "09:14" },
            staleNotice = TriageInbox.from(emptyList(), journalStale = true).staleNotice,
            reconciled = false,
        )

        assertEquals("", model.syncing)
        assertEquals(listOf(banner(ConnectionState.RELAY_UNTRUSTED).text), model.lines)
    }

    // ---- the empty banner carries the new fact too --------------------------

    @Test
    fun `the empty banner is silent on the hold as well`() {
        assertEquals("", StatusBanner.NONE.syncing)
        assertTrue(StatusBanner.NONE.silent)
    }
}
