package remotegw

// Failing-first tests for the gateway's relay-forwarding sink (R-GW.3): each journal
// record the daemon bridge delivers is sealed under the epoch content key
// (XChaCha20-Poly1305) and appended to the phone's relay mailbox as an opaque
// envelope. The relay never sees plaintext; only a holder of the content key (the
// paired phone) can open it. RED is undefined-only (NewRelaySink does not exist yet).

import (
	"context"
	"encoding/json"
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
	sink.Snapshot(roster, 5)
	sink.Event(protocol.JournalRecord{Cursor: 6, SessionID: "s2", Type: "launched"})

	if len(app.envs) != 2 {
		t.Fatalf("appended %d envelopes; want 2 (one roster + one event)", len(app.envs))
	}
	for _, target := range app.targets {
		if target != "phone-routing-id" {
			t.Fatalf("append target = %q; want the phone routing id", target)
		}
	}

	// Each opaque envelope must parse, carry the right header, and decrypt (under the
	// content key) back to the original record. A wrong key must NOT open it.
	want := []protocol.JournalRecord{
		{Cursor: 5, SessionID: "s1", Type: "roster", Group: "working"},
		{Cursor: 6, SessionID: "s2", Type: "launched"},
	}
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
		var got protocol.JournalRecord
		if err := json.Unmarshal(plain, &got); err != nil {
			t.Fatalf("env %d plaintext not a JournalRecord: %v", i, err)
		}
		if got != want[i] {
			t.Errorf("env %d record = %+v, want %+v", i, got, want[i])
		}

		// A different content key must fail to open (confidentiality).
		var wrong crypto.ContentKey
		if _, err := crypto.OpenMailbox(wrong, env); err == nil {
			t.Errorf("env %d opened under the WRONG key; confidentiality broken", i)
		}
	}
}

func TestRelaySink_AppendErrorSurfaced(t *testing.T) {
	var key crypto.ContentKey
	app := &fakeAppender{err: context.DeadlineExceeded}
	sink := newTestRelaySink(t, app, key)
	sink.Event(protocol.JournalRecord{Cursor: 1, SessionID: "s1", Type: "launched"})
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
				_ = sink.Terminal("s", []string{"line"}, 80, 24)
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
