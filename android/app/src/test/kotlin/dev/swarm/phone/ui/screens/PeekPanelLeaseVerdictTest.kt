package dev.swarm.phone.ui.screens

import dev.swarm.phone.ui.CommandVerdict
import dev.swarm.phone.ui.ControlLease
import dev.swarm.phone.ui.OperationOutcome
import dev.swarm.phone.ui.TerminalPeek
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotEquals
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * FAILING-FIRST (TDD RED, GG-5) for agents-tracker-qlf9's second verb: what the peek says when the
 * MACHINE refused control, rather than what it says when nobody has asked yet.
 *
 * **THE SENTENCE THAT IS WRONG TODAY.** `ControlLease.confirmedBy` reduces the whole reply to
 * `code == "lease"`, so a kill switch, a revoked device, a policy refusal and a phone that has
 * never pressed the button all produce the same `false` -- and the screen says "Your machine has
 * not confirmed control of this session ... Take control first." To a user who pressed Take
 * control and was refused, that reads as though the press never happened, and the remedy it names
 * is the very step that was just declined. `Outcome.Message` -- the machine's own words about why
 * -- is discarded on the way.
 *
 * **A SEVERANCE IS NOT A REFUSAL AND MUST NOT READ AS ONE.** internal/remotegw/lease_sever.go
 * seals the lease-death notice under the take_control's own operation id, so an ordinary session
 * that ends leaves a `detach` sitting on the very outcome this screen reads. PB-INPUT-2's
 * "visibly" wants that said; saying it as a refusal would accuse the machine of something it did
 * not do.
 */
class PeekPanelLeaseVerdictTest {

    private val id = "op-take-control-1"

    private fun peek(leaseHeld: Boolean = false, online: Boolean = true) = TerminalPeek(
        sessionId = "mbp/quanthome",
        text = "$ go test ./...",
        cols = 80,
        rows = 24,
        stale = false,
        leaseHeld = leaseHeld,
        online = online,
    )

    private fun verdict(code: String, message: String = "") = ControlLease.verdictOf(
        OperationOutcome(operationId = id, code = code, message = message),
        id,
    )

    @Test
    fun `a refused take control says the machine refused it, in the machine's own words`() {
        val notice = PeekPanelScreen.of(
            peek(leaseHeld = false),
            lease = verdict("kill_switch", "remote control is disabled (kill switch off)"),
        ).leaseNotice

        assertTrue(
            "the peek dropped the machine's reason for refusing control, so the user is left to " +
                "guess between a kill switch, a revoked device and a policy -- three different " +
                "remedies, none of which the screen names",
            notice.contains("remote control is disabled (kill switch off)"),
        )
        assertFalse(
            "a REFUSED take control still reads as one nobody has asked for, which tells a user " +
                "who has just pressed the button that they have not pressed it",
            notice == PeekPanelScreen.leaseNoticeFor(confirmed = false),
        )
        assertFalse(
            "the screen tells a user whose take control was refused to take control",
            notice.contains("Take control first"),
        )
    }

    @Test
    fun `a severed lease is reported as ended and not as a refusal`() {
        val ended = PeekPanelScreen.of(
            peek(leaseHeld = false),
            lease = verdict("detach", "control was released"),
        ).leaseNotice
        val refused = PeekPanelScreen.of(
            peek(leaseHeld = false),
            lease = verdict("not_authorized", "control was released"),
        ).leaseNotice

        assertTrue(
            "PB-INPUT-2: a lease that died is not visibly reported at all, so the phone types " +
                "into a void with the keyboard shut and no sentence saying why",
            ended.contains("control was released"),
        )
        assertNotEquals(
            "a lease that ended normally reads exactly like one the machine refused, which " +
                "accuses the machine of something it did not do",
            refused,
            ended,
        )
    }

    /**
     * The two states that were already right stay right. A screen whose refusal wording swallowed
     * the unasked case would have replaced one misleading sentence with another.
     */
    @Test
    fun `an unasked and a granted lease keep the two sentences PB-INPUT-2 already had`() {
        assertEquals(
            PeekPanelScreen.leaseNoticeFor(confirmed = false),
            PeekPanelScreen.of(peek(leaseHeld = false), lease = CommandVerdict.UNANSWERED).leaseNotice,
        )
        assertEquals(
            PeekPanelScreen.leaseNoticeFor(confirmed = true),
            PeekPanelScreen.of(peek(leaseHeld = true), lease = verdict("lease", "")).leaseNotice,
        )
    }

    /**
     * THE PEEK IS THE AUTHORITY ON WHETHER A LEASE IS HELD, not the verdict passed beside it. The
     * surface derives one from the other, so the two cannot disagree in production -- and where
     * they are made to, the screen must not claim a lease the peek says is not there.
     */
    @Test
    fun `a granted verdict never claims a lease the peek does not show`() {
        val notice = PeekPanelScreen.of(peek(leaseHeld = false), lease = verdict("lease", "")).leaseNotice

        assertNotEquals(
            "the screen announced a confirmed lease over a peek whose keyboard is shut",
            PeekPanelScreen.leaseNoticeFor(confirmed = true),
            notice,
        )
    }

    /**
     * `ControlLease` keeps ONE statement of what a granted lease looks like. A second reading of
     * protocol.OpLease -- one for the boolean, one for the verdict -- is the divergence this whole
     * issue is about, arriving one level down.
     */
    @Test
    fun `the lease boolean and the lease verdict are the same reading`() {
        for (code in listOf("lease", "detach", "kill_switch", "not_authorized", "")) {
            val outcome = OperationOutcome(operationId = id, code = code, message = "")
            assertEquals(
                "ControlLease.confirmedBy and ControlLease.verdictOf disagree about `$code`",
                ControlLease.confirmedBy(outcome),
                ControlLease.verdictOf(outcome, id).accepted,
            )
        }
    }
}
