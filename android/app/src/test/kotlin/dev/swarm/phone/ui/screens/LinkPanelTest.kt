package dev.swarm.phone.ui.screens

import dev.swarm.phone.ui.ClockBanner
import dev.swarm.phone.ui.StreamView
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * FAILING-FIRST (TDD RED, GG-5) for PB-DS-9, PB-APP-8 and PB-TIME-1 over the LINK section's model.
 *
 * **THE DEFECT THIS SUITE CLOSES IS THAT THERE WAS NO SCREEN.** `ClockBanner` and `StreamView`
 * were modelled, unit-tested in `ConnectionAndErrorTest`, reached by `FacadeBridge`, and rendered
 * by nothing (agents-tracker-ah2). Every assertion in this file is therefore about a fact reaching
 * a panel, not about the models themselves -- `ConnectionAndErrorTest` owns what `ClockBanner.of`
 * and `StreamView.badge` decide, and a second opinion here could disagree with the first.
 *
 * WHAT IT GUARDS AGAINST, in one sentence each:
 *
 *  - A clock notice on a phone whose clock is fine. That is a warning over a working system, and
 *    the reader who learns to skip it is the reader who skips the real one.
 *  - A channel with a known hole in it carrying the liveness word anyway. That is PB-SYNC-3's
 *    optimistic clear -- a known hole shown as live -- arriving through the screen instead of
 *    through the facade.
 *  - A live channel carrying nothing, so that "all four are fine" and "this screen forgot the
 *    reply channel" render identically.
 */
class LinkPanelTest {

    private val healthyClock = ClockBanner.of("")
    private val skewedClock = ClockBanner.of("this device's clock is 2m0s ahead of the machine")

    /** The four repair channels, in `FacadeBridge.REPAIR_CHANNELS` order. */
    private val channelNames = listOf("journal", "terminal", "reply", "grant")

    private fun streams(
        stale: Set<String> = emptySet(),
        repairing: Set<String> = emptySet(),
    ): List<StreamView> = channelNames.map { name ->
        StreamView(
            stream = name,
            stale = name in stale || name in repairing,
            resyncPending = name in repairing,
        )
    }

    private fun panel(
        clock: ClockBanner = healthyClock,
        stale: Set<String> = emptySet(),
        repairing: Set<String> = emptySet(),
    ) = LinkPanelScreen.of(clock, streams(stale, repairing))

    private fun LinkPanel.channel(stream: String): ChannelRow =
        channels.single { it.stream == stream }

    // ---- PB-TIME-1: the clock verdict ------------------------------------------

    /**
     * An EMPTY verdict is a healthy clock, and the section must treat it as one.
     *
     * `App.ClockVerdict` returns "" while the clock is inside budget, and `ClockBanner.of` already
     * rules that this is health rather than ignorance. The plausible wrong answer is a permanent
     * line -- "clock OK", or a blank notice view occupying its own line -- which is a warning over
     * a working system. `ActivityPanelTest` holds the same discipline for the stale journal notice.
     */
    @Test
    fun `a clock inside its budget produces no notice at all`() {
        assertEquals(
            "the link section says something about a clock that is fine. An empty verdict is a " +
                "HEALTHY clock, and a notice over it is the unconditional warning that teaches a " +
                "reader to skip the real one",
            "",
            panel(clock = healthyClock).clockNotice,
        )
    }

    /**
     * The verdict is the DAEMON's sentence and crosses unaltered.
     *
     * PB-TIME-1 computes the skew and words it; a screen re-wording it would be a second copy of a
     * measurement's meaning on the handset, and the two would disagree the first time the budget
     * moved. It must also not be routed as an error: the daemon's refusal of a skewed command
     * reads "not authorized", which sends a user to pair again when the fix is their clock.
     */
    @Test
    fun `a skewed clock carries the verdict verbatim`() {
        assertEquals(skewedClock.text, panel(clock = skewedClock).clockNotice)
        assertTrue(
            "the notice does not name the clock, so a user is told something is wrong and not " +
                "what they can do about it -- which is the opaque refusal PB-TIME-1 removes",
            panel(clock = skewedClock).clockNotice.contains("clock"),
        )
    }

    // ---- PB-APP-8: one row per channel, always ---------------------------------

    /**
     * All four, in the order they were asked about.
     *
     * The obvious implementation renders only the unhealthy ones, and it is wrong for the reason
     * PB-DS-9 gives about empty sections: a screen that drops what has nothing to say makes "all
     * four are fine" indistinguishable from "this screen forgot the reply channel".
     */
    @Test
    fun `every channel gets a row, healthy ones included, in the order asked`() {
        assertEquals(channelNames, panel().channels.map { it.stream })
        assertEquals(
            "a channel disappeared once it went stale, or once it stopped being stale",
            channelNames,
            panel(stale = setOf("terminal"), repairing = setOf("reply")).channels.map { it.stream },
        )
    }

    /**
     * The channel is named as the WIRE names it.
     *
     * `journal`, `terminal`, `reply` and `grant` are `internal/phonecore`'s own strings.
     * `ActivityEntry`'s rule applies: a table turning them into English would have to invent a
     * phrase for a fifth channel it did not know, and a screen that renders what arrived cannot
     * become a lie.
     */
    @Test
    fun `each row is named by its wire token and not by an invented phrase`() {
        panel().channels.forEach { row ->
            assertTrue(
                "`${row.stream}` is not one of the core's repair channels, so this row is named " +
                    "by something the screen made up",
                row.stream in channelNames,
            )
        }
    }

