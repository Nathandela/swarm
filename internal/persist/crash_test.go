package persist

// FIX 3 / S8 acceptance test (E1.4): real process-crash injection. A helper
// process Saves alternating metas in a tight loop; the parent SIGKILLs it while a
// write is in flight, then asserts Load/Scan observe a complete old-or-new meta —
// never a torn file, never a lost commit. Uses the standard Go re-exec pattern:
// the parent runs this same test binary with -test.run pinned to the guarded
// helper and SWARM_CRASH_HELPER=1.
//
// The test is deliberately non-vacuous: each cycle waits until the helper has
// demonstrably committed at least one Save (meta.json exists) before killing, so
// the kill lands mid-write and a not-yet-written pass can never happen; the
// kill/reap errors are checked; and once a commit exists it must never revert to
// not-found.

import (
	"errors"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"
)

// crashVictimID is the single session id the crash helper writes and the parent
// inspects.
const crashVictimID = "crash-victim"

// crashCycles is how many kill/inspect cycles the parent runs. Kept small so the
// whole test (re-exec + kill per cycle) stays well under ~5s, including -race.
const crashCycles = 15

// crashMeta returns one of two distinct-but-valid metas keyed by marker ("A" or
// "B"). LaunchOptions carries a few-KB filler so each Save is non-instant, giving
// the SIGKILL a real chance to land mid-write. Env is allowlisted and the schema
// is current, so the persisted form equals this value exactly (no normalization),
// letting the parent assert an exact old-or-new match.
func crashMeta(marker string) Meta {
	m := fullMeta()
	m.ID = crashVictimID
	m.Env = []string{"PATH=/usr/bin:/bin"}
	m.LaunchOptions = map[string]string{
		"marker": marker,
		"filler": strings.Repeat(marker, 4096),
	}
	return m
}

// TestHelperCrashWriter is the crash helper: it runs only when re-exec'd with
// SWARM_CRASH_HELPER=1, otherwise it is a no-op skip during a normal test run.
// It Saves crashMeta("A") and crashMeta("B") alternately, forever, until killed.
func TestHelperCrashWriter(t *testing.T) {
	if os.Getenv("SWARM_CRASH_HELPER") != "1" {
		t.Skip("crash helper; runs only when re-exec'd with SWARM_CRASH_HELPER=1")
	}
	s, err := NewStore(os.Getenv("SWARM_CRASH_DIR"))
	if err != nil {
		os.Exit(10)
	}
	metas := []Meta{crashMeta("A"), crashMeta("B")}
	for i := 0; ; i++ {
		if err := s.Save(metas[i%2]); err != nil {
			os.Exit(11)
		}
	}
}

// waitExists polls for path to appear, up to timeout. Returns true once it exists.
func waitExists(path string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		if _, err := os.Stat(path); err == nil {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(time.Millisecond)
	}
}

// isKillSignal reports whether err is the exit error expected from SIGKILL'ing a
// child: an *exec.ExitError whose wait status was terminated by SIGKILL. Any
// other error (a clean exit, or a different signal such as a panic/segv) is not.
func isKillSignal(err error) bool {
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		return false
	}
	ws, ok := ee.Sys().(syscall.WaitStatus)
	return ok && ws.Signaled() && ws.Signal() == syscall.SIGKILL
}

func TestCrashDuringSaveNeverTears(t *testing.T) {
	if os.Getenv("SWARM_CRASH_HELPER") == "1" {
		t.Skip("running as crash helper")
	}
	dir := filepath.Join(t.TempDir(), "sessions")
	metaPath := filepath.Join(dir, crashVictimID, metaFile)
	wantA, wantB := crashMeta("A"), crashMeta("B")
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	established := false

	for cycle := 0; cycle < crashCycles; cycle++ {
		cmd := exec.Command(os.Args[0], "-test.run=^TestHelperCrashWriter$")
		cmd.Env = append(os.Environ(), "SWARM_CRASH_HELPER=1", "SWARM_CRASH_DIR="+dir)
		if err := cmd.Start(); err != nil {
			t.Fatalf("cycle %d: start helper: %v", cycle, err)
		}

		// Gate the kill on a demonstrably-committed Save (meta.json exists), so
		// the cycle exercises a real mid-write crash rather than passing vacuously
		// because nothing was written yet.
		if !waitExists(metaPath, 2*time.Second) {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			t.Fatalf("cycle %d: helper never committed a Save within timeout", cycle)
		}

		// Let more writes accumulate so the SIGKILL interrupts an in-flight write.
		time.Sleep(time.Duration(5+rng.Intn(46)) * time.Millisecond)

		if err := cmd.Process.Kill(); err != nil {
			t.Fatalf("cycle %d: Process.Kill: %v", cycle, err)
		}
		if err := cmd.Wait(); !isKillSignal(err) {
			t.Fatalf("cycle %d: helper exited with an unexpected error (want signal: killed): %v", cycle, err)
		}

		s, err := NewStore(dir)
		if err != nil {
			t.Fatalf("cycle %d: NewStore: %v", cycle, err)
		}

		got, err := s.Load(crashVictimID)
		if err != nil {
			// meta.json existed before the kill and is never deleted, so any error
			// here — a torn/corrupt read or a NotExist — is a real failure once a
			// commit has been observed.
			t.Fatalf("cycle %d: Load after kill errored (torn read or lost commit): %v", cycle, err)
		}
		established = true
		if !reflect.DeepEqual(got, wantA) && !reflect.DeepEqual(got, wantB) {
			t.Fatalf("cycle %d: Load returned neither the old nor the new meta (torn): marker=%q",
				cycle, got.LaunchOptions["marker"])
		}

		sessions, err := s.Scan()
		if err != nil {
			t.Fatalf("cycle %d: Scan error: %v", cycle, err)
		}
		seen := false
		for _, sm := range sessions {
			if sm.ID != crashVictimID {
				continue
			}
			seen = true
			if !reflect.DeepEqual(sm, wantA) && !reflect.DeepEqual(sm, wantB) {
				t.Fatalf("cycle %d: Scan returned a torn meta: marker=%q", cycle, sm.LaunchOptions["marker"])
			}
		}
		if !seen {
			t.Fatalf("cycle %d: Scan omitted the committed session", cycle)
		}
	}

	if !established {
		t.Fatal("crash test never observed a committed meta; the run was vacuous")
	}
}
