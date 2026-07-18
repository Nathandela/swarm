// Package attach is the Epic 8 PART A raw attach passthrough (E8.1-E8.5): the
// client-side loop that puts the controlling terminal in raw mode, paints the one
// snapshot (S10), then streams live frames straight to the terminal while
// forwarding keystrokes and resizes to the session — and ALWAYS gives the terminal
// back (on detach, session end, panic, and SIGINT/SIGTERM/SIGHUP).
//
// The loop lives in its own bubbletea-free package so it is unit-testable against
// injected seams (TermControl + Session) AND provable end-to-end through a real PTY
// (see pty_test.go). internal/tui/attach.go is the thin bubbletea adapter that
// releases the tea terminal and calls Run with a real TermControl.
//
// SIGKILL restoration is NOT claimed: no handler can run under SIGKILL, so a hard
// kill -9 leaves the terminal raw. That is the documented boundary of the restore
// guarantee (pinned by TestPTY_SIGKILLLeavesRawUnrestored_DocumentedLimitation),
// not a bug — restoring it would require a wrapper process, which v1.0 does not add.
package attach

import (
	"fmt"
	"io"
	"os"
	"sync"

	"github.com/Nathandela/swarm/internal/vt"
)

// TermControl is the injectable seam over the real controlling terminal, so the
// loop is unit-testable without a TTY and a PTY smoke test proves termios restore.
type TermControl interface {
	// MakeRaw puts the terminal in raw mode (ISIG/ICANON/ECHO/IXON off) and returns
	// a restore func that returns it to its prior state. restore is idempotent and
	// safe to call from the loop's teardown after a signal.
	MakeRaw() (restore func() error, err error)
	// Size reports the current terminal size in cells.
	Size() (cols, rows int, err error)
	// In is the raw keystroke source.
	In() io.Reader
	// Out is the passthrough sink.
	Out() io.Writer
	// Resizes yields one tick per SIGWINCH; stop releases the notification.
	Resizes() (events <-chan struct{}, stop func())
	// Signals yields SIGINT/SIGTERM/SIGHUP to restore-then-exit on; stop releases it.
	Signals() (sigs <-chan os.Signal, stop func())
}

// Session is the subset of *protocol.Attachment the loop drives (stubbable): the
// one snapshot, the live output stream, and input/resize/detach under the lease.
type Session interface {
	Snapshot() []byte
	Frames() <-chan []byte
	Input(p []byte) error
	Resize(cols, rows int) error
	Detach() error
	Generation() uint64
}

// Config parameterizes one attach passthrough run.
type Config struct {
	Term      TermControl
	Session   Session
	DetachKey byte   // default DefaultDetachKey (0x11, Ctrl+q) when zero
	ReadOnly  bool   // completed/lost: paint final snapshot, forward no input (G3)
	Chrome    bool   // show the one-line chrome (name + detach hint) (A-5)
	Name      string // session label rendered in the chrome line
}

// Reason is why Run returned, so the caller knows whether to return to the general
// view (detached / session ended) or surface an error.
type Reason int

const (
	ReasonDetached   Reason = iota // the user pressed the detach key (or a signal tore down)
	ReasonSessionEnd               // the live stream closed (session ended / superseded)
	ReasonError                    // a fault was recovered; the terminal was still restored
)

// DefaultDetachKey is Ctrl+q (0x11) — the DC1 control byte. It is layout-friendly
// (the Q position is identical on US/Swiss/QWERTZ/AZERTY, unlike the near-untypeable
// Ctrl+\), and although 0x11 is XON, raw mode clears IXON (A-1) so it is delivered as
// a plain byte with no flow-control collision (ADR-006).
const DefaultDetachKey = 0x11

// inputBufSize bounds one raw read of keystrokes. A single keypress arrives as a
// short read well under this; the size only caps a paste burst per read.
const inputBufSize = 4096

