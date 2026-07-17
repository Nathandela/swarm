package adapter

// E9.1 / T-1 — ExtractConversationID must be PURE and TOTAL: it never panics on
// an empty or garbage grid+tail, and it is deterministic. It is the one method
// the engine calls on adversarial input (a half-drawn TUI grid + an arbitrary
// transcript tail), so a panic here would take down the status engine.
//
// This fuzz targets a representative conformant extractor (baseAdapter); the
// REAL reference adapter's extractor is fuzzed the same way in
// refadapter/extractid_fuzz_test.go, and every adapter's totality is probed by
// CheckConformance. A panic or a nondeterministic result on ANY input fails.

import (
	"testing"

	"github.com/Nathandela/swarm/internal/vt"
)

// snapFrom builds a *vt.Snap by feeding raw bytes through the real emulator —
// the same projection the engine hands the adapter at runtime.
func snapFrom(t testing.TB, feed []byte) *vt.Snap {
	t.Helper()
	emu := vt.NewEmulator(80, 24)
	defer emu.Close()
	emu.Feed(feed)
	b, err := emu.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	snap, err := vt.DecodeSnapshot(b)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	return snap
}

// TestExtractConversationID_NilAndEmpty — totality on the degenerate inputs the
// contract explicitly promises to survive: a nil grid, an empty grid, and a
// nil/empty tail must all return ("", false) without panicking.
func TestExtractConversationID_NilAndEmpty(t *testing.T) {
	a := baseAdapter{}
	cases := []struct {
		name string
		grid *vt.Snap
		tail []byte
	}{
		{"nil-grid-nil-tail", nil, nil},
		{"nil-grid-empty-tail", nil, []byte{}},
		{"empty-grid-nil-tail", snapFrom(t, nil), nil},
		{"garbage-grid-garbage-tail", snapFrom(t, []byte("\x1b[garbage\x00\xff")), []byte("\x00\xff not an id")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id, ok := a.ExtractConversationID(tc.grid, tc.tail)
			if ok || id != "" {
				t.Errorf("got (%q, %v), want (\"\", false)", id, ok)
			}
		})
	}
}

func FuzzExtractConversationIDTotality(f *testing.F) {
	f.Add([]byte("Welcome\r\nconv-id=abc123\r\n"), []byte("conv-id=abc123"))
	f.Add([]byte("\x1b[2J\x1b[H"), []byte(""))
	f.Add([]byte{0x00, 0xff, 0x1b, 0x5b}, []byte{0x1b})
	f.Add([]byte("conv-id="), []byte("conv-id=")) // marker with no value
	f.Add([]byte("多字节 conv-id=xyz\r\n"), []byte("多字节 conv-id=xyz"))

	a := baseAdapter{}
	f.Fuzz(func(t *testing.T, feed, tail []byte) {
		grid := snapFrom(t, feed)
		id1, ok1 := a.ExtractConversationID(grid, tail) // must not panic
		id2, ok2 := a.ExtractConversationID(grid, tail) // deterministic
		if id1 != id2 || ok1 != ok2 {
			t.Fatalf("nondeterministic: (%q,%v) then (%q,%v)", id1, ok1, id2, ok2)
		}
		if ok1 && id1 == "" {
			t.Fatalf("ok=true with empty id (contract: ok implies non-empty)")
		}
		// Also total with a nil grid on the same tail.
		_, _ = a.ExtractConversationID(nil, tail)
	})
}
