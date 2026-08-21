package dev.swarm.phone.ui.screens

import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * WAVE R8 / CLOSING ROUND 2 -- THE LAPSE RULE THE FALLBACK SCREEN RE-WATCHES ON
 * (closing review, finding 6, second pass).
 *
 * THE DEFECT. Round 4 gave `PhoneSurface.reconcileTerminalWatch` a lapse detector so a screen
 * whose watch the machine had reaped would RE-WATCH instead of renewing into nothing -- and wrote
 * it over the SNAPSHOT'S AGE alone. Round 3 had already made the machine BLANK the phone's copy
 * when it reaps, and that blank carries a ZERO rendered_at. The phone reads zero as UNKNOWN (a
 * machine that predates the closing round sends no render time, and calling its screens
 * rendered-just-now is the lie T4-b names), so `ageOf(0) = 0` and an age-only detector answers
 * FALSE for the one frame that PROVES the watch is over. Round 3's blank actively defeated round
 * 4's detector: the machine reaps, the blank lands, and the user sits on a permanently blank
 * terminal, re-watching never.
 *
 * THE RULE. The blank is distinguishable from a live screen by its GEOMETRY -- a live view
 * carries cols and rows, the blank carries none -- and that is a fact about what the machine
 * SENDS, not a guess. It is fenced on the machine side by
 * `internal/remotegw/r8r5_reapblank_test.go` (a real ServeRemote, a real Gateway, the real
 * TerminalWatcher) and through the phone's cache by
 * `internal/phonecore/r8r5_viewreset_test.go`. This is the third of those three: the rule that
 * READS those values.
 *
 * WHY IT IS NOT THE AGE. Stamping a render time on the blank would make it look FRESH, which is
 * the same disagreement with the sign flipped; and treating an unknown age as a lapse would
 * re-watch every tick against a pre-R8 machine forever, one unsigned append per tick per watched
 * session out of a budget shared with every other session's transcript.
 */
class TerminalFallbackWatchTest {

    private fun live(ageMs: Long) = TerminalGrid(
        rows = listOf("$ go test ./...", "ok  swarm/vt  1.4s"),
        gridRows = 24,
        ageMs = ageMs,
        streamStale = false,
    )

    /** Exactly what the machine's reap leaves in the phone's cache: no rows, no geometry, no age. */
    private val blanked = TerminalGrid(rows = emptyList(), gridRows = 0, ageMs = 0L, streamStale = false)

    @Test
    fun `the machine's blank is a lapsed watch`() {
        assertTrue(
            "the frame a REAPED watch leaves on the phone -- no rows, no geometry, no render time -- " +
                "must read as LAPSED. The age cannot say it: zero means the machine sent no render " +
                "time, so an age-only rule renews into a watch that no longer exists, forever.",
            TerminalFallbackBinding.watchLapsed(blanked),
        )
    }

    @Test
    fun `a live screen inside the horizon is not lapsed`() {
        assertFalse(
            "a screen the machine rendered seconds ago must NOT re-watch: a re-watch per redraw is " +
                "an unsigned append per redraw out of a shared budget.",
            TerminalFallbackBinding.watchLapsed(live(ageMs = 5_000L)),
        )
    }

    @Test
    fun `a live screen past the horizon is lapsed`() {
        assertTrue(
            "a screen older than the machine's watch horizon has been reaped on the other side; " +
                "renewing a reaped watch is a documented no-op, so the screen must re-establish it.",
            TerminalFallbackBinding.watchLapsed(live(ageMs = TerminalFallbackBinding.WATCH_HORIZON_MS)),
        )
    }

    @Test
    fun `a live screen whose machine sends no render time is not lapsed`() {
        assertFalse(
            "a machine that predates the closing round sends no rendered_at, and re-watching on an " +
                "UNKNOWN age would re-watch every tick against every such machine forever.",
            TerminalFallbackBinding.watchLapsed(live(ageMs = 0L)),
        )
    }

    @Test
    fun `the age rule alone still answers the age question`() {
        assertFalse(TerminalFallbackBinding.watchLapsed(ageMs = 0L))
        assertFalse(TerminalFallbackBinding.watchLapsed(ageMs = TerminalFallbackBinding.WATCH_HORIZON_MS - 1))
        assertTrue(TerminalFallbackBinding.watchLapsed(ageMs = TerminalFallbackBinding.WATCH_HORIZON_MS))
    }

    @Test
    fun `an unrouted session's empty grid is a lapse and not a screen to hold a watch for`() {
        assertTrue(
            "TerminalGrid.EMPTY is what the binding answers for a session the machine no longer " +
                "routes to the fallback. Holding a watch open for it is a machine rendering for a " +
                "screen that cannot show it.",
            TerminalFallbackBinding.watchLapsed(TerminalGrid.EMPTY),
        )
    }
}