// snapshotUnavailableNotice is the single plain line painted when the attach
// snapshot fails to decode: a one-line heads-up beats a blank screen, and (unlike
// raw JSON) it carries no escape bytes. It ends CRLF because the terminal is already
// in raw mode (OPOST/ONLCR off), so a bare \n would drop a row without returning the
// column.
const snapshotUnavailableNotice = "[swarm: snapshot unavailable - live output follows]\r\n"

// Run blocks driving the passthrough and ALWAYS restores the terminal before it
// returns. It paints the snapshot exactly once (raw-then-paint), then streams live
// frames to Out while forwarding keystrokes (except the detach key) and resizes to
// the session. It returns when the user detaches, the stream closes, a termination
// signal arrives, or a fault is recovered.
func Run(cfg Config) (reason Reason, err error) {
	detachKey := cfg.DetachKey
	if detachKey == 0 {
		detachKey = DefaultDetachKey
	}

	// Register termination-signal handling BEFORE entering raw mode: a signal that
	// arrives during raw-mode setup (or the instant a client observes the terminal
	// is raw) is then caught and restores, never falling through to the default
	// terminate disposition that would leave a wrecked terminal behind. A signal
	// caught before the select loop starts sits in the buffered channel and is
	// handled once the loop runs (restore is installed by then).
	sigCh, stopSig := cfg.Term.Signals()
	defer stopSig()

	restoreFn, rerr := cfg.Term.MakeRaw()
	if rerr != nil {
		return ReasonError, rerr
	}
	var restoreOnce sync.Once
	restore := func() { restoreOnce.Do(func() { _ = restoreFn() }) }
	// Last line of defense: however Run unwinds, the terminal is handed back.
	defer restore()
	// A fault anywhere in the loop (e.g. a mid-render write panic) is recovered so
	// the user lands back on a sane terminal and the general view, never a wrecked
	// screen behind a crashed TUI (E8.2). The deferred restore above still runs.
	defer func() {
		if r := recover(); r != nil {
			reason, err = ReasonError, fmt.Errorf("attach: recovered panic: %v", r)
		}
	}()

	// Raw-then-paint: the snapshot (and optional chrome) is the first thing written,
	// exactly once, before any live frame reaches the terminal (S10/A-1/A-4).
	//
	// The snapshot is a structured projection, not raw replay bytes, so it must be
	// decoded and rendered to ANSI before it can paint (P0 agents-tracker-a6f: raw
	// JSON was reaching the terminal).
	out := cfg.Term.Out()
	// Fetch the client terminal size BEFORE painting so the snapshot can be clipped
	// to it: a snapshot from a larger terminal would otherwise pile excess rows onto
	// the bottom line, and a wider row would wrap (a bottom-row wrap scrolls). On a
	// Size() error we fall back to an unclipped paint (0, 0). The same size drives the
	// resize below, so Size() is queried once here rather than twice.
	cols, rows, sizeErr := cfg.Term.Size()
	if sizeErr != nil {
		cols, rows = 0, 0
	}
	if snap, derr := vt.DecodeSnapshot(cfg.Session.Snapshot()); derr == nil {
		writeAll(out, vt.RenderSnapshotClipped(snap, cols, rows))
	} else {
		// A snapshot that fails to decode paints a single plain notice instead of
		// nothing, so an attach to a session with a skewed/corrupt snapshot is never
		// a silent blank screen — even an idle agent with no live frames shows this
		// line (A-4 never-blank). It carries no escape bytes; the live stream follows.
		writeAll(out, []byte(snapshotUnavailableNotice))
	}
	// Chrome is drawn AFTER the grid paint, whose clear+home would otherwise wipe
	// it; chromeLine saves/restores the cursor so the snapshot's cursor position
	// survives (A-5).
	if cfg.Chrome {
		writeAll(out, chromeLine(cfg.Name, detachKey))
	}

	// Sync the PTY to the client's terminal size on attach (E8.3); further changes
	// propagate via the resize pump.
	if sizeErr == nil {
		_ = cfg.Session.Resize(cols, rows)
	}

	// Input pump: raw keystrokes -> Session.Input, tight so echo latency stays low
	// (N-2). The detach key, recognized as a discrete single-byte keypress, is NOT
	// forwarded; it tears the loop down instead. In read-only mode nothing is
	// forwarded (G3), but the detach key still detaches.
	detachCh := make(chan struct{})
	var detachOnce sync.Once
	signalDetach := func() { detachOnce.Do(func() { close(detachCh) }) }
	go func() {
		// A pump fault never crashes the process. It does not itself re-raw or
		// re-restore the terminal, but Run's deferred restore still runs when the
		// main loop exits, so a panicking pump degrades to a detach, not a wrecked
		// terminal (pump-goroutine self-restore is a deferred hardening, not v1.0).
		defer func() { _ = recover() }()
		in := cfg.Term.In()
		buf := make([]byte, inputBufSize)
		for {
			n, e := in.Read(buf)
			if n == 1 && buf[0] == detachKey {
				signalDetach()
				return
			}
			if n > 0 && !cfg.ReadOnly {
				_ = cfg.Session.Input(append([]byte(nil), buf[:n]...))
			}
			if e != nil {
				return
			}
		}
	}()

	// Resize pump: one SIGWINCH tick -> propagate the current size to the session.
	resizeCh, stopResize := cfg.Term.Resizes()
	defer stopResize()
	go func() {
		defer func() { _ = recover() }()
		for range resizeCh {
			if cols, rows, e := cfg.Term.Size(); e == nil {
				_ = cfg.Session.Resize(cols, rows)
			}
		}
	}()

	// Termination signals (registered above, before raw mode) restore the terminal
	// before the loop tears down, so a raw-mode client killed by SIGINT/SIGTERM/
	// SIGHUP never leaves a wrecked terminal behind (E8.2).
	frames := cfg.Session.Frames()
	for {
		select {
		case f, ok := <-frames:
			if !ok {
				_ = cfg.Session.Detach()
				restore()
				return ReasonSessionEnd, nil
			}
			writeAll(out, f)
		case <-detachCh:
			_ = cfg.Session.Detach()
			restore()
			return ReasonDetached, nil
		case <-sigCh:
			_ = cfg.Session.Detach()
			restore()
			return ReasonDetached, nil
		}
	}
}

