package attach

import (
	"bytes"
	"strconv"
	"strings"
)

// mouseOutput holds a bounded CSI prefix so native mode queries can release our
// mouse override before the physical terminal sees the query, including split frames.
type mouseOutput struct {
	pending  []byte
	pos      outParser
	write    func([]byte)
	fallback func()
	observe  func(byte) bool
	released bool
}

func (m *mouseOutput) feed(p []byte) bool {
	m.pos.c1 = true
	start := 0
	for i, b := range p {
		if m.observe != nil && !m.observe(b) {
			return false
		}
		if len(m.pending) == 0 {
			payload := m.pos.inString()
			continuation := m.pos.utf8Left > 0 && b >= 0x80 && b < 0xc0
			m.pos.feed(b)
			if !payload && !continuation && (b == '\x1b' || b == 0x9b) {
				m.write(p[start:i])
				start = i + 1
				m.pending = append(m.pending, b)
			}
			continue
		}
		if b == '\x1b' || b == 0x9b && m.pos.utf8Left == 0 {
			m.release()
			m.write(m.pending)
			m.pending = append(m.pending[:0], b)
			m.pos.feed(b)
			start = i + 1
			continue
		}
		m.pos.feed(b)
		m.pending = append(m.pending, b)
		start = i + 1
		if b == 0x18 || b == 0x1a {
			m.write(m.pending)
			m.pending = nil
			continue
		}
		if len(m.pending) == 2 && m.pending[0] == '\x1b' && b != '[' {
			m.write(m.pending)
			m.pending = nil
			continue
		}
		prefix := 2
		if m.pending[0] == 0x9b {
			prefix = 1
		}
		if len(m.pending) > prefix && isCSIFinal(b) {
			seq := m.pending[prefix:]
			// Release capture before a mouse DECRQM can expose our override.
			if mouseModeQuery(seq) {
				m.release()
			}
			m.write(m.pending)
			m.pending = nil
		} else if len(m.pending) >= maxInputSequenceLen {
			m.release()
			m.write(m.pending)
			m.pending = nil
		}
	}
	if start < len(p) {
		m.write(p[start:])
	}
	return true
}

func mouseModeQuery(seq []byte) bool {
	if len(seq) < 3 || seq[0] != '?' {
		return false
	}
	end := len(seq) - 2
	if seq[len(seq)-1] == 's' {
		end = len(seq) - 1
	} else if !bytes.HasSuffix(seq, []byte("$p")) {
		return false
	}
	for _, p := range strings.Split(string(seq[1:end]), ";") {
		n, e := strconv.Atoi(p)
		if e != nil {
			continue
		}
		switch n {
		case 9, 1000, 1001, 1002, 1003, 1005, 1006, 1015, 1016:
			return true
		}
	}
	return false
}

func (m *mouseOutput) release() {
	if !m.released {
		m.released = true
		m.fallback()
	}
}
