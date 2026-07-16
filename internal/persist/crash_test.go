package persist

// FIX 3 / S8 acceptance test (E1.4): real process-crash injection. A helper
// process Saves alternating metas in a tight loop; the parent SIGKILLs it at a
// random moment mid-write, then asserts Load/Scan observe a complete old-or-new
// meta — never a torn file, never an error other than not-found-before-first-write.
// Uses the standard Go re-exec pattern: the parent runs this same test binary with
// -test.run pinned to the guarded helper and SWARM_CRASH_HELPER=1.

import (
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

func TestCrashDuringSaveNeverTears(t *testing.T) {
	if os.Getenv("SWARM_CRASH_HELPER") == "1" {
		t.Skip("running as crash helper")
	}
	dir := filepath.Join(t.TempDir(), "sessions")
	wantA, wantB := crashMeta("A"), crashMeta("B")
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))

	for cycle := 0; cycle < crashCycles; cycle++ {
		cmd := exec.Command(os.Args[0], "-test.run=^TestHelperCrashWriter$")
		cmd.Env = append(os.Environ(), "SWARM_CRASH_HELPER=1", "SWARM_CRASH_DIR="+dir)
		if err := cmd.Start(); err != nil {
			t.Fatalf("cycle %d: start helper: %v", cycle, err)
		}
		time.Sleep(time.Duration(5+rng.Intn(46)) * time.Millisecond)
		_ = cmd.Process.Signal(syscall.SIGKILL)
		_ = cmd.Wait() // returns "signal: killed"; expected

		s, err := NewStore(dir)
		if err != nil {
			t.Fatalf("cycle %d: NewStore: %v", cycle, err)
		}

		got, err := s.Load(crashVictimID)
		if err != nil {
			if os.IsNotExist(err) {
				continue // killed before the first write committed — acceptable
			}
			t.Fatalf("cycle %d: Load returned a non-NotExist error (torn/corrupt read): %v", cycle, err)
		}
		if !reflect.DeepEqual(got, wantA) && !reflect.DeepEqual(got, wantB) {
			t.Fatalf("cycle %d: Load returned neither the old nor the new meta (torn): marker=%q",
				cycle, got.LaunchOptions["marker"])
		}

		sessions, err := s.Scan()
		if err != nil {
			t.Fatalf("cycle %d: Scan error: %v", cycle, err)
		}
		for _, sm := range sessions {
			if sm.ID != crashVictimID {
				continue
			}
			if !reflect.DeepEqual(sm, wantA) && !reflect.DeepEqual(sm, wantB) {
				t.Fatalf("cycle %d: Scan returned a torn meta: marker=%q", cycle, sm.LaunchOptions["marker"])
			}
		}
	}
}
