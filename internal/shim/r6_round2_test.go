package shim

// R6 REVIEW FIX-PACK ROUND 2 (BLOCKER 2): hooks.spool had exactly ONE reader, the live
// hookServer, which is shut down when the agent is reaped -- so every record acked in
// the last drain interval of a session was unreachable forever while its bytes sat on
// disk. ReadHookSpoolFile is the socket-independent reader that closes that: the file
// is already in the session's own 0700 dir and its format is self-describing, so the
// spool's durability stops being hostage to a live socket.

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadHookSpoolFile_ReadsAckedRecordsWithNoLiveShim(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, HookSpoolFile)

	s, err := OpenHookSpool(path, 0)
	if err != nil {
		t.Fatalf("OpenHookSpool: %v", err)
	}
	for _, body := range []string{`{"a":1}`, `{"a":2}`, `{"a":3}`} {
		if _, err := s.Append([]byte(body)); err != nil {
			t.Fatalf("Append %s: %v", body, err)
		}
	}
	if err := s.Close(); err != nil { // the shim is gone: no socket, no live spool handle
		t.Fatalf("Close: %v", err)
	}

	recs, gapAt, hasGap, err := ReadHookSpoolFile(path, 1)
	if err != nil {
		t.Fatalf("ReadHookSpoolFile: %v", err)
	}
	if hasGap || gapAt != 0 {
		t.Fatalf("ReadHookSpoolFile reported gap=%v at %d over a clean spool, want no gap", hasGap, gapAt)
	}
	if len(recs) != 2 || recs[0].Seq != 2 || recs[1].Seq != 3 {
		t.Fatalf("ReadHookSpoolFile(after=1) = %+v, want the records at seq 2 and 3", recs)
	}
	if string(recs[0].Body) != `{"a":2}` {
		t.Fatalf("record body = %s, want the verbatim posted body", recs[0].Body)
	}
}

func TestReadHookSpoolFile_ReportsATornTailAndNeverReturnsPastIt(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, HookSpoolFile)

	s, err := OpenHookSpool(path, 0)
	if err != nil {
		t.Fatalf("OpenHookSpool: %v", err)
	}
	if _, err := s.Append([]byte(`{"a":1}`)); err != nil {
		t.Fatalf("Append: %v", err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if _, err := s.Append([]byte(`{"a":22222222}`)); err != nil {
		t.Fatalf("Append: %v", err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	_ = s.Close()
	if err := os.Truncate(path, before.Size()+(after.Size()-before.Size())/2); err != nil {
		t.Fatalf("truncate to tear the last record: %v", err)
	}

	recs, gapAt, hasGap, err := ReadHookSpoolFile(path, 0)
	if err != nil {
		t.Fatalf("ReadHookSpoolFile: %v", err)
	}
	if !hasGap || gapAt != 2 {
		t.Fatalf("ReadHookSpoolFile over a torn tail: gap=%v boundary=%d, want gap at 2", hasGap, gapAt)
	}
	for _, r := range recs {
		if r.Seq >= gapAt {
			t.Fatalf("ReadHookSpoolFile returned seq %d at or past the reported boundary %d -- ADR-017 forbids silently bridging the hole", r.Seq, gapAt)
		}
	}
}

func TestReadHookSpoolFile_MissingFileIsAnErrorNotAnEmptyRead(t *testing.T) {
	if _, _, _, err := ReadHookSpoolFile(filepath.Join(t.TempDir(), HookSpoolFile), 0); err == nil {
		t.Fatalf("ReadHookSpoolFile over a nonexistent spool returned nil error; a missing file must not read as a clean, empty spool")
	}
}
