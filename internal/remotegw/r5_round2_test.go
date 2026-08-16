package remotegw

// FAILING-FIRST (TDD RED, GG-5) for the Wave R5 round-2 review fix-pack (bead
// agents-tracker-hggx.6), MEDIUM 2: gateway crash-shaped redelivery coverage for the
// ONE op in the R1 vocabulary whose redelivery can now SPAWN A PROCESS.
//
// TestR1RefusalOps_CrashShapedRedeliveryGetsTheSameRefusal was retargeted off
// session_launch when its real handler landed (defensible: that test's premise is a
// STATELESS refusal), which left the gateway hop unfenced for a redelivered
// well-formed session_launch. This is the mutation-class mirror of
// TestCrashMatrix_MutationDuplicateBoundedToOneRedelivery (a killCmd there): the
// crash between the daemon forward and the inbound-state persist re-forwards the
// retained frame exactly ONCE more, under the SAME operation_id and with the SAME
// preset body riding -- which is precisely what lets the daemon's durable two-phase
// idempotency (r5_launchfault_test.go's double-driver bound) suppress the duplicate
// spawn. Without the unchanged operation_id the dedup keys on nothing; without the
// body the daemon refuses a launch naming no preset.

import (
	"context"
	"sync"
	"testing"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/protocol/schema"
)

// r5SLForwarder records the forwarded ops AND the session_launch bodies -- the
// package fakeForwarder drops rc.SessionLaunch, and the body's survival across the
// redelivery is half of what this fence pins.
type r5SLForwarder struct {
	mu     sync.Mutex
	ops    []string
	seen   []schema.DeviceCommandAuth
	bodies []*schema.SessionLaunchReq
}

func (f *r5SLForwarder) ForwardCommand(op string, rc protocol.RemoteCommand) (protocol.Control, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ops = append(f.ops, op)
	f.seen = append(f.seen, rc.DeviceCommandAuth)
	f.bodies = append(f.bodies, rc.SessionLaunch)
	return protocol.Control{Op: protocol.OpSessionLaunch, OperationID: rc.OperationID}, nil
}

// TestR5Round2_SessionLaunchRedeliveryBoundedToOneReforward: run 1 forwards the
// launch and the gateway dies before the inbound persist; run 2 re-forwards the
// retained frame exactly once -- same operation_id, body intact -- and the duplicate
// window then CLOSES: run 3 forwards nothing.
func TestR5Round2_SessionLaunchRedeliveryBoundedToOneReforward(t *testing.T) {
	key := inboundKey(83)
	const epoch uint32 = 5
	st := &memInboundState{failSave: errGatewayCrashed}
	rl := &retainingRelay{}
	cmd := protocol.RemoteCommand{
		DeviceCommandAuth: schema.DeviceCommandAuth{
			Action: protocol.ActionSessionLaunch, OperationID: "op-preset-launch-1",
			DeviceID: "d1", Sig: "device-signature",
		},
		SessionLaunch: &schema.SessionLaunchReq{
			PresetID: "preset-api", PresetRevision: "rev-1", InitialPrompt: "fix the flaky test",
			Cols: 80, Rows: 24,
		},
		BodyVersion: schema.CurrentProfileVersion,
	}
	env := sealAt(t, key, epoch, 1, cmd)
	rl.add(1, env)

	poll := func(cursor uint64) *r5SLForwarder {
		if cursor != 0 {
			rl.add(cursor, env)
		}
		fwd := &r5SLForwarder{}
		b := NewCommandBridge(CommandBridgeConfig{
			Mailbox: rl, Forwarder: fwd, Key: key, EpochID: epoch, ReplyTarget: "phone", Inbound: st,
		})
		_, err := b.PollOnce(context.Background())
		t.Logf("poll err = %v", err)
		return fwd
	}

	// Run 1: the daemon is asked to launch; the gateway dies before anything durable.
	f1 := poll(0)
	if len(f1.ops) != 1 || f1.ops[0] != protocol.OpSessionLaunch {
		t.Fatalf("run 1 forwarded %v, want one session_launch", f1.ops)
	}
	if f1.bodies[0] == nil || f1.bodies[0].PresetID != "preset-api" {
		t.Fatalf("run 1 body = %+v, want the preset body riding", f1.bodies[0])
	}

	// Run 2: persist works now. The retained frame is re-forwarded exactly once --
	// same operation_id (the daemon's two-phase idempotency keys on it), body intact.
	st.mu.Lock()
	st.failSave = nil
	st.mu.Unlock()
	f2 := poll(5)
	if len(f2.ops) != 1 {
		t.Fatalf("run 2 forwarded %d, want exactly 1 re-forward: losing the mutation would strand "+
			"the phone's op forever; forwarding it twice per restart would spawn per restart", len(f2.ops))
	}
	if f2.seen[0].OperationID != f1.seen[0].OperationID {
		t.Fatalf("re-forwarded operation_id = %q, want the original %q: the daemon's idempotent "+
			"reservation is the ONLY thing bounding this duplicate to at most one process, and it "+
			"keys on the operation_id", f2.seen[0].OperationID, f1.seen[0].OperationID)
	}
	if f2.bodies[0] == nil || f2.bodies[0].PresetID != "preset-api" ||
		f2.bodies[0].PresetRevision != "rev-1" || f2.bodies[0].InitialPrompt != "fix the flaky test" {
		t.Fatalf("re-forwarded body = %+v, want the original preset body unchanged", f2.bodies[0])
	}

	// Run 3: the window is CLOSED -- the durable high-water refuses the retained frame.
	f3 := poll(9)
	if len(f3.ops) != 0 {
		t.Fatalf("run 3 forwarded %v, want none: the duplicate window must close after one "+
			"redelivery, or every restart re-drives the launch for as long as the relay retains it", f3.ops)
	}
}
