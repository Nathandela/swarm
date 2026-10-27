package attach

import (
	"bytes"
	"sync"
	"time"

	uv "github.com/charmbracelet/ultraviolet"
)

const (
	inputSequenceTimeout = 50 * time.Millisecond
	maxInputSequenceLen  = 64
)

var (
	bracketedPasteStart = []byte("\x1b[200~")
	bracketedPasteEnd   = []byte("\x1b[201~")
)

// detachInputFilter recognizes the configured control key in both legacy byte
// form and Kitty CSI-u form without changing any other input bytes. It carries
// incomplete escape sequences across arbitrary Read boundaries and tracks
// bracketed paste so pasted control bytes and key-looking text remain data.
type detachInputFilter struct {
	mu                 sync.Mutex
	key                byte
	forward            func([]byte)
	detach             func()
	pending            []byte
	inPaste            bool
	pasteEnd           []byte
	detached           bool
	timer              *time.Timer
	timerGen           uint64
	activity           func()
	c1Input            func() bool
	retainMouse        func() bool
	dropMouseCandidate func() bool
	mouse              func([]byte) (handled, detach bool)
	decoder            uv.EventDecoder
	pos                outParser // ADR-019 string-payload gate, carried across reads
}

func newDetachInputFilter(key byte, forward func([]byte), detach func()) *detachInputFilter {
	return &detachInputFilter{key: key, forward: forward, detach: detach}
}

// Feed consumes one arbitrary raw-input chunk. It returns true when this call
// recognized the detach key. Bytes before that key are forwarded in order;
// the key and the remainder of its chunk are discarded because attach is over.
func (f *detachInputFilter) Feed(p []byte) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.detached {
		return true
	}
	out := make([]byte, 0, len(p)+len(f.pending))
	for _, b := range p {
		if f.consumeByte(b, &out) {
			f.detached = true
			f.pending = nil
			f.stopTimerLocked()
			f.forwardLocked(out)
			return true
		}
	}
	f.forwardLocked(out)
	f.armTimerLocked()
	return false
}

func (f *detachInputFilter) consumeByte(b byte, out *[]byte) bool {
	c1 := f.mouse != nil
	if f.c1Input != nil {
		c1 = f.c1Input()
	}
	f.pos.c1 = c1
	if f.inPaste {
		end := f.pasteEnd
		if end == nil {
			end = bracketedPasteEnd
		}
		f.pending = append(f.pending, b)
		for len(f.pending) > 0 && !bytes.HasPrefix(end, f.pending) {
			*out = append(*out, f.pending[0])
			f.pending = f.pending[1:]
		}
		if bytes.Equal(f.pending, end) {
			*out = append(*out, f.pending...)
			f.pending = nil
			f.inPaste = false
			// Bracketed paste is an input framing mode: arbitrary pasted escape
			// bytes do not carry parser state beyond its explicit end marker.
			f.pos = outParser{}
		}
		return false
	}

	payload := f.pos.inString()
	continuation := f.pos.c1 && f.pos.utf8Left > 0 && b >= 0x80 && b < 0xc0

	if len(f.pending) == 0 {
		if !payload && b == f.key && b != '\x1b' {
			return true
		}
		f.pos.feed(b)
		if !payload && !continuation && (b == '\x1b' || c1 && b == 0x9b) {
			f.pending = append(f.pending, b)
			return false
		}
		if !payload && f.activity != nil {
			f.activity()
		}
		*out = append(*out, b)
		return false
	}

	// ADR-019: CSI/ESC/nF reports use printable bytes. A C0 detach byte
	// arriving while such a report is incomplete is a real keypress, not part
	// of the report. Preserve the report prefix, withhold the key, and detach.
	if !payload && b == f.key && b != '\x1b' {
		if !mouseInputCandidate(f.pending) || f.dropMouseCandidate == nil || !f.dropMouseCandidate() {
			*out = append(*out, f.pending...)
		}
		f.pending = nil
		return true
	}
	f.pos.feed(b)
	f.pending = append(f.pending, b)
	if len(f.pending) == 2 && f.pending[0] == '\x1b' && f.pending[1] != '[' {
		if f.activity != nil {
			f.activity()
		}
		*out = append(*out, f.pending...)
		f.pending = nil
		return false
	}
	prefix := 2
	if f.pending[0] == 0x9b {
		prefix = 1
	}
	if len(f.pending) > prefix && isCSIFinal(f.pending[len(f.pending)-1]) {
		seq := append([]byte(nil), f.pending...)
		f.pending = nil
		logical := seq
		if seq[0] == 0x9b {
			logical = append([]byte("\x1b["), seq[1:]...)
		}
		if bytes.Equal(logical, bracketedPasteStart) {
			f.pasteEnd = bracketedPasteEnd
			if seq[0] == 0x9b {
				f.pasteEnd = []byte("\x9b201~")
			}
			if f.activity != nil {
				f.activity()
			}
			*out = append(*out, seq...)
			f.inPaste = true
			return false
		}
		if f.mouse != nil {
			if handled, detach := f.mouse(seq); handled {
				return detach
			}
		}
		if f.isKittyDetach(logical) {
			return true
		}
		if f.activity != nil {
			f.activity()
		}
		*out = append(*out, seq...)
		return false
	}
	if len(f.pending) >= maxInputSequenceLen {
		if f.activity != nil {
			f.activity()
		}
		*out = append(*out, f.pending...)
		f.pending = nil
	}
	return false
}