// writeAll writes p to w in full; a nil/empty slice is a no-op. A panicking writer
// (a mid-render fault) propagates to Run's recover, which restores the terminal.
func writeAll(w io.Writer, p []byte) {
	if len(p) == 0 {
		return
	}
	for len(p) > 0 {
		n, err := w.Write(p)
		if n > 0 {
			p = p[n:]
		}
		if err != nil {
			return
		}
	}
}

// chromeLine renders the single toggleable chrome line: the session name and the
// detach-key hint (A-5). It is drawn after the snapshot repaint, so it saves the
// cursor (DECSC), homes to the top line, writes the banner and clears to line end,
// then restores the cursor (DECRC) so the snapshot's cursor position is preserved.
func chromeLine(name string, detachKey byte) []byte {
	return []byte(fmt.Sprintf("\x1b7\x1b[1;1H[ %s  %s to detach ]\x1b[K\x1b8", name, keyLabel(detachKey)))
}

// keyLabel renders a control byte as a "Ctrl+X" hint (0x11 -> "Ctrl+Q"). DEL (0x7f)
// has no sensible Ctrl+<letter> form (0x7f|0x40 is 0x7f itself), so it is named "DEL".
func keyLabel(b byte) string {
	if b == 0x7f {
		return "DEL"
	}
	if b < 0x20 {
		return "Ctrl+" + string(rune(b|0x40))
	}
	return string(rune(b))
}
