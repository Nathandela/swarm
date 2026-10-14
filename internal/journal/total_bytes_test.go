package journal

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unsafe"
)

// seedSegments writes n segments of one record each (one Open per segment, as each daemon
// start does) and returns the size of one segment.
func seedSegments(t *testing.T, dir string, n int) int64 {
	t.Helper()
	for i := 0; i < n; i++ {
		j, err := Open(dir, Options{})
		if err != nil {
			t.Fatal(err)
		}
		mustAppend(t, j, Record{SessionID: strings.Repeat("s", 1024), Type: TypeGroupTransition})
		_ = j.Close()
	}
	segs := segFiles(t, dir)
	fi, err := os.Stat(segs[len(segs)-1])
	if err != nil {
		t.Fatal(err)
	}
	return fi.Size()
}

func segFiles(t *testing.T, dir string) []string {
	t.Helper()
	segs, err := filepath.Glob(filepath.Join(dir, "seg-*.log"))
	if err != nil {
		t.Fatal(err)
	}
	return segs
}

// TestOpen_MaxTotalBytesDropsTheOldestSegmentsBeforeLoading pins the memory bound: every
// retained record is mirrored in memory, so a journal already over its total cap must shed its
// oldest whole segments at Open, before they are loaded, not at a later Append.
func TestOpen_MaxTotalBytesDropsTheOldestSegmentsBeforeLoading(t *testing.T) {
	dir := jdir(t)
	size := seedSegments(t, dir, 5)

	j, err := Open(dir, Options{MaxTotalBytes: 2*size + size/2})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = j.Close() })

	if got := len(segFiles(t, dir)); got != 3 {
		t.Errorf("segments on disk after Open = %d, want the newest 2 plus the fresh active one", got)
	}
	if got := len(j.records); got != 2 {
		t.Errorf("records loaded = %d, want only the 2 in retained segments", got)
	}
	if got := j.Cursor(); got != 5 {
		t.Errorf("cursor after Open = %d, want the high-water 5", got)
	}
}

// TestOpen_MaxTotalBytesKeepsTheCursorWhenOnlyEmptySegmentsRemain pins cursor monotonicity:
// starts that append nothing leave empty newest segments, and dropping every older segment
// must not reset the high-water cursor a client may already hold.
func TestOpen_MaxTotalBytesKeepsTheCursorWhenOnlyEmptySegmentsRemain(t *testing.T) {
	dir := jdir(t)
	seedSegments(t, dir, 3)
	for i := 0; i < 2; i++ {
		j, err := Open(dir, Options{})
		if err != nil {
			t.Fatal(err)
		}
		_ = j.Close()
	}

	j, err := Open(dir, Options{MaxTotalBytes: 1})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = j.Close() })
	if got := j.Cursor(); got != 3 {
		t.Fatalf("cursor after dropping every non-empty segment = %d, want 3", got)
	}
	r := mustAppend(t, j, Record{SessionID: "next", Type: TypeGroupTransition})
	if r.Cursor != 4 {
		t.Fatalf("next cursor = %d, want 4", r.Cursor)
	}
}

// TestRetention_TheRetainedRecordsDoNotPinTheDroppedOnes pins that dropping a segment frees its
// records: re-slicing would keep the whole old backing array alive until the next restart.
func TestRetention_TheRetainedRecordsDoNotPinTheDroppedOnes(t *testing.T) {
	j, err := Open(jdir(t), Options{MaxBytes: 2 << 10, MaxFiles: 2})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = j.Close() })
	// Over several drops: a drop that re-slices leaves the retained tail starting exactly
	// `dropped` records into the array that still holds the dropped ones.
	drops := 0
	for i := 0; i < 5000 && drops < 5; i++ {
		first, oldest, dropped := &j.records, j.segments[0].seq, j.segments[0].count
		var head *Record
		if len(*first) > 0 {
			head = &(*first)[0]
		}
		mustAppend(t, j, Record{SessionID: "s", Type: TypeGroupTransition})
		if j.segments[0].seq == oldest {
			continue
		}
		drops++
		if head != nil && dropped > 0 && unsafe.SliceData(j.records) == (*Record)(unsafe.Add(unsafe.Pointer(head), uintptr(dropped)*unsafe.Sizeof(Record{}))) {
			t.Fatalf("drop %d re-sliced the retained records: the %d dropped records stay pinned "+
				"in memory by the retained tail's backing array", drops, dropped)
		}
	}
	if drops == 0 {
		t.Fatal("fixture: retention never dropped a segment")
	}
}
