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

// chromeHealInterval bounds how long reserved-row damage in the LAST frame of a burst
// can persist: after each frame batch a one-shot timer is (re)armed, and if it fires with
// no further output the main loop re-asserts region+hint (at a safe boundary only). It
// also caps the window an absolute-addressing bottom-row stomp (CSI 999;1H, which DECSTBM
// cannot clamp) stays visible to one interval (ADR-006 v0.3.0).
const chromeHealInterval = 300 * time.Millisecond

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

	out := cfg.Term.Out()
	// State the panic handler reads for best-effort chrome teardown (item 8): if a fault
	// unwinds Run while the reserved row was ENGAGED, reset the scroll region so the agent
	// is not left confined and the hint row is not left dirty. curCols/curRows track the
	// latest real terminal size (updated by the resize pump).
	var (
		chromeEngaged    bool
		curCols, curRows int
	)
	// A fault anywhere in the loop (e.g. a mid-render write panic) is recovered so the user
	// lands back on a sane terminal and the general view, never a wrecked screen behind a
	// crashed TUI (E8.2). The deferred restore above still runs.
	defer func() {
		if r := recover(); r != nil {
			if chromeEngaged {
				// Best-effort: out may be mid-fault, so guard the write against a re-panic.
				func() {
					defer func() { _ = recover() }()
					writeAll(out, chromeCleanup(curRows))
				}()
			}
			reason, err = ReasonError, fmt.Errorf("attach: recovered panic: %v", r)
		}
	}()

	// Raw-then-paint: the snapshot (and optional chrome) is the first thing written,
	// exactly once, before any live frame reaches the terminal (S10/A-1/A-4).
	//
	// The snapshot is a structured projection, not raw replay bytes, so it must be
	// decoded and rendered to ANSI before it can paint (P0 agents-tracker-a6f: raw
	// JSON was reaching the terminal).
	//
	// Fetch the client terminal size BEFORE painting so the snapshot can be clipped
	// to it: a snapshot from a larger terminal would otherwise pile excess rows onto
	// the bottom line, and a wider row would wrap (a bottom-row wrap scrolls). On a
	// Size() error we fall back to an unclipped paint (0, 0). The same size drives the
	// resize below, so Size() is queried once here rather than twice.
	cols, rows, sizeErr := cfg.Term.Size()
	if sizeErr != nil {
		cols, rows = 0, 0
	}
	// wasAlt records whether the snapshot paint entered the alternate screen
	// (render.go's RenderSnapshotClipped writes CSI ?1049h when the snapshot was
	// captured there). It is the ONLY source of truth teardownAlt uses: the
	// client never parses the live passthrough stream (R4.2.2), so it has no way
	// to know whether the agent's own output already exited alt by the time Run
	// tears down.
	var wasAlt bool
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
	// parser tracks the terminal-output parser's position across all agent bytes
	// (snapshot + frames) so the pump only injects re-assert bytes at a safe boundary
	// (GROUND) — never mid escape sequence (A-1, ADR-006 v0.3.0).
	var parser outParser
	if snap, derr := vt.DecodeSnapshot(cfg.Session.Snapshot()); derr == nil {
		wasAlt = snap.AltScreen
		// Clip the snapshot to the reserved area (rows-1 when chrome is on) so it never
		// paints over the hint row.
		rendered := vt.RenderSnapshotClipped(snap, cols, ptyRows(rows))
		writeAll(out, rendered)
		parser.feedAll(rendered)
	} else {
		// A snapshot that fails to decode paints a single plain notice instead of
		// nothing, so an attach to a session with a skewed/corrupt snapshot is never
		// a silent blank screen — even an idle agent with no live frames shows this
		// line (A-4 never-blank). It carries no escape bytes; the live stream follows.
		notice := []byte(snapshotUnavailableNotice)
		writeAll(out, notice)
		parser.feedAll(notice)
	}
	// Chrome is established AFTER the grid paint, whose clear+home would otherwise wipe
	// it: chromeHint sets the scroll region and paints the hint on the reserved bottom
	// row, saving/restoring the cursor (DECSC/DECRC) so the snapshot's cursor position
	// survives (A-5). lastAssert seeds the output pump's re-assert throttle; chromeEngaged
	// records that the region is ours to tear down.
	var lastAssert time.Time
	if chromeActive(rows) {
		writeAll(out, chromeHint(cfg.Name, detachKey, cols, rows))
		chromeEngaged = true
		lastAssert = time.Now()
	}

	// Sync the PTY to the client's terminal size on attach (E8.3), reserving the hint
	// row when chrome is engaged; further changes propagate via the resize pump.
	if sizeErr == nil {
		_ = cfg.Session.Resize(cols, ptyRows(rows))
	}
	// curCols/curRows (declared above for the panic handler) now hold the attach-time
	// size; the resize pump keeps them current so the output pump's re-assert and the
	// detach cleanup target the current bottom row rather than the attach-time one.
	curCols, curRows = cols, rows

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
			// Detach recognition is deliberately solo-byte (D4 RULED,
			// agents-tracker-rs8): only a read that yields the detach key ALONE
			// (n==1) detaches. The pump has no bracketed-paste state machine, so
			// scanning every read for the byte would risk detaching mid-paste on
			// any input that happens to carry it; solo-read is overwhelmingly the
			// common case for a real keypress, and a missed detach under an input
			// flood is rare and recoverable by pressing again. Documented
			// limitation, not a bug: revisit only on field evidence.
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

	// teardownAlt undoes the alt-screen entry the initial snapshot paint made
	// (R4.2.2, agents-tracker-rs8). It is idempotent on a terminal already back
	// in the main buffer — exit-alt (CSI ?1049l) is a no-op there, and
	// cursor-show/SGR-reset are always safe — so emitting it unconditionally
	// whenever wasAlt is true is safe regardless of whether the live stream
	// already exited alt itself. wasAlt false (never entered alt) emits nothing,
	// so a main-buffer session's terminal is never touched here.
	teardownAlt := func() {
		if wasAlt {
			writeAll(out, []byte("\x1b[?1049l\x1b[?25h\x1b[0m"))
		}
	}

	// Termination signals (registered above, before raw mode) restore the terminal
	// before the loop tears down, so a raw-mode client killed by SIGINT/SIGTERM/
	// SIGHUP never leaves a wrecked terminal behind (E8.2).
	frames := cfg.Session.Frames()
	// finish tears the loop down on the terminal paths: it releases the lease, and when
	// chrome actually ENGAGED a row it resets the scroll region to full and clears the hint
	// row so the board repaint that follows starts on a clean full-height screen, then hands
	// the terminal back. Keying on chromeEngaged (not cfg.Chrome) keeps a Chrome:true run on
	// a too-small (rows<=2) terminal byte-identical to Chrome:false at teardown (item 4). The
	// panic path does not route through here — its deferred restore still runs, but out may
	// be mid-fault, so its chrome cleanup is best-effort.
	finish := func(r Reason) (Reason, error) {
		_ = cfg.Session.Detach()
		// Order matters when BOTH chrome and alt are active (deployment-committee,
		// codex HIGH via agents-tracker-rs8): tear chrome down FIRST, then exit alt.
		// chromeCleanup's bottom-row CUP+clear is BUFFER-LOCAL, so run before the
		// alt-exit it clears the abandoned alt buffer's hint row (harmless); run after
		// it (the old order) it lands on the freshly restored PRIMARY buffer and erases
		// the user's shell line. The DECSTBM reset chromeCleanup carries is GLOBAL
		// state, effective from either side of the buffer switch. Keying on
		// chromeEngaged (not cfg.Chrome) keeps a too-small (rows<=2) run byte-identical
		// to Chrome:false. teardownAlt is a no-op when the snapshot was never alt.
		if chromeEngaged {
			writeAll(out, chromeCleanup(curRows))
		}
		teardownAlt()
		restore()
		return r, nil
	}

	// pendingReassert remembers that a re-assert is OWED (damage was seen) but could not be
	// injected because the parser was mid-sequence; the next safe boundary flushes it.
	var pendingReassert bool
	// pendingResize remembers that a resize's region/hint bytes are OWED but the parser was
	// mid-sequence when the resize arrived; the next safe boundary applies them at the
	// CURRENT size (the same ground-state gate as the frame and heal paths — a resize must
	// never splice bytes into the agent's escape sequence either).
	var pendingResize bool
	// healCh delivers the trailing-heal timer's fire into the main loop (the sole writer to
	// out); the timer goroutine only nudges, never writes. armHeal (re)arms the one-shot
	// after each frame batch so damage in the LAST frame of a burst self-heals with no
	// further output.
	healCh := make(chan struct{}, 1)
	var healTimer *time.Timer
	armHeal := func() {
		if healTimer == nil {
			healTimer = time.AfterFunc(chromeHealInterval, func() {
				select {
				case healCh <- struct{}{}:
				default:
				}
			})
			return
		}
		healTimer.Reset(chromeHealInterval)
	}
	defer func() {
		if healTimer != nil {
			healTimer.Stop()
		}
	}()
	// reassertChrome re-emits region+hint at the current size and clears the owed re-assert.
	// The caller has verified the parser is in GROUND (a safe boundary).
	reassertChrome := func() {
		writeAll(out, chromeHint(cfg.Name, detachKey, curCols, curRows))
		lastAssert = time.Now()
		pendingReassert = false
	}
	// applyResizeChrome emits the resize's chrome bytes for the CURRENT size — establish at
	// the new dimensions, or release the region when the terminal shrank below the reserve
	// threshold (only if chrome had actually ENGAGED, keeping never-engaged small terminals
	// byte-identical to Chrome:false). The caller has verified the parser is in GROUND.
	applyResizeChrome := func() {
		if chromeActive(curRows) {
			writeAll(out, chromeHint(cfg.Name, detachKey, curCols, curRows))
			chromeEngaged = true
			lastAssert = time.Now()
		} else if chromeEngaged {
			writeAll(out, []byte("\x1b[r"))
			chromeEngaged = false
		}
		pendingResize = false
	}

	for {
		select {
		case f, ok := <-frames:
			if !ok {
				return finish(ReasonSessionEnd)
			}
			writeAll(out, f)
			parser.feedAll(f)
			// An owed resize (one that arrived mid-sequence) is applied first, at the current
			// size, once the boundary is safe — outside the chromeActive gate below because
			// the release case (terminal shrank under the threshold) must drain too.
			if pendingResize && parser.inGround() {
				applyResizeChrome()
			}
			// The agent's own output can damage the reserved row — a full reset (ESC c), an
			// ED2/ED0 clear, an alt-screen swap, or a bare CSI r region reset. Re-assert
			// region+hint after the frame: on a damage signature (owed via pendingReassert)
			// or once the throttle elapses — but ONLY at a safe boundary (parser in GROUND),
			// so injected bytes never split the agent's escape sequence (A-1). A trailing heal
			// timer is (re)armed so damage in the burst's last frame heals with no further
			// output.
			if chromeActive(curRows) {
				if hasDamageSignature(f) {
					pendingReassert = true
				}
				if (pendingReassert || time.Since(lastAssert) >= chromeReassertInterval) && parser.inGround() {
					reassertChrome()
				}
				armHeal()
			}
		case <-healCh:
			// The trailing heal fired: apply an owed resize first, then re-assert if chrome
			// is engaged — both only at a safe boundary (parser in GROUND); otherwise re-arm
			// and try again next interval (never inject mid escape sequence).
			if parser.inGround() {
				if pendingResize {
					applyResizeChrome()
				} else if chromeActive(curRows) {
					reassertChrome()
				}
			} else if pendingResize || chromeActive(curRows) {
				armHeal()
			}
		case <-detachCh:
			return finish(ReasonDetached)
		case <-sigCh:
			return finish(ReasonDetached)
		case <-winCh:
			// A resize arrived (chrome only): re-read the current size and re-establish the
			// reserved row at the new dimensions. If the terminal shrank below the reserve
			// threshold, release the scroll region so the now-full-height agent is not
			// confined — but only if chrome had actually ENGAGED (a never-engaged small
			// terminal emits nothing, staying byte-identical to Chrome:false).
			if c, r, e := cfg.Term.Size(); e == nil {
				curCols, curRows = c, r
				if parser.inGround() {
					applyResizeChrome()
				} else {
					// Mid-sequence: defer the bytes to the next safe boundary (the PTY was
					// already resized by the pump; only the terminal writes wait). armHeal
					// guarantees a boundary check even if the stream goes silent.
					pendingResize = true
					armHeal()
				}
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
// paints the faint/dim return hint on the real bottom row. The whole sequence is
// wrapped in DECSC/DECRC so the agent's (or the snapshot's) cursor position and pen
// survive both the region change (DECSTBM homes the cursor) and the paint. The caller
// guarantees rows > 2. It is re-emitted by the output pump to self-heal damage (a bare
// CSI r region reset from the agent is repaired by the next re-assert).
func chromeHint(name string, detachKey byte, cols, rows int) []byte {
	var b strings.Builder
	b.WriteString("\x1b7")    // DECSC: save cursor + pen + origin mode
	b.WriteString("\x1b[?6l") // DECOM off: make the reserved-row CUP absolute even if the agent enabled origin mode; DECRC below restores the agent's DECOM
	b.WriteString("\x1b[1;")
	b.WriteString(strconv.Itoa(rows - 1))
	b.WriteByte('r') // DECSTBM: scroll region 1..rows-1
	b.WriteString("\x1b[")
	b.WriteString(strconv.Itoa(rows))
	b.WriteString(";1H") // CUP to the reserved bottom row
	// v0.4 P2 (dim not reverse): a faint default-colored hint, not the harsh full-width
	// reverse-video bar that read as a white strip on dark terminals.
	b.WriteString("\x1b[2m") // faint/dim
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
// damages the reserved row — a full reset (ESC c), an ED2 or ED0 screen clear, an
// alt-screen swap (?1049/?1047/?47), or a bare CSI r scroll-region reset — so the output
// pump re-asserts region+hint immediately instead of waiting for the throttle. It is a
// cheap per-frame byte scan, not a full parse; a rare false positive costs only one extra
// re-assert. Residual: a damage signature SPLIT across two frames is not caught here (the
// scan is per-frame), but the trailing heal timer re-asserts within one interval regardless.
func hasDamageSignature(f []byte) bool {
	return bytes.Contains(f, []byte("\x1bc")) || // RIS full reset
		bytes.Contains(f, []byte("[2J")) || // ED2: full-screen clear
		bytes.Contains(f, []byte("[J")) || // ED0: clear to end of screen (default param)
		bytes.Contains(f, []byte("[0J")) || // ED0 explicit
		bytes.Contains(f, []byte("?1049")) || // alt-screen swap (save/restore cursor)
		bytes.Contains(f, []byte("?1047")) || // alt-screen swap (no cursor save)
		bytes.Contains(f, []byte("?47")) || // legacy alt-screen swap
		bytes.Contains(f, []byte("\x1b[r")) // bare DECSTBM reset
}

// outParser is a minimal, allocation-free tracker of the terminal-output parser's
// position in the byte stream, so re-assert bytes are injected ONLY at a safe boundary
// (GROUND) — never mid escape sequence, where they would abort the agent's pending
// sequence and make its continuation render as literal text (A-1 purity, ADR-006 v0.3.0).
// It is fed every OUTPUT byte from the agent (snapshot + frames) in order. It errs toward
// "still in a sequence": a false non-ground only defers an injection (safe), whereas a
// false ground could split a sequence (unsafe).
type outParser struct{ st ptState }

type ptState uint8

const (
	ptGround ptState = iota // between sequences: a safe injection point
	ptEsc                   // ESC seen, awaiting the sequence type
	ptCSI                   // CSI (ESC [): params/intermediates until a final 0x40-0x7e
	ptNF                    // nF escape (ESC + intermediate): until a final 0x30-0x7e
	ptStr                   // string (OSC/DCS/APC/PM/SOS): until ST (ESC \) or BEL
	ptStrEsc                // ESC inside a string: awaiting the '\' of ST
)

// feed advances the tracker by one output byte.
func (p *outParser) feed(b byte) {
	switch p.st {
	case ptGround:
		if b == 0x1b {
			p.st = ptEsc
		}
	case ptEsc:
		switch {
		case b == '[':
			p.st = ptCSI
		case b == ']' || b == 'P' || b == '_' || b == '^' || b == 'X': // OSC/DCS/APC/PM/SOS
			p.st = ptStr
		case b >= 0x20 && b <= 0x2f: // nF intermediate (e.g. ESC ( B)
			p.st = ptNF
		case b == 0x1b: // ESC ESC: restart the escape
			p.st = ptEsc
		default: // a two-byte escape completes (ESC 7/8/c/=/> M ...)
			p.st = ptGround
		}
	case ptCSI:
		switch {
		case b == 0x1b: // aborted; a new escape begins
			p.st = ptEsc
		case b >= 0x40 && b <= 0x7e: // CSI final byte
			p.st = ptGround
		default: // params (0x30-0x3f), intermediates (0x20-0x2f), or stray: keep consuming
		}
	case ptNF:
		switch {
		case b == 0x1b:
			p.st = ptEsc
		case b >= 0x30 && b <= 0x7e: // nF final byte
			p.st = ptGround
		default: // more intermediates
		}
	case ptStr:
		switch {
		case b == 0x07: // BEL terminates (OSC)
			p.st = ptGround
		case b == 0x1b: // possible ST (ESC \)
			p.st = ptStrEsc
		default: // string payload
		}
	case ptStrEsc:
		switch {
		case b == '\\': // ST completes the string
			p.st = ptGround
		case b == 0x1b: // another ESC: keep awaiting '\'
			p.st = ptStrEsc
		default: // ESC + other inside a string: treat as string body
			p.st = ptStr
		}
	}
}

// feedAll advances the tracker by every byte of p, in order.
func (p *outParser) feedAll(b []byte) {
	for _, c := range b {
		p.feed(c)
	}
}

// inGround reports whether the parser is between sequences — a safe injection point.
func (p *outParser) inGround() bool { return p.st == ptGround }

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
