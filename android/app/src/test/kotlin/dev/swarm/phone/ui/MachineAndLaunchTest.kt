package dev.swarm.phone.ui

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * FAILING-FIRST (TDD RED, GG-5) for PB-APP-5 (machine pane) and PB-APP-6 (launch).
 *
 * PB-APP-5 NARROWS WITH ADR-007 B133. Its criterion was "revoke + kill switch GATED PER
 * PB-SEC-2", and PB-SEC-2 is VOID: the trust boundary is the wire, and there is no local
 * authentication on this handset for a freshness table to describe. What survives is the half
 * that was never about the holder -- the phone SHOWS the kill switch and can never set it, which
 * is a daemon-side refusal (`handleRemoteSetControl` refuses the remote tier before consulting
 * its backend) and is unaffected by anything the phone does.
 *
 * THREE TESTS WERE DELETED HERE, NOT REWRITTEN, and what they fenced is worth stating so nobody
 * goes looking:
 *
 *  - `destructive actions demand a per-use authentication and typing does not` transcribed
 *    section 6.0's freshness table onto `GateFreshness`;
 *  - `an authentication for one action never authorises another` drove PB-SEC-2's last clause --
 *    a grant scoped to the action it was obtained for, so an `authenticated = true` flag failed;
 *  - `backgrounding or a screen lock invalidates every outstanding grant` drove the three events
 *    that dropped one.
 *
 * `GatedAction`, `BiometricFreshness`, `GateFreshness`, `AuthGrant` and `GateEvent` are all gone
 * from `ui/MachineAndLaunch.kt`. They were deleted rather than left answering NONE for
 * everything, because a freshness model that always says "none" reads as coverage -- which is
 * exactly what a rewrite of these three would have produced.
 */
class MachinePaneTest {

    /**
     * This phone's clock, 19 hours after the fixture's `lastHeardUnixMs` -- so the elapsed
     * duration is arithmetic rather than a wait, and it crosses the CALENDAR day boundary that
     * made the retired wall-clock form unreadable while staying inside `sinceLastHeard`'s hours
     * bucket (24 h and over coarsens to days, which would state a different unit than the claim).
     */
    private val NOW = 1_753_900_000_000L + 19 * 3_600_000L

    private fun pane(
        presence: String = "online",
        killSwitchEngaged: Boolean = false,
        activity: List<JournalRow> = emptyList(),
        freshness: MachineFreshness = MachineFreshness(silent = false, lastHeardUnixMs = 1_753_900_000_000),
    ) = MachinePane(
        machineId = "machine-endpoint-0001",
        presence = presence,
        freshness = freshness,
        pairedDeviceName = "swarm phone",
        killSwitchEngaged = killSwitchEngaged,
        activity = activity,
    )

    @Test
    fun `the pane shows presence, the paired device and the activity log`() {
        val p = pane(activity = listOf(JournalRow(cursor = 3, sessionId = "swarm", type = "launch", group = "working", tsUnixMs = 0L)))
        assertEquals("online", p.presence)
        assertEquals("swarm phone", p.pairedDeviceName)
        assertEquals(1, p.activity.size)
    }

    /**
     * PB-APP-11. The relay is the declared adversary and it answers the presence query, so a
     * pane that renders "online" on its word alone is reporting the adversary's claim as the
     * machine's state. While this phone has not heard from the machine itself past section
     * 6.0's budget, what it can vouch for has to be said too.
     */
    @Test
    fun `a silent machine is never presented on the relay's word alone`() {
        val silent = pane(
            presence = "online",
            freshness = MachineFreshness(silent = true, lastHeardUnixMs = 1_753_900_000_000),
        )
        // agents-tracker-2pnu F5: `presenceExplanation` takes THIS PHONE'S CLOCK and no longer
        // a formatter, and the assertion moved with it. It read
        // `assertTrue("the user is told WHEN", line.contains("since 14:26"))` -- a wall clock
        // with no date on it, which at 09:00 the next morning reads the same as three minutes.
        val line = silent.presenceExplanation(NOW)
        assertTrue("the user is told HOW LONG", line.contains("for 19h"))
        assertTrue("the relay's word is attributed to the relay", line.contains("relay"))

        val healthy = pane(presence = "online")
        assertFalse(
            "a machine inside the budget carries no warning",
            healthy.presenceExplanation(NOW).contains("Not heard"),
        )
    }

    /**
     * agents-tracker-ksvb.6: "Your machine is $presence." used to print unconditionally, on
     * every visit, restating what the presence dot beside it already says in colour -- the one
     * always-on sentence this app's own conditional-notice discipline argues against everywhere
     * else. A healthy machine now renders NOTHING (the house pattern), and the fact moves to
     * [MachinePane.presenceAnnouncement] rather than disappearing, because the dot has no words
     * of its own for a screen reader.
     */
    @Test
    fun `a healthy machine's presence line is silent, and the fact moves to the announcement`() {
        val healthy = pane(presence = "online")

        assertEquals("", healthy.presenceExplanation(NOW))
        assertEquals("Your machine is online.", healthy.presenceAnnouncement)

        // The silent case is unaffected: it already says presence in words, so there is nothing
        // for the announcement to carry that the line does not.
        val silent = pane(
            presence = "online",
            freshness = MachineFreshness(silent = true, lastHeardUnixMs = 1_753_900_000_000),
        )
        assertTrue(silent.presenceExplanation(NOW).isNotEmpty())
    }

