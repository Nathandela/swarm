package attach

// Failing-first wiring tests for the P0 fix (agents-tracker-a6f): the one attach
// snapshot must reach the terminal as RENDERED ANSI, never as raw snapshot JSON,
// and a snapshot that fails to decode must paint nothing rather than dump garbage.

import (
	"bytes"
	"testing"

	"github.com/Nathandela/swarm/internal/vt"
)

// mustSnap builds a real vt snapshot whose grid holds text, so the passthrough is
// driven by a genuine (JSON) snapshot rather than an opaque marker string.
func mustSnap(t *testing.T, text string) []byte {
	t.Helper()
	e := vt.NewEmulator(80, 24)
	defer func() { _ = e.Close() }()
	e.Feed([]byte(text))
	b, err := e.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	return b
}

// The attached snapshot is repainted as ANSI: the terminal shows the rendered
// grid text and NEVER the raw snapshot JSON ({"runs":.../"version":...}).
func TestPassthrough_SnapshotRenderedNotRawJSON(t *testing.T) {
	term := newFakeTerm(80, 24)
	sess := newFakeSession(mustSnap(t, "HELLO-RENDER"))
	ch := runInBackground(Config{Term: term, Session: sess})

	eventually(t, func() bool { return len(term.outBytes()) > 0 })
	out := term.outBytes()

	if bytes.Contains(out, []byte(`{"runs":`)) || bytes.Contains(out, []byte(`"version":`)) {
		t.Fatalf("attach painted raw snapshot JSON to the terminal (P0 agents-tracker-a6f): %q", out)
	}
	if !bytes.Contains(out, []byte("HELLO-RENDER")) {
		t.Fatalf("attach must paint the rendered snapshot text; got %q", out)
	}
	if !bytes.Contains(out, []byte("\x1b[2J")) {
		t.Fatalf("a rendered snapshot must clear the screen before repainting; got %q", out)
	}

	sess.endSession()
	_ = waitResult(t, ch)
}

// A snapshot that fails to decode is skipped silently: no raw bytes reach the
// terminal, and live frames still paint.
func TestPassthrough_MalformedSnapshotPaintsNothing(t *testing.T) {
	term := newFakeTerm(80, 24)
	sess := newFakeSession([]byte(`{"runs": not-valid-json`))
	ch := runInBackground(Config{Term: term, Session: sess})

	sess.pushFrame([]byte("LIVE"))
	eventually(t, func() bool { return bytes.Contains(term.outBytes(), []byte("LIVE")) })

	if bytes.Contains(term.outBytes(), []byte("runs")) {
		t.Fatalf("a malformed snapshot must not leak raw bytes to the terminal; got %q", term.outBytes())
	}

	sess.endSession()
	_ = waitResult(t, ch)
}