    // ---- a live channel, and only a live channel, makes the liveness claim ------

    @Test
    fun `a channel with no hole in it carries the liveness word and no notice`() {
        val live = panel().channel("journal")

        assertNotNull(
            "a healthy channel says nothing at all, so a section with four healthy channels and " +
                "a section that failed to read them render the same",
            live.liveLabel,
        )
        assertNull(
            "a healthy channel carries a notice. There is no hole to describe, and a sentence " +
                "here is the unconditional warning this screen exists not to have",
            live.notice,
        )
    }

    /**
     * THE ASSERTION THIS SUITE EXISTS FOR.
     *
     * `statusLabel` -- what [ChannelRow.liveLabel] is rendered by -- is `--p-hero`, and derivation
     * row 15 spells out that hero is this skin's LIVENESS claim. A stale channel carrying it would
     * be a known hole painted with the colour that means alive, which is precisely what PB-APP-8
     * forbids and what PB-SYNC-3 refuses to let the facade do. The screen must not undo at the
     * last hop what the whole sync layer is built to preserve.
     */
    @Test
    fun `a stale channel never carries the liveness word, and says what is missing`() {
        val holed = panel(stale = setOf("terminal")).channel("terminal")

        assertNull(
            "a channel with a known hole in it is labelled live. Everything from `StreamState` " +
                "answering \"stale\" through a repair to `ResyncPending` being kept orthogonal " +
                "exists to stop exactly this, and the screen would be undoing it at the last hop",
            holed.liveLabel,
        )
        assertNotNull("a stale channel says nothing about its hole", holed.notice)
        assertTrue(
            "the notice does not say the view may be MISSING something. `stale` on its own leaves " +
                "a reader to guess whether what they see is old or incomplete",
            holed.notice.orEmpty().contains("missing") || holed.notice.orEmpty().contains("gap"),
        )
        assertTrue(
            "the notice does not name the channel it is about, so a user with one holed stream " +
                "and three whole ones cannot tell which is which -- which is the global stale " +
                "mark PB-APP-8 refuses, rebuilt out of four unlabelled sentences",
            holed.notice.orEmpty().contains("terminal"),
        )
    }

    /**
     * A repair in flight is a THIRD row state and NOT a cleared stale mark.
     *
     * `App.Resync` does not clear staleness; the mark clears when the repair LANDS. So a repairing
     * channel is still a channel with a hole, and it must still not carry the liveness word -- the
     * user is told a repair is happening, not that the hole is gone.
     */
    @Test
    fun `a repairing channel is still not live, and says a repair is in flight`() {
        val repairing = panel(repairing = setOf("journal")).channel("journal")

        assertNull(
            "a channel repairing a hole is labelled live. That is PB-SYNC-3's optimistic clear: " +
                "the request was made, the gap is still there, and the user has been told the " +
                "opposite",
            repairing.liveLabel,
        )
        assertTrue(
            "a repairing channel reads exactly like an idle stale one, so the user cannot tell " +
                "whether anything is being done about it",
            repairing.notice.orEmpty() != panel(stale = setOf("journal")).channel("journal").notice,
        )
        assertTrue(
            "the repairing notice does not say a repair is under way",
            repairing.notice.orEmpty().lowercase().contains("repair"),
        )
    }

    /**
     * The two slots are mutually exclusive BY CONSTRUCTION, over every state a channel can be in.
     *
     * A row carrying both would be a channel described as live and holed at once, and the row that
     * carries neither is a channel the section renders as a blank line.
     */
    @Test
    fun `every row fills exactly one of its two slots`() {
        val mixed = panel(stale = setOf("terminal"), repairing = setOf("reply"))

        mixed.channels.forEach { row ->
            val filled = listOfNotNull(row.liveLabel, row.notice).size
            assertEquals(
                "`${row.stream}` fills $filled of its two slots. One means the row says one true " +
                    "thing; two means it claims to be live and holed at once, and zero means the " +
                    "channel renders as a blank line",
                1,
                filled,
            )
        }
    }

    // ---- the section itself -----------------------------------------------------

    @Test
    fun `the section has a heading, and it is not the transport's word`() {
        val heading = panel().heading

        assertTrue("the link section has no heading", heading.isNotEmpty())
        assertFalse(
            "the heading calls this the CONNECTION. That is `ConnectionBanner`'s subject -- the " +
                "socket and the polls -- and this section exists to be the other fact: a phone " +
                "can read `Connected to your machine.` with four stale channels",
            heading.contains("Connect", ignoreCase = true),
        )
    }

    /** A phone with a skewed clock AND holed streams reports both, not whichever came first. */
    @Test
    fun `a skewed clock and a holed channel are both reported`() {
        val both = panel(clock = skewedClock, stale = setOf("grant"))

        assertTrue(both.clockNotice.isNotEmpty())
        assertNull(both.channel("grant").liveLabel)
        assertNotNull(both.channel("journal").liveLabel)
    }
}
