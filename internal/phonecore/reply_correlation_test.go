package phonecore

// FAILING-FIRST (TDD RED, GG-5) tests for the second verb PB-SYNC-7 folds in: REPLY
// CORRELATION on the phone side. ReplyCache is an unkeyed FIFO, so a phone with two ops
// in flight cannot tell which reply answers which -- yet PB-SYNC-2 (repair a
// command-reply gap "via the durable operation outcome"), PB-STATE-1 (persist "pending
// idempotent ops and their outcomes"), PB-INPUT-4 (retry keyed on the reply's error
// code) and PB-APP-9 all need exactly that attribution. Control already carries the
// operation_id tag; the cache has to key on it.
//
// THE SEAM these tests pin (undefined symbol -> compile-fail RED):
//
//	func (*ReplyCache) TakeFor(operationID string) (schema.Control, bool)
//
// Take (FIFO drain) stays: an uncorrelated reply must remain drainable, never silently
// discarded.

import (
	"testing"

	"github.com/Nathandela/swarm/internal/protocol"
)

// TestReplyCache_TakeForAttributesByOperationID: replies are claimed by the op that
// authored them, in ANY arrival order, and each is consumed exactly once.
func TestReplyCache_TakeForAttributesByOperationID(t *testing.T) {
	c := NewReplyCache()
	c.Append(protocol.Control{Op: protocol.OpOK, SessionID: "m/s1", OperationID: "op-1"})
	c.Append(protocol.Control{Op: protocol.OpError, SessionID: "m/s2", OperationID: "op-2", ErrorCode: protocol.CodeRateLimit})

	// The SECOND op's reply is claimed first: attribution is by tag, not by position.
	got, ok := c.TakeFor("op-2")
	if !ok {
		t.Fatalf("TakeFor(op-2) found nothing; replies cannot be attributed")
	}
	if got.OperationID != "op-2" || got.ErrorCode != protocol.CodeRateLimit {
		t.Fatalf("TakeFor(op-2) = %+v; want the op-2 reply with its error code intact", got)
	}
	if _, ok := c.TakeFor("op-2"); ok {
		t.Fatalf("TakeFor(op-2) returned a second reply; a claimed reply must be consumed")
	}

	got, ok = c.TakeFor("op-1")
	if !ok || got.OperationID != "op-1" {
		t.Fatalf("TakeFor(op-1) = %+v ok=%v; want the op-1 reply still cached", got, ok)
	}
	if n := c.Len(); n != 0 {
		t.Fatalf("cache holds %d replies after both were claimed; want 0", n)
	}
}

// TestReplyCache_UncorrelatedReplyIsNeverAttributed is the adversarial pin (9 rule 5):
// a reply carrying no operation_id must match NOTHING -- not the empty key, not some
// pending op by proximity. Attributing an untagged reply to an in-flight op is worse
// than leaving it unattributed: PB-STATE-1 would persist the wrong outcome for a
// mutating op. It stays drainable via Take so nothing is silently lost.
func TestReplyCache_UncorrelatedReplyIsNeverAttributed(t *testing.T) {
	c := NewReplyCache()
	c.Append(protocol.Control{Op: protocol.OpOK, SessionID: "m/s1"}) // no OperationID

	if got, ok := c.TakeFor(""); ok {
		t.Fatalf("TakeFor(\"\") = %+v; want no match (an untagged reply answers no op)", got)
	}
	if got, ok := c.TakeFor("op-1"); ok {
		t.Fatalf("TakeFor(op-1) = %+v; want no match (never attribute by proximity)", got)
	}
	if n := c.Len(); n != 1 {
		t.Fatalf("cache holds %d replies; want 1 (an unattributable reply is kept, not dropped)", n)
	}
	if _, ok := c.Take(); !ok {
		t.Fatalf("Take() found nothing; the FIFO drain must still surface an uncorrelated reply")
	}
}
