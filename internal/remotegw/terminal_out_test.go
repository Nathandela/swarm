package remotegw

// FAILING-FIRST (TDD RED, GG-5) tests for A7 renderer slice D -- the gateway's
// SNAPSHOT-OUT path. A server-rendered terminal snapshot is sealed under the epoch
// content key and appended to the phone's relay mailbox on the SAME seq stream as the
// journal records (one sender, one epoch => one increasing seq), matching the phone
// decoder's committed wire shape (phonecore snapshotFrame, pinned by
// TestSnapshotFrame_WireShape): {"kind":"terminal_snapshot","session":..,"lines":[..],
// "cols":..,"rows":..}. RunTerminal mirrors RunJournal: it subscribes to the daemon's
// terminal-snapshot stream and forwards each decoded snapshot to the sink.
//
// RED: RelaySink.Terminal and Gateway.RunTerminal are stubs (return nil, forward
// nothing), so every assertion below fails until GREEN.

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/wire"
)

// wireSnapshot is an INDEPENDENT decode of the sealed snapshot plaintext (the test does
// not reuse the production frame type): it pins that the bytes on the wire carry the
// kind discriminator plus the TerminalSnapshot fields in the committed shape.
type wireSnapshot struct {
	Kind    string   `json:"kind"`
	Session string   `json:"session"`
	Lines   []string `json:"lines"`
	Cols    int      `json:"cols"`
	Rows    int      `json:"rows"`
}

// TestRelaySink_ForwardsTerminalSnapshot: Terminal seals ONE snapshot into the mailbox in
// the committed wire shape, and it draws from the SAME seq allocator as the journal path
// (a journal Event then a Terminal yield consecutive seqs -- no separate counter).
func TestRelaySink_ForwardsTerminalSnapshot(t *testing.T) {
	var key crypto.ContentKey
	for i := range key {
		key[i] = byte(i + 1)
	}
	app := &fakeAppender{}
	sink := newTestRelaySink(t, app, key)

	// A journal record first, then a terminal snapshot: they must share s.seq.
	if err := sink.Event(protocol.JournalRecord{Cursor: 9, SessionID: "s0", Type: "launched"}); err != nil {
		t.Fatalf("journal event: %v", err)
	}
	if err := sink.Terminal("s1", []string{"a", "b"}, 80, 24); err != nil {
		t.Fatalf("terminal snapshot: %v", err)
	}

	if len(app.envs) != 2 {
		t.Fatalf("appended %d envelopes; want 2 (one journal + one snapshot)", len(app.envs))
	}
	if app.targets[1] != "phone-routing-id" {
		t.Fatalf("snapshot append target = %q; want the phone routing id", app.targets[1])
	}

	jEnv, err := crypto.ParseEnvelope(app.envs[0])
	if err != nil {
		t.Fatalf("journal env parse: %v", err)
	}
	sEnv, err := crypto.ParseEnvelope(app.envs[1])
	if err != nil {
		t.Fatalf("snapshot env parse: %v", err)
	}
	if sEnv.Header.Seq != jEnv.Header.Seq+1 {
		t.Fatalf("snapshot seq %d not consecutive after journal seq %d (shared allocator required)", sEnv.Header.Seq, jEnv.Header.Seq)
	}
	if sEnv.Header.EpochID != 7 {
		t.Errorf("snapshot EpochID = %d, want 7", sEnv.Header.EpochID)
	}

	plain, err := crypto.OpenMailbox(key, sEnv)
	if err != nil {
		t.Fatalf("snapshot env does not open under the content key: %v", err)
	}

	// Byte-exact wire shape (matches Slice C's committed contract).
	const want = `{"kind":"terminal_snapshot","session":"s1","lines":["a","b"],"cols":80,"rows":24}`
	if string(plain) != want {
		t.Fatalf("snapshot plaintext =\n  %s\nwant\n  %s", plain, want)
	}

	// Field-by-field decode (independent of the production frame type).
	var got wireSnapshot
	if err := json.Unmarshal(plain, &got); err != nil {
		t.Fatalf("snapshot plaintext not decodable: %v", err)
	}
	if got.Kind != "terminal_snapshot" {
		t.Errorf("kind = %q, want terminal_snapshot", got.Kind)
	}
	if got.Session != "s1" {
		t.Errorf("session = %q, want s1", got.Session)
	}
	if len(got.Lines) != 2 || got.Lines[0] != "a" || got.Lines[1] != "b" {
		t.Errorf("lines = %v, want [a b]", got.Lines)
	}
	if got.Cols != 80 || got.Rows != 24 {
		t.Errorf("cols/rows = %d/%d, want 80/24", got.Cols, got.Rows)
	}
}

