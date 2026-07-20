package remotegw

// Failing-first tests for the gateway's relay-forwarding sink (R-GW.3): each journal
// record the daemon bridge delivers is sealed under the epoch content key
// (XChaCha20-Poly1305) and appended to the phone's relay mailbox as an opaque
// envelope. The relay never sees plaintext; only a holder of the content key (the
// paired phone) can open it. RED is undefined-only (NewRelaySink does not exist yet).

import (
	"context"
	"encoding/json"
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
