package conformance_test

// FAILING-FIRST (TDD RED, GG-5) for PB-STATE-4's amendment of 2026-07-26: RevokeThisDevice is
// EXEMPT from PB-SYNC-7's fail-closed reconcile gate, and the three state-selected verbs are not.
//
// THE DEFECT THE AMENDMENT CLOSES. S18 wired device_revoke onto the ordinary mutating path, so
// it began running requireReconciled with every other signed command. An unreconciled phone is
// close to the definition of a lost or long-disconnected handset -- which is the exact state the
// panic button exists for -- so the button refused on precisely the device that needed it.
//
// THE BOUNDARY IS NOT "REVOKE IS SPECIAL", and that is why this file has two tests rather than
// one. The gate protects ops whose TARGET IS SELECTED FROM SYNCHRONIZED STATE (kill, launch,
// take_control), because stale state makes them act on the wrong object. A self-revoke selects
// no target -- it names its own signer, which needs no synchronized state to identify -- and it
// only REMOVES capability, never grants it, so a rollback attacker who forces one gains a denial
// of service they already had. The risk this amendment carries is the exemption quietly widening
// to the other three, and the second test below is the fence against exactly that.

import (
	"strings"
	"testing"

	swarmmobile "github.com/Nathandela/swarm/mobile"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/protocol/schema"
)

// pbstate4RequireUnreconciled is the non-vacuity guard both tests below stand on. Neither
// asserts anything worth having if the phone turned out to be reconciled: the first would prove
// only that a reconciled phone can revoke (which was never in doubt), and the second would be
// asserting a refusal that the gate under test is not even being asked for.
func pbstate4RequireUnreconciled(t *testing.T, h *harness) {
	t.Helper()
	s, err := h.App.StateSummary()
	if err != nil {
		t.Fatalf("StateSummary: %v", err)
	}
	if s.Reconciled {
		t.Fatalf("this harness's phone is already reconciled, so nothing below exercises " +
			"PB-SYNC-7's gate at all. newHarness must seed an UNRECONCILED phone; the reconcile " +
			"record is published only by an explicit h.PushReconcile()")
	}
}

// TestPBSTATE4_AnUnreconciledPhoneCompletesItsRevokeEndToEnd is the amendment's first stated
// direction, verbatim: "an unreconciled phone completes a revoke end to end".
//
// END TO END, on the path this suite owns: the verb returns an op rather than a refusal, the
// signed device_revoke is SEALED and REACHES THE MACHINE naming this device, and the machine's
// answer RESOLVES it. Asserting only that the call returned nil would pass on an implementation
// that skipped the gate and then sealed nothing -- which is the exact shape the verb had before
// S18, and the shape the amendment is not asking for.
func TestPBSTATE4_AnUnreconciledPhoneCompletesItsRevokeEndToEnd(t *testing.T) {
	h := newHarness(t)
	pbstate4RequireUnreconciled(t, h)

	op, err := h.App.RevokeThisDevice()
	if err != nil {
		t.Fatalf("PB-STATE-4 (amended): RevokeThisDevice refused on an unreconciled phone: %v.\n"+
			"An unreconciled phone is close to the definition of a lost handset, which is the "+
			"state this button exists for. The gate protects ops whose target is selected from "+
			"synchronized state; a self-revoke names its own signer and only removes capability", err)
	}
	if op == nil || op.OperationID == "" {
		t.Fatalf("RevokeThisDevice returned no operation id, so its verdict is unclaimable")
	}

	cmd := h.AwaitCommand(protocol.ActionDeviceRevoke)
	if cmd.Session != cmd.DeviceID {
		t.Errorf("the sealed device_revoke targets %q while this device is %q. The target rides "+
			"the session position of the signed tuple and the gateway copies it into "+
			"Control.TargetDeviceID, so this revokes the wrong device -- or, more likely, none",
			cmd.Session, cmd.DeviceID)
	}
	if cmd.OperationID != op.OperationID {
		t.Errorf("the sealed command carries operation id %q and the caller was handed %q, so the "+
			"reply cannot be claimed by the op that produced it (PB-SYNC-2)",
			cmd.OperationID, op.OperationID)
	}

	h.Reply(schema.Control{
		Op:          "ok",
		EndpointID:  h.Machine,
		SessionID:   cmd.Session,
		OperationID: op.OperationID,
	})
	eventually(t, "the revoke never resolved on an unreconciled phone", func() bool {
		out, oerr := h.App.Outcome(op.OperationID)
		return oerr == nil && out.Resolved
	})
}

