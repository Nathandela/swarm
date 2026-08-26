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
	mu       sync.Mutex
	key      byte
	forward  func([]byte)
	detach   func()
	pending  []byte
	inPaste  bool
	detached bool
	timer    *time.Timer
	timerGen uint64
	decoder  uv.EventDecoder
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
	if f.inPaste {
		f.pending = append(f.pending, b)
		for len(f.pending) > 0 && !bytes.HasPrefix(bracketedPasteEnd, f.pending) {
			*out = append(*out, f.pending[0])
			f.pending = f.pending[1:]
		}
		if bytes.Equal(f.pending, bracketedPasteEnd) {
			*out = append(*out, f.pending...)
			f.pending = nil
			f.inPaste = false
		}
		return false
	}

	if len(f.pending) == 0 {
		if b == f.key && b != '\x1b' {
			return true
		}
		if b == '\x1b' {
			f.pending = append(f.pending, b)
			return false
		}
		*out = append(*out, b)
		return false
	}

	f.pending = append(f.pending, b)
	if len(f.pending) == 2 && f.pending[1] != '[' {
		first, second := f.pending[0], f.pending[1]
		f.pending = nil
		*out = append(*out, first)
		return f.consumeByte(second, out)
	}
	if len(f.pending) >= 3 && isCSIFinal(f.pending[len(f.pending)-1]) {
		seq := append([]byte(nil), f.pending...)
		f.pending = nil
		if bytes.Equal(seq, bracketedPasteStart) {
			*out = append(*out, seq...)
			f.inPaste = true
			return false
		}
		if f.isKittyDetach(seq) {
			return true
		}
		*out = append(*out, seq...)
		return false
	}
	if len(f.pending) >= maxInputSequenceLen {
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
	if !f.inPaste && f.key == '\x1b' && bytes.Equal(f.pending, []byte{'\x1b'}) {
		f.pending = nil
		f.detached = true
		f.detach()
		return
	}
	p := append([]byte(nil), f.pending...)
	f.pending = nil
	f.forwardLocked(p)
}

func (f *detachInputFilter) forwardLocked(p []byte) {
	if len(p) > 0 && f.forward != nil {
		f.forward(p)
	}
}
