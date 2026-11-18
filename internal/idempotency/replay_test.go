package idempotency

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestReplayRepairsOnlyIncompleteFinalRecord(t *testing.T) {
	for _, tc := range []struct {
		name, tail      string
		valid, rejected bool
	}{
		{"torn", `{"operation_id":`, false, false},
		{"valid without newline", `{"operation_id":"legacy","phase":"executing"}`, true, false},
		{"malformed tail", `{"broken":!]}`, false, true},
		{"malformed interior", "{bad}\n", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			s, err := Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := s.Prepare("op", "launch", "s"); err != nil {
				t.Fatal(err)
			}
			if _, err = s.f.WriteString(tc.tail); err != nil {
				t.Fatal(err)
			}
			if err = s.f.Close(); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, logFile)
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			s, err = Open(dir)
			if tc.rejected {
				if err == nil {
					_ = s.f.Close()
					t.Fatal("accepted malformed log")
				}
				after, err := os.ReadFile(path)
				if err != nil || !bytes.Equal(after, before) {
					t.Fatal("changed rejected log", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = s.Begin("op"); err != nil {
				t.Fatal(err)
			}
			if err = s.f.Close(); err != nil {
				t.Fatal(err)
			}
			s, err = Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = s.f.Close() }()
			if _, ok := s.Get("legacy"); ok != tc.valid {
				t.Fatal("lost valid final record")
			}
			if err = s.Begin("op"); err == nil {
				t.Fatal("repeated executing transition")
			}
		})
	}
}

func TestReplayLargeOutcomePreservesLaterGuard(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.Prepare("large", "op", "s"); err != nil {
		t.Fatal(err)
	}
	outcome, err := json.Marshal(string(bytes.Repeat([]byte{'a'}, 4<<20)))
	if err != nil {
		t.Fatal(err)
	}
	// A pre-fix writer accepted this entire row even though its scanner could not replay it.
	rec, _ := s.Get("large")
	rec.Phase, rec.Outcome = PhaseCompleted, outcome
	legacy, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.f.Write(append(legacy, '\n')); err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.Prepare("later", "op", "s"); err != nil {
		t.Fatal(err)
	}
	if err = s.f.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.f.Close() }()
	if err = s.Compact(); err != nil {
		t.Fatal(err)
	}
	if got, ok := s.Get("large"); !ok || !bytes.Equal(got.Outcome, outcome) {
		t.Fatal("lost large outcome")
	}
	if _, ok := s.Get("later"); !ok {
		t.Fatal("lost later guard")
	}
	if err = s.Complete("large", outcome); err == nil {
		t.Fatal("appended row unreadable on rollback")
	}
	if err = s.Complete("large", []byte(`{"ok":true}`)); err != nil {
		t.Fatal("could not replace legacy outcome", err)
	}
}

func TestRecordLimitAndMarshalFailureKeepLogIntact(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.Prepare("op", "action", "session"); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, logFile)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	large := append([]byte{'"'}, bytes.Repeat([]byte{'a'}, maxReplayRecordBytes)...)
	large = append(large, '"')
	for _, outcome := range [][]byte{large, []byte("{")} {
		if err = s.Complete("op", outcome); err == nil {
			t.Fatal("unexpectedly committed unreadable outcome")
		}
	}
	if err = s.f.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, data) {
		t.Fatal("changed log after rejecting outcome", err)
	}
	// Simulate an oversized row written by a prior release. Its suffix and
	// contents must remain available for operator recovery.
	if err = os.WriteFile(path, append(append(large, '\n'), before...), 0600); err != nil {
		t.Fatal(err)
	}
	if reopened, err := Open(dir); err == nil {
		_ = reopened.f.Close()
		t.Fatal("opened incomplete replay")
	}
	data, err = os.ReadFile(path)
	if err != nil || len(data) != len(large)+1+len(before) || !bytes.HasSuffix(data, before) {
		t.Fatal("damaged oversized source", err)
	}
}

func TestUncertainAppendBlocksCompactionUntilReopen(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.Prepare("existing", "action", "session"); err != nil {
		t.Fatal(err)
	}
	original := writeLogRecord
	t.Cleanup(func() { writeLogRecord = original })
	writeLogRecord = func(*os.File, []byte) (int, error) { return 0, syscall.ENOSPC }
	if err = s.Begin("existing"); !errors.Is(err, syscall.ENOSPC) {
		t.Fatal(err)
	}
	writeLogRecord = original
	if _, _, err = s.Prepare("retry", "action", "session"); err != nil {
		t.Fatal("zero-byte failure prevented retry", err)
	}
	writeLogRecord = func(f *os.File, b []byte) (int, error) {
		n, e := f.Write(b[:len(b)/2])
		if e != nil {
			return n, e
		}
		return n, syscall.ENOSPC
	}
	if err = s.Begin("existing"); !errors.Is(err, syscall.ENOSPC) {
		t.Fatal(err)
	}
	writeLogRecord = original
	if err = s.Compact(); err == nil {
		t.Fatal("compacted uncertain state")
	}
	if _, _, err = s.Prepare("existing", "action", "session"); err == nil {
		t.Fatal("acknowledged cached operation after write failure")
	}
	if err = s.f.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.f.Close() }()
	if err = s.Begin("existing"); err != nil {
		t.Fatal(err)
	}
}
