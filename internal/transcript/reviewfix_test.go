package transcript

// Review-fix round tests (Epic 3). NEW tests only; the frozen designer
// contract in transcript_test.go is not touched. These cover: New rejecting a
// non-positive Config (FIX F4, R-1: an unset Config must never silently
// produce an uncapped transcript) and Flush returning promptly with an error
// once Close has been requested, instead of hanging forever waiting on a
// drain goroutine that has already exited (FIX F2).

import (
	"path/filepath"
	"testing"
	"time"
)

// FIX F4 — New must reject a non-positive MaxBytes rather than silently
// building an uncapped (never-rotating) transcript.
func TestNewRejectsNonPositiveMaxBytes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.log")
	if _, err := New(path, Config{MaxBytes: 0, MaxFiles: 2}); err == nil {
		t.Fatal("New with MaxBytes=0 returned nil error; want a rejection (R-1: never silently uncapped)")
	}
	if _, err := New(path, Config{MaxBytes: -1, MaxFiles: 2}); err == nil {
		t.Fatal("New with MaxBytes=-1 returned nil error; want a rejection")
	}
}

// FIX F4 — New must reject a non-positive MaxFiles for the same reason: a
// caller who forgets to set it must never get an unbounded rotation chain.
func TestNewRejectsNonPositiveMaxFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.log")
	if _, err := New(path, Config{MaxBytes: 16, MaxFiles: 0}); err == nil {
		t.Fatal("New with MaxFiles=0 returned nil error; want a rejection (R-1: never silently uncapped)")
	}
	if _, err := New(path, Config{MaxBytes: 16, MaxFiles: -1}); err == nil {
		t.Fatal("New with MaxFiles=-1 returned nil error; want a rejection")
	}
}

// FIX F2 — Flush called after Close must return an error promptly, not hang:
// once Close has returned, the drain goroutine that would have serviced a
// Flush request no longer exists.
func TestFlushAfterCloseReturnsErrorInsteadOfHanging(t *testing.T) {
	w, err := New(filepath.Join(t.TempDir(), "session.log"), Config{MaxBytes: 1 << 20, MaxFiles: 2})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- w.Flush() }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Flush after Close returned nil error; want a non-nil error instead of silently no-oping")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Flush after Close did not return within bound; it hung waiting on a drain goroutine that already exited")
	}
}
