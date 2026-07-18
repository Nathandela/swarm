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
	"bytes"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

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
	Chrome    bool   // reserve the real bottom row for the return hint (ADR-006 v0.3); off = full passthrough (A-5)
	Name      string // session label rendered in the reserved-row hint
}

// chromeReassertInterval throttles the reserved-row re-assert on benign live frames to
// at most once per this interval, so ordinary scrolling output is not amplified; a
// frame carrying a damage signature re-asserts immediately regardless (ADR-006 v0.3).
const chromeReassertInterval = 250 * time.Millisecond

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
	// chromeActive reports whether the reserved-row hint is engaged for a terminal of
	// the given height. Chrome (ADR-006 v0.3) reserves the REAL bottom row: the session
	// PTY is sized to rows-1 and a DECSTBM region keeps scrolling off the hint row. A
	// terminal of rows<=2 is too small to give up a row, so the hint is disabled and the
	// attach behaves exactly as Chrome:false (full rows, no region, no hint).
	chromeActive := func(rows int) bool { return cfg.Chrome && rows > 2 }
	// ptyRows maps the real terminal height to the height the session PTY is told about:
	// one row shorter when the hint is reserved, so the agent's absolute cursor
	// addressing never reaches the real bottom row.
	ptyRows := func(rows int) int {
		if chromeActive(rows) {
			return rows - 1
		}
		return rows
	}
	if snap, derr := vt.DecodeSnapshot(cfg.Session.Snapshot()); derr == nil {
		// Clip the snapshot to the reserved area (rows-1 when chrome is on) so it never
		// paints over the hint row.
		writeAll(out, vt.RenderSnapshotClipped(snap, cols, ptyRows(rows)))
	} else {
		// A snapshot that fails to decode paints a single plain notice instead of
		// nothing, so an attach to a session with a skewed/corrupt snapshot is never
		// a silent blank screen — even an idle agent with no live frames shows this
		// line (A-4 never-blank). It carries no escape bytes; the live stream follows.
		writeAll(out, []byte(snapshotUnavailableNotice))
	}
	// Chrome is established AFTER the grid paint, whose clear+home would otherwise wipe
	// it: chromeHint sets the scroll region and paints the hint on the reserved bottom
	// row, saving/restoring the cursor (DECSC/DECRC) so the snapshot's cursor position
	// survives (A-5). lastAssert seeds the output pump's re-assert throttle.
	var lastAssert time.Time
	if chromeActive(rows) {
		writeAll(out, chromeHint(cfg.Name, detachKey, cols, rows))
		lastAssert = time.Now()
	}

	// Sync the PTY to the client's terminal size on attach (E8.3), reserving the hint
	// row when chrome is engaged; further changes propagate via the resize pump.
	if sizeErr == nil {
		_ = cfg.Session.Resize(cols, ptyRows(rows))
	}
	// curCols/curRows track the latest real terminal size (updated by the resize pump
	// via winCh) so the output pump's re-assert and the detach cleanup target the
	// current bottom row rather than the attach-time one.
	curCols, curRows := cols, rows

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

	// Resize pump: one SIGWINCH tick -> propagate the current size to the session,
	// reserving the hint row when chrome is engaged. When chrome is on it also nudges
	// the main loop (winCh) to re-establish region+hint at the new size — the main loop
	// is the sole writer to out, so region/hint re-paints never race the frame writes.
	// winCh is a coalescing tick: the handler re-reads the current size, so a dropped
	// (already-pending) tick loses nothing.
	var winCh chan struct{}
	if cfg.Chrome {
		winCh = make(chan struct{}, 1)
	}
	resizeCh, stopResize := cfg.Term.Resizes()
	defer stopResize()
	go func() {
		defer func() { _ = recover() }()
		for range resizeCh {
			c, r, e := cfg.Term.Size()
			if e != nil {
				continue
			}
			_ = cfg.Session.Resize(c, ptyRows(r))
			if winCh != nil {
				select {
				case winCh <- struct{}{}:
				default: // a re-assert is already pending; it will read the current size
				}
			}
		}
	}()

	// Termination signals (registered above, before raw mode) restore the terminal
	// before the loop tears down, so a raw-mode client killed by SIGINT/SIGTERM/
	// SIGHUP never leaves a wrecked terminal behind (E8.2).
	frames := cfg.Session.Frames()
	// finish tears the loop down on the terminal paths: it releases the lease, and when
	// chrome reserved a row it resets the scroll region to full and clears the hint row
	// so the board repaint that follows starts on a clean full-height screen, then hands
	// the terminal back. The panic path does not route through here — its deferred
	// restore still runs, but out may be mid-fault, so chrome cleanup is best-effort.
	finish := func(r Reason) (Reason, error) {
		_ = cfg.Session.Detach()
		if cfg.Chrome {
			writeAll(out, chromeCleanup(curRows))
		}
		restore()
		return r, nil
	}
	for {
		select {
		case f, ok := <-frames:
			if !ok {
				return finish(ReasonSessionEnd)
			}
			writeAll(out, f)
			// The agent's own output can damage the reserved row — a full reset (ESC c),
			// an ED2 clear, an alt-screen swap, or a bare CSI r region reset. Re-assert
			// region+hint after the frame: immediately on a damage signature, otherwise
			// throttled so benign scrolling output is not amplified.
			if chromeActive(curRows) {
				if hasDamageSignature(f) || time.Since(lastAssert) >= chromeReassertInterval {
					writeAll(out, chromeHint(cfg.Name, detachKey, curCols, curRows))
					lastAssert = time.Now()
				}
			}
		case <-detachCh:
			return finish(ReasonDetached)
		case <-sigCh:
			return finish(ReasonDetached)
		case <-winCh:
			// A resize arrived (chrome only): re-read the current size and re-establish
			// the reserved row at the new dimensions. If the terminal shrank below the
			// reserve threshold, release the scroll region so the now-full-height agent
			// is not confined.
			if c, r, e := cfg.Term.Size(); e == nil {
				curCols, curRows = c, r
				if chromeActive(r) {
					writeAll(out, chromeHint(cfg.Name, detachKey, c, r))
				} else {
					writeAll(out, []byte("\x1b[r"))
				}
				lastAssert = time.Now()
			}
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

// chromeHint reserves the real bottom row (ADR-006 v0.3): it sets the DECSTBM scroll
// region to rows 1..rows-1 so normal-mode scrolling never drags the hint row, then
// paints the reverse-video return hint on the real bottom row. The whole sequence is
// wrapped in DECSC/DECRC so the agent's (or the snapshot's) cursor position and pen
// survive both the region change (DECSTBM homes the cursor) and the paint. The caller
// guarantees rows > 2. It is re-emitted by the output pump to self-heal damage (a bare
// CSI r region reset from the agent is repaired by the next re-assert).
func chromeHint(name string, detachKey byte, cols, rows int) []byte {
	var b strings.Builder
	b.WriteString("\x1b7") // DECSC: save cursor + pen
	b.WriteString("\x1b[1;")
	b.WriteString(strconv.Itoa(rows - 1))
	b.WriteByte('r') // DECSTBM: scroll region 1..rows-1
	b.WriteString("\x1b[")
	b.WriteString(strconv.Itoa(rows))
	b.WriteString(";1H")     // CUP to the reserved bottom row
	b.WriteString("\x1b[7m") // reverse video
	b.WriteString(hintText(name, detachKey, cols))
	b.WriteString("\x1b[0m") // reset pen
	b.WriteString("\x1b[K")  // clear to end of the reserved row
	b.WriteString("\x1b8")   // DECRC: restore cursor + pen
	return []byte(b.String())
}

// hintText builds the reserved-row hint, truncated to fit cols columns: the session
// name and the return affordance ("<name>  ctrl+q returns to swarm"). The key label
// tracks the configured detach key. Truncation keeps a wide row from wrapping (a wrap
// on the bottom row scrolls); cols<=0 disables truncation.
func hintText(name string, detachKey byte, cols int) string {
	s := name + "  " + strings.ToLower(keyLabel(detachKey)) + " returns to swarm"
	if cols > 0 {
		if r := []rune(s); len(r) > cols {
			s = string(r[:cols])
		}
	}
	return s
}

// chromeCleanup releases the reserved row on detach: it resets the scroll region to the
// full screen (bare CSI r) and clears the hint row, leaving the cursor on the now-blank
// bottom row so the board repaint that follows starts clean. rows<=0 (unknown size)
// still resets the region.
func chromeCleanup(rows int) []byte {
	var b strings.Builder
	b.WriteString("\x1b[r") // DECSTBM reset: full-screen scroll region
	if rows > 0 {
		b.WriteString("\x1b[")
		b.WriteString(strconv.Itoa(rows))
		b.WriteString(";1H")
		b.WriteString("\x1b[K")
	}
	return []byte(b.String())
}

// hasDamageSignature reports whether a live frame carries an escape sequence that
// damages the reserved row — a full reset (ESC c), an ED2 screen clear, an alt-screen
// swap (?1049h/l), or a bare CSI r scroll-region reset — so the output pump re-asserts
// region+hint immediately instead of waiting for the throttle. It is a cheap byte scan,
// not a full parse; a rare false positive costs only one extra re-assert.
func hasDamageSignature(f []byte) bool {
	return bytes.Contains(f, []byte("\x1bc")) ||
		bytes.Contains(f, []byte("[2J")) ||
		bytes.Contains(f, []byte("?1049")) ||
		bytes.Contains(f, []byte("\x1b[r"))
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
