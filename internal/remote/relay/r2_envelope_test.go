package relay

// R2 review H-1 — regression test for the single-oversized-envelope brick. The
// mailbox_read reply framing ({"has_more":..,"items":[{"cursor":<digits>,
// "envelope":".."}]}) is larger than the mailbox_append framing, so an envelope
// that just fits an append frame can, once the storage cursor reaches enough
// digits, produce a read reply that exceeds MaxFrame -> WriteFrame writes nothing,
// the socket tears, the item is never delivered and the read re-fails forever: a
// permanent brick, exactly what CR-4 must prevent. The relay must refuse such an
// envelope at APPEND time so every stored item is always readable within one page.

import (
	"testing"
)

func TestMailbox_AppendRejectsUnreadableEnvelope(t *testing.T) {
	srv, _, _, clk := startTestRelay(t, func(c *Config) {
		c.Quotas.MailboxAppendPerMin = 100_000 // keep the append-rate window out of the way
	})
	machine, device, devRID, _ := mailboxFixture(t, srv, clk)

	// 786370 bytes: base64 = 1048496, which leaves ~20 bytes of headroom under the
	// append frame (so the append frame itself is legal and this is a SERVER-side
	// decision, not a client WriteFrame rejection), yet a mailbox_read reply carrying
	// it alone would exceed MaxFrame at a multi-digit cursor. The relay must refuse it.
	unreadable := make([]byte, 786370)
	if _, err := machine.MailboxAppend(testCtx(t), devRID, unreadable); err == nil {
		t.Fatalf("append of a %d-byte envelope was accepted; a mailbox_read carrying it alone can exceed MaxFrame and permanently brick the read (R2 review H-1)", len(unreadable))
	}

	// The refusal is clean, not a tear: a readable-sized envelope still appends and
	// reads back fine on the same connections.
	ok := make([]byte, 700_000)
	if _, err := machine.MailboxAppend(testCtx(t), devRID, ok); err != nil {
		t.Fatalf("append of a readable %d-byte envelope: %v (the oversized refusal must be clean)", len(ok), err)
	}
	items, more, err := device.MailboxReadPage(testCtx(t), 0, 0)
	if err != nil {
		t.Fatalf("MailboxReadPage after the readable append: %v", err)
	}
	if len(items) != 1 || more {
		t.Fatalf("want exactly 1 readable item and has_more=false, got %d items has_more=%v", len(items), more)
	}
}
