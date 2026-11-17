package journal

import (
	"errors"
	"io"
	"math"
	"os"
	"syscall"
	"testing"
)

func TestRecoverAfterStorageFailure_LiveAndLegacy(t *testing.T) {
	for _, live := range []bool{false, true} {
		for _, failure := range []string{"partial", "short", "sync"} {
			mode := "legacy"
			if live {
				mode = "live"
			}
			t.Run(mode+"/"+failure, func(t *testing.T) {
				dir := t.TempDir()
				opts := Options{}
				if live {
					opts = liveOptions()
				}
				j := openLiveTest(t, dir, opts)
				first := mustAppend(t, j, Record{SessionID: "s1", Type: TypeLaunched})
				var liveInteract Record
				if live {
					liveInteract = mustAppend(t, j, liveMessage("m1", "completed", "retained-in-ram"))
				}

				feed, cancel := j.Subscribe()
				defer cancel()

				original := j.activeF
				f := &failingSegment{File: original.(*os.File), short: failure == "short", syncErr: failure == "sync"}
				j.activeF = f

				_, err := j.Append(Record{SessionID: "s1", Type: TypePresence})
				want := error(syscall.ENOSPC)
				if failure == "short" {
					want = io.ErrShortWrite
				}
				if failure == "sync" {
					want = syscall.EIO
				}
				if !errors.Is(err, want) {
					t.Fatalf("failure = %v, want %v", err, want)
				}
				if !j.NeedsRecovery() {
					t.Fatal("NeedsRecovery() = false after storage write failure")
				}

				// Direct Append without Recover must stay latched.
				if _, err := j.Append(Record{SessionID: "s1", Type: TypePresence}); !errors.Is(err, want) {
					t.Fatalf("direct Append before Recover = %v, want latched %v", err, want)
				}

				// While storage is still broken (directory unwritable), Recover fails cleanly
				// and keeps the failure latched without corrupting existing records.
				if err := os.Chmod(dir, 0o500); err != nil {
					t.Fatal(err)
				}
				if rerr := j.Recover(); rerr == nil {
					_ = os.Chmod(dir, 0o700)
					t.Fatal("Recover succeeded while journal directory was unwritable")
				}
				if err := os.Chmod(dir, 0o700); err != nil {
					t.Fatal(err)
				}
				if !j.NeedsRecovery() {
					t.Fatal("NeedsRecovery() cleared after failed Recover")
				}

				// Storage recovers: Recover succeeds, keeps subscribers and in-RAM live items,
				// and advances cursors monotonically past any bytes that reached disk.
				if err := j.Recover(); err != nil {
					t.Fatalf("Recover after storage restored: %v", err)
				}
				if j.NeedsRecovery() {
					t.Fatal("NeedsRecovery() still true after successful Recover")
				}

				minPrevCursor := first.Cursor
				if live && liveInteract.Cursor > minPrevCursor {
					minPrevCursor = liveInteract.Cursor
				}
				if failure == "sync" {
					// Sync failed after full line write: disk already has first.Cursor+1 (or liveInteract.Cursor+1).
					minPrevCursor++
				}

				after := mustAppend(t, j, Record{SessionID: "s2", Type: TypeLaunched})
				if after.Cursor <= minPrevCursor {
					t.Fatalf("post-recovery cursor = %d, want > %d", after.Cursor, minPrevCursor)
				}

				select {
				case got, ok := <-feed:
					if !ok || got.Cursor != after.Cursor || got.SessionID != "s2" {
						t.Fatalf("subscriber after recovery got (%+v, ok=%v), want cursor %d", got, ok, after.Cursor)
					}
				default:
					t.Fatal("existing subscriber received no event after recovery")
				}

				if live {
					res, rerr := j.ReadFrom(0)
					if rerr != nil {
						t.Fatal(rerr)
					}
					foundInteract := false
					for _, ev := range res.Events {
						if ev.Type == TypeInteraction && ev.Cursor == liveInteract.Cursor {
							foundInteract = true
						}
					}
					if !foundInteract {
						t.Fatalf("in-memory live interaction lost across Recover: %+v", res.Events)
					}
				}

				if err := j.Close(); err != nil {
					t.Fatalf("Close: %v", err)
				}
				reopened := openLiveTest(t, dir, opts)
				reloaded := mustAppend(t, reopened, Record{SessionID: "s3", Type: TypePresence})
				if reloaded.Cursor <= after.Cursor {
					t.Fatalf("reopened cursor = %d, want > %d", reloaded.Cursor, after.Cursor)
				}
			})
		}
	}
}

func TestRecoverAfterReservationStorageFailure(t *testing.T) {
	dir := t.TempDir()
	j := openLiveTest(t, dir, liveOptions())
	first := mustAppend(t, j, Record{SessionID: "s", Type: TypeLaunched})
	original := j.activeF
	j.activeF = &failingSegment{File: original.(*os.File), syncErr: true}

	if _, err := j.Append(liveMessage("first", "completed", "private")); err == nil {
		t.Fatal("expected reservation sync failure")
	}
	if !j.NeedsRecovery() {
		t.Fatal("NeedsRecovery() = false after reservation failure")
	}
	if err := j.Recover(); err != nil {
		t.Fatalf("Recover: %v", err)
	}
	next := mustAppend(t, j, liveMessage("second", "completed", "private"))
	if next.Cursor <= first.Cursor {
		t.Fatalf("cursor = %d, want > %d", next.Cursor, first.Cursor)
	}
}

func TestRecoverRefusesClosedOrExhaustedAndNoopsWhenHealthy(t *testing.T) {
	dir := t.TempDir()
	j := openLiveTest(t, dir, liveOptions())
	if j.NeedsRecovery() {
		t.Fatal("healthy journal reported NeedsRecovery")
	}
	if err := j.Recover(); err != nil {
		t.Fatalf("healthy Recover: %v", err)
	}

	j.mu.Lock()
	j.cursor = math.MaxUint64
	j.mu.Unlock()
	if _, err := j.Append(Record{SessionID: "s", Type: TypeLaunched}); err == nil {
		t.Fatal("expected cursor exhaustion error")
	}
	if j.NeedsRecovery() {
		t.Fatal("cursor-exhausted journal must not report NeedsRecovery")
	}
	if err := j.Recover(); err == nil {
		t.Fatal("cursor-exhausted journal must not recover")
	}

	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	if j.NeedsRecovery() {
		t.Fatal("closed journal reported NeedsRecovery")
	}
	if err := j.Recover(); err == nil {
		t.Fatal("closed journal must refuse Recover")
	}
}