func isCSIFinal(b byte) bool { return b >= 0x40 && b <= 0x7e }

func (f *detachInputFilter) isKittyDetach(seq []byte) bool {
	n, event := f.decoder.Decode(seq)
	if n != len(seq) {
		return false
	}
	press, ok := event.(uv.KeyPressEvent)
	if !ok {
		return false // releases and non-key CSI sequences are passthrough
	}
	key := press.Key()
	return key.Code == detachKeyRune(f.key) && key.Mod.Contains(uv.ModCtrl)
}

func detachKeyRune(key byte) rune {
	if key >= 0x01 && key <= 0x1a {
		return rune('a' + key - 1)
	}
	if key >= 0x1c && key <= 0x1f {
		return rune(key | 0x40)
	}
	return rune(key)
}

func (f *detachInputFilter) armTimerLocked() {
	if len(f.pending) == 0 {
		f.stopTimerLocked()
		return
	}
	if f.timer != nil {
		f.timer.Stop()
	}
	f.timerGen++
	gen := f.timerGen
	f.timer = time.AfterFunc(inputSequenceTimeout, func() { f.flushTimedOut(gen) })
}

func (f *detachInputFilter) stopTimerLocked() {
	f.timerGen++
	if f.timer != nil {
		f.timer.Stop()
		f.timer = nil
	}
}

func (f *detachInputFilter) flushTimedOut(gen uint64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if gen != f.timerGen || f.detached || len(f.pending) == 0 {
		return
	}
	f.timer = nil
	if f.inPaste {
		return
	} // pending is only a bounded possible paste-end marker
	// A recognized mouse prefix stays bounded here until its final byte arrives.
	if f.mouse != nil && !f.inPaste && mouseInputCandidate(f.pending) && (f.retainMouse == nil || f.retainMouse()) {
		return
	}
	if !f.inPaste && f.key == '\x1b' && bytes.Equal(f.pending, []byte{'\x1b'}) {
		f.pending = nil
		f.detached = true
		f.detach()
		return
	}
	p := append([]byte(nil), f.pending...)
	f.pending = nil
	if f.activity != nil {
		f.activity()
	}
	f.forwardLocked(p)
}

func (f *detachInputFilter) forwardLocked(p []byte) {
	if len(p) > 0 && f.forward != nil {
		f.forward(p)
	}
}

func mouseInputCandidate(p []byte) bool {
	return bytes.HasPrefix(p, []byte("\x1b[<")) || bytes.HasPrefix(p, []byte("\x9b<"))
}
