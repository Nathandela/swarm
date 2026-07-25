package remotegw

// FAILING-FIRST (TDD RED, GG-5) tests for the first verb PB-SYNC-7 folds in: the LEASE
// CONFIRMATION. routeCommand seals NO reply for take_control (command_loop.go: Begin,
// then `return nil`), so PB-INPUT-2's "no keystroke is ever sent without a confirmed
// current lease generation" has nothing to confirm against -- the phone can only assume
// the lease, and assuming it is exactly what PB-INPUT-2 forbids. The gateway already
// HOLDS the generation (LeaseManager.Generation, captured from the daemon's OpLease
// grant); nothing carries it back.
//
// THE SEAM these tests pin (undefined symbol -> compile-fail RED):
//
//	LeaseRouter gains: Generation(session string) uint64   // *LeaseManager ALREADY implements it
//
// and the behavior: a take_control seals a confirmation onto the command-reply bucket,
// and a FAILED take_control seals an error reply -- never silence.
//
// Implementer note: LeaseRouter lives in command_loop.go, which slice S2 is editing. Two
// lines there are enough (the interface method, and calling the confirm helper in the
// take_control arm); the helper itself belongs in a new file. fakeLeaseRouter
// (mailbox_route_test.go) needs the one new method to keep satisfying the interface.

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"testing"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/remote/relay"
)

// TestLeaseRouter_RequiresGeneration is the adversarial pin (9 rule 5) for the seam
// itself. The lazy way to pass the two behavior tests below is a call-site type assert
// (`if g, ok := b.cfg.Leases.(interface{ Generation(string) uint64 }); ok`), which makes
// the confirmation OPTIONAL -- a router that does not implement it silently reverts to
// today's silence, and PB-INPUT-2 has nothing to confirm against again. Reporting the
// granted generation is part of what it means to route leases, so it belongs on the
// interface. (*LeaseManager already implements it.)
func TestLeaseRouter_RequiresGeneration(t *testing.T) {
	rt := reflect.TypeOf((*LeaseRouter)(nil)).Elem()
	if _, ok := rt.MethodByName("Generation"); !ok {
		t.Fatalf("LeaseRouter has no Generation method; the lease confirmation must not be an optional type-assert")
	}
}

// confirmingLeaseRouter is a LeaseRouter that reports a lease generation, so the bridge
// has something to confirm with. The compile-time assertion below is the seam pin:
// Generation must be part of the interface the bridge routes through, not a method that
// only the concrete LeaseManager happens to have.
type confirmingLeaseRouter struct {
	mu     sync.Mutex
	gen    uint64
	err    error
	begins []protocol.RemoteCommand
}

var _ LeaseRouter = (*confirmingLeaseRouter)(nil)

func (f *confirmingLeaseRouter) Begin(cmd protocol.RemoteCommand) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.begins = append(f.begins, cmd)
	return f.err
}

func (f *confirmingLeaseRouter) Input(string, InputFrame) error { return nil }

func (f *confirmingLeaseRouter) End(string) {}

func (f *confirmingLeaseRouter) Generation(string) uint64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.gen
}

// openReplyControl opens one sealed reply envelope back to its Control.
func openReplyControl(t *testing.T, key crypto.ContentKey, raw []byte) (crypto.EnvelopeHeader, protocol.Control) {
	t.Helper()
	env, err := crypto.ParseEnvelope(raw)
	if err != nil {
		t.Fatalf("parse reply envelope: %v", err)
	}
	plain, err := crypto.OpenMailbox(key, env)
	if err != nil {
		t.Fatalf("open reply envelope: %v", err)
	}
	var ctrl protocol.Control
	if err := json.Unmarshal(plain, &ctrl); err != nil {
		t.Fatalf("decode reply control: %v", err)
	}
	return env.Header, ctrl
}

func leaseTestKey() crypto.ContentKey {
	var key crypto.ContentKey
	for i := range key {
		key[i] = byte(i + 7)
	}
	return key
}

