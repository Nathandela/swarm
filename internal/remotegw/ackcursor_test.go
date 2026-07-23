package remotegw

// FAILING-FIRST (TDD RED, GG-5) tests closing review finding GW-H1: the gateway must
// NOT silently drop a journal event on a transient relay failure (R-GW.5: journal
// events are never dropped; the cursor only advances as records are delivered/acked).
//
// THE BUG these tests pin against: Gateway.deliver (gateway.go) advances g.cursor to
// rec.Cursor BEFORE calling the sink, and RelaySink.forward (relaysink.go) swallows a
// failed MailboxAppend into lastErr and returns. So a single relay hiccup moves the
// cursor PAST an un-appended record; the next reconnect's journal_read(cursor) starts
// AFTER it and the record is dropped permanently.
//
// THE SEAM these tests pin (the implementer makes it GREEN): JournalSink.Event and
// .Snapshot gain an `error` return; RelaySink RETURNS the seal/append error instead of
// only stashing it in Err(); Gateway.deliver calls the sink FIRST and advances g.cursor
// ONLY when the sink returns nil (RunJournal then propagates the non-nil error so the
// reconnect re-reads from the un-advanced cursor and re-delivers the record):
//
//	JournalSink.Event(rec protocol.JournalRecord) error
//	JournalSink.Snapshot(roster []protocol.JournalRecord, cursor uint64) error
//
// JournalSink implementers that must adopt the new signature: the production *RelaySink
// (internal/remotegw/relaysink.go) and the *collectSink test fake
// (internal/skeleton/gateway_bridge_test.go). No JournalSink fake lives in THIS package
// (the gateway tests drive the real RelaySink), so TestGateway_* below are
// signature-agnostic: they observe only Gateway.Cursor() across g.deliver, which
// compiles whether Event returns void or error, and produce a cursor-assertion failure
// the moment the seam lands but deliver is not yet gated. TestRelaySink_* pins the
// RelaySink return value directly and is the signature-change RED until the seam lands.

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/remote/crypto"
)

var errAppendFailed = errors.New("relay mailbox append failed (transient)")

// flakyAppender is a MailboxAppender whose fail hook decides, per call (1-based), which
// appends fail. A failing append returns errAppendFailed and is NOT counted as ok, so a
// test can assert exactly which records reached the relay.
type flakyAppender struct {
	mu   sync.Mutex
	n    int
	ok   int
	fail func(call int) error
}

func (f *flakyAppender) MailboxAppend(_ context.Context, _ string, _ []byte) (uint64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.n++
	if f.fail != nil {
		if err := f.fail(f.n); err != nil {
			return 0, err
		}
	}
	f.ok++
	return uint64(f.ok), nil
}

func (f *flakyAppender) okCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.ok
}

func ackTestKey() crypto.ContentKey {
	var key crypto.ContentKey
	for i := range key {
		key[i] = byte(i + 3)
	}
	return key
}

// TestGateway_AppendFailureDoesNotAdvanceCursor pins R-GW.5 at the cursor: a record
// whose relay append FAILS must not advance the delivered cursor, while a record whose
// append SUCCEEDS must. Today deliver advances the cursor before (and regardless of) the
// append, so the failed record's cursor is retained and the record is lost.
func TestGateway_AppendFailureDoesNotAdvanceCursor(t *testing.T) {
	app := &flakyAppender{fail: func(call int) error {
		if call == 2 { // the second delivered record's append fails
			return errAppendFailed
		}
		return nil
	}}
	sink := newTestRelaySink(t, app, ackTestKey())
	g := New("", sink)

	// Record A: append succeeds -> cursor advances to 5.
	g.deliver(protocol.JournalRecord{Cursor: 5, SessionID: "m/s1", Type: "launched"})
	if got := g.Cursor(); got != 5 {
		t.Fatalf("after a successful delivery, Cursor() = %d, want 5", got)
	}

	// Record B: append fails -> cursor must NOT advance past the last acked record (5).
	g.deliver(protocol.JournalRecord{Cursor: 6, SessionID: "m/s2", Type: "launched"})
	if got := g.Cursor(); got != 5 {
		t.Fatalf("after a FAILED relay append, Cursor() = %d, want it to stay at 5 "+
			"(advancing to 6 drops the un-appended record on the next reconnect)", got)
	}
}

// TestGateway_FailedRecordRedeliveredAfterReconnect pins that a failed record is not
// permanently dropped: after the append fails the cursor stays at the highest acked
// value, so the reconnect's journal_read(from=cursor) re-includes the record; a
// re-delivery then appends it and advances the cursor. The reconnect is simulated by
// re-calling deliver with the same record, exactly as RunJournal would after re-reading
// from the un-advanced cursor.
func TestGateway_FailedRecordRedeliveredAfterReconnect(t *testing.T) {
	var bFailed bool
	app := &flakyAppender{fail: func(call int) error {
		if call == 2 && !bFailed { // fail record B's first append exactly once
			bFailed = true
			return errAppendFailed
		}
		return nil
	}}
	sink := newTestRelaySink(t, app, ackTestKey())
	g := New("", sink)

	recA := protocol.JournalRecord{Cursor: 5, SessionID: "m/s1", Type: "launched"}
	recB := protocol.JournalRecord{Cursor: 6, SessionID: "m/s2", Type: "launched"}

	g.deliver(recA) // append #1 ok
	g.deliver(recB) // append #2 FAILS
	if got := g.Cursor(); got != 5 {
		t.Fatalf("after B's append failed, Cursor() = %d, want 5 so the reconnect "+
			"re-reads from 5 and re-includes B", got)
	}

	// Reconnect re-reads from the un-advanced cursor (5) and re-delivers B.
	g.deliver(recB) // append #3 ok
	if got := g.Cursor(); got != 6 {
		t.Fatalf("after B was re-delivered and acked, Cursor() = %d, want 6", got)
	}
	if ok := app.okCount(); ok != 2 {
		t.Fatalf("successful appends = %d, want 2 (A once + B on retry); a value of 1 "+
			"means B was dropped, not redelivered", ok)
	}
}

// TestRelaySink_EventReturnsAppendError pins the seam the gateway gates on: RelaySink
// must RETURN the underlying MailboxAppend error from Event (not only stash it in
// Err()), so deliver can decline to advance the cursor. This is the signature-change
// RED: today Event returns no value.
func TestRelaySink_EventReturnsAppendError(t *testing.T) {
	app := &flakyAppender{fail: func(int) error { return errAppendFailed }}
	sink := newTestRelaySink(t, app, ackTestKey())

	err := sink.Event(protocol.JournalRecord{Cursor: 1, SessionID: "m/s1", Type: "launched"})
	if err == nil {
		t.Fatalf("RelaySink.Event swallowed a failed mailbox append; want the error returned")
	}
	if !errors.Is(err, errAppendFailed) {
		t.Fatalf("RelaySink.Event returned %v, want it to wrap errAppendFailed", err)
	}
}
