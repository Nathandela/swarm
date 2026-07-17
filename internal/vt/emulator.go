// Package vt is the Epic 2 in-shim terminal emulator: a thin wrapper around
// github.com/charmbracelet/x/vt (ADR-005) that feeds raw PTY bytes into a grid
// and serializes the visible screen to a versioned, hostile-escape-free
// snapshot.
//
// The snapshot is a structured projection (Snap/Line/Run) rather than a raw
// byte replay. Combined with sanitization of every text field, that structure
// is the N-6 filter: no ESC/C0/C1/DEL byte can reach a consumer, because the
// only free-form strings in a snapshot are cell content and the window title,
// and both are stripped of control runes on the way out.
//
// Concurrency model (system-spec Epic 2): Feed is single-goroutine — the caller
// serializes calls — and Snapshot is atomic with respect to Feed. A single
// mutex makes Feed/Resize/Snapshot mutually exclusive so a snapshot always
// observes a consistent grid.
package vt

import (
	"encoding/json"
	"errors"
	"fmt"
	"image/color"
	"io"
	"runtime"
	"strings"
	"sync"
	"unicode"

	uv "github.com/charmbracelet/ultraviolet"
	xvt "github.com/charmbracelet/x/vt"
)

// SnapshotVersion is the schema version embedded in every snapshot. Bump it
// whenever the Snap/Line/Run wire format changes; DecodeSnapshot rejects any
// other version.
const SnapshotVersion = 1

// Run is the projection of a single grapheme cell: its content and style. Runs
// are per-cell (a cell is never merged with a neighbor), so a run's Text is one
// grapheme, or a single space for a blank cell. A double-width grapheme keeps
// its one grapheme in Text and carries Width 2, accounting for the spacer cell
// that follows it. Text never contains ESC/C0/C1/DEL bytes.
type Run struct {
	Text      string `json:"text"`
	Width     int    `json:"width"`
	Fg        string `json:"fg,omitempty"`
	Bg        string `json:"bg,omitempty"`
	Bold      bool   `json:"bold,omitempty"`
	Faint     bool   `json:"faint,omitempty"`
	Italic    bool   `json:"italic,omitempty"`
	Underline bool   `json:"underline,omitempty"`
	Reverse   bool   `json:"reverse,omitempty"`
}

// Line is one grid row as an ordered list of cell runs. The runs' Width fields
// sum to the snapshot's Cols.
type Line struct {
	Runs []Run `json:"runs"`
}

// Snap is the serialized visible screen: dimensions, cursor, mode flags, the
// sanitized window title, and the grid as Rows lines.
type Snap struct {
	Version       int    `json:"version"`
	Cols          int    `json:"cols"`
	Rows          int    `json:"rows"`
	AltScreen     bool   `json:"altScreen"`
	CursorX       int    `json:"cursorX"`
	CursorY       int    `json:"cursorY"`
	CursorVisible bool   `json:"cursorVisible"`
	Title         string `json:"title,omitempty"`
	Lines         []Line `json:"lines"`
}

// Emulator wraps a charm x/vt emulator with Feed/Snapshot/Resize and the
// hostile-escape filter. The zero value is not usable; call NewEmulator.
type Emulator struct {
	mu      sync.Mutex
	term    *xvt.Emulator
	cols    int
	rows    int
	title   string // latest raw window title (OSC 0/2), sanitized on snapshot
	visible bool   // cursor visibility (DECTCEM), tracked via callback

	reply     replySink     // query-reply destination; discards until SetReplyWriter
	closing   chan struct{} // closed by Close; isClosed does a lock-free receive on it
	drainDone chan struct{} // closed when the reply-drain goroutine exits
	closeOnce sync.Once
}

// isClosed reports whether Close has run. It never blocks and never touches
// mu: the drain goroutine calls it between reads, and if it had to wait on mu
// it could deadlock against a Close that is itself waiting inside term.Write
// for that same goroutine to read the provoked reply (see Close).
func (e *Emulator) isClosed() bool {
	select {
	case <-e.closing:
		return true
	default:
		return false
	}
}

// replySink is a concurrency-safe, swappable io.Writer: the drain goroutine
// copies charm's query replies through it while SetReplyWriter may retarget
// the destination at any time, including before the first Feed. A nil
// destination discards.
type replySink struct {
	mu sync.Mutex
	w  io.Writer
}

func (s *replySink) Write(p []byte) (int, error) {
	s.mu.Lock()
	w := s.w
	s.mu.Unlock()
	if w == nil {
		return len(p), nil
	}
	return w.Write(p)
}

