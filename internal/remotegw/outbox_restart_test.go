package remotegw

// FAILING-FIRST (TDD RED, GG-5) tests for PB-GW-8: the gateway's OUTBOUND JOURNAL CURSOR
// must be durable.
//
// THE DEFECT. Gateway.cursor is a bare in-memory uint64 that nothing persists and nothing
// seeds, while TWO comments call it durable -- "its durable resume point" (gateway.go) and
// "resumes journal delivery from its last durable cursor" (service.go). Every restart
// therefore re-reads from cursor 0 and re-appends the ENTIRE journal at fresh seqs into the
// same 600-per-tumbling-minute mailbox, which is also how PB-GW-7's budget gets blown on a
// restart loop. This is the FOURTH comment in this tree presuming a durability that does
// not exist, and PB-LIFE-1/-5 make restarts routine.
//
// A LOCAL CURSOR WRITE IS NOT ENOUGH, and these tests are built so that it cannot pass:
// persisting a cursor is not atomic with a remote append (the same distributed-commit hole
// as PB-GW-7), so what is required is a durable OUTBOX coupling {journal cursor, sealed
// envelope, relay outcome}, replayed IDEMPOTENTLY on restart.
//
// THE SEAM these tests pin:
//
//	type OutboxEntry struct {
//		Cursor   uint64 // the journal cursor this envelope carries
//		Envelope []byte // the EXACT sealed bytes; a replay re-appends these, never re-seals
//	}
//	type Outbox interface {
//		Reserve(cursor uint64, env []byte) error // durable, BEFORE the append is attempted
//		Commit(cursor uint64) error              // the relay acked; raises Cursor()
//		Pending() ([]OutboxEntry, error)         // reserved but uncommitted, oldest first
//		Cursor() uint64                          // highest cursor durably COMMITTED
//	}
//	func OpenOutbox(path string) (Outbox, error) // "" => in-memory (today's behaviour)
//
//	// RelayConfig gains: Outbox Outbox   (nil => in-memory)
//	func (s *RelaySink) Replay() error         // re-append every pending entry VERBATIM, oldest first
//	func (s *RelaySink) DeliveredCursor() uint64 // == Outbox.Cursor()
//
//	// CursorSource lets New seed the gateway's resume point without changing its signature,
//	// mirroring how RunTerminal already discovers a TerminalSink:
//	type CursorSource interface{ DeliveredCursor() uint64 }
//
// Two boundaries are pinned deliberately:
//   - Event (a live journal record) is outbox-keyed and idempotent: a cursor already
//     committed is NOT re-appended.
//   - Snapshot (the reconnect ROSTER) is NOT: roster records are current state re-sent as of
//     the read cursor, sharing cursors with journal records they do not duplicate. Skipping
//     them would leave a restarted phone with no roster at all.

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/wire"
)

// countingAppender records every appended envelope. It is the plain, always-succeeding
// appender the restart tests need (fakeAppender's error mode is all-or-nothing).
type countingAppender struct {
	mu   sync.Mutex
	envs [][]byte
}

func (a *countingAppender) MailboxAppend(_ context.Context, _ string, env []byte) (uint64, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.envs = append(a.envs, append([]byte(nil), env...))
	return uint64(len(a.envs)), nil
}

func (a *countingAppender) count() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.envs)
}

func (a *countingAppender) all() [][]byte {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([][]byte(nil), a.envs...)
}

// commitThenFailAppender STORES the envelope and then reports failure: the post-commit
// connection loss of PB-GW-7, without needing a socket. The stored bytes are what a real
// relay would already be holding.
type commitThenFailAppender struct {
	mu     sync.Mutex
	stored [][]byte
}

var errReplyLost = errors.New("relay: connection closed")

func (a *commitThenFailAppender) MailboxAppend(_ context.Context, _ string, env []byte) (uint64, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.stored = append(a.stored, append([]byte(nil), env...))
	return 0, errReplyLost
}

func (a *commitThenFailAppender) all() [][]byte {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([][]byte(nil), a.stored...)
}

func outboxTestSink(t *testing.T, app MailboxAppender, seqPath, outboxPath string) (*RelaySink, Outbox) {
	t.Helper()
	seq, err := OpenSeqSource(seqPath)
	if err != nil {
		t.Fatalf("open durable seq: %v", err)
	}
	ob, err := OpenOutbox(outboxPath)
	if err != nil {
		t.Fatalf("open outbox: %v", err)
	}
	return NewRelaySink(RelayConfig{
		Appender:    app,
		Target:      "phone-routing-id",
		EpochID:     5,
		Key:         budgetTestKey(),
		SenderKeyID: [8]byte{1, 1, 2, 3, 5, 8, 13, 21},
		Seq:         seq,
		Outbox:      ob,
		Now:         func() time.Time { return time.Unix(1_700_000_000, 0) },
	}), ob
}

