package transcript

// Follow-up to R3.3.3 (agents-tracker-tbw, item 3.3 review, non-blocking
// recommendation): the Close-path merge in drainLoop (transcript.go:249-253)
// folds every still-pending chunk into one final sink.Write via
// append(batch, chunk...), with batch starting from nil each time. WriteOwned
// hands the Writer a chunk's backing array with NO defensive copy (R3.3.3), so
// anything downstream still holding a reference to that array (emu/subscriber
// fan-out) depends on the merge never writing INTO it. This pins that
// property: chunks are built with deliberate spare capacity (today's real
// caller, shim/server.go's make([]byte, n), has none, so this is a
// forward-looking regression guard rather than a reproduction of a live bug)
// so a hypothetical "reuse pending[0] as the accumulator" change would show up
// as bytes written past a chunk's own length — which -race would not catch
// (no concurrent access, just aliased mutation after ownership transfer).
// Failing-first is impractical here: the invariant already holds (append
// never overwrites its source's own backing array); this is a
// characterization pin, not a bug fix.

import (
	"bytes"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

func TestCloseMergeCopiesOwnedChunksWithoutAliasing(t *testing.T) {
	fs := &fakeSink{slow: 50 * time.Millisecond} // holds each dispatch open long enough for the rest to queue up
	restore := installFakeSink(fs)
	defer restore()

	w, err := New(filepath.Join(t.TempDir(), "session.log"), Config{MaxBytes: 1 << 20, MaxFiles: 2})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	const n = 4
	wants := make([][]byte, n)
	fulls := make([][]byte, n) // full-capacity view of each chunk's backing array
	for i := 0; i < n; i++ {
		want := []byte(fmt.Sprintf("owned-chunk-%d\n", i))
		raw := make([]byte, len(want), len(want)+64) // deliberate spare capacity
		copy(raw, want)
		wants[i] = want
		fulls[i] = raw[:cap(raw)]
		if _, err := w.WriteOwned(raw[:len(want)]); err != nil {
			t.Fatalf("WriteOwned(%d): %v", i, err)
		}
	}
	// Chunk 0 is dispatched singly (drainLoop wakes before Close is called);
	// chunks 1..n-1 are still pending when Close sets closing=true, so they
	// are folded together through the merge branch this test targets.

	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	for i, want := range wants {
		if !bytes.Equal(fulls[i][:len(want)], want) {
			t.Fatalf("chunk %d's own bytes changed: got %q, want %q", i, fulls[i][:len(want)], want)
		}
		for j, b := range fulls[i][len(want):] {
			if b != 0 {
				t.Fatalf("chunk %d's spare capacity byte %d was written by the Close merge (aliasing)", i, j)
			}
		}
	}
}