func (s *replySink) set(w io.Writer) {
	s.mu.Lock()
	s.w = w
	s.mu.Unlock()
}

// NewEmulator creates an emulator with a cols x rows grid.
func NewEmulator(cols, rows int) *Emulator {
	e := &Emulator{
		term:      xvt.NewEmulator(cols, rows),
		cols:      cols,
		rows:      rows,
		visible:   true, // DECTCEM defaults to set (cursor shown)
		closing:   make(chan struct{}),
		drainDone: make(chan struct{}),
	}
	// These callbacks fire synchronously inside term.Write, which only runs
	// while Feed holds e.mu, so the writes below need no extra locking.
	e.term.SetCallbacks(xvt.Callbacks{
		Title:            func(s string) { e.title = s },
		CursorVisibility: func(v bool) { e.visible = v },
	})
	// Charm answers device queries (DA, DSR, mode/color reports, ...) by writing
	// synchronously into an unbuffered reply pipe during Write; an undrained
	// pipe would block Feed forever on such input. A goroutine drains that pipe
	// into e.reply for the emulator's lifetime, discarding replies until
	// SetReplyWriter retargets them.
	//
	// The underlying charm Emulator's own Read/Close race on an unsynchronized
	// field (its Read checks a closed bool that its Close sets, with no lock),
	// so this wrapper never calls term.Close() while the drain goroutine might
	// be blocked in term.Read() — see Close below and ADR-005 "Known
	// limitations". The goroutine captures e (via e.reply and e.isClosed), so e
	// stays reachable for as long as the goroutine runs; it exits only when
	// Close provokes it or term.Read returns an error. The finalizer is a
	// best-effort fallback for the path where Close is never called: it can run
	// only once the goroutine has already exited on its own (e.g. term.Read
	// errored) and e has become unreachable, closing the inner terminal that
	// would otherwise leak. It is not a substitute for Close — a goroutine
	// parked forever in term.Read keeps e alive and the finalizer never fires.
	// By the time Close returns, the drain goroutine has exited, so a later
	// finalizer run never overlaps it either.
	term := e.term
	go func() {
		defer close(e.drainDone)
		buf := make([]byte, 4096)
		for {
			n, err := term.Read(buf)
			if n > 0 {
				_, _ = e.reply.Write(buf[:n])
			}
			if err != nil {
				return
			}
			if e.isClosed() {
				return
			}
		}
	}()
	runtime.SetFinalizer(e, func(e *Emulator) { _ = e.term.Close() })
	return e
}

// SetReplyWriter routes every reply the emulator generates while processing
// Feed (device queries such as DSR/DA) to w, replacing the discard drain. It
// may be called before the first Feed.
func (e *Emulator) SetReplyWriter(w io.Writer) {
	e.reply.set(w)
}

// Close retires the emulator and stops the reply-drain goroutine. It never
// calls the underlying charm terminal's Close (see the race note in
// NewEmulator); instead, under mu (so it is serialized with any in-flight
// Feed/Resize the same way they serialize with each other), it marks the
// emulator closed and provokes a harmless device-status query (DSR
// "operating status", always answered unconditionally with "ESC[0n"), whose
// synchronous reply unblocks a drain goroutine that may be parked in a
// blocking Read, letting it observe closed and exit on its own. Close waits
// for that exit before returning, so the drain is stopped deterministically.
// After Close, Feed and Resize become no-ops (either could otherwise write
// into the reply pipe with no reader left to drain it, deadlocking);
// Snapshot keeps working since it only reads grid state and never touches
// the pipe. Close always returns nil and is idempotent and safe to call
// concurrently or more than once.
func (e *Emulator) Close() error {
	e.closeOnce.Do(func() {
		e.mu.Lock()
		close(e.closing)
		_, _ = e.term.Write([]byte("\x1b[5n"))
		e.mu.Unlock()
		<-e.drainDone
	})
	return nil
}

// Feed writes raw terminal bytes into the grid. Partial escape sequences and
// multi-byte runes split across calls are buffered by the underlying parser.
// A no-op after Close.
func (e *Emulator) Feed(p []byte) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.isClosed() {
		return
	}
	_, _ = e.term.Write(p)
}

// Resize changes the grid dimensions. A no-op after Close (in-band-resize
// mode can make charm write a reply into the pipe, same as Feed).
func (e *Emulator) Resize(cols, rows int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.isClosed() {
		return
	}
	e.term.Resize(cols, rows)
	e.cols = cols
	e.rows = rows
}

