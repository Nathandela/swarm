package phonecore

// Failing-first tests for the phone-core SNAPSHOT-RECEIVE path (A7 renderer slice C):
// a server-rendered terminal snapshot arrives SEALED on the SAME relay mailbox as the
// journal records, tagged by a "kind" discriminator. The phone demuxes the two on one
// MailboxReceiver (shared seq space), decodes the snapshot into a thin per-session cache
// (text lines only -- no VT emulator on-device), and leaves the journal path byte-
// identical for kind-less plaintext. RED is behavior-only: the demux stubs exist so this
// compiles, but route nothing, so every assertion below fails until GREEN.

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/status"
)

// testContentKey (shared with input_test.go) is a deterministic epoch content key.

// sealFrame seals one mailbox plaintext at seq under the content key, mirroring the
// gateway RelaySink's header (one sender, one epoch => a single increasing seq stream).
func sealFrame(t *testing.T, key crypto.ContentKey, seq uint64, plain []byte) []byte {
	t.Helper()
	env, err := crypto.SealMailbox(key, crypto.EnvelopeHeader{
		Version: crypto.VersionV1,
		EpochID: 7,
		Seq:     seq,
	}, plain)
	if err != nil {
		t.Fatalf("seal seq %d: %v", seq, err)
	}
	return env.Marshal()
}

// marshalSnapshot builds the committed sealed-snapshot plaintext: the TerminalSnapshot
// fields plus a kind:"terminal_snapshot" tag.
func marshalSnapshot(t *testing.T, session string, lines []string, cols, rows int) []byte {
	t.Helper()
	plain, err := json.Marshal(snapshotFrame{
		Kind: kindTerminalSnapshot,
		TerminalSnapshot: protocol.TerminalSnapshot{
			Session: session, Lines: lines, Cols: cols, Rows: rows,
		},
	})
	if err != nil {
		t.Fatalf("marshal snapshot frame: %v", err)
	}
	return plain
}

// TestSnapshotFrame_WireShape pins the exact committed plaintext JSON shape (D matches
// this): the kind discriminator plus the TerminalSnapshot fields, in order.
func TestSnapshotFrame_WireShape(t *testing.T) {
	got := marshalSnapshot(t, "s1", []string{"a", "b"}, 80, 24)
	want := `{"kind":"terminal_snapshot","session":"s1","lines":["a","b"],"cols":80,"rows":24}`
	if string(got) != want {
		t.Fatalf("snapshot wire shape =\n  %s\nwant\n  %s", got, want)
	}
}

// TestSnapshotReceiver_DecodesSealedFrame: a sealed terminal-snapshot frame is opened,
// demuxed by kind, and cached; the journal/session cache is untouched.
func TestSnapshotReceiver_DecodesSealedFrame(t *testing.T) {
	key := testContentKey()
	router := NewMailboxRouter(key)

	plain := marshalSnapshot(t, "s1", []string{"a", "b"}, 80, 24)
	if _, err := router.Accept(sealFrame(t, key, 1, plain)); err != nil {
		t.Fatalf("accept snapshot: %v", err)
	}

	snap, ok := router.Snapshots().Get("s1")
	if !ok {
		t.Fatalf("snapshot cache has no s1")
	}
	if !reflect.DeepEqual(snap.Lines, []string{"a", "b"}) || snap.Cols != 80 || snap.Rows != 24 {
		t.Fatalf("snap = %+v; want lines [a b], 80x24", snap)
	}
	if n := len(router.Sessions().List()); n != 0 {
		t.Fatalf("session cache has %d entries; want 0 (a snapshot is not a journal record)", n)
	}
}

