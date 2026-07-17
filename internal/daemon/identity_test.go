package daemon

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/status"
)

// TestProcessStartTime_StableAndDistinct asserts the identity primitive behind
// S3/D-4: process start time is stable across repeated reads of the same live
// PID (so a match check is deterministic) and distinguishes two processes
// started at different instants (so PID reuse is detectable). Mechanism is
// per-OS: /proc/<pid>/stat field 22 on linux, sysctl kinfo_proc on darwin.
func TestProcessStartTime_StableAndDistinct(t *testing.T) {
	self := os.Getpid()
	a, err := processStartTime(self)
	if err != nil {
		t.Fatalf("processStartTime(self): %v", err)
	}
	b, err := processStartTime(self)
	if err != nil {
		t.Fatalf("processStartTime(self) again: %v", err)
	}
	if a != b {
		t.Fatalf("start time not stable across reads: %d != %d", a, b)
	}

	// Ensure the clock advances measurably, then spawn a child that started
	// distinctly later than this process.
	time.Sleep(40 * time.Millisecond)
	child := exec.Command(selfExe(t), markerCatchTerm, filepath.Join(t.TempDir(), "x"))
	if err := child.Start(); err != nil {
		t.Fatalf("start child: %v", err)
	}
	childPID := child.Process.Pid
	t.Cleanup(func() { killTree(childPID); _, _ = child.Process.Wait() })

	c1, err := processStartTime(childPID)
	if err != nil {
		t.Fatalf("processStartTime(child): %v", err)
	}
	c2, err := processStartTime(childPID)
	if err != nil {
		t.Fatalf("processStartTime(child) again: %v", err)
	}
	if c1 != c2 {
		t.Fatalf("child start time not stable: %d != %d", c1, c2)
	}
	if c1 == a {
		t.Fatalf("child start time equals this process's (%d); want distinct instants", a)
	}
}

// TestProcessStartTime_ReapedPIDNotFalselyMatched asserts the PID-reuse safety
// property directly (S3): once a PID has exited, reading its start time must not
// yield the SAME value the live process had — either it errors (gone) or returns
// a different value (a new, unrelated process now holds the PID). Either way a
// stale (PID, start-time) pair cannot match.
func TestProcessStartTime_ReapedPIDNotFalselyMatched(t *testing.T) {
	child := exec.Command(selfExe(t), markerCatchTerm, filepath.Join(t.TempDir(), "x"))
	if err := child.Start(); err != nil {
		t.Fatalf("start child: %v", err)
	}
	pid := child.Process.Pid
	live, err := processStartTime(pid)
	if err != nil {
		t.Fatalf("processStartTime(live child): %v", err)
	}
	_ = child.Process.Kill()
	_, _ = child.Process.Wait()

	after, err := processStartTime(pid)
	if err == nil && after == live {
		t.Fatalf("reaped PID %d still reports the same start time %d; PID reuse would be undetectable", pid, live)
	}
}

// TestGenerateID_LowercaseAndCollisionResistant asserts the Epic 1 review
// carry-forward: session ids are lowercase-only and two ids never differ only by
// case (the case-insensitive-filesystem collision guard), while remaining
// collision-resistant and path-safe.
func TestGenerateID_LowercaseAndCollisionResistant(t *testing.T) {
	const n = 4000
	idRE := regexp.MustCompile(`^[a-z0-9._-]+$`)

	unique := make(map[string]struct{}, n)
	lowered := make(map[string]struct{}, n)
	for i := 0; i < n; i++ {
		id := generateID()
		if id == "" {
			t.Fatalf("generateID returned empty string")
		}
		if strings.ToLower(id) != id {
			t.Fatalf("generateID returned non-lowercase id %q", id)
		}
		if !idRE.MatchString(id) {
			t.Fatalf("generateID id %q has characters outside [a-z0-9._-]", id)
		}
		if strings.HasPrefix(id, "-") || id == "." || id == ".." {
			t.Fatalf("generateID produced an unsafe id %q", id)
		}
		unique[id] = struct{}{}
		lowered[strings.ToLower(id)] = struct{}{}
	}
	if len(unique) != n {
		t.Fatalf("collision: %d unique ids out of %d generated", len(unique), n)
	}
	// Case-collision guard: lowercasing the set must not merge any two ids.
	if len(lowered) != len(unique) {
		t.Fatalf("case collision: two ids differ only by case (%d lowercased vs %d unique)", len(lowered), len(unique))
	}
}

// TestGenerateID_PathSafeForStore asserts a generated id is accepted by the
// persistence layer's id validation — the daemon's ids round-trip through the
// store that will hold their sessions (ADR-004 path-safety).
func TestGenerateID_PathSafeForStore(t *testing.T) {
	dir := shortStateDir(t)
	st, err := persist.NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	for i := 0; i < 50; i++ {
		id := generateID()
		m := persist.Meta{
			ID:           id,
			AgentType:    "fake",
			Cwd:          "/tmp",
			CreatedAt:    time.Now(),
			Status:       status.Status{Process: status.ProcessRunning, Turn: status.TurnUnknown, Interaction: status.InteractionNone},
			LastActivity: time.Now(),
		}
		if err := st.Save(m); err != nil {
			t.Fatalf("Store.Save rejected generated id %q: %v", id, err)
		}
	}
}
