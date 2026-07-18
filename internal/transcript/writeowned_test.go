package transcript

// R3.3.3 (agents-tracker-tbw) — WriteOwned, an entry point that takes
// ownership of p without a defensive copy, for a caller (hub.feed,
// internal/shim/server.go) whose chunk is freshly allocated and never touched
// again. Public Write is untouched and keeps its defensive copy: these tests
// pin that Write is unaffected while WriteOwned delivers correctly AND does
// NOT copy (mutating p after WriteOwned returns is observable at the sink,
// proving no copy was made).

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// lockedLen reads fs.buf's length under fs.mu, so polling it from the test
// goroutine while the drain goroutine writes under the same lock is race-free.
func (fs *fakeSink) lockedLen() int {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	return fs.buf.Len()
}

// TestWriteOwnedDeliversToSink is the functional smoke test: WriteOwned
// behaves like Write from the caller's perspective (same delivery, same
// (len(p), nil) contract).
func TestWriteOwnedDeliversToSink(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.log")
	w, err := New(path, Config{MaxBytes: 1 << 20, MaxFiles: 2})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer w.Close()

	payload := []byte("owned chunk\n")
	n, err := w.WriteOwned(payload)
	if err != nil || n != len(payload) {
		t.Fatalf("WriteOwned = (%d, %v), want (%d, nil)", n, err, len(payload))
	}
	if err := w.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read transcript: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("transcript = %q, want %q", got, payload)
	}
}

// TestWriteOwnedTakesOwnershipWithoutCopy proves WriteOwned does NOT copy p:
// with the sink's Write gated, we hand p to WriteOwned, mutate p in place
// (simulating nothing else touching it, as hub.feed's caller guarantees), then
// release the gate. If WriteOwned copied p (like public Write), the sink would
// see the ORIGINAL bytes; since it does not copy, the sink observes the
// MUTATED bytes.
func TestWriteOwnedTakesOwnershipWithoutCopy(t *testing.T) {
	gate := make(chan struct{})
	fs := &fakeSink{block: gate}
	restore := installFakeSink(fs)
	defer restore()

	w, err := New(filepath.Join(t.TempDir(), "session.log"), Config{MaxBytes: 1 << 20, MaxFiles: 2})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer w.Close()

	p := []byte("hello")
	if _, err := w.WriteOwned(p); err != nil {
		t.Fatalf("WriteOwned: %v", err)
	}
	p[0] = 'X' // mutate the backing array before the gated sink.Write reads it
	close(gate) // release the gated sink now that the mutation has landed

	if !waitFor(2*time.Second, func() bool { return fs.lockedLen() >= len("hello") }) {
		t.Fatal("sink never observed the write")
	}
	fs.mu.Lock()
	got := append([]byte(nil), fs.buf.Bytes()...)
	fs.mu.Unlock()
	if !bytes.Equal(got, []byte("Xello")) {
		t.Fatalf("sink saw %q, want %q (WriteOwned must hand ownership without copying)", got, "Xello")
	}
}

// TestWritePublicStillCopiesDefensively pins the unchanged Write contract:
// mutating p after Write returns must NEVER be observable at the sink.
func TestWritePublicStillCopiesDefensively(t *testing.T) {
	gate := make(chan struct{})
	fs := &fakeSink{block: gate}
	restore := installFakeSink(fs)
	defer restore()

	w, err := New(filepath.Join(t.TempDir(), "session.log"), Config{MaxBytes: 1 << 20, MaxFiles: 2})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer w.Close()

	p := []byte("hello")
	if _, err := w.Write(p); err != nil {
		t.Fatalf("Write: %v", err)
	}
	p[0] = 'X'
	close(gate) // release the gated sink now that the mutation has landed

	if !waitFor(2*time.Second, func() bool { return fs.lockedLen() >= len("hello") }) {
		t.Fatal("sink never observed the write")
	}
	fs.mu.Lock()
	got := append([]byte(nil), fs.buf.Bytes()...)
	fs.mu.Unlock()
	if !bytes.Equal(got, []byte("hello")) {
		t.Fatalf("sink saw %q, want %q (public Write must keep its defensive copy)", got, "hello")
	}
}
