package relay

// The storage cursor is the relay's OWN coordinate, but the value a reader asks to resume
// FROM arrives over the wire and is therefore untrusted input to the store. readItemsPage
// computed its scan start as `afterCursor + 1`, which for MaxUint64 wraps to zero -- so the
// one cursor value that means "past every item this mailbox can ever hold" was read as
// "from the beginning", and the relay re-served the whole mailbox on every read.
//
// Both read paths (mailbox_read at server.go and the server-side wait at wait.go) share
// readItemsPage, so the boundary is fenced once, there.

import (
	"math"
	"testing"
)

// TestMailboxRead_MaxUint64CursorDoesNotWrapAndReserveTheWholeMailbox drives a REAL
// authenticated client against a REAL relay and asks to resume past MaxUint64. Nothing can
// be strictly greater than MaxUint64, so the honest answer is an empty page.
func TestMailboxRead_MaxUint64CursorDoesNotWrapAndReserveTheWholeMailbox(t *testing.T) {
	srv, _, _, clk := startTestRelay(t, nil)
	machine, device, devRID, sp := mailboxFixture(t, srv, clk)

	for i := uint64(1); i <= 3; i++ {
		env := sp.sealMailbox(t, i, []byte{byte('a' + i)}, clk)
		if _, err := machine.MailboxAppend(testCtx(t), devRID, env); err != nil {
			t.Fatalf("MailboxAppend #%d: %v", i, err)
		}
	}

	// Premise: the mailbox really does hold three items.
	all, err := device.MailboxRead(testCtx(t), 0)
	if err != nil {
		t.Fatalf("premise MailboxRead(0): %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("premise: mailbox holds %d items, want 3", len(all))
	}

	items, err := device.MailboxRead(testCtx(t), math.MaxUint64)
	if err != nil {
		t.Fatalf("MailboxRead(MaxUint64): %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("MailboxRead(MaxUint64) returned %d items, want 0: the scan start "+
			"`afterCursor+1` wrapped to zero, so the relay re-serves the ENTIRE mailbox "+
			"on every read from that cursor", len(items))
	}
}
