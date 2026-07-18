// Package attach failing-test suite for Epic 8 PART A — the raw attach passthrough
// (E8.1-E8.5, A-1..A-5, N-2, S10, G3).
//
// FAILING TESTS ONLY. Written against the FROZEN API the implementer must provide.
// The passthrough loop lives in its OWN package (internal/attach), decoupled from
// bubbletea, so it is unit-testable against injected seams AND provable end-to-end
// through a real PTY. internal/tui/attach.go becomes a thin bubbletea adapter that
// releases the tea terminal and calls attach.Run (see internal/tui/epic8_test.go).
//
// FROZEN API (internal/attach):
//
//	// TermControl is the injectable seam over the real controlling terminal, so the
//	// loop is unit-testable without a TTY and a PTY smoke test proves termios restore.
//	type TermControl interface {
//	    MakeRaw() (restore func() error, err error) // ISIG/ICANON/ECHO/IXON off; restore is idempotent + signal-safe
//	    Size() (cols, rows int, err error)
//	    In() io.Reader                              // raw keystroke source
//	    Out() io.Writer                             // passthrough sink
//	    Resizes() (events <-chan struct{}, stop func()) // one tick per SIGWINCH
//	    Signals() (sigs <-chan os.Signal, stop func())  // SIGINT/SIGTERM/SIGHUP to restore-then-exit on
//	}
//	// Session is the subset of *protocol.Attachment the loop drives (stubbable).
//	type Session interface {
//	    Snapshot() []byte
//	    Frames() <-chan []byte
//	    Input(p []byte) error
//	    Resize(cols, rows int) error
//	    Detach() error
//	    Generation() uint64
//	}
//	type Config struct {
//	    Term      TermControl
//	    Session   Session
//	    DetachKey byte   // default DefaultDetachKey (0x11, Ctrl+q) when zero
//	    ReadOnly  bool   // completed/lost: paint final snapshot, forward no input (G3)
//	    Chrome    bool   // show the one-line chrome (name + detach hint), toggleable (A-5)
//	    Name      string // session label rendered in the chrome line
//	}
//	type Reason int
//	const ( ReasonDetached Reason = iota; ReasonSessionEnd; ReasonError )
//	const DefaultDetachKey = 0x11
//	func Run(cfg Config) (Reason, error)          // blocking; ALWAYS restores termios
//	func NewTermControl(in, out *os.File) (TermControl, error) // production impl over real fds
package attach

import (
	"bytes"
	"io"
	"os"
	"sync"
)

// Compile-time proof the fakes satisfy the frozen seams. These assertions are the
// primary RED markers: they fail "undefined: attach.TermControl / attach.Session"
// until the implementer lands the interfaces.
var (
	_ TermControl = (*fakeTerm)(nil)
	_ Session     = (*fakeSession)(nil)
)

// ---------------------------------------------------------------------------
// fakeTerm — an in-memory TermControl driving the passthrough without a TTY.
// ---------------------------------------------------------------------------

type fakeTerm struct {
	mu sync.Mutex

	inR   *io.PipeReader // keystrokes the test feeds via feed()
	inW   *io.PipeWriter
	out   *lockedBuffer // passthrough sink
	cols  int
	rows  int
	sizeN int // number of Size() calls (resize path reads current size)

	rawCalls     int
	restoreCalls int
	rawBeforeOut bool // set true if MakeRaw happened before the first Out write
	panicOnOut   bool // Out().Write panics, to exercise the panic-restore path

	resizeCh chan struct{}
	sigCh    chan os.Signal
}

func newFakeTerm(cols, rows int) *fakeTerm {
	r, w := io.Pipe()
	return &fakeTerm{
		inR:      r,
		inW:      w,
		out:      &lockedBuffer{},
		cols:     cols,
		rows:     rows,
		resizeCh: make(chan struct{}, 8),
		sigCh:    make(chan os.Signal, 4),
	}
}

