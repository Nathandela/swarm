package remotegw

// The relay MINTS relay.Item.Cursor -- the field's own doc calls it "the relay's own
// monotonic storage cursor (UNTRUSTED ordering)" -- and the bridge adopts it as its DURABLE
// resume point. processBatch took the batch maximum from EVERY item it read, BEFORE handle()
// and regardless of whether the envelope opened, so a relay needed no key at all: six bytes
// of garbage beside a cursor of its choosing moved the resume point past every real command,
// durably, and the ack that followed ordered the relay to compact the undelivered backlog.
//
// The fence here is what the bridge has EVIDENCE for: a cursor is adopted only from an item
// the bridge actually HANDLED. It does not (and cannot) validate the VALUE a handled item
// carries -- that is the relay's own coordinate -- so a relay that rewrites the cursor of a
// GENUINE phone-sealed item can still move the resume point. That residual needs a bound on
// how far a cursor may move per page, which is a number no requirement states.

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/Nathandela/swarm/internal/protocol"
)

// unreachableForwarder is the daemon socket being down: the command NEVER reached it, which
// is the case an ack would destroy the only copy of.
type unreachableForwarder struct{}

func (unreachableForwarder) ForwardCommand(_ string, _ protocol.RemoteCommand) (protocol.Control, error) {
	return protocol.Control{}, errors.New("dial remote.sock: connection refused")
}

// poisonCursor is "past every cursor a real mailbox can mint". MaxUint64 is deliberately
// NOT used: it exercises the store's own scan-start wrap instead (see relay's
// cursorwrap_test.go), which is a different defect.
const poisonCursor = uint64(1) << 63

// TestCommandBridge_AnUnopenableItemNeverMovesTheResumePoint drives the REAL bridge over the
// REAL durable InboundState. The adversary is the CONNECTION -- the mailbox the bridge reads
// -- not a constant this test transcribes into the bridge.
func TestCommandBridge_AnUnopenableItemNeverMovesTheResumePoint(t *testing.T) {
	key := inboundKey(31)
	const epoch uint32 = 7
	path := filepath.Join(t.TempDir(), "inbound.json")
	st, err := OpenInboundState(path, "machine-1")
	if err != nil {
		t.Fatalf("OpenInboundState: %v", err)
	}
	rl := &retainingRelay{}
	rl.add(1, sealAt(t, key, epoch, 1, killCmd("m/s1", "op-1")))

	fwd := &fakeForwarder{}
	b := NewCommandBridge(CommandBridgeConfig{
		Mailbox: rl, Forwarder: fwd, Key: key, EpochID: epoch, ReplyTarget: "phone", Inbound: st,
	})
	if _, err := b.PollOnce(context.Background()); err != nil {
		t.Fatalf("premise PollOnce: %v", err)
	}
	if len(fwd.seen) != 1 {
		t.Fatalf("premise: forwarded %d commands, want 1", len(fwd.seen))
	}

	// NOT AN ENVELOPE AT ALL, beside a cursor the relay chose.
	rl.add(poisonCursor, []byte("poison"))
	if _, err := b.PollOnce(context.Background()); err == nil {
		t.Fatalf("the unopenable item was reported as success; it cannot open and must surface")
	}
	if got := b.Cursor(); got >= poisonCursor {
		t.Fatalf("after ONE unopenable item the resume point is %d: the bridge adopted a cursor "+
			"from an item it could not handle, so every later read asks for items past it", got)
	}

	// Every later command, the relay behaving honestly again.
	rl.add(2, sealAt(t, key, epoch, 2, killCmd("m/s1", "op-2")))
	rl.add(3, sealAt(t, key, epoch, 3, killCmd("m/s1", "op-3")))
	if _, err := b.PollOnce(context.Background()); err == nil {
		t.Fatalf("the retained unopenable item must keep surfacing as an error")
	}
	if len(fwd.seen) != 3 {
		t.Fatalf("forwarded %d of 3 commands after the unopenable item", len(fwd.seen))
	}

	// The resume point is the highest cursor the bridge HANDLED, never the poison.
	if got := b.Cursor(); got != 3 {
		t.Fatalf("resume point = %d, want 3 (the highest handled item)", got)
	}

	// Durable: a restarted gateway seeds from disk, and what is on disk is the handled cursor.
	st2, err := OpenInboundState(path, "machine-1")
	if err != nil {
		t.Fatalf("reopen InboundState: %v", err)
	}
	if ck := st2.Load(); ck.Cursor != 3 {
		t.Fatalf("persisted cursor = %d, want 3", ck.Cursor)
	}

	// And the ack never orders the relay to compact past what was handled: ackItems deletes
	// every item at or below the acked cursor.
	rl.mu.Lock()
	acked := append([]uint64(nil), rl.acked...)
	rl.mu.Unlock()
	for _, a := range acked {
		if a > 3 {
			t.Fatalf("acked cursor %d (acks: %v): the gateway ordered the destruction of items "+
				"it never consumed", a, acked)
		}
	}
}

// TestCommandBridge_AFailedForwardDoesNotAckTheCommandAway pins the other half of the same
// rule at the OTHER failure class: an item that opened but whose daemon forward failed was
// never delivered, so acking it -- which compacts it out of the relay's store -- destroys the
// only copy. The cursor stops below it.
func TestCommandBridge_AFailedForwardDoesNotAckTheCommandAway(t *testing.T) {
	key := inboundKey(32)
	const epoch uint32 = 3
	rl := &retainingRelay{}
	rl.add(1, sealAt(t, key, epoch, 1, killCmd("m/s1", "op-1")))

	fwd := unreachableForwarder{}
	b := NewCommandBridge(CommandBridgeConfig{
		Mailbox: rl, Forwarder: fwd, Key: key, EpochID: epoch, ReplyTarget: "phone",
	})
	if _, err := b.PollOnce(context.Background()); err == nil {
		t.Fatalf("PollOnce reported success although the forward failed")
	}
	if got := b.Cursor(); got != 0 {
		t.Fatalf("resume point = %d, want 0: the command never reached the daemon", got)
	}
	rl.mu.Lock()
	acked := append([]uint64(nil), rl.acked...)
	rl.mu.Unlock()
	if len(acked) != 0 {
		t.Fatalf("acked %v after a failed forward: the relay is told to compact away a command "+
			"the daemon never received", acked)
	}
}