// serveFakeJournalDaemon is a one-shot fake daemon on a unix socket for the JOURNAL path:
// it completes the hello handshake, records the client's journal_read control (whose Cursor
// is the resume point under test) on gotRead, replies with res, then holds the connection
// open under a live subscription until the client goes away.
func serveFakeJournalDaemon(t *testing.T, ln net.Listener, endpointID string, res protocol.Control, gotRead chan<- protocol.Control) {
	t.Helper()
	conn, err := ln.Accept()
	if err != nil {
		return
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))

	typ, body, err := wire.ReadFrame(conn)
	if err != nil || typ != wire.TControl {
		return
	}
	if hello, err := protocol.DecodeControl(body); err != nil || hello.Op != protocol.OpHello {
		return
	}
	reply, err := protocol.EncodeControl(protocol.Control{Op: protocol.OpHello, EndpointID: endpointID, ProtocolVersion: protocol.Version})
	if err != nil || wire.WriteFrame(conn, wire.TControl, reply) != nil {
		return
	}

	typ, body, err = wire.ReadFrame(conn)
	if err != nil || typ != wire.TControl {
		return
	}
	read, err := protocol.DecodeControl(body)
	if err != nil {
		return
	}
	gotRead <- read

	res.Op = protocol.OpJournalRead
	res.EndpointID = endpointID
	frame, err := protocol.EncodeControl(res)
	if err != nil || wire.WriteFrame(conn, wire.TControl, frame) != nil {
		return
	}
	// The journal_subscribe frame, then hold the connection open.
	_, _, _ = wire.ReadFrame(conn)
	_, _, _ = wire.ReadFrame(conn)
}

func journalSocket(t *testing.T) (string, net.Listener) {
	t.Helper()
	// /tmp keeps the socket under the 104-byte sun_path limit (macOS $TMPDIR is long).
	dir, err := os.MkdirTemp("/tmp", "gwob")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "d.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })
	return sock, ln
}

// seededCursorSink is an OutboundSink that reports a durable delivered-cursor, standing in
// for a restarted RelaySink whose outbox has already been reopened. It isolates the SEEDING
// defect (New never asks anyone where to resume) from the outbox's own durability, so both
// fail independently rather than one hiding behind the other.
type seededCursorSink struct {
	cursor uint64
	snapshotRecorder
}

func (s *seededCursorSink) DeliveredCursor() uint64 { return s.cursor }

// TestGateway_SeedsResumePointFromDurableCursor: a restarted gateway's resume point is the
// cursor its sink durably delivered, known BEFORE it has contacted anyone -- and the
// journal_read it then issues asks the daemon for that cursor, not 0.
func TestGateway_SeedsResumePointFromDurableCursor(t *testing.T) {
	sock, ln := journalSocket(t)
	sink := &seededCursorSink{cursor: 6}

	g := New(sock, sink)
	if got := g.Cursor(); got != 6 {
		t.Fatalf("a restarted gateway's Cursor() = %d before it has contacted anyone, want 6. Nothing "+
			"seeds Gateway.cursor, so every restart re-reads from 0 and re-appends the whole journal "+
			"at fresh seqs into the same capped mailbox -- while two comments call that cursor "+
			"durable (PB-GW-8)", got)
	}
	// Production wires the coalescing wrapper between the gateway and the sink; the resume
	// point must not be lost behind it.
	if got := New(sock, NewCoalescingSink(CoalesceConfig{Inner: sink})).Cursor(); got != 6 {
		t.Errorf("with the coalescing wrapper in place a restarted gateway's Cursor() = %d, want 6: "+
			"the wrapper must forward the inner sink's durable cursor", got)
	}

	gotRead := make(chan protocol.Control, 1)
	go serveFakeJournalDaemon(t, ln, "m", protocol.Control{Cursor: 9}, gotRead)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	errc := make(chan error, 1)
	go func() { errc <- g.RunJournal(ctx) }()
	select {
	case read := <-gotRead:
		if read.Op != protocol.OpJournalRead {
			t.Fatalf("first op = %q, want journal_read", read.Op)
		}
		if read.Cursor != 6 {
			t.Fatalf("journal_read asked the daemon for cursor %d, want 6: a restarted gateway must "+
				"resume from its persisted cursor instead of re-reading the whole journal (PB-GW-8)", read.Cursor)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the restarted gateway never issued a journal_read")
	}
	cancel()
	<-errc
}

// TestRelaySink_OutboxCommitsDeliveredCursorsAcrossRestart is the durability half: the
// outbox records the relay OUTCOME per journal cursor, and a reopened one still knows it.
func TestRelaySink_OutboxCommitsDeliveredCursorsAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	seqPath := filepath.Join(dir, "outbound-journal.seq")
	outboxPath := filepath.Join(dir, "outbound-journal.outbox")

	app := &countingAppender{}
	sink, ob := outboxTestSink(t, app, seqPath, outboxPath)
	g := New("", sink)
	for _, c := range []uint64{4, 5, 6} {
		if err := g.deliver(protocol.JournalRecord{Cursor: c, SessionID: "m/s1", Type: "launched"}); err != nil {
			t.Fatalf("deliver cursor %d: %v", c, err)
		}
	}
	if got := ob.Cursor(); got != 6 {
		t.Fatalf("outbox cursor after delivering journal cursors 4,5,6 = %d, want 6: the outbox must "+
			"record the relay OUTCOME per journal cursor, not just the envelope (PB-GW-8)", got)
	}

	// RESTART: a fresh outbox + seq over the SAME files.
	app2 := &countingAppender{}
	sink2, ob2 := outboxTestSink(t, app2, seqPath, outboxPath)
	if got := ob2.Cursor(); got != 6 {
		t.Fatalf("a reopened outbox reports cursor %d, want 6: the committed cursor is not durable, "+
			"so the restart re-reads from 0 and re-appends the whole journal", got)
	}
	if got := sink2.DeliveredCursor(); got != 6 {
		t.Fatalf("the restarted sink's DeliveredCursor() = %d, want 6: this is the value New seeds "+
			"the gateway's resume point from", got)
	}
}

