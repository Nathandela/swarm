package vt

import (
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/ansi/parser"
)

// MouseState records requested DEC modes as masks. Multiple enabled modes stay
// explicit, allowing consumers to refuse ambiguous combinations. Known=false
// means no safe ownership decision can be made from this observation.
type MouseState struct {
	Tracking uint16 `json:"tracking"`
	Encoding uint16 `json:"encoding"`
	Known    bool   `json:"known"`
}

// InputModes records the effective DEC private input modes active at the
// snapshot boundary so an interactive attach can restore mouse, alternate-scroll,
// paste, focus, and cursor-key reporting enabled before attach or prior to detach.
type InputModes struct {
	Known          bool  `json:"known"`
	Tracking       int   `json:"tracking,omitempty"`   // 0, 9, 1000, 1001, 1002, 1003
	Encoding       int   `json:"encoding,omitempty"`   // 0, 1005, 1006, 1015, 1016
	AltScroll      *bool `json:"altScroll,omitempty"`  // ?1007 when explicitly set (h) or reset (l)
	BracketedPaste bool  `json:"paste,omitempty"`      // ?2004h
	FocusEvents    bool  `json:"focus,omitempty"`      // ?1004h
	CursorKeys     bool  `json:"cursorKeys,omitempty"` // ?1h (DECCKM)
}

func (m InputModes) validTracking() bool {
	switch m.Tracking {
	case 0, 9, 1000, 1001, 1002, 1003:
		return true
	}
	return false
}

func (m InputModes) validEncoding() bool {
	switch m.Encoding {
	case 0, 1005, 1006, 1015, 1016:
		return true
	}
	return false
}

const (
	MouseTrackX10 uint16 = 1 << iota
	MouseTrackNormal
	MouseTrackHighlight
	MouseTrackButton
	MouseTrackAny
)

const (
	MouseEncodeUTF8 uint16 = 1 << iota
	MouseEncodeSGR
	MouseEncodeURXVT
	MouseEncodePixel
)

func (s MouseState) valid() bool {
	return s.Tracking&^(MouseTrackX10|MouseTrackNormal|MouseTrackHighlight|MouseTrackButton|MouseTrackAny) == 0 &&
		s.Encoding&^(MouseEncodeUTF8|MouseEncodeSGR|MouseEncodeURXVT|MouseEncodePixel) == 0
}

func (s MouseState) ambiguous() bool {
	return s.Tracking&(s.Tracking-1) != 0 || s.Encoding&(s.Encoding-1) != 0
}

// MouseTracker observes raw terminal output without retaining payloads. Callers
// serialize Feed/State. A nil initial state is unknown (an older snapshot).
type MouseTracker struct {
	p                  *ansi.Parser
	state              MouseState
	changed            bool
	private, malformed bool
	length, digits     int
	params             int
	stringKind         byte
	stringEsc          bool
	utf8Left           int

	inputKnown     bool
	inputTracking  int
	inputEncoding  int
	altScrollKnown bool
	altScroll      bool
	bracketedPaste bool
	focusEvents    bool
	cursorKeys     bool
}

func NewMouseTracker(initial *MouseState) *MouseTracker {
	t := &MouseTracker{p: new(ansi.Parser)}
	t.p.SetParamsSize(32)
	t.p.SetDataSize(1)
	if initial != nil {
		t.inputKnown = true
		t.state = *initial
		if !t.state.valid() || t.state.ambiguous() {
			t.state.Known = false
		}
	}
	t.p.SetHandler(ansi.Handler{HandleCsi: t.csi, HandleEsc: func(cmd ansi.Cmd) {
		if cmd == 'c' {
			t.set(MouseState{Known: true})
			t.resetInputModes()
		}
	}})
	return t
}

func (t *MouseTracker) resetInputModes() {
	t.inputKnown = true
	t.inputTracking = 0
	t.inputEncoding = 0
	t.altScrollKnown = false
	t.altScroll = false
	t.bracketedPaste = false
	t.focusEvents = false
	t.cursorKeys = false
}

func (t *MouseTracker) State() MouseState { return t.state }

func (t *MouseTracker) snapshotState() MouseState {
	s := t.state
	// A new consumer cannot resume a parser prefix omitted by the snapshot.
	if t.stringKind != 0 || t.p.State() != parser.GroundState {
		s.Known = false
	}
	return s
}

func (t *MouseTracker) snapshotInputModes() *InputModes {
	m := &InputModes{
		Known:          t.inputKnown,
		Tracking:       t.inputTracking,
		Encoding:       t.inputEncoding,
		BracketedPaste: t.bracketedPaste,
		FocusEvents:    t.focusEvents,
		CursorKeys:     t.cursorKeys,
	}
	if t.altScrollKnown {
		v := t.altScroll
		m.AltScroll = &v
	}
	return m
}

