package dev.swarm.phone.ui.screens

import dev.swarm.phone.ui.CommandVerdict
import dev.swarm.phone.ui.OperationOutcome
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * FAILING-FIRST (TDD RED, GG-5) for agents-tracker-jx23: the second thing a revoke can leave
 * behind, which no sentence in this product states.
 *
 * **THE FACT.** `App.PurgeKeys` returns an error to say the key material AT REST survived the
 * purge -- a full disk, a data directory gone read-only -- while the memory half happened
 * regardless, and both Go layers keep that promise deliberately. `PhoneRuntime.purgeKeys` now
 * hands it back (agents-tracker-r3os) instead of discarding it, so for the first time there is
 * something to say.
 *
 * **IT IS NOT THE FACT [PairOnlyScreen.revokeNoticeFor] ALREADY CARRIES.** Those three arms are
 * about the MACHINE's answer to the removal, and `revokeUnsentNotice`'s tail is about the
 * REGISTRATION -- whether the device is still known to a computer somewhere. This one is about
 * the sealed containers on the handset in the user's hand. The two are independent and can both
 * be true at once: a machine that refused the removal AND a purge that could not finish at rest
 * is the worst case and the one worth being able to state.
 *
 * **SO THE JOIN IS THE SCREEN'S TOO.** PB-DS-9 puts copy in the screen model, and a caller that
 * concatenated the two sentences would be deciding their order and their separator -- copy
 * decisions -- in a surface. It is one composer with two facts, and it must be able to say each
 * alone, both together, and NOTHING at all when the machine confirmed the removal and the purge
 * finished, which is `revokeNoticeFor`'s own rule and the reason the arms are not simply
 * concatenated.
 *
 * **WHY BOTH CALLERS NEED THE PARAMETER** rather than the panel joining it once. The pair-only
 * screen re-reads the machine's verdict on every draw (agents-tracker-4zue) and RECOMPOSES this
 * sentence when the answer lands -- so a purge failure joined on by `SettingsSurface` and not
 * passed through the composer would vanish from the screen at the moment the machine finally
 * answered, which is the one moment the user is reading it.
 */
class PairOnlyPurgeNoticeTest {

    private val id = "op-revoke-jx23"

    /** PB-APP-9's routed sentence for a custody failure, as `PhoneRuntime` hands it over. */
    private val routed = "This phone's key store refused. Pair this device again."

    private fun verdict(code: String, message: String = "") = CommandVerdict.of(
        OperationOutcome(operationId = id, code = code, message = message),
        id,
        CommandVerdict.ACCEPTED_OK,
    )

    @Test
    fun `a confirmed removal whose purge finished still says nothing`() {
        assertEquals(
            "an unpaired phone is warned about a state that is entirely fine, which teaches the " +
                "user to ignore the warning that is not",
            "",
            PairOnlyScreen.revokeNoticeFor(verdict("ok", "ok"), purgeFailure = ""),
        )
    }

    @Test
    fun `the sentence is unchanged for every caller that has no purge failure to report`() {
        // The parameter defaults, so the existing call sites keep their exact wording. A revoke
        // that purged cleanly must read today as it read yesterday.
        for (v in listOf(CommandVerdict.UNANSWERED, verdict("kill_switch", "the kill switch is off"))) {
            assertEquals(
                "adding the purge fact changed what a phone with no purge failure is told",
                PairOnlyScreen.revokeNoticeFor(v),
                PairOnlyScreen.revokeNoticeFor(v, purgeFailure = ""),
            )
        }
    }

    @Test
    fun `a purge that could not finish is stated even when the machine confirmed the removal`() {
        val notice = PairOnlyScreen.revokeNoticeFor(verdict("ok", "ok"), purgeFailure = routed)

        assertTrue(
            "the machine removed the device and this handset could not destroy the key material " +
                "it was holding, and the screen says nothing at all. The one fact that survives a " +
                "successful revoke is the one on no screen in the product",
            notice.isNotEmpty(),
        )
        assertTrue(
            "the routed reason the purge failed was re-worded here rather than carried, so two " +
                "files now decide what a custody failure reads as",
            notice.contains(routed),
        )
        assertTrue(
            "the notice never says what survived. \"Something went wrong\" over a handset whose " +
                "owner has just disowned it is a warning with no subject",
            notice.contains("key"),
        )
    }

    @Test
    fun `both failures are stated when both happened`() {
        val refusal = "remote control is disabled (kill switch off)"
        val notice = PairOnlyScreen.revokeNoticeFor(verdict("kill_switch", refusal), purgeFailure = routed)

        assertTrue(
            "the machine's own reason for keeping this device was dropped once a purge also failed",
            notice.contains(refusal),
        )
        assertTrue(
            "the purge failure was dropped once the machine also refused. These are independent " +
                "facts about two different computers and neither answers for the other",
            notice.contains(routed),
        )
        assertTrue(
            "the machine-side remedy went with them: this device is still registered AND still " +
                "holding key material",
            notice.contains("swarm remote revoke"),
        )
    }

    /**
     * THE SAME PAIR ON THE OTHER ARM OF THE SAME SETTLE. A revoke that never reached the wire --
     * offline, or a facade refusal -- and a purge that could not finish at rest are both reachable
     * in one press, and the panel composing that arm calls [PairOnlyScreen.revokeUnsentNotice]
     * rather than [PairOnlyScreen.revokeNoticeFor]. An overload that dropped the second fact would
     * be the silence this issue is about, on the arm nobody looked at.
     */
    @Test
    fun `a revoke that never reached the wire still reports a purge that could not finish`() {
        val transport = "No link to your machine right now."
        val notice = PairOnlyScreen.revokeUnsentNotice(transport, purgeFailure = routed)

        assertTrue(
            "the routed transport failure was dropped or re-worded once a purge also failed",
            notice.startsWith(transport),
        )
        assertTrue(
            "this device is still registered for certain -- the command never left the handset -- " +
                "and the screen stopped saying so once a second failure arrived",
            notice.contains("swarm remote revoke"),
        )
        assertTrue(
            "the key material this phone could not destroy is reported on the arm where the " +
                "machine answered and dropped on the arm where it never heard the question",
            notice.contains(routed),
        )
    }

    @Test
    fun `the unsent sentence is unchanged when the purge finished`() {
        val transport = "No link to your machine right now."

        assertEquals(
            "adding the purge fact changed what a phone whose purge finished is told on the " +
                "unsent arm",
            PairOnlyScreen.revokeUnsentNotice(transport),
            PairOnlyScreen.revokeUnsentNotice(transport, purgeFailure = ""),
        )
    }

    @Test
    fun `the machine's answer is stated before the handset's own`() {
        val refusal = "remote control is disabled (kill switch off)"
        val notice = PairOnlyScreen.revokeNoticeFor(verdict("kill_switch", refusal), purgeFailure = routed)

        // THE ORDER IS A COPY DECISION AND IS MADE HERE. The machine's answer is what the user's
        // next action depends on -- whether `swarm remote pair` will be refused -- and the purge
        // failure is a fact about this handset that no command of theirs undoes. Leading with the
        // second buries the actionable one.
        assertTrue(
            "the handset's own failure is stated before the machine's answer, so the sentence the " +
                "user can act on is the second one they reach",
            notice.indexOf(refusal) < notice.indexOf(routed),
        )
    }
}
