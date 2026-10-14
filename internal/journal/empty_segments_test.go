package journal

import (
	"path/filepath"
	"testing"
)

// TestOpen_UnlinksRetainedEmptySegmentsExceptTheNewest pins that daemon starts which
// append nothing do not consume MaxFiles: at Open every zero-record segment but the
// newest is unlinked, and the cursor and segment numbering stay monotonic.
func TestOpen_UnlinksRetainedEmptySegmentsExceptTheNewest(t *testing.T) {
	dir := jdir(t)
	for _, appendOne := range []bool{true, false, true, false, false} { // seg 1..5
		j, err := Open(dir, Options{})
		if err != nil {
			t.Fatal(err)
		}
		if appendOne {
			mustAppend(t, j, Record{SessionID: "s", Type: TypeGroupTransition})
		}
		_ = j.Close()
	}

	j, err := Open(dir, Options{})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = j.Close() })

	var got []string
	for _, p := range segFiles(t, dir) {
		got = append(got, filepath.Base(p))
	}
	want := []string{segName(1), segName(3), segName(5), segName(6)}
	if len(got) != len(want) {
		t.Fatalf("segments after Open = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("segments after Open = %v, want %v", got, want)
		}
	}
	if len(j.records) != 2 || j.Cursor() != 2 {
		t.Fatalf("records=%d cursor=%d, want 2 and 2", len(j.records), j.Cursor())
	}
	if r := mustAppend(t, j, Record{SessionID: "s", Type: TypeGroupTransition}); r.Cursor != 3 {
		t.Fatalf("next cursor = %d, want 3", r.Cursor)
	}
}
