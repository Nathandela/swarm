package journal

import (
	"errors"
	"io"
	"os"
	"syscall"
	"testing"
)

type failingSegment struct {
	*os.File
	short   bool
	syncErr bool
	writes  int
}

func (f *failingSegment) Write(p []byte) (int, error) {
	f.writes++
	if f.syncErr {
		return f.File.Write(p)
	}
	n, err := f.File.Write(p[:len(p)/2])
	if err != nil {
		return n, err
	}
	if f.short {
		return n, nil
	}
	return n, syscall.ENOSPC
}
func (f *failingSegment) Sync() error { return syscall.EIO }

func TestAppendStorageFailurePreventsFurtherWrites(t *testing.T) {
	for _, live := range []bool{false, true} {
		for _, failure := range []string{"partial", "short", "sync"} {
			t.Run(map[bool]string{false: "legacy", true: "live"}[live]+"/"+failure, func(t *testing.T) {
				dir := t.TempDir()
				opts := Options{}
				if live {
					opts = liveOptions()
				}
				j := openLiveTest(t, dir, opts)
				first := mustAppend(t, j, Record{SessionID: "s", Type: TypeLaunched})
				original := j.activeF
				f := &failingSegment{File: original.(*os.File), short: failure == "short", syncErr: failure == "sync"}
				j.activeF = f
				feed, cancel := j.Subscribe()
				defer cancel()
				_, err := j.Append(Record{SessionID: "s", Type: TypePresence})
				want := error(syscall.ENOSPC)
				if failure == "short" {
					want = io.ErrShortWrite
				}
				if failure == "sync" {
					want = syscall.EIO
				}
				if !errors.Is(err, want) {
					t.Fatalf("failure=%v, want %v", err, want)
				}
				// Even after disk recovery, this handle must not append onto the torn line.
				j.activeF = original
				for _, r := range []Record{{SessionID: "s", Type: TypePresence}, liveMessage("later", "completed", "body")} {
					if _, err := j.Append(r); !errors.Is(err, want) {
						t.Fatalf("storage failure was not latched: %v", err)
					}
				}
				if f.writes != 1 || len(j.records) != 1 {
					t.Fatal("failed append mutated journal records")
				}
				select {
				case <-feed:
					t.Fatal("failed append published a record")
				default:
				}
				_ = j.Close()
				j = openLiveTest(t, dir, opts)
				next := mustAppend(t, j, Record{SessionID: "s", Type: TypePresence})
				if next.Cursor <= first.Cursor {
					t.Fatal("restart reused committed cursor")
				}
			})
		}
	}
}

func TestReservationStorageFailureStopsPublication(t *testing.T) {
	for _, failure := range []string{"partial", "short", "sync"} {
		t.Run(failure, func(t *testing.T) {
			dir := t.TempDir()
			j := openLiveTest(t, dir, liveOptions())
			first := mustAppend(t, j, Record{SessionID: "s", Type: TypeLaunched})
			original := j.activeF
			j.activeF = &failingSegment{File: original.(*os.File), short: failure == "short", syncErr: failure == "sync"}
			feed, cancel := j.Subscribe()
			defer cancel()
			_, failureErr := j.Append(liveMessage("first", "completed", "private body"))
			if failureErr == nil {
				t.Fatal("failed reservation accepted a message")
			}
			j.activeF = original
			if _, err := j.Append(liveMessage("second", "completed", "private body")); err != failureErr {
				t.Fatalf("reservation failure not latched: %v", err)
			}
			if j.Cursor() != first.Cursor || len(j.records) != 1 {
				t.Fatal("failed reservation published cursor custody")
			}
			select {
			case <-feed:
				t.Fatal("failed reservation published an event")
			default:
			}
			_ = j.Close()
			j = openLiveTest(t, dir, liveOptions())
			if next := mustAppend(t, j, liveMessage("recovered", "completed", "body")); next.Cursor <= first.Cursor {
				t.Fatal("reopen reused a cursor")
			}
		})
	}
}