// TestCommandBridge_TakeControlSealsALeaseConfirmation: a granted take_control returns a
// confirmation the phone can gate keystrokes on -- the daemon's lease GENERATION, tagged
// with the take_control's operation_id so it is attributable, on the command-reply
// bucket (SenderKeyID zero, its own seq stream -- command_in.go's deliberate split).
func TestCommandBridge_TakeControlSealsALeaseConfirmation(t *testing.T) {
	key := leaseTestKey()
	takeCtrl := protocol.RemoteCommand{DeviceCommandAuth: protocol.DeviceCommandAuth{
		Action: protocol.ActionTakeControl, Session: "m/s1", OperationID: "op-tc", DeviceID: "d1", Sig: "sig-tc",
	}}
	mb := &fakeMailbox{inbox: []relay.Item{{Cursor: 1, Envelope: sealRemoteCmd(t, key, 1, takeCtrl)}}}
	leases := &confirmingLeaseRouter{gen: 42}
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
		t.Fatalf("PollOnce: %v", err)
	}
	if n != 1 {
		t.Fatalf("processed %d, want 1", n)
	}
	if len(leases.begins) != 1 {
		t.Fatalf("Begin called %d times; want 1", len(leases.begins))
	}
	if len(mb.replies) != 1 {
		t.Fatalf("sealed replies = %d; want 1 (a granted lease must be CONFIRMED, not assumed)", len(mb.replies))
	}
	if mb.target != "phone-routing-id" {
		t.Fatalf("confirmation target = %q; want the phone routing id", mb.target)
	}

	hdr, ctrl := openReplyControl(t, key, mb.replies[0])
	if ctrl.Op != protocol.OpLease {
		t.Fatalf("confirmation op = %q; want %q (the lease grant the phone gates input on)", ctrl.Op, protocol.OpLease)
	}
	if ctrl.Generation != 42 {
		t.Fatalf("confirmation generation = %d; want 42 (the daemon-granted lease generation)", ctrl.Generation)
	}
	if ctrl.SessionID != "m/s1" {
		t.Fatalf("confirmation session = %q; want m/s1", ctrl.SessionID)
	}
	if ctrl.OperationID != "op-tc" {
		t.Fatalf("confirmation operation_id = %q; want op-tc (attributable to the take_control that asked)", ctrl.OperationID)
	}
	if hdr.SenderKeyID != [8]byte{} {
		t.Fatalf("confirmation SenderKeyID = %v; want zero (the command-reply bucket, per command_in.go)", hdr.SenderKeyID)
	}
}

// TestCommandBridge_FailedTakeControlSealsAnErrorNotSilence: when the lease is NOT
// granted the phone must be told, tagged with the same operation_id and carrying no
// generation. Today a failed Begin returns an error into the poll aggregate and the
// phone hears nothing at all -- indistinguishable from a lease that is merely slow,
// which is how a keystroke gets sent against a lease that does not exist.
func TestCommandBridge_FailedTakeControlSealsAnErrorNotSilence(t *testing.T) {
	key := leaseTestKey()
	takeCtrl := protocol.RemoteCommand{DeviceCommandAuth: protocol.DeviceCommandAuth{
		Action: protocol.ActionTakeControl, Session: "m/s1", OperationID: "op-tc", DeviceID: "d1", Sig: "sig-tc",
	}}
	mb := &fakeMailbox{inbox: []relay.Item{{Cursor: 1, Envelope: sealRemoteCmd(t, key, 1, takeCtrl)}}}
	leases := &confirmingLeaseRouter{err: errors.New("timed out awaiting the lease grant")}
	b := NewCommandBridge(CommandBridgeConfig{
		Mailbox:     mb,
		Forwarder:   &fakeForwarder{},
		Leases:      leases,
		Key:         key,
		EpochID:     1,
		ReplyTarget: "phone-routing-id",
	})

	if _, err := b.PollOnce(context.Background()); err == nil {
		t.Fatalf("PollOnce = nil error; a refused lease must still surface locally")
	}
	if len(mb.replies) != 1 {
		t.Fatalf("sealed replies = %d; want 1 (silence is indistinguishable from a slow grant)", len(mb.replies))
	}
	_, ctrl := openReplyControl(t, key, mb.replies[0])
	if ctrl.Op != protocol.OpError {
		t.Fatalf("refusal op = %q; want %q", ctrl.Op, protocol.OpError)
	}
	if ctrl.OperationID != "op-tc" {
		t.Fatalf("refusal operation_id = %q; want op-tc", ctrl.OperationID)
	}
	if ctrl.Generation != 0 {
		t.Fatalf("refusal generation = %d; want 0 (nothing was granted, so nothing may be confirmed)", ctrl.Generation)
	}
	if ctrl.Error == "" {
		t.Fatalf("refusal carries no error text; the phone cannot show why control was denied")
	}
}
