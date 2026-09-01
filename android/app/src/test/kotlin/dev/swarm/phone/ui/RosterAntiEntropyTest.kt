package dev.swarm.phone.ui

import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class RosterAntiEntropyTest {
    @Test
    fun `foreground waits for transport and dispatches once until authority advances`() {
        val sync = RosterAntiEntropy(timeoutMillis = 20_000L)
        sync.foreground()

        assertFalse(sync.observe(online = false, rosterRevision = 7L, nowMillis = 0L))
        assertTrue(sync.observe(online = true, rosterRevision = 7L, nowMillis = 1L))
        assertFalse("redraw duplicated the admitted sync", sync.observe(true, 7L, 2L))
        assertFalse("unrelated time duplicated the admitted sync", sync.observe(true, 7L, 19_999L))
        assertFalse("authority advance authored another sync", sync.observe(true, 8L, 20_000L))
        assertFalse("settled foreground trigger remained armed", sync.observe(true, 8L, 20_001L))
    }

    @Test
    fun `network recovery arms exactly one new sync`() {
        val sync = RosterAntiEntropy(timeoutMillis = 20_000L)

        assertFalse(sync.observe(online = false, rosterRevision = 3L, nowMillis = 0L))
        assertTrue(sync.observe(online = true, rosterRevision = 3L, nowMillis = 1L))
        assertFalse(sync.observe(online = true, rosterRevision = 3L, nowMillis = 2L))
        assertFalse(sync.observe(online = true, rosterRevision = 4L, nowMillis = 3L))
        assertFalse(sync.observe(online = false, rosterRevision = 4L, nowMillis = 4L))
        assertTrue(sync.observe(online = true, rosterRevision = 4L, nowMillis = 5L))
    }

    @Test
    fun `timeout and refusal do not create an automatic retry loop`() {
        val sync = RosterAntiEntropy(timeoutMillis = 20_000L)
        sync.foreground()
        assertTrue(sync.observe(online = true, rosterRevision = 2L, nowMillis = 0L))

        assertFalse("timeout immediately re-dispatched", sync.observe(true, 2L, 20_001L))
        assertFalse("redraw after timeout re-dispatched", sync.observe(true, 2L, 40_002L))

        sync.foreground()
        assertTrue("a later explicit lifecycle edge could not retry", sync.observe(true, 2L, 40_003L))
        sync.refused()
        assertFalse("a refusal immediately re-dispatched", sync.observe(true, 2L, 40_004L))
    }

    @Test
    fun `release cancels an unspent foreground generation`() {
        val sync = RosterAntiEntropy(timeoutMillis = 20_000L)
        sync.foreground()
        sync.release()

        assertFalse("a released surface dispatched its old foreground request", sync.observe(true, 0L, 1L))
        sync.foreground()
        assertTrue(sync.observe(true, 0L, 2L))
    }
}