    /**
     * A phone that has never heard from its machine says so rather than naming a time it does
     * not have. Zero is not a timestamp, and rendering it would read as 1 January 1970.
     */
    @Test
    fun `a phone that has never heard from its machine names no time`() {
        val never = MachineFreshness(silent = true, lastHeardUnixMs = 0)
        val notice = never.notice(NOW)
        assertEquals("Not heard from your machine yet.", notice)
        assertNull("inside the budget there is nothing to say", MachineFreshness(false, 0).notice(NOW))
    }

    /**
     * THE KILL SWITCH IS READ-ONLY, and this is a security property rather than a simplification.
     * protocol/server.go handleRemoteSetControl refuses the remote tier BEFORE consulting its
     * backend -- "a remote device must never re-enable a switch its owner turned off" -- so a
     * screen control that offered to turn it back on would be advertising a bypass of a daemon
     * gate (PB-SEC-6). The phone SHOWS it, so a kill_switch refusal is legible, and offers the
     * only panic action a phone legitimately has: revoking itself.
     */
    @Test
    fun `the kill switch is displayed and can never be set from the phone`() {
        val engaged = pane(killSwitchEngaged = true)
        assertTrue(engaged.killSwitchEngaged)
        assertTrue(engaged.killSwitchExplanation.isNotBlank())
        assertFalse("PB-SEC-6: no control may re-enable remote control", engaged.canSetKillSwitch)
        assertTrue("revoking THIS device is the phone's own panic action", engaged.canRevokeThisDevice)
    }

    /**
     * agents-tracker-ksvb.6: the ON state's paragraph shrinks to one short line -- it is
     * reporting nothing wrong, unlike the OFF state below it, which keeps its full teaching.
     */
    @Test
    fun `the enabled state's sentence is one short line, and the disabled state keeps its teaching`() {
        val disengaged = pane(killSwitchEngaged = false)
        assertEquals("Remote control is on. Only the machine can switch it off.", disengaged.killSwitchExplanation)

        val engaged = pane(killSwitchEngaged = true)
        assertTrue(
            "the disabled state lost the teaching that only the owner can switch it back on",
            engaged.killSwitchExplanation.contains("Only the machine's owner"),
        )
    }

}

class LaunchScreenTest {

    /**
     * The two fields the machine has no default for, refused BEFORE anything is signed.
     *
     * The daemon refuses a launch without an agent and without a working directory, so a form
     * that sent one anyway would burn a durable command seq and a signature on a request the
     * phone could see was incomplete -- and would hand the user back a routed facade error about
     * a field they simply left empty. The answer is the MODEL's, so the screen and [submit]
     * cannot enforce different bars.
     */
    @Test
    fun `a draft missing a required field is named rather than sent`() {
        val screen = LaunchScreen()
        assertNull(screen.missingField(LaunchDraft(agent = "claude", cwd = "/repo", prompt = "")))
        assertTrue(
            screen.missingField(LaunchDraft(agent = " ", cwd = "/repo", prompt = ""))!!
                .isNotBlank(),
        )
        assertTrue(
            screen.missingField(LaunchDraft(agent = "claude", cwd = "", prompt = ""))!!
                .isNotBlank(),
        )
        assertNull("nothing is in flight after a refused draft", screen.inFlight)
    }

    /** The v1 builder path: a spec goes out as one operation. */
    @Test
    fun `a submitted spec becomes one launch operation`() {
        val screen = LaunchScreen()
        val op = screen.submit(LaunchDraft(agent = "claude", cwd = "/repo", prompt = "fix the build"))
        assertEquals("launch", op.action)
        assertTrue(op.operationId.isNotBlank())
    }

    /**
     * POLICY REJECTION IS SURFACED, which is the requirement's own second clause. The machine's
     * refusal arrives as an operation outcome carrying schema.CodePolicy; the screen must render
     * the reason rather than a generic failure, because the user's next step depends entirely on
     * which refusal it was.
     */
    @Test
    fun `a policy rejection is rendered with its reason and is not retried`() {
        val screen = LaunchScreen()
        val op = screen.submit(LaunchDraft(agent = "claude", cwd = "/etc", prompt = ""))
        val rendered = screen.resolve(
            OperationOutcome(operationId = op.operationId, code = "policy", message = "cwd is outside the allowed roots"),
        )
        assertEquals(LaunchResult.REJECTED_BY_POLICY, rendered.result)
        assertEquals("cwd is outside the allowed roots", rendered.reason)
        assertFalse("a policy refusal is the machine's answer, not a transient failure", rendered.retryable)
    }

    /** A rate-limited refusal IS retryable, so the two must not be collapsed. */
    @Test
    fun `a rate limited refusal is distinguishable from a policy one`() {
        val screen = LaunchScreen()
        val op = screen.submit(LaunchDraft(agent = "claude", cwd = "/repo", prompt = ""))
        val rendered = screen.resolve(
            OperationOutcome(operationId = op.operationId, code = "rate_limit", message = "too many launches"),
        )
        assertEquals(LaunchResult.REFUSED_TRANSIENTLY, rendered.result)
        assertTrue(rendered.retryable)
    }

    /**
     * An unresolved launch is neither a success nor a failure, and saying either is worse than
     * saying nothing: PB-SYNC-2 keys outcomes by operation id precisely so a screen never
     * resolves one by proximity.
     */
    @Test
    fun `an unanswered launch stays pending rather than being guessed at`() {
        val screen = LaunchScreen()
        val op = screen.submit(LaunchDraft(agent = "claude", cwd = "/repo", prompt = ""))
        val rendered = screen.resolve(OperationOutcome(operationId = "some-other-op", code = "ok", message = ""))
        assertEquals(LaunchResult.PENDING, rendered.result)
        assertTrue(op.operationId != "some-other-op")
    }
}
