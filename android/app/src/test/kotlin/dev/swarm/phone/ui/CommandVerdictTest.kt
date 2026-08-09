package dev.swarm.phone.ui

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * FAILING-FIRST (TDD RED, GG-5) for agents-tracker-qlf9: the machine's verdict on a command, read
 * as a CODE rather than thrown away.
 *
 * **THE DEFECT THIS MODEL EXISTS FOR.** Three verbs were fire-and-forget. `kill` took [Press]'s
 * default settle and dropped the operation id outright, so a refusal -- a kill switch the owner
 * turned off, a device the machine no longer authorises, a policy that forbids it -- left the
 * outcome line cleared to `""` and the session sitting in the inbox, which is exactly what a
 * SUCCESSFUL kill looks like one redraw before the roster updates. `take_control` kept the id but
 * reduced the whole reply to `code == "lease"`, so every refusal collapsed to `false` and the
 * screen said "your machine has not confirmed control", which reads as "you have not pressed the
 * button yet". `device_revoke` never claimed its outcome at all.
 *
 * **IT IS ONE TABLE AND NOT THREE.** `LaunchScreen.resolve` already read these codes correctly and
 * is the reasoning this mirrors -- policy is separated from every other refusal because "a
 * kill-switch refusal ends when the owner flips a switch, and telling that user their launch was
 * against policy sends them to change a spec that was fine", and a rate limit is separated from
 * both because waiting is what fixes it and nothing else is. A second table copied beside that one
 * is two readings of one taxonomy, and the codes are the MACHINE'S: this side must not claim to
 * know the whole set, which is why the last arm is an else.
 *
 * **WHY THE CODES ARE ASSERTED AS STRING LITERALS.** The unit-test JVM does not load the gomobile
 * AAR, so the wire ops cannot be read from the Go constants -- `ControlLeaseTest` states the same
 * limit for the same reason. `ok` is protocol.OpOK, `lease` is protocol.OpLease, `detach` is
 * protocol.OpDetach, and `policy` / `rate_limit` / `kill_switch` / `not_authorized` are
 * schema.CodePolicy, schema.CodeRateLimit, schema.CodeKillSwitch and schema.CodeNotAuthorized.
 */
class CommandVerdictTest {

    private val id = "op-kill-1"

    private fun outcome(
        code: String,
        message: String = "",
        operationId: String = id,
    ) = OperationOutcome(operationId = operationId, code = code, message = message)

    // ---- PB-SYNC-2: an outcome is claimed by operation id, never by proximity ----

    @Test
    fun `an operation the machine has not answered is pending rather than guessed at`() {
        val verdict = CommandVerdict.of(outcome(code = ""), id, CommandVerdict.ACCEPTED_OK)

        assertEquals(CommandResult.PENDING, verdict.result)
        assertFalse("an unanswered command reported itself as answered", verdict.answered)
        assertEquals("a pending verdict invented a reason nobody sent", "", verdict.reason)
    }

    @Test
    fun `somebody else's outcome leaves this operation pending`() {
        val other = outcome(code = "ok", operationId = "op-someone-else")

        assertEquals(
            "PB-SYNC-2: an outcome was attributed to an operation it does not name, which is the " +
                "proximity resolution operation ids exist to forbid",
            CommandResult.PENDING,
            CommandVerdict.of(other, id, CommandVerdict.ACCEPTED_OK).result,
        )
    }

    @Test
    fun `a screen that issued nothing is told nothing happened`() {
        assertEquals(
            "an empty operation id claimed an outcome, so a surface that has pressed no control " +
                "would report whatever the last command answered",
            CommandResult.PENDING,
            CommandVerdict.of(outcome(code = "ok"), operationId = "", CommandVerdict.ACCEPTED_OK).result,
        )
    }

    // ---- what the machine accepted ----

    @Test
    fun `the accepting code is the one the caller names and not a constant`() {
        assertTrue(
            "protocol.OpOK is what an accepted command replies",
            CommandVerdict.of(outcome(code = "ok"), id, CommandVerdict.ACCEPTED_OK).accepted,
        )
        assertTrue(
            "protocol.OpLease is what a take_control is answered with, and a table that only " +
                "knew `ok` would report every granted lease as a refusal",
            CommandVerdict.of(outcome(code = "lease"), id, accepted = "lease").accepted,
        )
        assertFalse(
            "a lease answered a command that was never a take_control",
            CommandVerdict.of(outcome(code = "lease"), id, CommandVerdict.ACCEPTED_OK).accepted,
        )
    }

    // ---- the refusals, told apart ----

    @Test
    fun `a policy rejection is its own answer and is never worth retrying`() {
        val verdict = CommandVerdict.of(outcome("policy", "not permitted on the remote tier"), id, CommandVerdict.ACCEPTED_OK)

        assertEquals(CommandResult.REJECTED_BY_POLICY, verdict.result)
        assertFalse("the machine's considered answer was offered as retryable", verdict.retryable)
        assertTrue(verdict.refused)
    }

    @Test
    fun `a rate limit is the one refusal waiting fixes`() {
        val verdict = CommandVerdict.of(outcome("rate_limit", "too many requests"), id, CommandVerdict.ACCEPTED_OK)

        assertEquals(CommandResult.REFUSED_TRANSIENTLY, verdict.result)
        assertTrue(
            "a rate limit was reported as permanent, so the user is told to change something " +
                "that was never the problem",
            verdict.retryable,
        )
    }

