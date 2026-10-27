package attach

import (
	"strconv"
	"strings"
	"sync"

	"github.com/Nathandela/swarm/internal/vt"
)

// touchButton shares displayed geometry and native mouse ownership with the input pump.
type touchButton struct {
	mu                     sync.Mutex
	generation             uint64
	resized                bool
	wantedCols, wantedRows int
	enabled                bool
	capturing              bool
	discardSGR             bool
	nativeOff              bool
	rows                   int
	tracking               int
	pressed                bool
	nativeGesture          bool
	nativeButtons          uint8
	ownedButton            int
	ownedGesture           bool
}

func (t *touchButton) resize(cols, rows int) {
	t.mu.Lock()
	t.generation++
	t.resized = true
	t.wantedCols = cols
	t.wantedRows = rows
	t.pressed = false
	t.enabled = false
	t.mu.Unlock()
}

func (t *touchButton) cancelPress() { t.mu.Lock(); t.pressed = false; t.mu.Unlock() }

func (t *touchButton) input(seq []byte) (bool, bool) {
	prefix := 3
	if len(seq) > 0 && seq[0] == 0x9b {
		prefix = 2
	}
	if len(seq) < prefix+6 || (prefix == 3 && string(seq[:3]) != "\x1b[<") || (prefix == 2 && seq[1] != '<') || (seq[len(seq)-1] != 'M' && seq[len(seq)-1] != 'm') {
		return false, false
	}
	parts := strings.Split(string(seq[prefix:len(seq)-1]), ";")
	if len(parts) != 3 {
		return false, false
	}
	var values [3]int
	for i, p := range parts {
		if p == "" {
			return false, false
		}
		for _, c := range p {
			if c < '0' || c > '9' {
				return false, false
			}
		}
		n, e := strconv.Atoi(p)
		if e != nil || n < 0 || n > 65535 {
			return false, false
		}
		values[i] = n
	}
	b, x, y := values[0], values[1], values[2]
	if x == 0 || y == 0 {
		return false, false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.discardSGR {
		t.pressed = false
		return true, false
	}
	release := seq[len(seq)-1] == 'm'
	target := t.enabled && x >= 1 && x <= 7 && y == t.rows
	// Preserve native gesture ownership even when its release lands on our footer.
	if t.nativeGesture {
		button := b & 3
		if button < 3 && b&96 == 0 {
			if release {
				t.nativeButtons &^= 1 << button
			} else {
				t.nativeButtons |= 1 << button
			}
		}
		t.nativeGesture = t.nativeButtons != 0
		return t.tracking == 1000 && b&32 != 0, false
	}
	if t.ownedGesture {
		matching := release && b&3 == t.ownedButton
		click := matching && b == 0 && target && t.pressed
		if matching {
			t.ownedGesture = false
		}
		t.pressed = false
		return true, click
	}
	if !t.enabled && !t.capturing {
		return false, false
	}
	if !release && b&96 == 0 && b&3 < 3 {
		if target {
			t.ownedGesture = true
			t.ownedButton = b & 3
			t.pressed = b == 0
			return true, false
		}
		if t.tracking != 0 {
			t.nativeGesture = true
			t.nativeButtons = 1 << (b & 3)
		}
	}
	return target || t.tracking == 0 || (t.tracking == 1000 && b&32 != 0), false
}

func supportedMouse(s vt.MouseState) (int, bool) {
	if !s.Known {
		return 0, false
	}
	if s.Tracking == 0 {
		return 0, s.Encoding == 0 || s.Encoding == vt.MouseEncodeSGR
	}
	if s.Encoding != vt.MouseEncodeSGR {
		return 0, false
	}
	switch s.Tracking {
	case vt.MouseTrackNormal:
		return 1000, true
	case vt.MouseTrackButton:
		return 1002, true
	case vt.MouseTrackAny:
		return 1003, true
	}
	return 0, false
}

func mouseModes(s vt.MouseState) []byte {
	var b strings.Builder
	b.WriteString("\x1b[?1000l\x1b[?1002l\x1b[?1003l\x1b[?1006l")
	for _, m := range []struct {
		bit      uint16
		mode     int
		encoding bool
	}{
		{vt.MouseTrackX10, 9, false}, {vt.MouseTrackNormal, 1000, false}, {vt.MouseTrackHighlight, 1001, false},
		{vt.MouseTrackButton, 1002, false}, {vt.MouseTrackAny, 1003, false},
		{vt.MouseEncodeUTF8, 1005, true}, {vt.MouseEncodeSGR, 1006, true}, {vt.MouseEncodeURXVT, 1015, true}, {vt.MouseEncodePixel, 1016, true},
	} {
		mask := s.Tracking
		if m.encoding {
			mask = s.Encoding
		}
		if mask&m.bit != 0 {
			b.WriteString("\x1b[?")
			b.WriteString(strconv.Itoa(m.mode))
			b.WriteByte('h')
		}
	}
	return []byte(b.String())
}

func permanentMouseFallback(s vt.MouseState) bool {
	return !s.Known || s.Encoding&vt.MouseEncodePixel != 0
}
func ambiguousMouse(s vt.MouseState) bool {
	return s.Tracking&(s.Tracking-1) != 0 || s.Encoding&(s.Encoding-1) != 0
}
func (t *touchButton) dropMouseCandidate() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.ownedGesture || t.discardSGR || t.capturing && t.tracking == 0
}

func (t *touchButton) c1Input() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.enabled || t.capturing || t.discardSGR && t.nativeOff
}
func (t *touchButton) retainMouse() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.enabled || t.capturing || t.discardSGR || t.ownedGesture
}

func discardAfterMouseRelease(s vt.MouseState, previous, owned bool) bool {
	if s.Encoding&vt.MouseEncodePixel != 0 || s.Tracking != 0 && s.Encoding&vt.MouseEncodeSGR != 0 {
		return false
	}
	return previous || owned
}
