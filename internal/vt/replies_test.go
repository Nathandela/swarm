package vt

// Epic 4 carry-forward for the VT wrapper (bead agents-tracker-b2l notes; ADR
// depends-on note on Epic 2): agent CLIs emit terminal queries (DA "\x1b[c",
// DSR "\x1b[6n") and BLOCK waiting for the reply. The charm x/vt emulator
// generates the correct replies on an internal pipe; today NewEmulator drains
// that pipe to io.Discard so the replies are lost. For the shim to keep an agent
// from hanging, those replies must be routed back into the PTY master. This
// epic therefore grows the wrapper by two methods, and these are the
// FAILING-FIRST tests for them (new file — the existing E2 suite is untouched):
//
//	func (e *Emulator) SetReplyWriter(w io.Writer)  // route query replies to w
//	func (e *Emulator) Close() error                // explicit lifecycle,
//	                                                // replacing the finalizer
//
// DESIGN PINS (this suite is the contract):
//   - After SetReplyWriter(w), every reply the emulator generates while
//     processing Feed is delivered to w (asynchronously is fine — replies are
//     produced during Feed and copied out by the wrapper's drain goroutine).
//   - A DSR (\x1b[6n) yields a Cursor-Position Report "\x1b[<row>;<col>R".
//   - A DA (\x1b[c) yields a primary Device-Attributes reply "\x1b[?...c".
//   - Close stops the drain goroutine and releases the underlying emulator; it
//     returns nil on success and is safe to call more than once (no panic, no
//     hang). Close replaces the runtime finalizer for deterministic shim
//     shutdown.
//   - SetReplyWriter may be called before any Feed (the shim wires it up at
//     construction, before the agent produces output).

import (
	"bytes"
	"regexp"
	"sync"
	"testing"
	"time"
)

// syncBuffer is a concurrency-safe sink: the wrapper's drain goroutine writes
// replies into it while the test polls its contents.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *syncBuffer) Bytes() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]byte(nil), s.buf.Bytes()...)
}

// waitForMatch polls sb until re matches its accumulated bytes or the deadline
// passes, returning the matched submatch (or failing the test).
func waitForMatch(t *testing.T, sb *syncBuffer, re *regexp.Regexp) []byte {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if m := re.Find(sb.Bytes()); m != nil {
			return m
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("no reply matching %s within deadline; got %q", re, sb.Bytes())
	return nil
}

func TestSetReplyWriter_DSRCursorPositionReport(t *testing.T) {
	e := NewEmulator(80, 24)
	defer e.Close()
	var sb syncBuffer
	e.SetReplyWriter(&sb)

	// Park the cursor somewhere non-trivial, then ask for its position.
	e.Feed([]byte("\x1b[5;9H")) // row 5, col 9 (1-based)
	e.Feed([]byte("\x1b[6n"))   // DSR - Report Cursor Position

	// CPR is ESC [ <row> ; <col> R.
	cpr := regexp.MustCompile(`\x1b\[\d+;\d+R`)
	got := waitForMatch(t, &sb, cpr)
	if !bytes.Contains(got, []byte("5;9")) {
		t.Errorf("CPR = %q, want it to report row 5 col 9", got)
	}
}

func TestSetReplyWriter_DeviceAttributes(t *testing.T) {
	e := NewEmulator(80, 24)
	defer e.Close()
	var sb syncBuffer
	e.SetReplyWriter(&sb)

	e.Feed([]byte("\x1b[c")) // DA - primary Device Attributes request

	// A primary DA reply is ESC [ ? ... c.
	da := regexp.MustCompile(`\x1b\[\?[0-9;]*c`)
	waitForMatch(t, &sb, da)
}

func TestSetReplyWriter_BeforeAnyFeed(t *testing.T) {
	// The shim sets the reply writer at construction, before the agent runs;
	// setting it on a fresh emulator and only then feeding a query must still
	// deliver the reply.
	e := NewEmulator(40, 10)
	defer e.Close()
	var sb syncBuffer
	e.SetReplyWriter(&sb)
	e.Feed([]byte("\x1b[6n"))
	waitForMatch(t, &sb, regexp.MustCompile(`\x1b\[\d+;\d+R`))
}

func TestClose_ReturnsNilAndIsIdempotent(t *testing.T) {
	e := NewEmulator(20, 5)
	if err := e.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	// A second Close must be safe (deterministic shutdown may double-close).
	done := make(chan error, 1)
	go func() { done <- e.Close() }()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("second Close returned %v, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("second Close hung; it must be safe to call twice")
	}
}

func TestClose_StopsReplyDelivery(t *testing.T) {
	// After Close, the emulator is finished; Close must return promptly (the
	// drain goroutine exits), not hang. This is the deterministic-shutdown
	// contract the shim relies on at agent exit.
	e := NewEmulator(30, 6)
	var sb syncBuffer
	e.SetReplyWriter(&sb)
	e.Feed([]byte("\x1b[6n"))
	waitForMatch(t, &sb, regexp.MustCompile(`\x1b\[\d+;\d+R`))

	done := make(chan error, 1)
	go func() { done <- e.Close() }()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Close returned %v, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("Close hung after replies were delivered")
	}
}
