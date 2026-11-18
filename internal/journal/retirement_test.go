package journal

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestLiveRecoveryRetirementFailure(t *testing.T) {
	for _, restart := range []bool{false, true} {
		t.Run(map[bool]string{true: "restart", false: "retry"}[restart], func(t *testing.T) {
			dir := t.TempDir()
			opts := liveOptions()
			opts.MaxFiles = 32
			opts.MaxBytes = 1
			j := openLiveTest(t, dir, opts)
			var controls []Record
			for _, id := range []string{"a", "b", "c"} {
				controls = append(controls, mustAppend(t, j, Record{SessionID: id, Type: TypeLaunched}))
			}
			blocked := j.segments[len(j.segments)-2].path
			parked := filepath.Join(dir, "parked")
			original := writeLiveSegment
			t.Cleanup(func() { writeLiveSegment = original })
			writeLiveSegment = func(dir, name string, data []byte) error {
				writeLiveSegment = original
				if err := os.Rename(blocked, parked); err != nil {
					return err
				}
				if err := os.Mkdir(blocked, 0700); err != nil {
					return err
				}
				if err := os.WriteFile(filepath.Join(blocked, "busy"), nil, 0600); err != nil {
					return err
				}
				return original(dir, name, data)
			}
			j.storageErr = errors.New("prior failure")
			feed, cancel := j.Subscribe()
			defer cancel()
			if err := j.Recover(); err == nil || !j.NeedsRecovery() {
				t.Fatal("retirement failure cleared journal latch", err)
			}
			if _, err := j.Append(Record{Type: TypeDeleted}); err == nil {
				t.Fatal("failed recovery acknowledged append")
			}
			if err := os.RemoveAll(blocked); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(parked, blocked); err != nil {
				t.Fatal(err)
			}
			if restart {
				if err := j.Close(); err != nil {
					t.Fatal(err)
				}
				j = openLiveTest(t, dir, opts)
			} else if err := j.Recover(); err != nil {
				t.Fatal(err)
			}
			res, err := j.ReadFrom(0)
			if err != nil || len(res.Events) != len(controls) {
				t.Fatalf("recovered controls %+v, err %v", res, err)
			}
			for i, want := range controls {
				if got := res.Events[i]; got.Cursor != want.Cursor || got.SessionID != want.SessionID {
					t.Fatalf("event %+v want %+v", got, want)
				}
			}
			next := mustAppend(t, j, Record{Type: TypePresence})
			if next.Cursor <= controls[len(controls)-1].Cursor {
				t.Fatal("cursor regressed")
			}
			if !restart {
				select {
				case got := <-feed:
					if got.Cursor != next.Cursor {
						t.Fatal("subscriber cursor", got.Cursor)
					}
				default:
					t.Fatal("lost subscriber")
				}
			}
		})
	}
}