    /**
     * THE ARM THAT MATTERS MOST, and the one a fold into [CommandResult.REJECTED_BY_POLICY] would
     * destroy. A kill-switch refusal ends when the owner flips a switch at the machine; a
     * not_authorized refusal ends when the owner re-grants the device. Telling either user their
     * command was against policy sends them to change something that was fine.
     */
    @Test
    fun `a kill switch and a bad authorisation keep the machine's own words`() {
        val killSwitch = CommandVerdict.of(
            outcome("kill_switch", "remote control is disabled (kill switch off)"),
            id,
            CommandVerdict.ACCEPTED_OK,
        )
        assertEquals(CommandResult.REFUSED, killSwitch.result)
        assertEquals("remote control is disabled (kill switch off)", killSwitch.reason)

        val notAuthorized = CommandVerdict.of(outcome("not_authorized", "device not registered"), id, CommandVerdict.ACCEPTED_OK)
        assertEquals(CommandResult.REFUSED, notAuthorized.result)
        assertTrue(notAuthorized.refused)
    }

    /**
     * A SEVERANCE IS NOT A REFUSAL, and this is the distinction a bare "not the accepting code"
     * reading gets wrong. internal/remotegw/lease_sever.go seals the lease-death notice as
     * protocol.OpDetach under the take_control's OWN operation id, so the phone's durable outcome
     * for that operation BECOMES the detach -- a lease that was granted, used and then ended
     * normally. A screen that read it as a refusal would tell the user their machine refused
     * control of a session they had just been typing into.
     */
    @Test
    fun `a severance is the end of an accepted operation and not a refusal of it`() {
        val verdict = CommandVerdict.of(outcome("detach", "control was released"), id, accepted = "lease")

        assertEquals(CommandResult.ENDED, verdict.result)
        assertFalse("a lease that ended normally was reported as a refusal", verdict.refused)
        assertFalse("a lease that ended was reported as still held", verdict.accepted)
        assertTrue("a severance is an answer, and the screen has to know it arrived", verdict.answered)
    }

    // ---- the sentence ----

    /**
     * FAILING-FIRST (TDD RED, GG-5) for agents-tracker-ksvb.10: the head sentence is the WHOLE
     * primary copy, and the machine's own words are a detail beside it.
     *
     * **THE OLD CONTRACT SPLICED A GO ERROR INTO THE PRODUCT'S OWN SENTENCE.** `head: reason.` put
     * a string the daemon wrote -- `kill_switch: remote control is disabled (kill switch off)`,
     * lower case, parenthesised, with a wire code in front of it -- inside a sentence this app
     * authored, in this app's own body type. A reader cannot tell where the copy stops and the
     * diagnostic starts, and the product reads as though it wrote both.
     *
     * IT IS NOT A DELETION. [CommandVerdict.reason] still carries the words verbatim and the five
     * screens still show them: what changes is the REGISTER they are shown in -- `Mono.Meta` in
     * `--p-ink3` under the sentence, which is the machine's own voice rather than this one's.
     */
    @Test
    fun `the head is the whole sentence, and the machine's own words are the detail beside it`() {
        val verdict = CommandVerdict.of(outcome("kill_switch", "remote control is disabled"), id, CommandVerdict.ACCEPTED_OK)

        assertEquals(
            "the machine's raw reason is still spliced into the middle of the screen's own " +
                "sentence, so a daemon error string reads as copy this product wrote",
            "Your machine said no.",
            verdict.sentence("Your machine said no"),
        )
        assertEquals(
            "the machine's reason was dropped rather than demoted, and the user's next step " +
                "depends on which refusal it was",
            "remote control is disabled",
            verdict.reason,
        )
    }

    /**
     * A reply can carry no words at all -- `remotegw.refusePushPrefs` seals one with no error code
     * and no message, in its own words because "none of the six in the taxonomy describes a
     * machine-side custody failure". A screen left holding `""` would report a refusal with no
     * explanation, which is this defect wearing a different coat.
     */
    @Test
    fun `a refusal the machine sent no words with still reads as a sentence`() {
        val verdict = CommandVerdict.of(outcome("kill_switch"), id, CommandVerdict.ACCEPTED_OK)

        assertEquals("Your machine said no.", verdict.sentence("Your machine said no"))
    }

    @Test
    fun `a retryable refusal says so, and a permanent one does not`() {
        val transient = CommandVerdict.of(outcome("rate_limit", "too many requests"), id, CommandVerdict.ACCEPTED_OK)
        val permanent = CommandVerdict.of(outcome("policy", "not permitted"), id, CommandVerdict.ACCEPTED_OK)

        assertTrue(
            "a refusal waiting would fix was reported with no way forward",
            transient.sentence("No").endsWith(CommandVerdict.RETRY_HINT),
        )
        assertFalse(
            "a considered refusal was offered as worth retrying, which is advice that cannot work",
            permanent.sentence("No").endsWith(CommandVerdict.RETRY_HINT),
        )
    }

    @Test
    fun `the unanswered verdict is the one a screen starts from`() {
        assertEquals(CommandResult.PENDING, CommandVerdict.UNANSWERED.result)
        assertFalse(CommandVerdict.UNANSWERED.answered)
        assertFalse(CommandVerdict.UNANSWERED.refused)
    }
}
