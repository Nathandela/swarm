// FAILING-FIRST (TDD RED, GG-5) tests for the gateway command-IN + reply loop
// (agents-tracker-5mm): the reusable poll loop that reads phone-authored sealed
// command envelopes from the machine's relay mailbox, opens each under the epoch
// content key, forwards the device-signed command to the daemon (blind conduit),
// and seals the daemon's reply back to the phone mailbox. Complements RelaySink's
// journal-OUT with the command-IN half.
//
// THE CONTRACT these tests freeze (undefined symbols -> compile-fail RED):
//   - type CommandBridge; func NewCommandBridge(CommandBridgeConfig) *CommandBridge
//   - CommandBridgeConfig{ Mailbox; Forwarder; Key; EpochID; ReplyTarget }
//   - (*CommandBridge).PollOnce(ctx) (processed int, err error): reads items past
//     the durable cursor, opens+forwards each, seals+appends the reply to
//     ReplyTarget, advances the cursor, and returns how many it processed.
//   - A malformed/wrong-key envelope is skipped (fail-closed per item) without
//     wedging the loop or advancing past good items incorrectly.
package remotegw

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/remote/relay"
)

// fakeMailbox is an in-memory Mailbox: Read returns items past a cursor; Append
// records sealed replies the bridge sends back to the phone.
type fakeMailbox struct {
	mu      sync.Mutex
	inbox   []relay.Item
	replies [][]byte
	target  string
}

func (f *fakeMailbox) MailboxRead(_ context.Context, cursor uint64) ([]relay.Item, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []relay.Item
	for _, it := range f.inbox {
		if it.Cursor > cursor {
			out = append(out, it)
		}
	}
	return out, nil
}

func (f *fakeMailbox) MailboxAppend(_ context.Context, target string, env []byte) (uint64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.target = target
	f.replies = append(f.replies, env)
	return uint64(len(f.replies)), nil
}

// fakeForwarder records forwarded commands and returns a canned OK reply.
type fakeForwarder struct {
	mu   sync.Mutex
	seen []protocol.DeviceCommandAuth
	ops  []string
}

func (f *fakeForwarder) ForwardCommand(op, sessionID string, cmd protocol.DeviceCommandAuth, _ *protocol.LaunchReq) (protocol.Control, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seen = append(f.seen, cmd)
	f.ops = append(f.ops, op)
	return protocol.Control{Op: protocol.OpOK, SessionID: sessionID}, nil
}

func sealedCmd(t *testing.T, key crypto.ContentKey, seq uint64, cmd protocol.DeviceCommandAuth) []byte {
	t.Helper()
	plain, err := json.Marshal(cmd)
	if err != nil {
		t.Fatalf("marshal cmd: %v", err)
	}
	env, err := crypto.SealMailbox(key, crypto.EnvelopeHeader{Version: crypto.VersionV1, EpochID: 1, Seq: seq}, plain)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	return env.Marshal()
}

func TestCommandBridge_PollOpensForwardsAndSealsReply(t *testing.T) {
	var key crypto.ContentKey
	for i := range key {
		key[i] = byte(i + 7)
	}
	mb := &fakeMailbox{inbox: []relay.Item{
		{Cursor: 1, Envelope: sealedCmd(t, key, 1, protocol.DeviceCommandAuth{Action: protocol.ActionKill, Session: "m/s1", OperationID: "op-1", DeviceID: "d1", Sig: "s1"})},
		{Cursor: 2, Envelope: sealedCmd(t, key, 2, protocol.DeviceCommandAuth{Action: protocol.ActionDelete, Session: "m/s2", OperationID: "op-2", DeviceID: "d1", Sig: "s2"})},
	}}
	fwd := &fakeForwarder{}
	b := NewCommandBridge(CommandBridgeConfig{
		Mailbox:     mb,
		Forwarder:   fwd,
		Key:         key,
		EpochID:     1,
		ReplyTarget: "phone-routing-id",
	})

	n, err := b.PollOnce(context.Background())
	if err != nil {
		t.Fatalf("PollOnce: %v", err)
	}
	if n != 2 {
		t.Fatalf("processed %d, want 2", n)
	}
	// Both commands were forwarded with the right ops and preserved signatures.
	if len(fwd.ops) != 2 || fwd.ops[0] != protocol.OpKill || fwd.ops[1] != protocol.OpDelete {
		t.Fatalf("forwarded ops = %v, want [kill delete]", fwd.ops)
	}
	if fwd.seen[0].Sig != "s1" || fwd.seen[1].Sig != "s2" {
		t.Fatalf("device signatures not forwarded intact: %+v", fwd.seen)
	}
	// A sealed reply went to the phone for each command.
	if len(mb.replies) != 2 {
		t.Fatalf("sealed replies = %d, want 2", len(mb.replies))
	}
	if mb.target != "phone-routing-id" {
		t.Fatalf("reply target = %q, want phone-routing-id", mb.target)
	}
	// Each reply opens under the content key back to an OK control.
	for i, env := range mb.replies {
		e, err := crypto.ParseEnvelope(env)
		if err != nil {
			t.Fatalf("reply %d parse: %v", i, err)
		}
		plain, err := crypto.OpenMailbox(key, e)
		if err != nil {
			t.Fatalf("reply %d open: %v", i, err)
		}
		var ctrl protocol.Control
		if err := json.Unmarshal(plain, &ctrl); err != nil {
			t.Fatalf("reply %d decode: %v", i, err)
		}
		if ctrl.Op != protocol.OpOK {
			t.Errorf("reply %d op = %q, want ok", i, ctrl.Op)
		}
	}

	// The cursor advanced past both items: a second poll processes nothing.
	n2, err := b.PollOnce(context.Background())
	if err != nil {
		t.Fatalf("second PollOnce: %v", err)
	}
	if n2 != 0 {
		t.Fatalf("second poll processed %d, want 0 (cursor did not advance)", n2)
	}
}

func TestCommandBridge_MalformedEnvelopeSkippedNotWedged(t *testing.T) {
	var key crypto.ContentKey
	for i := range key {
		key[i] = byte(i + 7)
	}
	mb := &fakeMailbox{inbox: []relay.Item{
		{Cursor: 1, Envelope: []byte("not-a-valid-envelope")},
		{Cursor: 2, Envelope: sealedCmd(t, key, 2, protocol.DeviceCommandAuth{Action: protocol.ActionKill, Session: "m/s1", OperationID: "op-2", DeviceID: "d1", Sig: "s2"})},
	}}
	fwd := &fakeForwarder{}
	b := NewCommandBridge(CommandBridgeConfig{Mailbox: mb, Forwarder: fwd, Key: key, EpochID: 1, ReplyTarget: "phone"})

	n, _ := b.PollOnce(context.Background())
	// The good item is still processed even though the first was malformed.
	if n != 1 {
		t.Fatalf("processed %d good items, want 1 (malformed item must be skipped, not wedge the loop)", n)
	}
	if len(fwd.seen) != 1 || fwd.seen[0].OperationID != "op-2" {
		t.Fatalf("the valid command after a malformed one was not forwarded: %+v", fwd.seen)
	}
	// The cursor advanced past BOTH (the malformed one is not retried forever).
	n2, _ := b.PollOnce(context.Background())
	if n2 != 0 {
		t.Fatalf("second poll processed %d, want 0 (cursor must advance past the skipped malformed item)", n2)
	}
}
