package relay

// R-REL.4 — per-device mailbox append / read-from-cursor / ack. The relay
// assigns its OWN monotonic STORAGE cursor (untrusted ordering), kept DISTINCT
// from the authenticated per-epoch seq the device trusts (R-CRY.12); ack
// advances a durable consumed cursor for compaction.

import (
	"crypto/ed25519"
	"errors"
	"testing"

	"github.com/Nathandela/swarm/internal/remote/crypto"
)

// mailboxFixture wires an authenticated machine + an authorized device and
// returns the device's routing id and a content-key sealing party.
func mailboxFixture(t *testing.T, srv *Server, clk *fakeClock) (machine, device *Client, devRID string, sp sealParty) {
	t.Helper()
	mPub, mPriv := newRelayAuthKey(t)
	dPub, dPriv := newRelayAuthKey(t)
	machine = dialAuthed(t, srv.URL(), authFor(mPub, mPriv))
	device = dialAuthed(t, srv.URL(), authFor(dPub, dPriv))
	if err := machine.AuthorizeDevice(testCtx(t), ed25519.PublicKey(dPub)); err != nil {
		t.Fatalf("AuthorizeDevice: %v", err)
	}
	devRID = RoutingID(dPub)
	sp = newSealParty(t, []byte("machine-sender-pub-000000000000x"), []byte("device-recipient-pub-0000000000x"))
	return machine, device, devRID, sp
}

// TestMailbox_AppendReadAckCursor asserts the relay storage cursor is monotonic,
// read-from-cursor returns only items strictly after the cursor, and ack advances
// a durable consumed watermark.
func TestMailbox_AppendReadAckCursor(t *testing.T) {
	srv, _, _, clk := startTestRelay(t, nil)
	machine, device, devRID, sp := mailboxFixture(t, srv, clk)

	var cursors []uint64
	for i := uint64(1); i <= 3; i++ {
		env := sp.sealMailbox(t, i, []byte{byte('a' + i)}, clk)
		cur, err := machine.MailboxAppend(testCtx(t), devRID, env)
		if err != nil {
			t.Fatalf("MailboxAppend #%d: %v", i, err)
		}
		cursors = append(cursors, cur)
	}
	for i := 1; i < len(cursors); i++ {
		if cursors[i] <= cursors[i-1] {
			t.Fatalf("storage cursor not monotonic: %v", cursors)
		}
	}

	items, err := device.MailboxRead(testCtx(t), 0)
	if err != nil {
		t.Fatalf("MailboxRead(0): %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("read-from-0 count: got %d, want 3", len(items))
	}

	// Read strictly after the first item's cursor -> only the last two.
	rest, err := device.MailboxRead(testCtx(t), items[0].Cursor)
	if err != nil {
		t.Fatalf("MailboxRead(after first): %v", err)
	}
	if len(rest) != 2 {
		t.Fatalf("read-after-cursor count: got %d, want 2", len(rest))
	}

	// Ack through the last cursor: acked items are compacted away.
	if err := device.MailboxAck(testCtx(t), items[len(items)-1].Cursor); err != nil {
		t.Fatalf("MailboxAck: %v", err)
	}
	after, err := device.MailboxRead(testCtx(t), 0)
	if err != nil {
		t.Fatalf("MailboxRead after ack: %v", err)
	}
	if len(after) != 0 {
		t.Fatalf("acked items still present: got %d, want 0", len(after))
	}
}

// TestMailbox_RelayReorderStillDetected proves the storage cursor is untrusted:
// even if the relay serves items out of storage order, the authenticated seq
// (R-CRY.12) still detects the reorder end to end, and the storage cursor is a
// DIFFERENT coordinate from the authenticated seq.
func TestMailbox_RelayReorderStillDetected(t *testing.T) {
	srv, _, _, clk := startTestRelay(t, nil)
	machine, device, devRID, sp := mailboxFixture(t, srv, clk)

	// Authenticated seq starts at 5 while the relay's storage cursor starts at
	// its own base — the two coordinate systems are deliberately offset.
	const firstSeq = 5
	for i := 0; i < 3; i++ {
		env := sp.sealMailbox(t, uint64(firstSeq+i), []byte{byte('x' + i)}, clk)
		if _, err := machine.MailboxAppend(testCtx(t), devRID, env); err != nil {
			t.Fatalf("MailboxAppend #%d: %v", i, err)
		}
	}

	items, err := device.MailboxRead(testCtx(t), 0)
	if err != nil {
		t.Fatalf("MailboxRead: %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("read count: got %d, want 3", len(items))
	}
	// Storage cursor is distinct from the authenticated seq: cursor != seq.
	first, err := crypto.ParseEnvelope(items[0].Envelope)
	if err != nil {
		t.Fatalf("ParseEnvelope: %v", err)
	}
	if items[0].Cursor == first.Header.Seq {
		t.Fatalf("storage cursor collides with authenticated seq (%d) — they must be distinct coordinates", first.Header.Seq)
	}

	// Simulate the relay serving out of storage order: feed the highest-seq item
	// first, then a lower-seq item. The authenticated receiver rejects the reorder.
	rcv := crypto.NewMailboxReceiver()
	last, err := crypto.ParseEnvelope(items[2].Envelope)
	if err != nil {
		t.Fatalf("ParseEnvelope last: %v", err)
	}
	if _, err := rcv.Accept(sp.keys.ContentKey, last); err != nil {
		t.Fatalf("Accept(highest seq) unexpectedly failed: %v", err)
	}
	lower, err := crypto.ParseEnvelope(items[1].Envelope)
	if err != nil {
		t.Fatalf("ParseEnvelope lower: %v", err)
	}
	if _, err := rcv.Accept(sp.keys.ContentKey, lower); !errors.Is(err, crypto.ErrStaleSeq) {
		t.Fatalf("reorder served by the relay went undetected: got %v, want ErrStaleSeq", err)
	}
}
