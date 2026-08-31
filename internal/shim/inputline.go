package shim

// inputLineTracker is the shim's deliberately small, conservative model of keys written to
// the agent's interactive composer. It answers exactly one question for submitMessage:
// whether the logical input line is provably empty.
//
// The old model counted bytes since the last CR/LF. That was safe against merging a real
// draft, but ordinary editing made the count a permanent false positive: five letters followed
// by five Backspaces still counted as ten dirty bytes, and complete cursor navigation counted as
// text too. This model tracks only editor operations whose effects are characterized from the
// input stream. Anything stateful that depends on provider-owned word boundaries, history or
// completion becomes unknown and remains busy until a line-running CR/LF proves the composer
// clean. It never reads or guesses from the rendered grid.

import (
	"strings"
	"unicode/utf8"
)

type inputLineTracker struct {
	text    []rune
	cursor  int
	unknown bool
	pending []byte // an escape/UTF-8 sequence split across TDataIn frames
	paste   bool   // bracketed paste: CR/LF are content until CSI 201~
}

func (l *inputLineTracker) reset() {
	l.text = nil
	l.cursor = 0
	l.unknown = false
	l.pending = nil
	l.paste = false
}

func (l *inputLineTracker) dirty() bool {
	if l.unknown || l.paste || len(l.text) != 0 {
		return true
	}
	// Any incomplete sequence has already put a prefix into the provider's parser and cannot
	// safely be interleaved with a message. A lone Escape is indistinguishable from the prefix
	// of a split terminal sequence at this boundary; an incomplete UTF-8 rune is text too.
	return len(l.pending) != 0
}

func (l *inputLineTracker) apply(b []byte) {
	if len(b) == 0 {
		return
	}
	data := make([]byte, 0, len(l.pending)+len(b))
	data = append(data, l.pending...)
	data = append(data, b...)
	l.pending = nil

	for i := 0; i < len(data); {
		if data[i] == '\x1b' {
			n, complete := inputEscapeLen(data[i:])
			if !complete {
				l.pending = append(l.pending[:0], data[i:]...)
				return
			}
			l.applyEscape(data[i : i+n])
			i += n
			continue
		}
		if data[i] < utf8.RuneSelf {
			l.applyASCII(data[i])
			i++
			continue
		}
		if !utf8.FullRune(data[i:]) {
			l.pending = append(l.pending[:0], data[i:]...)
			return
		}
		r, n := utf8.DecodeRune(data[i:])
		if r == utf8.RuneError && n == 1 {
			l.unknown = true
			i++
			continue
		}
		l.insert(r)
		i += n
	}
}

// inputEscapeLen returns one terminal key sequence's length. A bare Escape followed by a control
// key is kept separate so a following CR/LF can still provide the unambiguous clean boundary.
func inputEscapeLen(b []byte) (int, bool) {
	if len(b) < 2 {
		return 0, false
	}
	if b[1] == '\x1b' || b[1] < 0x20 || b[1] == 0x7f {
		return 1, true
	}
	switch b[1] {
	case '[':
		for i := 2; i < len(b); i++ {
			if b[i] >= 0x40 && b[i] <= 0x7e {
				return i + 1, true
			}
		}
		return 0, false
	case 'O':
		if len(b) < 3 {
			return 0, false
		}
		return 3, true
	default:
		return 2, true // one Meta-modified byte
	}
}

