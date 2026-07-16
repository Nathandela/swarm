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
}

// NewEmulator creates an emulator with a cols x rows grid.
func NewEmulator(cols, rows int) *Emulator {
	e := &Emulator{
		term:    xvt.NewEmulator(cols, rows),
		cols:    cols,
		rows:    rows,
		visible: true, // DECTCEM defaults to set (cursor shown)
	}
	// These callbacks fire synchronously inside term.Write, which only runs
	// while Feed holds e.mu, so the writes below need no extra locking.
	e.term.SetCallbacks(xvt.Callbacks{
		Title:            func(s string) { e.title = s },
		CursorVisibility: func(v bool) { e.visible = v },
	})
	// Charm answers device queries (DA, DSR, mode/color reports, ...) by writing
	// synchronously into an unbuffered reply pipe during Write; an undrained
	// pipe would block Feed forever on such input. We never consume replies, so
	// drain and discard them on a goroutine. The goroutine captures only the
	// inner terminal, not e, so e stays collectable; a finalizer closes the
	// terminal when e is dropped (Feed/Snapshot/Resize are the whole API — there
	// is no Close), which returns EOF to the drain and lets it exit.
	term := e.term
	go func() { _, _ = io.Copy(io.Discard, term) }()
	runtime.SetFinalizer(e, func(e *Emulator) { _ = e.term.Close() })
	return e
}

// Feed writes raw terminal bytes into the grid. Partial escape sequences and
// multi-byte runes split across calls are buffered by the underlying parser.
func (e *Emulator) Feed(p []byte) {
	e.mu.Lock()
	defer e.mu.Unlock()
	_, _ = e.term.Write(p)
}

// Resize changes the grid dimensions.
func (e *Emulator) Resize(cols, rows int) {
	e.mu.Lock()
	defer e.mu.Unlock()
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

// sanitizeText strips control runes (C0, DEL, C1) from s, leaving printable
// content and spaces. This is the N-6 filter for the two free-form snapshot
// fields, Run.Text and Snap.Title. Invalid UTF-8 decodes to U+FFFD (printable)
// and is preserved, so no raw 0x80-0x9f byte can survive.
func sanitizeText(s string) string {
	if !strings.ContainsFunc(s, isControlRune) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if isControlRune(r) {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// isControlRune reports whether r is a C0 control, DEL, or a C1 control.
func isControlRune(r rune) bool {
	return r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f)
}
