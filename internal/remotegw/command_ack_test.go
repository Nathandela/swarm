// FAILING-FIRST (TDD RED, GG-5) tests for slice GW-M1: MailboxAck + durable
// cursor (agents-tracker-5mm). CommandBridge.PollOnce currently reads command
// items from the machine's relay mailbox and advances its IN-MEMORY cursor past
// everything it consumed, but it never tells the relay to ack (delete) those
// items from the durable mailbox store. That means a gateway restart -- a fresh
// process with a fresh in-memory cursor=0 -- re-reads and re-processes every
// command still sitting in the relay's durable bbolt store, up to the relay's
// full retention window.
//
// The real relay already supports acking: relay.Client.MailboxAck(ctx, cursor)
// -> server handleMailboxAck -> store.ackItems physically deletes every item at
// or below cursor from the durable store (internal/remote/relay/store.go
// ackItems). So the relay's durable store IS the durable cursor: if the bridge
// acks each consumed item, a restarted bridge reading from cursor 0 simply never
// sees the already-acked items again -- no local persistence file needed.
//
// THE CONTRACT these tests freeze (assertion-fail -> RED today; the Mailbox
// interface in command_loop.go does not yet require MailboxAck, and PollOnce
// never calls it):
//   - PollOnce, having consumed items up to some highest cursor C, must ack C
//     against the mailbox (an ack call recorder must show the highest consumed
//     cursor).
//   - Because acking purges the durable store, a bridge built fresh over the
//     SAME mailbox after PollOnce has already acked must not re-forward a
//     command that was already consumed and acked by a prior bridge instance.
package remotegw

import (
	"context"
	"sync"
	"testing"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/remote/relay"
)

// ackRecordingMailbox wraps fakeMailbox and additionally records every
// MailboxAck call. It does NOT purge on ack -- it exists only to prove PollOnce
// invokes the ack call with the right cursor.
type ackRecordingMailbox struct {
	fakeMailbox
	acked []uint64
}

func (m *ackRecordingMailbox) MailboxAck(_ context.Context, cursor uint64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.acked = append(m.acked, cursor)
	return nil
}

func TestCommandBridge_PollOnceAcksConsumedItems(t *testing.T) {
	var key crypto.ContentKey
	for i := range key {
		key[i] = byte(i + 3)
	}
	mb := &ackRecordingMailbox{fakeMailbox: fakeMailbox{inbox: []relay.Item{
		{Cursor: 1, Envelope: sealedCmd(t, key, 1, protocol.DeviceCommandAuth{Action: protocol.ActionKill, Session: "m/s1", OperationID: "op-1", DeviceID: "d1", Sig: "s1"})},
		{Cursor: 2, Envelope: sealedCmd(t, key, 2, protocol.DeviceCommandAuth{Action: protocol.ActionDelete, Session: "m/s2", OperationID: "op-2", DeviceID: "d1", Sig: "s2"})},
	}}}
	fwd := &fakeForwarder{}
	b := NewCommandBridge(CommandBridgeConfig{
		Mailbox:     mb,
		Forwarder:   fwd,
		Key:         key,
		EpochID:     1,
		ReplyTarget: "phone-routing-id",
	})

	if _, err := b.PollOnce(context.Background()); err != nil {
		t.Fatalf("PollOnce: %v", err)
	}

	mb.mu.Lock()
	acked := append([]uint64(nil), mb.acked...)
	mb.mu.Unlock()

	if len(acked) == 0 {
		t.Fatalf("PollOnce never called MailboxAck: consumed commands are never durably acked, so a gateway restart will replay them from the relay's mailbox (up to its full retention window)")
	}
	if got := acked[len(acked)-1]; got != 2 {
		t.Fatalf("MailboxAck highest cursor = %d, want 2 (the highest cursor consumed this poll)", got)
	}
}

// ackPurgingItem pairs a relay.Item with the cursor it lives at, so
// ackPurgingMailbox can delete by cursor the same way the real relay store's
// ackItems does (internal/remote/relay/store.go).
type ackPurgingMailbox struct {
	mu     sync.Mutex
	inbox  []relay.Item
	acked  []uint64
	target string
}

// MailboxRead mirrors relay.Client.MailboxRead: items with cursor strictly
// greater than the given cursor, from whatever remains in the durable store.
func (m *ackPurgingMailbox) MailboxRead(_ context.Context, cursor uint64) ([]relay.Item, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []relay.Item
	for _, it := range m.inbox {
		if it.Cursor > cursor {
			out = append(out, it)
		}
	}
	return out, nil
}

func (m *ackPurgingMailbox) MailboxAppend(_ context.Context, target string, env []byte) (uint64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.target = target
	return 0, nil
}

// MailboxAck mirrors store.ackItems: it PHYSICALLY DELETES every item at or
// below cursor from the durable backing store, so a later MailboxRead (even
// from a fresh reader at cursor 0) never sees them again.
func (m *ackPurgingMailbox) MailboxAck(_ context.Context, cursor uint64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.acked = append(m.acked, cursor)
	kept := m.inbox[:0]
	for _, it := range m.inbox {
		if it.Cursor > cursor {
			kept = append(kept, it)
		}
	}
	m.inbox = kept
	return nil
}

func TestCommandBridge_RestartDoesNotReplayAckedCommands(t *testing.T) {
	var key crypto.ContentKey
	for i := range key {
		key[i] = byte(i + 5)
	}
	mb := &ackPurgingMailbox{inbox: []relay.Item{
		{Cursor: 1, Envelope: sealedCmd(t, key, 1, protocol.DeviceCommandAuth{Action: protocol.ActionKill, Session: "m/s1", OperationID: "op-1", DeviceID: "d1", Sig: "s1"})},
	}}
	// A single forwarder shared across both bridge instances -- the assertion
	// below is about total forwards across a simulated restart, not about either
	// bridge in isolation.
	fwd := &fakeForwarder{}

	b1 := NewCommandBridge(CommandBridgeConfig{
		Mailbox:     mb,
		Forwarder:   fwd,
		Key:         key,
		EpochID:     1,
		ReplyTarget: "phone-routing-id",
	})
	if _, err := b1.PollOnce(context.Background()); err != nil {
		t.Fatalf("b1 PollOnce: %v", err)
	}

	// Simulate a gateway restart: a brand-new CommandBridge over the SAME
	// mailbox, with a fresh in-memory cursor (0) and a fresh crypto.MailboxReceiver
	// (no memory of b1's sequence numbers) -- exactly what happens when the
	// gateway process restarts today, since nothing durable survives it.
	b2 := NewCommandBridge(CommandBridgeConfig{
		Mailbox:     mb,
		Forwarder:   fwd,
		Key:         key,
		EpochID:     1,
		ReplyTarget: "phone-routing-id",
	})
	if _, err := b2.PollOnce(context.Background()); err != nil {
		t.Fatalf("b2 PollOnce: %v", err)
	}

	fwd.mu.Lock()
	total := len(fwd.seen)
	fwd.mu.Unlock()

	if total != 1 {
		t.Fatalf("command forwarded %d times across a restart, want exactly 1: PollOnce never acks consumed items, so the relay's durable mailbox still holds cursor 1 after b1, and a restarted bridge (b2) re-reads and re-forwards it", total)
	}
}