func (l *inputLineTracker) applyASCII(b byte) {
	if l.paste {
		// In bracketed paste, newline is content rather than the key that runs the line.
		l.insert(rune(b))
		return
	}
	switch b {
	case '\r', '\n':
		l.reset()
	case 0x08, 0x7f: // Backspace / DEL-as-backspace
		l.backspace()
	case 0x01: // Ctrl-A: beginning of line
		if !l.unknown {
			l.cursor = 0
		}
	case 0x05: // Ctrl-E: end of line
		if !l.unknown {
			l.cursor = len(l.text)
		}
	case 0x02: // Ctrl-B: left
		l.move(-1)
	case 0x06: // Ctrl-F: right
		l.move(1)
	case 0x04: // Ctrl-D: delete under cursor
		l.deleteForward()
	case 0x0b: // Ctrl-K: delete cursor -> end
		if !l.unknown {
			l.text = l.text[:l.cursor]
		}
	case 0x15: // Ctrl-U: delete beginning -> cursor
		if !l.unknown && l.cursor > 0 {
			l.text = append(l.text[:0], l.text[l.cursor:]...)
			l.cursor = 0
		}
	case 0x17: // Ctrl-W: provider-dependent word boundary
		l.unknown = true
	case 0x03, 0x07, 0x0c: // Ctrl-C / bell-cancel / redraw: no text insertion
		// These may affect provider lifecycle, but they do not make an empty composer
		// non-empty. If a known/unknown draft already exists, it remains conservative.
	case 0x09, 0x0e, 0x10, 0x12, 0x19: // completion/history/yank depend on provider state
		l.unknown = true
	default:
		if b < 0x20 {
			l.unknown = true
			return
		}
		l.insert(rune(b))
	}
}

func (l *inputLineTracker) applyEscape(seq []byte) {
	if len(seq) == 1 {
		// At this seam a standalone lifecycle Escape is indistinguishable from an escape
		// prefix whose remaining bytes crossed a frame boundary. Do not interleave a message.
		l.unknown = true
		return
	}
	if l.paste {
		if string(seq) == "\x1b[201~" {
			l.paste = false
			return
		}
		// An escape inside pasted content has provider-specific meaning. Keep the line
		// conservatively busy rather than interpreting it as an editor key.
		l.unknown = true
		return
	}
	if len(seq) == 2 {
		// Meta-modified word movement/deletion depends on the provider's word-boundary
		// rules (not merely whitespace), and other Meta bindings are provider-owned too.
		l.unknown = true
		return
	}
	prefix := seq[1]
	final := seq[len(seq)-1]
	body := string(seq[2 : len(seq)-1])
	if prefix == 'O' {
		body = ""
	}
	switch final {
	case 'D':
		if body != "" {
			l.unknown = true // modified/parameterized arrows may be provider word movement
		} else {
			l.move(-1)
		}
	case 'C':
		if body != "" {
			l.unknown = true
		} else {
			l.move(1)
		}
	case 'H':
		if body != "" {
			l.unknown = true
		} else if !l.unknown {
			l.cursor = 0
		}
	case 'F':
		if body != "" {
			l.unknown = true
		} else if !l.unknown {
			l.cursor = len(l.text)
		}
	case 'A', 'B':
		l.unknown = true // history recall may replace the whole line
	case 'I', 'O':
		// Focus-in/out reports are lifecycle metadata, not composer text.
	case '~':
		if strings.Contains(body, ";") {
			l.unknown = true // modifier semantics are provider-owned
			return
		}
		param := strings.Split(body, ";")[0]
		switch param {
		case "1", "7":
			if !l.unknown {
				l.cursor = 0
			}
		case "4", "8":
			if !l.unknown {
				l.cursor = len(l.text)
			}
		case "3":
			l.deleteForward()
		case "200":
			l.paste = true
		case "201":
			// A stray paste-end marker inserts nothing.
		default:
			l.unknown = true
		}
	default:
		l.unknown = true
	}
}

func (l *inputLineTracker) insert(r rune) {
	if l.unknown {
		return
	}
	l.text = append(l.text, 0)
	copy(l.text[l.cursor+1:], l.text[l.cursor:])
	l.text[l.cursor] = r
	l.cursor++
}

func (l *inputLineTracker) move(delta int) {
	if l.unknown {
		return
	}
	l.cursor += delta
	if l.cursor < 0 {
		l.cursor = 0
	}
	if l.cursor > len(l.text) {
		l.cursor = len(l.text)
	}
}

func (l *inputLineTracker) backspace() {
	if l.unknown || l.cursor == 0 {
		return
	}
	copy(l.text[l.cursor-1:], l.text[l.cursor:])
	l.text = l.text[:len(l.text)-1]
	l.cursor--
}

func (l *inputLineTracker) deleteForward() {
	if l.unknown || l.cursor >= len(l.text) {
		return
	}
	copy(l.text[l.cursor:], l.text[l.cursor+1:])
	l.text = l.text[:len(l.text)-1]
}