// Snapshot serializes the current visible screen to versioned JSON. It is
// deterministic: repeated calls with no intervening Feed return identical bytes.
func (e *Emulator) Snapshot() ([]byte, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return json.Marshal(e.buildSnap())
}

// buildSnap assembles the Snap from the current terminal state. Caller holds mu.
func (e *Emulator) buildSnap() *Snap {
	pos := e.term.CursorPosition()
	s := &Snap{
		Version:       SnapshotVersion,
		Cols:          e.cols,
		Rows:          e.rows,
		AltScreen:     e.term.IsAltScreen(),
		CursorX:       pos.X,
		CursorY:       pos.Y,
		CursorVisible: e.visible,
		Title:         sanitizeText(e.title),
		Lines:         make([]Line, e.rows),
	}
	for y := 0; y < e.rows; y++ {
		s.Lines[y] = e.buildLine(y)
	}
	return s
}

// buildLine walks a row's cells left to right, emitting one run per grapheme
// cell. A wide grapheme's content cell yields a run of Width 2, and its spacer
// cell is skipped by advancing past it. A blank cell yields a single space.
// Caller holds mu.
func (e *Emulator) buildLine(y int) Line {
	runs := make([]Run, 0, e.cols)
	for x := 0; x < e.cols; {
		c := e.term.CellAt(x, y)
		w := 1
		content := " "
		var st uv.Style
		if c != nil {
			if c.Width > 1 {
				w = c.Width
			}
			if c.Content != "" {
				content = c.Content
			}
			st = c.Style
		}
		// Never let a cell spill past the last column (defensive: charm tiles
		// rows exactly, so a wide glyph never straddles the right edge).
		if x+w > e.cols {
			w = e.cols - x
		}
		runs = append(runs, styleRun(st, sanitizeText(content), w))
		x += w
	}
	return Line{Runs: runs}
}

// DecodeSnapshot parses snapshot bytes and validates the schema version.
func DecodeSnapshot(b []byte) (*Snap, error) {
	if len(b) == 0 {
		return nil, errors.New("vt: empty snapshot")
	}
	var s Snap
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("vt: decode snapshot: %w", err)
	}
	if s.Version != SnapshotVersion {
		return nil, fmt.Errorf("vt: unsupported snapshot version %d (want %d)", s.Version, SnapshotVersion)
	}
	return &s, nil
}

// styleRun builds a Run for one cell from its style, sanitized text, and width.
func styleRun(st uv.Style, text string, width int) Run {
	return Run{
		Text:      text,
		Width:     width,
		Fg:        colorSpec(st.Fg),
		Bg:        colorSpec(st.Bg),
		Bold:      st.Attrs&uv.AttrBold != 0,
		Faint:     st.Attrs&uv.AttrFaint != 0,
		Italic:    st.Attrs&uv.AttrItalic != 0,
		Underline: st.Underline != uv.UnderlineNone,
		Reverse:   st.Attrs&uv.AttrReverse != 0,
	}
}

// colorSpec renders a color as a deterministic "#rrggbb" string, or "" for the
// terminal default (nil). Determinism matters for byte-identical snapshots.
func colorSpec(c color.Color) string {
	if c == nil {
		return ""
	}
	r, g, b, _ := c.RGBA()
	return fmt.Sprintf("#%02x%02x%02x", uint8(r>>8), uint8(g>>8), uint8(b>>8))
}

// sanitizeText replaces every non-printable rune of s with a space, keeping
// only printable runes and the ASCII space. This is the N-6 filter for the two
// free-form snapshot fields, Run.Text and Snap.Title. It removes ESC/C0/C1/DEL,
// and — because the snapshot feeds a security-sensitive viewer — every other
// non-printable rune too: NBSP, bidi overrides (e.g. U+202E RLO), zero-width
// marks (U+200B, U+FEFF) and line/paragraph separators (U+2028/U+2029), which
// are the Trojan-source class that reorders, hides, or spoofs content.
// Replacing rather than dropping preserves cell width and never shifts columns.
// Invalid UTF-8 decodes to U+FFFD, which is printable and kept, so no raw
// 0x80-0x9f byte can survive.
func sanitizeText(s string) string {
	return strings.Map(func(r rune) rune {
		if r == ' ' || unicode.IsPrint(r) {
			return r
		}
		return ' '
	}, s)
}
