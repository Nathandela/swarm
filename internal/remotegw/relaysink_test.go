package remotegw

// Failing-first tests for the gateway's relay-forwarding sink (R-GW.3): each journal
// record the daemon bridge delivers is sealed under the epoch content key
// (XChaCha20-Poly1305) and appended to the phone's relay mailbox as an opaque
// envelope. The relay never sees plaintext; only a holder of the content key (the
// paired phone) can open it. RED is undefined-only (NewRelaySink does not exist yet).

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/remote/crypto"
)

// fakeAppender records every mailbox append so a test can inspect the opaque envelopes.
type fakeAppender struct {
	targets []string
	envs    [][]byte
	err     error
}

func (f *fakeAppender) MailboxAppend(_ context.Context, target string, env []byte) (uint64, error) {
	if f.err != nil {
		return 0, f.err
	}
	f.targets = append(f.targets, target)
	f.envs = append(f.envs, env)
	return uint64(len(f.envs)), nil
}

func newTestRelaySink(t *testing.T, app MailboxAppender, key crypto.ContentKey) *RelaySink {
	t.Helper()
	fixed := time.Unix(1_700_000_000, 0)
	return NewRelaySink(RelayConfig{
		Appender:       app,
		Target:         "phone-routing-id",
		EpochID:        7,
		Key:            key,
		RecipientKeyID: [8]byte{1, 2, 3, 4, 5, 6, 7, 8},
		SenderKeyID:    [8]byte{9, 10, 11, 12, 13, 14, 15, 16},
		Now:            func() time.Time { return fixed },
	})
}

func TestRelaySink_SealsAndAppendsDecryptableRecords(t *testing.T) {
	var key crypto.ContentKey
	for i := range key {
		key[i] = byte(i + 1)
	}
	app := &fakeAppender{}
	sink := newTestRelaySink(t, app, key)

	roster := []protocol.JournalRecord{{Cursor: 5, SessionID: "s1", Type: "roster", Group: "working"}}
	// Both returns are checked. This test decrypts what was appended, so a seal or append
	// that silently failed would leave app.envs short and be reported as a count mismatch --
	// the right symptom attributed to the wrong cause.
	if err := sink.Snapshot(roster, 5); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if err := sink.Event(protocol.JournalRecord{Cursor: 6, SessionID: "s2", Type: "launched"}); err != nil {
		t.Fatalf("Event: %v", err)
	}

	if len(app.envs) != 2 {
		t.Fatalf("appended %d envelopes; want 2 (one roster reseed + one event)", len(app.envs))
	}
	for _, target := range app.targets {
		if target != "phone-routing-id" {
			t.Fatalf("append target = %q; want the phone routing id", target)
		}
	}

	// Each opaque envelope must parse, carry the right header, and decrypt (under the
	// content key) back to the original record. A wrong key must NOT open it.
	var lastSeq uint64
	for i, raw := range app.envs {
		env, err := crypto.ParseEnvelope(raw)
		if err != nil {
			t.Fatalf("env %d does not parse: %v", i, err)
		}
		if env.Header.EpochID != 7 {
			t.Errorf("env %d EpochID = %d, want 7", i, env.Header.EpochID)
		}
		if i > 0 && env.Header.Seq <= lastSeq {
			t.Errorf("env %d Seq %d not strictly increasing (prev %d)", i, env.Header.Seq, lastSeq)
		}
		lastSeq = env.Header.Seq

		plain, err := crypto.OpenMailbox(key, env)
		if err != nil {
			t.Fatalf("env %d does not open under the content key: %v", i, err)
		}
		if i == 0 {
			var got reseedFrame
			if err := json.Unmarshal(plain, &got); err != nil {
				t.Fatalf("env %d plaintext not a roster reseed: %v", i, err)
			}
			if got.Kind != kindJournalReseed || got.Cursor != 5 ||
				!reflect.DeepEqual(got.Roster, roster) {
				t.Errorf("env %d reseed = %+v, want roster %+v at cursor 5", i, got, roster)
			}
		} else {
			var got protocol.JournalRecord
			if err := json.Unmarshal(plain, &got); err != nil {
				t.Fatalf("env %d plaintext not a JournalRecord: %v", i, err)
			}
			want := protocol.JournalRecord{Cursor: 6, SessionID: "s2", Type: "launched"}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("env %d record = %+v, want %+v", i, got, want)
			}
		}

		// A different content key must fail to open (confidentiality).
		var wrong crypto.ContentKey
		if _, err := crypto.OpenMailbox(wrong, env); err == nil {
			t.Errorf("env %d opened under the WRONG key; confidentiality broken", i)
		}
	}
}

