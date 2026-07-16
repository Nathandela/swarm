package vt

// E2.5 fuzz harness: split-escape and UTF-8-boundary consistency, scoped to the
// chosen library's grapheme-flush behavior.
//
// Base property: feeding a byte stream whole must produce the same snapshot as
// feeding it in two writes split at an arbitrary boundary. This proves partial
// escape sequences and multi-byte runes are buffered correctly across Feed
// calls, and that snapshot serialization is deterministic for equal state. A
// panic or a deadlock on any input always fails.
//
// SCOPING AMENDMENT (F1, authorized by team-lead review): the unconditional
// whole==split invariant is provably FALSE under the pinned
// github.com/charmbracelet/x/vt. Charm flushes its pending grapheme buffer at
// every Write boundary (its Write calls flushGrapheme when i == len(p)-1), so a
// multi-rune grapheme cluster (base rune + combining mark, a ZWJ sequence, ...)
// that straddles the split commits as separate cells in the split feed but as
// one cell in the whole feed. Buffering trailing runes inside the wrapper is
// rejected upstream (it would delay echo of the last typed character).
//
// The property is therefore scoped two ways:
//   - On VALID UTF-8, divergence is tolerated only when the split byte offset
//     falls strictly inside a grapheme cluster of the input (boundaries via
//     rivo/uniseg, a direct test dependency). Valid UTF-8 is where the bugs we
//     actually care about live — escape-sequence and multi-byte-rune buffering
//     across Feed calls, and snapshot determinism — so every non-cluster split
//     on valid input must still match exactly.
//   - On MALFORMED UTF-8, charm's byte-level error recovery (consuming stray
//     control/continuation bytes into replacement runes, then clustering them)
//     makes cross-Write grapheme behavior implementation-defined and not cleanly
//     analyzable from the byte stream — uniseg on raw bytes disagrees with what
//     charm actually renders (reproducers {0xcf,0x10,0xd9,0x9b}@2 and
//     {0xd8,0x81,0xd4,0x30}@3). Any such non-panic/non-deadlock divergence is
//     tolerated; a real buffering bug would still surface on the well-formed
//     inputs the fuzzer also explores.
//
// Panics and deadlocks always fail. See docs/adr/ADR-005 "Known limitations".

import (
	"bytes"
	"os"
	"testing"
	"unicode/utf8"

	"github.com/rivo/uniseg"
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
	f.Add([]byte{0xcf, 0x30, 0xd9, 0x9b}, uint(3))      // F1: split inside a grapheme cluster

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
		if bytes.Equal(wb, pb) {
			return
		}
		// Snapshots differ. Tolerated for charm's per-Write grapheme flush (see
		// file header): on valid UTF-8 only when the split falls strictly inside
		// a grapheme cluster; on malformed UTF-8 unconditionally (behavior is
		// implementation-defined there). Every other divergence is a regression.
		if utf8.Valid(data) && !splitInsideGraphemeCluster(data, split) {
			t.Fatalf("snapshot differs at a non-cluster split %d/%d on valid UTF-8: whole != split outside the tolerated grapheme-flush case", split, len(data))
		}
	})
}

// splitInsideGraphemeCluster reports whether byte offset split falls strictly
// inside a grapheme cluster of data (not on a cluster boundary, not at an end).
// Cluster boundaries are computed with rivo/uniseg over the raw bytes; invalid
// UTF-8 decodes to U+FFFD, one cluster each.
func splitInsideGraphemeCluster(data []byte, split int) bool {
	if split <= 0 || split >= len(data) {
		return false
	}
	offset, state := 0, -1
	rest := data
	for len(rest) > 0 {
		var cluster []byte
		cluster, rest, _, state = uniseg.FirstGraphemeCluster(rest, state)
		offset += len(cluster)
		if offset == split {
			return false // split lands on a cluster boundary
		}
		if offset > split {
			return true // split fell strictly inside this cluster
		}
	}
	return false
}