// TestGateway_RestartDoesNotReAppendDeliveredJournalRecords asserts the "does not re-append"
// half at the SINK, where a local cursor write cannot reach it: a record whose delivery the
// outbox already committed is not appended again even when it is handed straight to the sink
// (as it is by a daemon that re-serves an overlapping range, or by any resume that is
// conservative about its cursor). The ROSTER is deliberately exempt.
func TestGateway_RestartDoesNotReAppendDeliveredJournalRecords(t *testing.T) {
	dir := t.TempDir()
	seqPath := filepath.Join(dir, "outbound-journal.seq")
	outboxPath := filepath.Join(dir, "outbound-journal.outbox")

	app := &countingAppender{}
	sink, _ := outboxTestSink(t, app, seqPath, outboxPath)
	g := New("", sink)
	for _, c := range []uint64{4, 5, 6} {
		if err := g.deliver(protocol.JournalRecord{Cursor: c, SessionID: "m/s1", Type: "launched"}); err != nil {
			t.Fatalf("deliver cursor %d: %v", c, err)
		}
	}
	if got := app.count(); got != 3 {
		t.Fatalf("pre-restart appends = %d, want 3", got)
	}

	// RESTART.
	app2 := &countingAppender{}
	sink2, _ := outboxTestSink(t, app2, seqPath, outboxPath)
	if err := sink2.Replay(); err != nil {
		t.Fatalf("replay on restart: %v", err)
	}
	if got := app2.count(); got != 0 {
		t.Fatalf("replay re-appended %d envelopes, want 0: every entry was committed before the "+
			"restart, so there is nothing in flight", got)
	}

	// Records the outbox already committed, handed DIRECTLY to the sink.
	for _, c := range []uint64{4, 5, 6} {
		if err := sink2.Event(protocol.JournalRecord{Cursor: c, SessionID: "m/s1", Type: "launched"}); err != nil {
			t.Fatalf("re-offered cursor %d: %v", c, err)
		}
	}
	if got := app2.count(); got != 0 {
		t.Fatalf("re-offering 3 already-delivered journal records appended %d envelopes, want 0: the "+
			"outbox's committed cursor is what makes the resume idempotent -- a local cursor write "+
			"cannot, because persisting it is not atomic with the remote append (PB-GW-8)", got)
	}

	// The roster is state, not a replayable event: it MUST still be re-sent after a restart,
	// at cursors that overlap records already delivered.
	roster := []protocol.JournalRecord{
		{Cursor: 4, SessionID: "m/s1", Type: "roster", Group: "working"},
		{Cursor: 6, SessionID: "m/s2", Type: "roster", Group: "waiting"},
	}
	if err := sink2.Snapshot(roster, 6); err != nil {
		t.Fatalf("roster snapshot after restart: %v", err)
	}
	if got := app2.count(); got != len(roster) {
		t.Fatalf("the reconnect roster appended %d of %d records: roster records are CURRENT STATE "+
			"re-sent as of the read cursor, not journal events to dedup -- suppressing them leaves a "+
			"restarted phone with no roster at all", got, len(roster))
	}

	// And a genuinely new record still flows.
	if err := sink2.Event(protocol.JournalRecord{Cursor: 7, SessionID: "m/s2", Type: "exited"}); err != nil {
		t.Fatalf("new record cursor 7: %v", err)
	}
	if got := app2.count(); got != len(roster)+1 {
		t.Fatalf("appends after the new record = %d, want %d: idempotent resume must not swallow "+
			"records past the committed cursor", got, len(roster)+1)
	}
}

