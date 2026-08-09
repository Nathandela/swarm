package dev.swarm.phone.ui.screens

import dev.swarm.phone.ui.CommandVerdict
import dev.swarm.phone.ui.ControlLease
import dev.swarm.phone.ui.OperationOutcome
import dev.swarm.phone.ui.SessionDetail
import dev.swarm.phone.ui.SessionLease
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotEquals
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * agents-tracker-qlf9's second verb: what the session detail says when the MACHINE refused control,
 * rather than what it says when nobody has asked yet.
 *
 * **THIS FILE WAS `PeekPanelLeaseVerdictTest` AND IT IS RE-HOMED, NOT REWRITTEN.** Every assertion
 * below is that suite's. `docs/adr/ADR-009-structured-chat-interaction.md` (3) deletes the terminal
 * peek and its two test files at slice I1's exit, and none of that ruling touches this subject: (5)
 * keeps the keystroke transport "exactly as decided, as the substrate", PB-INPUT-2 is unchanged, and
 * `leaseNoticeFor` survived the deletion intact -- it moved from `PeekPanelScreen` to
 * [SessionDetailScreen], because the peek carried the lease copy only for as long as the peek was
 * where the keyboard was. The screen a session is read on is this one now, so its coverage is here.
 * What changed on the way: `PeekPanelScreen.of(peek, lease = verdict)` becomes
 * `SessionDetailScreen.of(detail, transcript, lease, verdict)`, and the grid the fixture used to
 * carry is gone with the model that held it.
 *
 * **THE SENTENCE THAT WAS WRONG.** `ControlLease.confirmedBy` reduced the whole reply to
 * `code == "lease"`, so a kill switch, a revoked device, a policy refusal and a phone that has never
 * pressed the button all produced the same `false` -- and the screen said "Your machine has not
 * confirmed control of this session ... Take control first." To a user who pressed Take control and
 * was refused, that reads as though the press never happened, and the remedy it names is the very
 * step that was just declined. `Outcome.Message` -- the machine's own words about why -- was
 * discarded on the way.
 *
 * **A SEVERANCE IS NOT A REFUSAL AND MUST NOT READ AS ONE.** internal/remotegw/lease_sever.go seals
 * the lease-death notice under the take_control's own operation id, so an ordinary session that ends
 * leaves a `detach` sitting on the very outcome this screen reads. PB-INPUT-2's "visibly" wants that
 * said; saying it as a refusal would accuse the machine of something it did not do.
 */
class SessionDetailLeaseVerdictTest {

    private val id = "op-take-control-1"

    /**
     * The screen, in the state the two lease facts put it in.
     *
     * THE CONVERSATION IS EMPTY AND IRRELEVANT HERE, which is what the three-way split buys: the
     * lease notice is a function of the lease and the verdict alone, so a transcript in this fixture
     * would be a fact the assertions below do not read. `TranscriptPanelTest` owns that surface.
     */
    private fun notice(leaseHeld: Boolean, verdict: CommandVerdict): String = SessionDetailScreen.of(
        SessionDetail(
            sessionId = "mbp/quanthome",
            leaseHeld = leaseHeld,
            online = true,
            journalStale = false,
        ),
        TranscriptScreen.of(emptyList()),
        SessionLease(sessionId = "mbp/quanthome", leaseHeld = leaseHeld, online = true),
        verdict,
    ).leaseNotice

    /**
     * The panel's SECOND lease cell (agents-tracker-ksvb.10): the machine's own words, which the
     * view draws mono and tertiary under the sentence rather than inside it.
     */
    private fun detail(leaseHeld: Boolean, verdict: CommandVerdict): String = SessionDetailScreen.of(
        SessionDetail(
            sessionId = "mbp/quanthome",
            leaseHeld = leaseHeld,
            online = true,
            journalStale = false,
        ),
        TranscriptScreen.of(emptyList()),
        SessionLease(sessionId = "mbp/quanthome", leaseHeld = leaseHeld, online = true),
        verdict,
    ).leaseDetail

    private fun verdict(code: String, message: String = "") = ControlLease.verdictOf(
        OperationOutcome(operationId = id, code = code, message = message),
        id,
    )