// TestPBSTATE4_TheRevokeExemptionDoesNotWidenToTheStateSelectedVerbs is the amendment's second
// stated direction, and it is the one that matters: "kill, launch and take_control still refuse
// with swarm/unreconciled on that same phone -- an exemption that widens to the other three is
// the failure this amendment risks".
//
// SAME PHONE, SAME PROCESS, one harness, deliberately. Two harnesses would let the exemption be
// implemented as a mode the phone enters -- revoke works because the phone stopped enforcing --
// and every assertion here would still pass. Interleaved on one unreconciled phone, the only
// implementation that satisfies both tests is one that discriminates BY ACTION.
//
// THE REFUSAL MUST ALSO PRECEDE THE SEAL. A verb that seals its command and then reports an
// error has burnt a durable send-seq on a frame the machine will act on, so the wire is checked
// as well as the return value.
func TestPBSTATE4_TheRevokeExemptionDoesNotWidenToTheStateSelectedVerbs(t *testing.T) {
	h := newHarness(t)
	pbstate4RequireUnreconciled(t, h)

	// The exemption is live on this phone: the revoke goes through. Asserting the three
	// refusals without this would pass on an implementation that simply never landed the
	// amendment, which is not the widening this test is fencing.
	if _, err := h.App.RevokeThisDevice(); err != nil {
		t.Fatalf("PB-STATE-4 (amended): the revoke exemption is not in place (%v), so the "+
			"refusals asserted below say nothing about whether it WIDENED", err)
	}
	h.AwaitCommand(protocol.ActionDeviceRevoke)

	for _, tc := range []struct {
		verb   string
		action string
		call   func() (*swarmmobile.Op, error)
	}{
		{"Kill", protocol.ActionKill, func() (*swarmmobile.Op, error) {
			return h.App.Kill(testSession)
		}},
		{"Launch", protocol.ActionLaunch, func() (*swarmmobile.Op, error) {
			return h.App.Launch(&swarmmobile.LaunchSpec{Agent: "claude", Cwd: "/tmp", Prompt: "hi"})
		}},
		{"TakeControl", protocol.ActionTakeControl, func() (*swarmmobile.Op, error) {
			return h.App.TakeControl(testSession)
		}},
	} {
		t.Run(tc.verb, func(t *testing.T) {
			op, err := tc.call()
			if err == nil {
				t.Fatalf("PB-STATE-4/PB-SYNC-7: %s SUCCEEDED on an unreconciled phone (op %+v). "+
					"The revoke exemption has widened to a verb whose TARGET IS SELECTED FROM "+
					"SYNCHRONIZED STATE, so a rollback makes it act on the wrong object",
					tc.verb, op)
			}
			if !strings.Contains(err.Error(), swarmmobile.ErrClassUnreconciled) {
				t.Fatalf("%s failed with %q, want the %s class. Any other refusal means this "+
					"assertion is passing on an unrelated failure and would go on passing if the "+
					"gate were removed", tc.verb, err, swarmmobile.ErrClassUnreconciled)
			}
			if h.sawCommand(tc.action) {
				t.Errorf("%s was refused but a %q command still reached the machine. The gate "+
					"must precede the seal: sealing first burns a durable send-seq on a frame "+
					"the machine acts on, and reports failure for an op that happened",
					tc.verb, tc.action)
			}
		})
	}

	// THE CONTROL. Without it, all three refusals above are satisfiable by three broken verbs:
	// a Kill that always errors passes the assertion for the wrong reason, and would keep
	// passing after the gate was deleted. The reconcile record is what makes them work, so it
	// is what proves the refusals were the gate.
	h.PushReconcile()
	eventually(t, "reconcile never adopted", func() bool {
		s, err := h.App.StateSummary()
		return err == nil && s.Reconciled
	})
	if _, err := h.App.Kill(testSession); err != nil {
		t.Fatalf("Kill still refused AFTER the machine published its authorities: %v. The three "+
			"refusals above were then not this gate, and this file fences nothing", err)
	}
	h.AwaitCommand(protocol.ActionKill)
}