func (f *fakeTerm) MakeRaw() (func() error, error) {
	f.mu.Lock()
	f.rawCalls++
	if f.out.Len() == 0 {
		f.rawBeforeOut = true
	}
	f.mu.Unlock()
	return func() error {
		f.mu.Lock()
		f.restoreCalls++
		f.mu.Unlock()
		return nil
	}, nil
}

func (f *fakeTerm) Size() (int, int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sizeN++
	return f.cols, f.rows, nil
}

func (f *fakeTerm) In() io.Reader { return f.inR }
func (f *fakeTerm) Out() io.Writer {
	if f.panicOnOut {
		return panicWriter{}
	}
	return f.out
}

// panicWriter panics on Write, standing in for a mid-render fault so the
// panic-restore path can be exercised (E8.2).
type panicWriter struct{}

func (panicWriter) Write([]byte) (int, error) { panic("attach: simulated render panic") }

func (f *fakeTerm) Resizes() (<-chan struct{}, func())  { return f.resizeCh, func() {} }
func (f *fakeTerm) Signals() (<-chan os.Signal, func()) { return f.sigCh, func() {} }

// feed writes raw keystroke bytes to the terminal input, as if typed.
func (f *fakeTerm) feed(b []byte) { _, _ = f.inW.Write(b) }

// setSize changes the reported terminal size and fires a resize tick.
func (f *fakeTerm) setSize(cols, rows int) {
	f.mu.Lock()
	f.cols, f.rows = cols, rows
	f.mu.Unlock()
	f.resizeCh <- struct{}{}
}

func (f *fakeTerm) outBytes() []byte { return f.out.Bytes() }

func (f *fakeTerm) counts() (raw, restore int, rawFirst bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.rawCalls, f.restoreCalls, f.rawBeforeOut
}

// lockedBuffer is a concurrency-safe bytes.Buffer for the Out sink.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) Bytes() []byte {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]byte(nil), l.b.Bytes()...)
}

func (l *lockedBuffer) Len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Len()
}

// ---------------------------------------------------------------------------
// fakeSession — a stub *protocol.Attachment: scripted snapshot + frames, records
// input/resize, optionally echoes input back onto Frames (for round-trip latency).
// ---------------------------------------------------------------------------

type fakeSession struct {
	mu sync.Mutex

	snapshot []byte
	frames   chan []byte
	gen      uint64

	inputs      [][]byte
	resizes     [][2]int
	detachCalls int
	echo        bool // echo each Input onto Frames (N-2 round-trip)
}

func newFakeSession(snapshot []byte) *fakeSession {
	return &fakeSession{
		snapshot: snapshot,
		frames:   make(chan []byte, 1024),
		gen:      1,
	}
}

func (s *fakeSession) Snapshot() []byte      { return s.snapshot }
func (s *fakeSession) Frames() <-chan []byte { return s.frames }
func (s *fakeSession) Generation() uint64    { return s.gen }

func (s *fakeSession) Input(p []byte) error {
	s.mu.Lock()
	s.inputs = append(s.inputs, append([]byte(nil), p...))
	echo := s.echo
	s.mu.Unlock()
	if echo {
		s.frames <- append([]byte(nil), p...)
	}
	return nil
}

func (s *fakeSession) Resize(cols, rows int) error {
	s.mu.Lock()
	s.resizes = append(s.resizes, [2]int{cols, rows})
	s.mu.Unlock()
	return nil
}

func (s *fakeSession) Detach() error {
	s.mu.Lock()
	s.detachCalls++
	s.mu.Unlock()
	return nil
}

// pushFrame delivers one live output frame to the passthrough.
func (s *fakeSession) pushFrame(b []byte) { s.frames <- b }

// endSession closes the live stream (session ended / superseded).
func (s *fakeSession) endSession() { close(s.frames) }

func (s *fakeSession) inputBytes() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []byte
	for _, in := range s.inputs {
		out = append(out, in...)
	}
	return out
}

func (s *fakeSession) resizeCalls() [][2]int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([][2]int(nil), s.resizes...)
}