// TestMailboxDemux_JournalUnaffected: a sealed BARE journal record (no kind field) still
// decodes into the existing session cache exactly as before, and the snapshot cache stays
// empty -- no regression to the journal path.
func TestMailboxDemux_JournalUnaffected(t *testing.T) {
	key := testContentKey()
	router := NewMailboxRouter(key)

	plain, err := json.Marshal(protocol.JournalRecord{
		Cursor: 5, SessionID: "m/s1", Type: "launched", Group: status.Group("working"),
	})
	if err != nil {
		t.Fatalf("marshal record: %v", err)
	}
	if bytes.Contains(plain, []byte(`"kind"`)) {
		t.Fatalf("journal plaintext unexpectedly carries a kind field: %s", plain)
	}
	if _, err := router.Accept(sealFrame(t, key, 1, plain)); err != nil {
		t.Fatalf("accept journal: %v", err)
	}

	cs, ok := router.Sessions().Get("m/s1")
	if !ok || !cs.Present || cs.Group != status.Group("working") {
		t.Fatalf("session s1 = %+v ok=%v; want present, Group working (verbatim)", cs, ok)
	}
	if n := router.Snapshots().Len(); n != 0 {
		t.Fatalf("snapshot cache has %d entries; want 0 (journal path must not touch it)", n)
	}
}

// TestSnapshotCache_LatestPerSession: two snapshots for one session -> the latest wins.
func TestSnapshotCache_LatestPerSession(t *testing.T) {
	key := testContentKey()
	router := NewMailboxRouter(key)

	first := marshalSnapshot(t, "s1", []string{"old"}, 80, 24)
	second := marshalSnapshot(t, "s1", []string{"new"}, 100, 40)
	if _, err := router.Accept(sealFrame(t, key, 1, first)); err != nil {
		t.Fatalf("accept first: %v", err)
	}
	if _, err := router.Accept(sealFrame(t, key, 2, second)); err != nil {
		t.Fatalf("accept second: %v", err)
	}

	snap, ok := router.Snapshots().Get("s1")
	if !ok || !reflect.DeepEqual(snap.Lines, []string{"new"}) || snap.Cols != 100 || snap.Rows != 40 {
		t.Fatalf("snap = %+v ok=%v; want latest: lines [new], 100x40", snap, ok)
	}
	if n := router.Snapshots().Len(); n != 1 {
		t.Fatalf("snapshot cache has %d entries; want 1 (latest replaces prior)", n)
	}
}

// TestMailboxSeqGate_SharedJournalAndSnapshot: journal and snapshot frames interleaved on
// ONE increasing seq stream through ONE MailboxReceiver are ALL accepted. The demux must
// Accept exactly once per envelope then dispatch on kind -- a second Accept on the same
// envelope would see seq <= highest and fail ErrStaleSeq.
func TestMailboxSeqGate_SharedJournalAndSnapshot(t *testing.T) {
	key := testContentKey()
	router := NewMailboxRouter(key)

	j1, err := json.Marshal(protocol.JournalRecord{Cursor: 1, SessionID: "m/s1", Type: "launched"})
	if err != nil {
		t.Fatalf("marshal j1: %v", err)
	}
	j3, err := json.Marshal(protocol.JournalRecord{Cursor: 2, SessionID: "m/s2", Type: "launched"})
	if err != nil {
		t.Fatalf("marshal j3: %v", err)
	}
	frames := [][]byte{
		sealFrame(t, key, 1, j1),
		sealFrame(t, key, 2, marshalSnapshot(t, "m/s1", []string{"x"}, 80, 24)),
		sealFrame(t, key, 3, j3),
		sealFrame(t, key, 4, marshalSnapshot(t, "m/s2", []string{"y"}, 80, 24)),
	}
	for i, raw := range frames {
		if _, err := router.Accept(raw); err != nil {
			t.Fatalf("frame %d rejected on the shared seq stream: %v", i, err)
		}
	}

	if n := len(router.Sessions().List()); n != 2 {
		t.Fatalf("session cache has %d entries; want 2", n)
	}
	if n := router.Snapshots().Len(); n != 2 {
		t.Fatalf("snapshot cache has %d entries; want 2", n)
	}
}