// TestTerminalSink_OpaqueToRelay: the appended mailbox bytes are ciphertext -- the relay
// never sees the plaintext grid content.
func TestTerminalSink_OpaqueToRelay(t *testing.T) {
	var key crypto.ContentKey
	for i := range key {
		key[i] = byte(i + 1)
	}
	app := &fakeAppender{}
	sink := newTestRelaySink(t, app, key)

	const secret = "SECRET-GRID-LINE-XYZ"
	if err := sink.Terminal("s1", []string{secret}, 80, 24); err != nil {
		t.Fatalf("terminal snapshot: %v", err)
	}
	if len(app.envs) != 1 {
		t.Fatalf("appended %d envelopes; want 1", len(app.envs))
	}
	if bytes.Contains(app.envs[0], []byte(secret)) {
		t.Fatalf("mailbox bytes leak the plaintext grid content %q; the relay must see only ciphertext", secret)
	}
	if bytes.Contains(app.envs[0], []byte(`"lines"`)) {
		t.Fatalf("mailbox bytes leak the plaintext JSON key \"lines\"; the frame must be sealed")
	}
}

// recordingTerminalSink is a JournalSink that also records forwarded terminal snapshots,
// so RunTerminal can be exercised against a fake daemon.
type recordingTerminalSink struct {
	done chan protocol.TerminalSnapshot
}

func (s *recordingTerminalSink) Snapshot([]protocol.JournalRecord, uint64) error { return nil }
func (s *recordingTerminalSink) Event(protocol.JournalRecord) error              { return nil }
func (s *recordingTerminalSink) Terminal(session string, lines []string, cols, rows int) error {
	s.done <- protocol.TerminalSnapshot{Session: session, Lines: lines, Cols: cols, Rows: rows}
	return nil
}

// serveFakeTerminalDaemon is a one-shot fake daemon on a unix socket: it completes the
// hello handshake (replying with endpointID), records the client's subscribe frame on
// gotSub, then writes each snapshot as a terminal_snapshot Control frame. A deadline
// keeps it from hanging if RunTerminal is a stub that never dials.
func serveFakeTerminalDaemon(t *testing.T, ln net.Listener, endpointID string, snaps []protocol.TerminalSnapshot, gotSub chan<- protocol.Control) {
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
	sub, err := protocol.DecodeControl(body)
	if err != nil {
		return
	}
	gotSub <- sub

	for i := range snaps {
		s := snaps[i]
		frame, err := protocol.EncodeControl(protocol.Control{Op: protocol.OpTerminalSnapshot, EndpointID: endpointID, Terminal: &s})
		if err != nil || wire.WriteFrame(conn, wire.TControl, frame) != nil {
			return
		}
	}
	// Hold the connection open so RunTerminal streams under a live subscription; it
	// unblocks when the client closes on ctx cancel (or the deadline fires).
	_, _, _ = wire.ReadFrame(conn)
}

// TestGatewayRunTerminal_SubscribesAndForwards: RunTerminal dials the daemon, sends a
// terminal_subscribe frame, and forwards each decoded terminal_snapshot to the sink with
// the session id namespaced to the endpoint (mirroring RunJournal's remote egress).
func TestGatewayRunTerminal_SubscribesAndForwards(t *testing.T) {
	// /tmp keeps the socket under the 104-byte sun_path limit (macOS $TMPDIR is long).
	dir, err := os.MkdirTemp("/tmp", "gwt")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "d.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	gotSub := make(chan protocol.Control, 1)
	snaps := []protocol.TerminalSnapshot{{Session: "s1", Lines: []string{"hello"}, Cols: 80, Rows: 24}}
	go serveFakeTerminalDaemon(t, ln, "m", snaps, gotSub)

	sink := &recordingTerminalSink{done: make(chan protocol.TerminalSnapshot, 1)}
	gw := New(sock, sink)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	errc := make(chan error, 1)
	go func() { errc <- gw.RunTerminal(ctx) }()

	select {
	case sub := <-gotSub:
		if sub.Op != protocol.OpTerminalSubscribe {
			t.Fatalf("subscribe op = %q, want terminal_subscribe", sub.Op)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RunTerminal never sent a terminal_subscribe frame")
	}

	select {
	case got := <-sink.done:
		if got.Session != "m/s1" {
			t.Errorf("forwarded session = %q, want m/s1 (endpoint-namespaced)", got.Session)
		}
		if len(got.Lines) != 1 || got.Lines[0] != "hello" {
			t.Errorf("forwarded lines = %v, want [hello]", got.Lines)
		}
		if got.Cols != 80 || got.Rows != 24 {
			t.Errorf("forwarded cols/rows = %d/%d, want 80/24", got.Cols, got.Rows)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RunTerminal did not forward the decoded snapshot")
	}

	cancel()
	<-errc
}
