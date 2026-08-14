package vt

import (
	"errors"
	"strings"
	"testing"
)

// TestEmulatorFeedContainsOversizedScrollRegion pins the field crash: a frame
// produced for a taller terminal may arrive after the local mirror has already
// shrunk. Terminal output is untrusted input and must never panic the daemon or
// shim process. FeedChecked reports the contained parser failure, resets the
// scroll bounds, and leaves the emulator usable for subsequent output.
func TestEmulatorFeedContainsOversizedScrollRegion(t *testing.T) {
	e := NewEmulator(80, 45)
	t.Cleanup(func() { _ = e.Close() })

	if err := e.FeedChecked([]byte("\x1b[1;94r\x1bM")); !errors.Is(err, ErrParserPanic) {
		t.Fatalf("FeedChecked oversized region error = %v, want ErrParserPanic", err)
	}
	if err := e.FeedChecked([]byte("\x1b[r\x1b[HRECOVERED")); err != nil {
		t.Fatalf("FeedChecked after contained parser panic: %v", err)
	}

	s := snapshotDecode(t, e)
	if got := strings.Join(SnapText(s), "\n"); !strings.Contains(got, "RECOVERED") {
		t.Fatalf("emulator did not accept output after contained parser panic:\n%s", got)
	}
}

// Feed preserves its established no-error API while delegating to the same
// containment boundary. Callers that do not need observability must still be
// protected from hostile terminal bytes.
func TestEmulatorFeedContainsParserPanicWithoutCheckedAPI(t *testing.T) {
	e := NewEmulator(80, 45)
	t.Cleanup(func() { _ = e.Close() })

	e.Feed([]byte("\x1b[1;94r\x1bM"))
	e.Feed([]byte("\x1b[r\x1b[HRECOVERED"))

	if got := strings.Join(SnapText(snapshotDecode(t, e)), "\n"); !strings.Contains(got, "RECOVERED") {
		t.Fatalf("Feed did not recover from contained parser panic:\n%s", got)
	}
}
