package dev.swarm.phone.ui

import dev.swarm.phone.keys.ConnectionState
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * FAILING-FIRST (TDD RED, GG-5) for agents-tracker-nx44.2 -- ONE composed sync status, where three
 * stacked sentences used to be.
 *
 * ## The defect the owner photographed
 *
 * [StatusBanner] rendered up to four facts as four stacked lines of `--p-ink2` prose above every
 * destination: the transport's opinion, PB-SYNC-7's write-hold, the machine's own clock and the
 * roster's completeness. Field test 3 shows what that costs on a real handset. Three sentences of
 * transparent body copy sit where the screen's title should be, they push the whole app down, and
 * they say four things when the user needs one -- *is what I am looking at current, and if not, what
 * do I do*. Worse, the machine's line read `Not heard from your machine since 14:57.` with no date
 * on it: at 09:00 the next morning that is indistinguishable from a machine heard from three minutes
 * ago, so the ONE fact the whole banner exists to carry was unreadable exactly when it mattered.
 *
 * ## What replaces it, and why it is a MODEL first
 *
 * A single ranked status -- broken > quiet > syncing > live -- and the rank is the substance. The
 * four facts do not compose into a paragraph; they compose into a PRIORITY, because a phone whose
 * transport has stopped retrying does not also need to be told its journal has a hole. Live renders
 * NOTHING AT ALL, which is the discipline `ConnectionBanner` has stated since S16 ("online is the
 * only quiet state") and which the banner stack broke the moment two facts were true at once.
 *
 * The interesting behaviour is the ranking, the duration and the sheet's contents -- a mapping from
 * what the phone knows onto what a person reads -- which needs no Robolectric to state. The
 * composition (a pill in the nav row, a strip above it when broken, a sheet on tap) is
 * `SyncStatusViewTest`'s.
 *
 * ## The duration is the whole of the machine's line now
 *
 * `MachineFreshness.notice` formats `lastHeardUnixMs` through an Android time formatter, which
 * yields a BARE CLOCK TIME. This model never does: an elapsed duration is monotonic in the one
 * direction a reader cares about and cannot be mistaken for a fresh reading a day later.
 */
class SyncStatusTest {

    private fun connection(state: ConnectionState) = ConnectionBanner.of(state)

    private val nowMs = 1_754_000_000_000L

    private fun heardAgo(millis: Long) =
        MachineFreshness(silent = true, lastHeardUnixMs = nowMs - millis)

    private val freshMachine = MachineFreshness(silent = false, lastHeardUnixMs = nowMs - HOUR)

    private val neverHeard = MachineFreshness(silent = true, lastHeardUnixMs = 0L)

    /**
     * The four repair channels, in `FacadeBridge.REPAIR_CHANNELS` order.
     *
     * SPELLED HERE rather than read off the adapter, which was `LinkPanelTest`'s arrangement too
     * (that suite retired with its subject in agents-tracker-nx44.3):
     * touching `FacadeBridge` initialises a class whose fields are gomobile types, and the AAR is
     * not on this JVM. `android/gate/pbapp8_repairchannels_test.go` is what joins the two lists.
     */
    private val channelNames = listOf("journal", "terminal", "reply", "grant")

    /** All four channels whole -- `FacadeBridge.streamViews()` over a healthy core. */
    private fun wholeStreams() = channelNames.map {
        StreamView(stream = it, stale = false, resyncPending = false)
    }

    /** The same four with [holed] carrying a gap. */
    private fun streamsHoledAt(holed: String) = channelNames.map {
        StreamView(stream = it, stale = it == holed, resyncPending = false)
    }

    private fun status(
        state: ConnectionState = ConnectionState.ONLINE,
        freshness: MachineFreshness = freshMachine,
        streams: List<StreamView> = wholeStreams(),
        reconciled: Boolean = true,
    ) = SyncStatus.of(
        connection = connection(state),
        freshness = freshness,
        nowUnixMs = nowMs,
        streams = streams,
        reconciled = reconciled,
    )

    // ---- live says nothing at all ------------------------------------------

    @Test
    fun `a healthy phone renders no status at all`() {
        val model = status()

        assertEquals(SyncState.LIVE, model.state)
        assertEquals("a live phone carries a pill in its nav row", "", model.pill)
        assertEquals("a live phone carries a strip above its nav row", "", model.strip)
        assertTrue("a phone with nothing to report does not report itself silent", model.silent)
    }

    @Test
    fun `the healthy state is the only silent one`() {
        assertFalse(status(reconciled = false).silent)
        assertFalse(status(freshness = heardAgo(18 * HOUR)).silent)
        assertFalse(status(state = ConnectionState.RELAY_UNTRUSTED).silent)
    }

    // ---- the rank ----------------------------------------------------------

    @Test
    fun `broken outranks every other fact being true at the same time`() {
        // A transport that has STOPPED RETRYING is the only fact left worth acting on: the machine
        // is silent and the journal is holed BECAUSE the link is dead, and three marks for one
        // cause is the stacked banner again wearing colours.
        val model = status(
            state = ConnectionState.RELAY_UNTRUSTED,
            freshness = heardAgo(18 * HOUR),
            streams = streamsHoledAt("journal"),
            reconciled = false,
        )

        assertEquals(SyncState.BROKEN, model.state)
        assertEquals(SyncStatus.BROKEN, model.pill)
    }

    @Test
    fun `quiet outranks syncing`() {
        // ADR-007 D9's adversary answers every poll and withholds the frames, so the link reads
        // healthy while the machine has not spoken for hours. That the phone is also mid-repair is
        // the smaller fact: a repair cannot land from a machine that is not talking.
        val model = status(
            freshness = heardAgo(18 * HOUR),
            streams = streamsHoledAt("journal"),
            reconciled = false,
        )

        assertEquals(SyncState.QUIET, model.state)
    }

    @Test
    fun `syncing outranks live`() {
        assertEquals(SyncState.SYNCING, status(reconciled = false).state)
        assertEquals(SyncState.SYNCING, status(streams = streamsHoledAt("terminal")).state)
        assertEquals(SyncState.SYNCING, status(state = ConnectionState.RECONNECTING).state)
    }

    @Test
    fun `a link the app is still working on is syncing and never broken`() {
        // OFFLINE, CONNECTING and RECONNECTING all come back on their own. Marking them BROKEN
        // would send a user through a destructive re-pair for a lift ride.
        for (state in listOf(
            ConnectionState.OFFLINE,
            ConnectionState.CONNECTING,
            ConnectionState.RECONNECTING,
        )) {
            assertEquals("$state is not a link the user has to repair", SyncState.SYNCING, status(state = state).state)
        }
    }

    @Test
    fun `every terminal state is broken and no other state is`() {
        for (state in ConnectionState.entries) {
            val model = status(state = state)
            if (state.isTerminal) {
                assertEquals("$state has stopped retrying and is not marked broken", SyncState.BROKEN, model.state)
            } else {
                assertFalse("$state is marked broken and the app is still retrying it", model.state == SyncState.BROKEN)
            }
        }
    }

    // ---- the pill's words --------------------------------------------------

    @Test
    fun `the quiet pill carries an elapsed duration and never a clock time`() {
        assertEquals("QUIET 18h", status(freshness = heardAgo(18 * HOUR)).pill)
        assertEquals("QUIET 4m", status(freshness = heardAgo(4 * MINUTE)).pill)
        assertEquals("QUIET 3d", status(freshness = heardAgo(3 * DAY)).pill)
    }

    @Test
    fun `a machine never heard from carries the word alone`() {
        // Zero is "this phone has never taken a frame" -- a first launch, or a restore. There is no
        // elapsed time to state, and `0h` would claim the machine spoke at the epoch.
        assertEquals(SyncStatus.QUIET, status(freshness = neverHeard).pill)
    }

    @Test
    fun `the syncing and broken pills are one word each`() {
        assertEquals("SYNCING", status(reconciled = false).pill)
        assertEquals("BROKEN", status(state = ConnectionState.REVOKED).pill)
    }

    // ---- the strip escalates, and only for broken --------------------------

    @Test
    fun `only broken escalates to the strip, and it carries the transport's own sentence`() {
        assertEquals(
            connection(ConnectionState.RELAY_INSECURE).text,
            status(state = ConnectionState.RELAY_INSECURE).strip,
        )
        assertEquals("a quiet machine escalated to the strip", "", status(freshness = heardAgo(HOUR)).strip)
        assertEquals("a syncing phone escalated to the strip", "", status(reconciled = false).strip)
        assertEquals("", status().strip)
    }

    // ---- the sheet: three labelled rows, the gaps, the repair ---------------

    @Test
    fun `the sheet labels the three facts the banner used to stack`() {
        val detail = status(freshness = heardAgo(18 * HOUR)).detail

        assertEquals(
            listOf(SyncStatus.HEARD, SyncStatus.READING, SyncStatus.VIEWS),
            detail.rows.map { it.label },
        )
    }

    @Test
    fun `the heard row is an elapsed duration and the never case says so in words`() {
        assertEquals("18h ago", status(freshness = heardAgo(18 * HOUR)).detail.rows[0].value)
        assertEquals(SyncStatus.NEVER, status(freshness = neverHeard).detail.rows[0].value)
    }

    @Test
    fun `the reading row is the transport's own sentence, including when it is healthy`() {
        // The sheet is a READOUT and not a fault report: it is opened deliberately, and hiding the
        // healthy rows makes "all three are fine" indistinguishable from "the sheet forgot one" --
        // which was `LinkPanel`'s own ruling about its four channels, and is this sheet's now that
        // the section it ruled over has retired into the settings CONNECTION summary.
        assertEquals(connection(ConnectionState.ONLINE).text, status().detail.rows[1].value)
        assertEquals(
            connection(ConnectionState.RECONNECTING).text,
            status(state = ConnectionState.RECONNECTING).detail.rows[1].value,
        )
    }

    @Test
    fun `the views row counts the channels that are current`() {
        assertEquals("4 of 4 current", status().detail.rows[2].value)
        assertEquals("3 of 4 current", status(streams = streamsHoledAt("journal")).detail.rows[2].value)
    }

    @Test
    fun `the gaps are named per channel, in the model's own words, and a whole roster names none`() {
        val holed = StreamView(stream = "journal", stale = true, resyncPending = false)

        assertEquals(listOf(holed.notice), status(streams = streamsHoledAt("journal")).detail.gaps)
        assertEquals(emptyList<String>(), status().detail.gaps)
    }

    @Test
    fun `a repair in flight is still a gap and still named`() {
        // PB-SYNC-3's optimistic clear: a repair that has been ASKED FOR has not landed, and a
        // channel drawn as whole while its hole is being worked on is the one thing PB-APP-8
        // forbids.
        val repairing = channelNames.map {
            StreamView(stream = it, stale = it == "journal", resyncPending = it == "journal")
        }

        assertEquals(1, status(streams = repairing).detail.gaps.size)
        assertEquals("3 of 4 current", status(streams = repairing).detail.rows[2].value)
    }

    // ---- the repair action -------------------------------------------------

    @Test
    fun `a gap offers the resync verb`() {
        assertEquals(SyncStatus.REPAIR, status(streams = streamsHoledAt("journal")).detail.repair)
    }

    @Test
    fun `a revoked device offers the pairing destination and not a resync`() {
        // PB-STATE-10: `swarm remote pair` is refused while the machine still holds a registration,
        // so the offer is the Settings destination whose leading section clears it. A resync here
        // would repair a transport the machine has stopped answering on.
        assertEquals(SyncStatus.PAIR_AGAIN, status(state = ConnectionState.REVOKED).detail.repair)
    }

    @Test
    fun `pairing outranks the resync when both would apply`() {
        assertEquals(
            SyncStatus.PAIR_AGAIN,
            status(
                state = ConnectionState.RELAY_UNTRUSTED,
                streams = streamsHoledAt("journal"),
            ).detail.repair,
        )
    }

    @Test
    fun `a healthy phone offers no repair`() {
        assertEquals("", status().detail.repair)
        assertEquals("a dropped link offers a repair; the remedy is to wait", "", status(state = ConnectionState.RECONNECTING).detail.repair)
    }

    // ---- the empty status --------------------------------------------------

    @Test
    fun `the empty status is silent and its sheet has nothing in it`() {
        assertTrue(SyncStatus.NONE.silent)
        assertEquals("", SyncStatus.NONE.pill)
        assertEquals("", SyncStatus.NONE.strip)
        assertEquals(emptyList<SyncRow>(), SyncStatus.NONE.detail.rows)
        assertEquals("", SyncStatus.NONE.detail.repair)
    }

    private companion object {
        const val MINUTE = 60_000L
        const val HOUR = 60 * MINUTE
        const val DAY = 24 * HOUR
    }
}
