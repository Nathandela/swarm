package remotegw

// FAILING-FIRST (TDD RED, GG-5) tests for the two holes review found in the lease
// CONFIRMATION (lease_confirm.go), both of which put the phone back where PB-INPUT-2
// forbids: acting on a lease it cannot actually gate on.
//
// F-1: confirmLease reads the generation AFTER Begin returned. LeaseManager.Generation
// reports 0 for a session that holds no conn, and the per-conn watcher removes the conn
// the instant the lease dies (kill switch, device revoke, session exit). So a Begin that
// succeeded and was then severed seals a POSITIVE OpLease naming generation 0 -- a
// generation that does not exist. The refusal path already treats 0 as the marker for
// "nothing was granted"; the grant path must not contradict it.
//
// F-2: the refusal path computes sealErr and throws it away. If the refusal itself fails
// to append, the phone gets NEITHER the lease NOR the refusal and the fault is invisible:
// only beginErr reaches the poll aggregate. That is exactly the silence the file's own
// header calls the worse half.

import (
	"context"
	"errors"
	"testing"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/remote/relay"
)

var errReplyAppendRefused = errors.New("relay refused the reply append")

// appendFailingMailbox reads like fakeMailbox but every reply append fails, so the seal
// of a refusal faults the way an unreachable relay would.
type appendFailingMailbox struct {
	fakeMailbox
}

func (m *appendFailingMailbox) MailboxAppend(context.Context, string, []byte) (uint64, error) {
	return 0, errReplyAppendRefused
}

// TestCommandBridge_SeveredLeaseIsRefusedNotConfirmedAtGenerationZero (F-1): Begin
// succeeded, then the lease was severed before the generation could be read. The bridge
// must NOT seal a grant naming generation 0 -- the phone would gate keystrokes on a
// generation the daemon does not hold. It seals a refusal instead.
//
// The item still counts as processed and does not fail the poll: Begin consumed the
// take_control (in production it opened and stored a real lease conn), so failing it here
// would hold back the inbound high-water and invite a replay of a take_control that
// already ran.
func TestCommandBridge_SeveredLeaseIsRefusedNotConfirmedAtGenerationZero(t *testing.T) {
	key := leaseTestKey()
	takeCtrl := protocol.RemoteCommand{DeviceCommandAuth: protocol.DeviceCommandAuth{
		Action: protocol.ActionTakeControl, Session: "m/s1", OperationID: "op-tc", DeviceID: "d1", Sig: "sig-tc",
	}}
	mb := &fakeMailbox{inbox: []relay.Item{{Cursor: 1, Envelope: sealRemoteCmd(t, key, 1, takeCtrl)}}}
	// gen 0: Begin returned nil, but the lease is already gone by the time the bridge
	// asks for its generation (LeaseManager.watch removed the conn).
	leases := &confirmingLeaseRouter{gen: 0}
	b := NewCommandBridge(CommandBridgeConfig{
		Mailbox:     mb,
		Forwarder:   &fakeForwarder{},
		Leases:      leases,
		Key:         key,
		EpochID:     1,
		ReplyTarget: "phone-routing-id",
	})

	n, err := b.PollOnce(context.Background())
	if err != nil {
		t.Fatalf("PollOnce: %v; the take_control was consumed, it must not fail the poll", err)
	}
	if n != 1 {
		t.Fatalf("processed %d, want 1", n)
	}
	if len(mb.replies) != 1 {
		t.Fatalf("sealed replies = %d; want 1 (the phone must still hear an outcome)", len(mb.replies))
	}

	_, ctrl := openReplyControl(t, key, mb.replies[0])
	if ctrl.Op != protocol.OpError {
		t.Fatalf("confirmation op = %q; want %q -- a grant at generation 0 names a lease that does not exist, and PB-INPUT-2 forbids gating keystrokes on it", ctrl.Op, protocol.OpError)
	}
	if ctrl.Generation != 0 {
		t.Fatalf("refusal generation = %d; want 0 (nothing may be confirmed)", ctrl.Generation)
	}
	if ctrl.OperationID != "op-tc" {
		t.Fatalf("refusal operation_id = %q; want op-tc (attributable to the take_control that asked)", ctrl.OperationID)
	}
	if ctrl.Error == "" {
		t.Fatalf("refusal carries no error text; the phone cannot show why control was denied")
	}
}

// TestCommandBridge_FailedRefusalSealSurfacesLocally (F-2): the refusal itself fails to
// append. The phone now has neither the lease nor the refusal, so the local error must
// name BOTH faults -- the refusal reason and the fact that the phone never heard it.
// Today only beginErr reaches the caller and the lost reply is silent.
func TestCommandBridge_FailedRefusalSealSurfacesLocally(t *testing.T) {
	key := leaseTestKey()
	takeCtrl := protocol.RemoteCommand{DeviceCommandAuth: protocol.DeviceCommandAuth{
		Action: protocol.ActionTakeControl, Session: "m/s1", OperationID: "op-tc", DeviceID: "d1", Sig: "sig-tc",
	}}
	beginErr := errors.New("timed out awaiting the lease grant")
	mb := &appendFailingMailbox{fakeMailbox: fakeMailbox{
		inbox: []relay.Item{{Cursor: 1, Envelope: sealRemoteCmd(t, key, 1, takeCtrl)}},
	}}
	b := NewCommandBridge(CommandBridgeConfig{
		Mailbox:     mb,
		Forwarder:   &fakeForwarder{},
		Leases:      &confirmingLeaseRouter{err: beginErr},
		Key:         key,
		EpochID:     1,
		ReplyTarget: "phone-routing-id",
	})

	_, err := b.PollOnce(context.Background())
	if err == nil {
		t.Fatalf("PollOnce = nil error; a refused lease must still surface locally")
	}
	if !errors.Is(err, beginErr) {
		t.Fatalf("PollOnce err = %v; want it to wrap the refusal reason", err)
	}
	if !errors.Is(err, errReplyAppendRefused) {
		t.Fatalf("PollOnce err = %v; want it to ALSO wrap the failed seal -- the phone got neither the lease nor the refusal, and that must not be silent", err)
	}
}
