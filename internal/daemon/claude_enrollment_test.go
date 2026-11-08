package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestClaudeEnrollmentSurvivesDaemonRestartAndDescriptorLoss(t *testing.T) {
	cfg := daemonConfig(t)
	d := openDaemon(t, cfg)
	script := filepath.Join(t.TempDir(), "agent.txt")
	if err := os.WriteFile(script, []byte("idle 60s\n"), 0600); err != nil {
		t.Fatal(err)
	}
	m, err := d.Launch(LaunchSpec{AgentType: "claude", Argv: []string{fakeAgentBin, script}, Cwd: t.TempDir(), ClientEnv: []string{"PATH=" + os.Getenv("PATH")}, Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	launch := r7ReadLaunchConfig(t, d, m.ID)
	launch["claude_input"] = true
	raw, err := json.Marshal(launch)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d.sessionDir(m.ID), shimLaunchConfigFile), raw, 0600); err != nil {
		t.Fatal(err)
	}
	if enrolled, err := d.SessionClaudeInputEnrollment(m.ID); err != nil || !enrolled {
		t.Fatalf("enrollment=%t error=%v", enrolled, err)
	}
	if _, err := d.SessionClaudeInput(m.ID); err == nil {
		t.Fatal("missing native descriptor was accepted")
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	restarted := openDaemon(t, cfg)
	if enrolled, err := restarted.SessionClaudeInputEnrollment(m.ID); err != nil || !enrolled {
		t.Fatalf("restart lost enrollment=%t error=%v", enrolled, err)
	}
	if _, err := restarted.SessionClaudeInput(m.ID); err == nil {
		t.Fatal("restart accepted missing native descriptor")
	}
	path := filepath.Join(restarted.sessionDir(m.ID), shimLaunchConfigFile)
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.SessionClaudeInputEnrollment(m.ID); err == nil {
		t.Fatal("accepted public launch custody")
	}
}
