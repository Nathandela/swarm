package attach

import (
	"bytes"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/Nathandela/swarm/internal/vt"
)

const codexStartupSeq = "\x1b[?2004h\x1b[>4;0m\x1b[>7u\x1b[?1004h\x1b[?1049h\x1b[>4;0m\x1b[>7u\x1b[?1007l\x1b[?1000h\x1b[?1002h\x1b[?1003h\x1b[?1006hCODEX-UI"

func codexStartupSnapshot(t *testing.T) []byte {
	t.Helper()
	e := vt.NewEmulator(80, 23)
	defer func() { _ = e.Close() }()
	e.Feed([]byte(codexStartupSeq))
	b, err := e.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	return b
}

func TestAttach_RestoresCodexScrollModesAcrossReattach(t *testing.T) {
	snapBytes := codexStartupSnapshot(t)
	wheel := []byte("\x1b[<64;20;10M\x1b[<65;20;10M")

	for _, touchBack := range []bool{false, true} {
		t.Run(fmt.Sprintf("touchBack=%v", touchBack), func(t *testing.T) {
			for attempt := 0; attempt < 2; attempt++ {
				term := newFakeTerm(80, 24)
				sess := newFakeSession(snapBytes)
				ch := runInBackground(Config{
					Term:      term,
					Session:   sess,
					Chrome:    true,
					TouchBack: touchBack,
					Name:      "codex",
				})

				waitOut(t, term, func(b []byte) bool {
					return bytes.Contains(b, []byte("returns to swarm"))
				})
				out := term.outBytes()
				for _, want := range []string{
					"\x1b[?1049h",
					"CODEX-UI",
					"\x1b[?1007l",
					"\x1b[?1003h",
					"\x1b[?1006h",
					"\x1b[?2004h",
					"\x1b[?1004h",
				} {
					if !bytes.Contains(out, []byte(want)) {
						t.Fatalf("attempt %d missing %q in attach output: %q", attempt, want, out)
					}
				}

				term.feed(wheel)
				eventually(t, func() bool {
					return bytes.Contains(sess.inputBytes(), wheel)
				})

				term.feed([]byte{DefaultDetachKey})
				res := waitResult(t, ch)
				if res.reason != ReasonDetached {
					t.Fatalf("attempt %d reason = %v, want ReasonDetached", attempt, res.reason)
				}
				if got := term.outBytes(); !bytes.HasSuffix(got, []byte(inputModeResetSeq)) {
					t.Fatalf("attempt %d detach did not end with inputModeResetSeq: %q", attempt, got)
				}
			}
		})
	}
}

func TestAttach_OlderShimMouseMaskFallbackRestoresScroll(t *testing.T) {
	snap, err := vt.DecodeSnapshot(codexStartupSnapshot(t))
	if err != nil {
		t.Fatalf("DecodeSnapshot: %v", err)
	}
	var raw map[string]any
	b, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	delete(raw, "inputModes")
	raw["mouse"] = map[string]any{
		"tracking": vt.MouseTrackNormal | vt.MouseTrackButton | vt.MouseTrackAny,
		"encoding": vt.MouseEncodeSGR,
		"known":    false,
	}
	legacyBytes, err := json.Marshal(raw)
	if err != nil {
		t.Fatalf("Marshal legacy: %v", err)
	}

	term := newFakeTerm(80, 24)
	sess := newFakeSession(legacyBytes)
	ch := runInBackground(Config{
		Term:    term,
		Session: sess,
		Chrome:  true,
		Name:    "codex",
	})

	waitOut(t, term, func(b []byte) bool {
		return bytes.Contains(b, []byte("returns to swarm"))
	})
	out := term.outBytes()
	for _, want := range []string{"\x1b[?1003h", "\x1b[?1006h"} {
		if !bytes.Contains(out, []byte(want)) {
			t.Fatalf("legacy fallback missing %q in attach output: %q", want, out)
		}
	}

	wheel := []byte("\x1b[<64;20;10M")
	term.feed(wheel)
	eventually(t, func() bool {
		return bytes.Equal(sess.inputBytes(), wheel)
	})

	term.feed([]byte{DefaultDetachKey})
	if res := waitResult(t, ch); res.reason != ReasonDetached {
		t.Fatalf("reason = %v, want ReasonDetached", res.reason)
	}
}

func TestAttach_ReadOnlySkipsInputModeRestore(t *testing.T) {
	snapBytes := codexStartupSnapshot(t)
	term := newFakeTerm(80, 24)
	sess := newFakeSession(snapBytes)
	ch := runInBackground(Config{
		Term:     term,
		Session:  sess,
		Chrome:   true,
		ReadOnly: true,
		Name:     "codex",
	})

	waitOut(t, term, func(b []byte) bool {
		return bytes.Contains(b, []byte("returns to swarm"))
	})
	out := term.outBytes()
	for _, forbidden := range []string{"\x1b[?1003h", "\x1b[?1006h", "\x1b[?2004h", "\x1b[?1004h", "\x1b[?1007l"} {
		if bytes.Contains(out, []byte(forbidden)) {
			t.Fatalf("read-only attach unexpectedly wrote %q: %q", forbidden, out)
		}
	}

	term.feed([]byte{DefaultDetachKey})
	if res := waitResult(t, ch); res.reason != ReasonDetached {
		t.Fatalf("reason = %v, want ReasonDetached", res.reason)
	}
}