    /**
     * FAILING-FIRST (TDD RED, GG-5) for agents-tracker-ksvb.10 on this verb. The reason survives
     * and it leaves the sentence: PB-INPUT-2 wants the refusal said VISIBLY, and a wire string in
     * the middle of the product's own prose is not the same claim as a wire string labelled as one.
     */
    @Test
    fun `a refused take control says so in its own words, with the machine's beside them`() {
        val refused = verdict("kill_switch", "remote control is disabled (kill switch off)")
        val notice = notice(leaseHeld = false, verdict = refused)

        assertFalse(
            "the machine's raw reason is spliced into the screen's own sentence, so a daemon " +
                "error string reads as prose this product wrote about a lease",
            notice.contains("remote control is disabled (kill switch off)"),
        )
        assertEquals(
            "the screen dropped the machine's reason for refusing control, so the user is left to " +
                "guess between a kill switch, a revoked device and a policy -- three different " +
                "remedies, none of which the screen names",
            "remote control is disabled (kill switch off)",
            detail(leaseHeld = false, verdict = refused),
        )
        assertFalse(
            "a REFUSED take control still reads as one nobody has asked for, which tells a user " +
                "who has just pressed the button that they have not pressed it",
            notice == SessionDetailScreen.leaseNoticeFor(confirmed = false),
        )
        assertFalse(
            // agents-tracker-nx44.6: this read `notice.contains("Take control first")` -- the
            // exact phrase the unconfirmed sentence used to end with. That sentence is
            // "Read-only -- take control to type." now (agents-tracker-ksvb.6, re-applied), so
            // the old spelling is nowhere in the app and an assertion pinning it would pass by
            // measuring nothing. The claim is unchanged and it is now made against the words the
            // screen actually has.
            "the screen tells a user whose take control was refused to take control",
            notice.contains("take control", ignoreCase = true),
        )
    }

    @Test
    fun `a severed lease is reported as ended and not as a refusal`() {
        val severed = verdict("detach", "control was released")
        val ended = notice(leaseHeld = false, verdict = severed)
        val refused = notice(leaseHeld = false, verdict = verdict("not_authorized", "control was released"))

        assertTrue(
            "PB-INPUT-2: a lease that died is not visibly reported at all, so the phone types " +
                "into a void with the keyboard shut and no sentence saying why",
            ended.contains("ended this phone's control"),
        )
        assertEquals(
            "a severance carries no detail, so the machine's own account of why control went away " +
                "reaches the user nowhere",
            "control was released",
            detail(leaseHeld = false, verdict = severed),
        )
        assertNotEquals(
            "a lease that ended normally reads exactly like one the machine refused, which " +
                "accuses the machine of something it did not do",
            refused,
            ended,
        )
    }

    /**
     * The detail is a DETAIL. A confirmed lease and one nobody asked for are not refusals, so
     * there is no machine reason to print under them -- and a mono line under a sentence that
     * reports success is a diagnostic about nothing.
     */
    @Test
    fun `a granted and an unasked lease carry no detail`() {
        assertEquals("", detail(leaseHeld = true, verdict = verdict("lease", "granted")))
        assertEquals("", detail(leaseHeld = false, verdict = CommandVerdict.UNANSWERED))
    }

    /**
     * The two states that were already right stay right. A screen whose refusal wording swallowed
     * the unasked case would have replaced one misleading sentence with another.
     */
    @Test
    fun `an unasked and a granted lease keep the two sentences PB-INPUT-2 already had`() {
        assertEquals(
            SessionDetailScreen.leaseNoticeFor(confirmed = false),
            notice(leaseHeld = false, verdict = CommandVerdict.UNANSWERED),
        )
        assertEquals(
            SessionDetailScreen.leaseNoticeFor(confirmed = true),
            notice(leaseHeld = true, verdict = verdict("lease", "")),
        )
    }

    /**
     * THE LEASE MODEL IS THE AUTHORITY ON WHETHER A LEASE IS HELD, not the verdict passed beside it.
     * `PhoneSurface.detailPanel` derives one from the other, so the two cannot disagree in
     * production -- and where they are made to, the screen must not claim a lease
     * `SessionLease.showsRelease` says is not there.
     */
    @Test
    fun `a granted verdict never claims a lease the model does not show`() {
        assertNotEquals(
            "the screen announced a confirmed lease over a session whose keyboard is shut",
            SessionDetailScreen.leaseNoticeFor(confirmed = true),
            notice(leaseHeld = false, verdict = verdict("lease", "")),
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