func TestRelaySinkSnapshotPublishesOneAuthoritativeRosterReseed(t *testing.T) {
	var key crypto.ContentKey
	for i := range key {
		key[i] = byte(i + 1)
	}
	app := &fakeAppender{}
	sink := newTestRelaySink(t, app, key)
	roster := []protocol.JournalRecord{
		{SessionID: "m/s1", Type: "roster", Group: "working"},
		{SessionID: "m/s2", Type: "roster"},
	}
	if err := sink.Snapshot(roster, 5); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	assertReseed := func(index int, wantRoster []protocol.JournalRecord, wantCursor uint64) {
		t.Helper()
		env, err := crypto.ParseEnvelope(app.envs[index])
		if err != nil {
			t.Fatalf("parse envelope %d: %v", index, err)
		}
		plain, err := crypto.OpenMailbox(key, env)
		if err != nil {
			t.Fatalf("open envelope %d: %v", index, err)
		}
		var got reseedFrame
		if err := json.Unmarshal(plain, &got); err != nil {
			t.Fatalf("decode envelope %d: %v", index, err)
		}
		if got.Kind != kindJournalReseed || got.Cursor != wantCursor {
			t.Fatalf("snapshot frame = %#v, want journal_reseed at cursor %d", got, wantCursor)
		}
		if !reflect.DeepEqual(got.Roster, wantRoster) {
			t.Fatalf("snapshot roster = %#v, want %#v", got.Roster, wantRoster)
		}
		if got.Events == nil || len(got.Events) != 0 {
			t.Fatalf("snapshot events = %#v, want an explicit empty event list", got.Events)
		}
	}
	if len(app.envs) != 1 {
		t.Fatalf("non-empty Snapshot appended %d envelopes, want one atomic reseed", len(app.envs))
	}
	assertReseed(0, roster, 5)

	if err := sink.Snapshot(nil, 0); err != nil {
		t.Fatalf("empty Snapshot: %v", err)
	}
	if len(app.envs) != 2 {
		t.Fatalf("empty Snapshot appended %d total envelopes, want an authoritative reseed too", len(app.envs))
	}
	assertReseed(1, []protocol.JournalRecord{}, 0)
}

// hangingAppender blocks in MailboxAppend until its ctx is cancelled, simulating a hung
// relay. It returns the ctx error so a bounded-timeout caller surfaces a deadline error.
type hangingAppender struct {
	entered chan struct{}
}

func (h *hangingAppender) MailboxAppend(ctx context.Context, _ string, _ []byte) (uint64, error) {
	select {
	case h.entered <- struct{}{}:
	default:
	}
	<-ctx.Done() // hang until the sink's bounded append context expires
	return 0, ctx.Err()
}

// TestRelaySink_AppendBounded pins Blocker 2: seal holds s.mu across MailboxAppend (ordering
// is required so concurrent producers append in seq order), but a Background context let a
// HUNG relay hold that lock FOREVER — wedging every producer (RunJournal + every RunTerminal)
// AND Err() (which also takes s.mu). The append must run under a BOUNDED context so a hung
// relay surfaces a timeout via Err() instead of wedging everything. The append here never
// completes; the call must still return (with an error) within the bound, and Err() reports it.
func TestRelaySink_AppendBounded(t *testing.T) {
	var key crypto.ContentKey
	app := &hangingAppender{entered: make(chan struct{}, 1)}
	sink := NewRelaySink(RelayConfig{
		Appender:      app,
		Target:        "phone-routing-id",
		EpochID:       7,
		Key:           key,
		Now:           func() time.Time { return time.Unix(1_700_000_000, 0) },
		AppendTimeout: 100 * time.Millisecond,
	})

	done := make(chan error, 1)
	go func() { done <- sink.Event(protocol.JournalRecord{Cursor: 1, SessionID: "s1", Type: "launched"}) }()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("hung relay: append returned nil; want the bounded-timeout error")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("append blocked past its bounded timeout on a hung relay; a hung relay must not wedge " +
			"producers forever under s.mu (Blocker 2)")
	}
	if sink.Err() == nil {
		t.Fatal("hung relay: Err() is nil; the bounded-append timeout must surface via Err()")
	}
}