// Feed reports any intermediate state change, even a change undone in this chunk.
// Consumers that must inspect each intermediate value can feed one byte at a time.
func (t *MouseTracker) Feed(p []byte) bool {
	t.changed = false
	for _, b := range p {
		if t.stringKind != 0 {
			if t.utf8Left > 0 && b >= 0x80 && b < 0xc0 {
				if b == 0x9c {
					t.unknown() // Terminals differ on C1 ST inside UTF-8 string data.
				}
				t.utf8Left--
				continue
			}
			if t.stringEsc && b != '\\' {
				t.unknown() // Nested escapes/passthrough wrappers may execute terminal modes.
			}
			t.utf8Left = 0
			switch {
			case b >= 0xc2 && b <= 0xdf:
				t.utf8Left = 1
			case b >= 0xe0 && b <= 0xef:
				t.utf8Left = 2
			case b >= 0xf0 && b <= 0xf4:
				t.utf8Left = 3
			}
			if b == 0x18 || b == 0x1a || b == 0x9c || t.stringEsc && b == '\\' || t.stringKind == ']' && b == 0x07 {
				t.stringKind = 0
				t.p.Reset()
			}
			t.stringEsc = b == 0x1b
			continue
		}
		old := t.p.State()
		if old == parser.EscapeState && (b == ']' || b == 'P' || b == '_' || b == '^' || b == 'X') {
			t.stringKind, t.stringEsc = b, false
			t.p.Reset()
			continue
		}
		if old != parser.Utf8State && (b == 0x9d || b == 0x90 || b == 0x9f || b == 0x9e || b == 0x98) {
			t.stringKind, t.stringEsc = 'P', false
			if b == 0x9d {
				t.stringKind = ']'
			}
			t.p.Reset()
			continue
		}
		if b == 0x1b || old != parser.Utf8State && b == 0x9b {
			t.private, t.malformed = false, false
			t.length, t.digits, t.params = 0, 0, 1
		}
		if old == parser.CsiEntryState && b == '?' {
			t.private = true
		}
		if old == parser.CsiEntryState || old == parser.CsiParamState || old == parser.CsiIntermediateState {
			if t.length <= 256 {
				t.length++
			}
			switch {
			case b >= '0' && b <= '9':
				if t.digits <= 5 {
					t.digits++
				}
			case b == ';' || b == ':':
				t.digits = 0
				if t.params <= 32 {
					t.params++
				}
			}
			if t.length > 256 || t.digits > 5 || t.params > 32 || b < 0x20 && b != 0x1b {
				t.malformed = true
			}
		}
		t.p.Advance(b)
	}
	return t.changed
}

func (t *MouseTracker) set(s MouseState) {
	// Requested masks do not preserve a terminal's mutually exclusive mode
	// selection order. Clearing a bit later cannot reconstruct that history.
	if s.ambiguous() {
		s.Known = false
	}
	if s != t.state {
		t.state = s
		t.changed = true
	}
}

func (t *MouseTracker) unknownMouse() { s := t.state; s.Known = false; t.set(s) }

func (t *MouseTracker) unknown() {
	t.unknownMouse()
	t.inputKnown = false
}

func isTrackedInputMode(n int) bool {
	switch n {
	case 1, 1004, 1007, 2004:
		return true
	}
	return false
}

func (t *MouseTracker) observeInputMode(n int, enable bool) {
	switch n {
	case 9, 1000, 1001, 1002, 1003:
		if enable {
			t.inputTracking = n
		} else if t.inputTracking == n {
			t.inputTracking = 0
		}
	case 1005, 1006, 1015, 1016:
		if enable {
			t.inputEncoding = n
		} else if t.inputEncoding == n {
			t.inputEncoding = 0
		}
	case 1:
		t.cursorKeys = enable
	case 1004:
		t.focusEvents = enable
	case 1007:
		t.altScrollKnown = true
		t.altScroll = enable
	case 2004:
		t.bracketedPaste = enable
	}
}

func (t *MouseTracker) csi(cmd ansi.Cmd, params ansi.Params) {
	if cmd == ansi.Cmd('p'|('!'<<16)) {
		t.cursorKeys = false
		t.unknownMouse() // DECSTR handling varies; only RIS establishes a known mouse reset.
		return
	}
	final := byte(cmd)
	if !t.private || final != 'h' && final != 'l' && final != 'r' {
		return // Queries and saves do not change active mouse modes.
	}
	if t.malformed || cmd.Prefix() != '?' || cmd.Intermediate() != 0 {
		t.unknown()
		return
	}
	for _, p := range params {
		if p.HasMore() {
			t.unknown()
			return
		}
	}
	if final == 'r' && len(params) == 0 {
		t.unknown()
	}
	for _, p := range params {
		n := p.Param(0)
		if final == 'h' || final == 'l' {
			t.observeInputMode(n, final == 'h')
		}
		var tracking, encoding uint16
		switch n {
		case 9:
			tracking = MouseTrackX10
		case 1000, 1001, 1002, 1003:
			tracking = MouseTrackNormal << (n - 1000)
		case 1005:
			encoding = MouseEncodeUTF8
		case 1006:
			encoding = MouseEncodeSGR
		case 1015:
			encoding = MouseEncodeURXVT
		case 1016:
			encoding = MouseEncodePixel
		default:
			if final == 'r' && isTrackedInputMode(n) {
				t.inputKnown = false
			}
			continue
		}
		if final == 'r' {
			t.unknown() // Save slots are not projected; do not invent restored values.
			return
		}
		s := t.state
		if final == 'h' {
			s.Tracking |= tracking
			s.Encoding |= encoding
		} else {
			s.Tracking &^= tracking
			s.Encoding &^= encoding
		}
		t.set(s)
	}
}
