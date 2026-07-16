package vt

// E2.5 fuzz harness: split-escape and UTF-8-boundary consistency.
//
// Property: feeding a byte stream whole must produce the same snapshot as
// feeding it in two writes split at an arbitrary boundary. This proves partial
// escape sequences and multi-byte runes are buffered correctly across Feed
// calls, and that snapshot serialization is deterministic for equal state.
// A panic on any input also fails the fuzz.

import (
	"bytes"
	"os"
	"testing"
)

func FuzzFeedSplitConsistency(f *testing.F) {
	// Seed corpus: real fixtures plus hand-chosen splits through a rune, a CSI,
	// an OSC, and an alt-screen enter.
	if d, err := os.ReadFile("testdata/vim_altscreen.raw"); err == nil {
		f.Add(d, uint(len(d)/2))
	}
	if d, err := os.ReadFile("testdata/hostile.raw"); err == nil {
		f.Add(d, uint(7))
	}
	f.Add([]byte("Hello 你好 world"), uint(8))            // split mid-rune (inside 你)
	f.Add([]byte("\x1b[1;31mred\x1b[0m"), uint(4))      // split mid-CSI
	f.Add([]byte("\x1b]0;title\x07rest"), uint(5))      // split mid-OSC
	f.Add([]byte("\x1b[?1049h\x1b[2J\x1b[HX"), uint(3)) // split mid alt-enter

	f.Fuzz(func(t *testing.T, data []byte, at uint) {
		split := 0
		if len(data) > 0 {
			split = int(at % uint(len(data)+1))
		}

		whole := NewEmulator(80, 24)
		whole.Feed(data)

		parts := NewEmulator(80, 24)
		parts.Feed(data[:split])
		parts.Feed(data[split:])

		wb, werr := whole.Snapshot()
		pb, perr := parts.Snapshot()
		if werr != nil || perr != nil {
			t.Fatalf("snapshot error: whole=%v parts=%v", werr, perr)
		}
		if !bytes.Equal(wb, pb) {
			t.Fatalf("snapshot differs when input split at %d/%d", split, len(data))
		}
	})
}