// A Service shutdown is a stronger bound than the per-append timeout: once its generation
// is cancelled, an append already blocked in the relay client must release the sink lock and
// let Service.Run join its journal goroutine promptly. Standalone sinks still rely on the
// configured timeout, but a service-owned sink is explicitly parented by Service.Run.
func TestRelaySink_AppendStopsWhenServiceParentIsCancelled(t *testing.T) {
	var key crypto.ContentKey
	app := &hangingAppender{entered: make(chan struct{}, 1)}
	sink := NewRelaySink(RelayConfig{
		Appender:      app,
		Target:        "phone-routing-id",
		EpochID:       7,
		Key:           key,
		Now:           func() time.Time { return time.Unix(1_700_000_000, 0) },
		AppendTimeout: time.Hour,
	})
	parent, cancel := context.WithCancel(context.Background())
	sink.bindParent(parent)

	done := make(chan error, 1)
	go func() {
		done <- sink.Event(protocol.JournalRecord{Cursor: 1, SessionID: "s1", Type: "launched"})
	}()
	select {
	case <-app.entered:
	case <-time.After(time.Second):
		t.Fatal("append did not enter the relay client")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("append error = %v, want service parent cancellation", err)
		}
	case <-time.After(time.Second):
		t.Fatal("append ignored service parent cancellation")
	}
	if err := sink.Err(); err != nil {
		t.Fatalf("normal service cancellation latched a degraded sink error: %v", err)
	}
}

func TestRelaySink_AppendErrorSurfaced(t *testing.T) {
	var key crypto.ContentKey
	app := &fakeAppender{err: context.DeadlineExceeded}
	sink := newTestRelaySink(t, app, key)
	// The append is EXPECTED to fail, so the return carries it too: Err() alone would pass
	// against a sink that stashed the error and told its caller everything was fine, which is
	// precisely the seam the gateway's cursor gating depends on.
	if err := sink.Event(protocol.JournalRecord{Cursor: 1, SessionID: "s1", Type: "launched"}); err == nil {
		t.Fatalf("Event returned nil for a failing append; the gateway gates its cursor on this return")
	}
	if sink.Err() == nil {
		t.Fatalf("a failed mailbox append was not surfaced via Err()")
	}
}

// seqOrderAppender records the envelope Seq of every append in ARRIVAL order and
// yields briefly inside MailboxAppend to exercise the window between seq allocation
// and append. RunJournal (Event) and RunTerminal (Terminal) drive one shared sink from
// separate goroutines; the phone gates a single MailboxReceiver on seq (seq<=hi ->
// ErrStaleSeq, seq>hi+1 -> Gap), so if a higher seq is appended before a lower one the
// phone drops the lower record and spuriously resyncs. The seq allocation and the append
// MUST therefore be serialized so appends arrive in strictly increasing seq order.
type seqOrderAppender struct {
	mu    sync.Mutex
	seqs  []uint64
	delay time.Duration
}

func (a *seqOrderAppender) MailboxAppend(_ context.Context, _ string, env []byte) (uint64, error) {
	parsed, err := crypto.ParseEnvelope(env)
	if err != nil {
		return 0, err
	}
	if a.delay > 0 {
		// Widen the seq-alloc -> append gap so a concurrent higher seq can overtake a
		// lower one when the seq counter is unlocked before the append (the bug).
		time.Sleep(a.delay)
	}
	a.mu.Lock()
	a.seqs = append(a.seqs, parsed.Header.Seq)
	n := uint64(len(a.seqs))
	a.mu.Unlock()
	return n, nil
}

func TestRelaySink_ConcurrentProducersPreserveSeqAppendOrder(t *testing.T) {
	var key crypto.ContentKey
	for i := range key {
		key[i] = byte(i + 1)
	}
	app := &seqOrderAppender{delay: 200 * time.Microsecond}
	sink := newTestRelaySink(t, app, key)

	// Many concurrent journal Events and terminal snapshots through the ONE sink, all
	// released together to maximize contention on the shared seq counter.
	const producers = 64
	var wg sync.WaitGroup
	wg.Add(producers)
	start := make(chan struct{})
	for i := 0; i < producers; i++ {
		go func(i int) {
			defer wg.Done()
			<-start
			if i%2 == 0 {
				_ = sink.Event(protocol.JournalRecord{Cursor: uint64(i), SessionID: "s", Type: "launched"})
			} else {
				_ = sink.Terminal(protocol.TerminalViewV1{Session: "s", Lines: []string{"line"}, Cols: 80, Rows: 24})
			}
		}(i)
	}
	close(start)
	wg.Wait()

	app.mu.Lock()
	defer app.mu.Unlock()
	if len(app.seqs) != producers {
		t.Fatalf("recorded %d appends; want %d", len(app.seqs), producers)
	}
	for i := 1; i < len(app.seqs); i++ {
		if app.seqs[i] <= app.seqs[i-1] {
			t.Fatalf("append %d has seq %d, not strictly greater than the previous append's seq %d: "+
				"concurrent seals allocated seq under the lock but appended out of order once it was released",
				i, app.seqs[i], app.seqs[i-1])
		}
	}
}
