// Epic 11 RESUME-AS-NEW-SESSION end-to-end test (E11.5 / R-2, scenario 12). When an
// ended/lost session captured a native conversation id and its adapter supports
// resume, relaunching it produces a NEW session linked to the original via
// meta.ResumedFrom — never a mutation of the old row. The generic engine/daemon
// resume wiring is core work landed inside this epic (separate from the adapter
// packages); the adapter's contribution — composing the resume argv that carries
// the conversation id — is pinned in the claude/codex adapter suites.
//
// PINNED SURFACE (mirrors the worktree toggle's reserved-option pattern,
// protocol.OptionWorktree = "worktree"): the client requests resume by carrying the
// source session's id under the reserved launch option "resume_from"; the daemon
// resolves it, launches a fresh session, and records meta.ResumedFrom = <source
// local id>. The implementer may instead promote this to a typed
// LaunchReq.ResumeFrom field + protocol.OptionResumeFrom const and update THIS one
// test's key. Using an existing map value keeps the suite COMPILING today; it
// FAILS AT RUNTIME because no resume wiring exists yet (the new session's
// ResumedFrom stays empty).
//
// COST: fake agent only; no billable real-CLI run.
package e2e

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/status"
)

// optResumeFrom is the reserved launch-option key carrying the source session id to
// resume. See the pinned-surface note above.
const optResumeFrom = "resume_from"

// intPtr returns a pointer to i (for Meta.ExitCode).
func intPtr(i int) *int { return &i }

// TestE2E_ResumeAsNewSession_R2 seeds an ENDED session that captured a conversation
// id, then resumes it and asserts a distinct NEW session is created and linked via
// resumed_from (R-2). The source is seeded on disk before the daemon starts, so the
// daemon rebuilds it from the meta scan (D-4) as a completed, resumable row.
func TestE2E_ResumeAsNewSession_R2(t *testing.T) {
	buildBinaries(t)
	env := newDaemonEnv(t)

	// Seed an ended session with a captured conversation id, directly on disk (the
	// daemon is the sole meta writer at runtime, but this is pre-start fixture state
	// the daemon discovers by scan).
	srcDir := t.TempDir()
	store, err := persist.NewStore(env.stateDir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	const srcLocal = "resumesrc"
	src := persist.Meta{
		ID:             srcLocal,
		AgentType:      "fake",
		Cwd:            srcDir,
		ConversationID: "conv-RESUME-abc123",
		Status:         status.Status{Process: status.ProcessExited, Turn: status.TurnIdle, Interaction: status.InteractionNone},
		ExitCode:       intPtr(0),
		CreatedAt:      time.Now().Add(-time.Hour),
		LastActivity:   time.Now().Add(-time.Hour),
	}
	if err := store.Save(src); err != nil {
		t.Fatalf("seed ended session: %v", err)
	}

	startDaemon(t, env)
	c := dial(t, env.sock)

	// The daemon lists the seeded, completed session (R-3: completed rows remain).
	srcID := findSessionByLocal(t, c, srcLocal)

	// Resume it: relaunch carrying the source id under the reserved option. A script
	// makes the NEW fake session actually runnable.
	spath := filepath.Join(t.TempDir(), "resume-script.txt")
	if err := os.WriteFile(spath, []byte("print RESUMED\nidle 120s\n"), 0o644); err != nil {
		t.Fatalf("write script: %v", err)
	}
	newID, err := c.Launch(protocol.LaunchReq{
		Agent:   "fake",
		Cwd:     srcDir,
		Options: map[string]string{optResumeFrom: srcID, "script": spath},
		Env:     []string{"PATH=" + os.Getenv("PATH")},
		Cols:    80,
		Rows:    24,
	})
	if err != nil {
		t.Fatalf("resume launch: %v", err)
	}

	newLocal := localOf(t, newID)
	if newLocal == srcLocal {
		t.Fatalf("resume reused the source id %q; R-2 resumes as a NEW session", srcLocal)
	}

	// The new session's meta must link back to the source via resumed_from.
	deadline := time.Now().Add(l1Bound)
	for time.Now().Before(deadline) {
		m := readMeta(t, env.stateDir, newLocal)
		if m.ResumedFrom == srcLocal {
			return // linked — R-2 satisfied
		}
		time.Sleep(50 * time.Millisecond)
	}
	got := readMeta(t, env.stateDir, newLocal)
	t.Fatalf("new session %q has ResumedFrom=%q; want %q — the resume-as-new-session flow must record the "+
		"resumed_from link (R-2). Wire the reserved %q option (or a typed LaunchReq.ResumeFrom) through the "+
		"daemon launch path so it copies the source's conversation id into the adapter's Resume argv and "+
		"stamps meta.ResumedFrom.", newLocal, got.ResumedFrom, srcLocal, optResumeFrom)
}

// findSessionByLocal returns the namespaced id of the session whose local id equals
// want, waiting until the daemon lists it.
func findSessionByLocal(t *testing.T, c *protocol.Client, want string) string {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		views, err := c.List()
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		for _, v := range views {
			if _, local, ok := protocol.ParseID(v.ID); ok && local == want {
				return v.ID
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("daemon never listed the seeded session %q", want)
	return ""
}