// TestRelaySink_OutboxReplayIsIdempotent covers the entry a restart finds IN FLIGHT: reserved
// before the append, never committed, and possibly already stored by the relay. The replay
// must re-append the byte-identical sealed envelope -- the phone's receiver stale-drops a
// duplicate for free (crypto/envelope.go Accept), which is why this needs no relay protocol
// change -- and a second replay must append nothing.
func TestRelaySink_OutboxReplayIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	seqPath := filepath.Join(dir, "outbound-journal.seq")
	outboxPath := filepath.Join(dir, "outbound-journal.outbox")

	// Run 1: the relay commits the record, then the reply is lost.
	lossy := &commitThenFailAppender{}
	sink, ob := outboxTestSink(t, lossy, seqPath, outboxPath)
	recB := protocol.JournalRecord{Cursor: 2, SessionID: "m/s2", Type: "launched"}
	if err := sink.Event(recB); err == nil {
		t.Fatal("Event returned nil for an append whose reply was lost; the error must reach the caller")
	}
	if got := ob.Cursor(); got != 0 {
		t.Fatalf("outbox cursor = %d after a delivery-unknown append, want 0: an unacked entry must "+
			"not raise the committed cursor", got)
	}
	committed := lossy.all()
	if len(committed) != 1 {
		t.Fatalf("the relay stored %d envelopes, want 1", len(committed))
	}

	// RESTART: the in-flight entry is found and replayed verbatim.
	app2 := &countingAppender{}
	sink2, ob2 := outboxTestSink(t, app2, seqPath, outboxPath)
	pending, err := ob2.Pending()
	if err != nil {
		t.Fatalf("outbox pending: %v", err)
	}
	if len(pending) != 1 || pending[0].Cursor != recB.Cursor {
		t.Fatalf("reopened outbox pending = %+v, want exactly one entry for cursor %d: the outbox must "+
			"durably couple {cursor, sealed envelope, outcome} so a restart can tell 'maybe delivered' "+
			"from 'never attempted' (PB-GW-8)", pending, recB.Cursor)
	}
	if err := sink2.Replay(); err != nil {
		t.Fatalf("replay: %v", err)
	}
	replayed := app2.all()
	if len(replayed) != 1 {
		t.Fatalf("replay appended %d envelopes, want 1", len(replayed))
	}
	if string(replayed[0]) != string(committed[0]) {
		t.Fatal("the replayed envelope differs from the one the relay had already committed. A replay " +
			"must re-append the IDENTICAL sealed bytes: re-sealing produces a fresh nonce (and possibly " +
			"a fresh seq), so the phone either accepts the same record twice or stale-drops one of two " +
			"rival envelopes at the same seq (PB-GW-7)")
	}
	if got := ob2.Cursor(); got != recB.Cursor {
		t.Errorf("after a successful replay the outbox cursor = %d, want %d", got, recB.Cursor)
	}
	if err := sink2.Replay(); err != nil {
		t.Fatalf("second replay: %v", err)
	}
	if got := app2.count(); got != 1 {
		t.Fatalf("a second replay appended %d envelopes in total, want 1: replay must be idempotent, "+
			"or a restart loop re-floods a 600-per-minute mailbox", got)
	}

	// The phone side: the duplicate costs nothing -- it is stale-dropped, not a gap.
	key := budgetTestKey()
	phone := crypto.NewMailboxReceiver()
	first, err := crypto.ParseEnvelope(committed[0])
	if err != nil {
		t.Fatalf("committed envelope does not parse: %v", err)
	}
	if _, err := phone.Accept(key, first); err != nil {
		t.Fatalf("the phone rejected the pre-cut envelope (seq %d): %v", first.Header.Seq, err)
	}
	dup, err := crypto.ParseEnvelope(replayed[0])
	if err != nil {
		t.Fatalf("replayed envelope does not parse: %v", err)
	}
	if _, err := phone.Accept(key, dup); !errors.Is(err, crypto.ErrStaleSeq) {
		t.Fatalf("the phone's verdict on the replayed duplicate was %v, want crypto.ErrStaleSeq: "+
			"receiver-side idempotency is what makes a verbatim replay free", err)
	}
}
