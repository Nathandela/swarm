package attach

import (
	"bytes"
	"testing"
)

// PR #47: detach must turn off the input modes an agent enabled through the live
// passthrough (all-motion mouse, Kitty keyboard flags, ...), on both screen buffers,
// or the board inherits them and the arrow keys go dead after Ctrl+Q.
func TestPassthrough_DetachResetsAgentInputModes(t *testing.T) {
	for _, tc := range []struct {
		name string
		snap []byte
	}{
		{"main buffer", []byte("S")},
		{"alt buffer", mustAltSnap(t, "ALT-SNAP")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			term := newFakeTerm(80, 24)
			sess := newFakeSession(tc.snap)
			ch := runInBackground(Config{Term: term, Session: sess})

			sess.pushFrame([]byte("\x1b[?1003h\x1b[?1006h\x1b[>5u")) // agent enables modes
			eventually(t, func() bool { return bytes.Contains(term.outBytes(), []byte("\x1b[>5u")) })
			term.feed([]byte{DefaultDetachKey})
			if res := waitResult(t, ch); res.reason != ReasonDetached {
				t.Fatalf("reason = %v, want ReasonDetached", res.reason)
			}

			out := term.outBytes()
			tail := out[bytes.LastIndex(out, []byte("\x1b[>5u")):]
			if !bytes.HasSuffix(tail, []byte(inputModeResetSeq)) {
				t.Fatalf("detach must end with the input-mode reset; tail=%q", tail)
			}
			if altExit := bytes.LastIndex(out, []byte("\x1b[?1049l")); tc.snap[0] == 0x1b && altExit < 0 {
				t.Fatalf("alt detach must exit alt; out=%q", out)
			}
		})
	}
}
