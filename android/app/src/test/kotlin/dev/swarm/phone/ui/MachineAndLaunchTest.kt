package dev.swarm.phone.ui

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * FAILING-FIRST (TDD RED, GG-5) for PB-APP-5 (machine pane) and PB-APP-6 (launch).
 *
 * PB-APP-5's criterion is "revoke + kill switch gated per PB-SEC-2", and PB-SEC-2's own
 * criterion is that "a test must fail if the implementation is an in-memory
 * `authenticated = true` flag". So what is modelled here is the POLICY -- which freshness a
 * given action demands, and that the demand is per-use for the destructive ones -- and NOT a
 * biometric. Whether the platform enforces it is PB-E2E-5, which is deferred; nothing in this
 * file imports androidx.biometric, and android/gate/s16_ui_test.go fences that.
 */
class MachinePaneTest {

    private fun pane(
        presence: String = "online",
        killSwitchEngaged: Boolean = false,
        activity: List<JournalRow> = emptyList(),
    ) = MachinePane(
        machineId = "machine-endpoint-0001",
        presence = presence,
        pairedDeviceName = "swarm phone",
        killSwitchEngaged = killSwitchEngaged,
        activity = activity,
    )

    @Test
    fun `the pane shows presence, the paired device and the activity log`() {
        val p = pane(activity = listOf(JournalRow(cursor = 3, type = "launch", group = "working")))
        assertEquals("online", p.presence)
        assertEquals("swarm phone", p.pairedDeviceName)
        assertEquals(1, p.activity.size)
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
     * PB-SEC-2's freshness table, transcribed from section 6.0: 60 s for input and take_control;
     * PER-USE for revoke, kill switch, launch and kill.
     *
     * A per-use requirement is what a CryptoObject-bound Keystore key enforces, and the reason
     * it cannot be a boolean is the last clause of PB-SEC-2: "no reuse of one authentication for
     * a different action". A flag set by one prompt authorises everything after it.
     */
    @Test
    fun `destructive actions demand a per-use authentication and typing does not`() {
        assertEquals(BiometricFreshness.PER_USE, GateFreshness.of(GatedAction.REVOKE_DEVICE))
        assertEquals(BiometricFreshness.PER_USE, GateFreshness.of(GatedAction.KILL_SESSION))
        assertEquals(BiometricFreshness.PER_USE, GateFreshness.of(GatedAction.LAUNCH))
        assertEquals(BiometricFreshness.WINDOW_60S, GateFreshness.of(GatedAction.TAKE_CONTROL))
        assertEquals(BiometricFreshness.WINDOW_60S, GateFreshness.of(GatedAction.SEND_INPUT))
    }

    /**
     * The negative half, and the one PB-SEC-2 names: an authentication satisfied for one action
     * must not authorise a different one. Expressed as policy -- a grant is scoped to the action
     * it was obtained for -- so an implementation holding a single `authenticated = true` fails.
     */
    @Test
    fun `an authentication for one action never authorises another`() {
        val grant = AuthGrant(action = GatedAction.TAKE_CONTROL, atMillis = 1_000)
        assertTrue(grant.authorises(GatedAction.TAKE_CONTROL, nowMillis = 1_500))
        assertFalse(grant.authorises(GatedAction.REVOKE_DEVICE, nowMillis = 1_500))
        assertFalse(
            "a per-use action is never covered by an earlier grant, however fresh",
            grant.authorises(GatedAction.KILL_SESSION, nowMillis = 1_000),
        )
        assertFalse(
            "and the 60 s window really expires",
            grant.authorises(GatedAction.TAKE_CONTROL, nowMillis = 1_000 + 60_001),
        )
    }

    /**
     * Backgrounding, screen lock and process death invalidate it. PB-SEC-2 lists all three, and
     * PB-KEY-7's purge is what makes the third real on the Go side.
     */
    @Test
    fun `backgrounding or a screen lock invalidates every outstanding grant`() {
        val grant = AuthGrant(action = GatedAction.TAKE_CONTROL, atMillis = 1_000)
        assertNull(grant.afterEvent(GateEvent.BACKGROUNDED))
        assertNull(grant.afterEvent(GateEvent.SCREEN_LOCKED))
        assertNull(grant.afterEvent(GateEvent.BIOMETRIC_ENROLLMENT_CHANGED))
    }
}

class LaunchScreenTest {

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
