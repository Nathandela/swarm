package daemon

import (
	"os"
	"testing"
)

func TestReviewClaudeWorktreeEnrollmentUsesActualCwd(t *testing.T) {
	cfg := daemonConfig(t)
	actual := t.TempDir()
	cfg.PreLaunch = func(string, LaunchSpec) (string, error) { return actual, nil }
	d := openDaemon(t, cfg)
	killAllSessions(t, d)
	spec := announceSpec(t, t.TempDir()+"/pid")
	spec.AgentType = "claude"
	spec.ClientEnv = []string{"PATH=" + os.Getenv("PATH")}
	m, err := d.Launch(spec)
	if err != nil {
		t.Fatal(err)
	}
	if m.AgentCwd != actual || m.Cwd == m.AgentCwd {
		t.Fatalf("invalid worktree fixture: %+v", m)
	}
	enrolled, err := d.SessionClaudeInputEnrollment(m.ID)
	if err != nil || enrolled {
		t.Fatalf("ordinary worktree session lost composer compatibility: enrolled=%v err=%v", enrolled, err)
	}
}
