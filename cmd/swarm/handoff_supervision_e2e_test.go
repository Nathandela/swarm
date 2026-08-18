package main

// End-to-end through the REAL binaries (ADR-010 Amendment 3): a real `swarm daemon`, a
// scripted fake SOURCE session, the real `swarm handoff --supervision passive` CLI run
// with the source's SWARM_SESSION_ID (exactly what a source agent does), and the
// supervisor's notification observed on the source's own screen through `swarm peek`.
// The child is a fake with no script, so it exits at once: the attention event under
// test is `completed`, delivered as soon as the idle source is safe.

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/hookclient"
	"github.com/Nathandela/swarm/internal/protocol"
)

func TestHandoffSupervision_PassiveChildWakesSourceThroughRealBinaries(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns a real daemon + sessions")
	}
	swarmBin, fakeAgentBin := buildRoleBinaries(t)
	env := startSmokeDaemon(t, swarmBin, fakeAgentBin)
	cc := ccFromSmokeEnv(env, swarmBin)
	c, err := protocol.Dial(cc.SocketPath, nil)
	if err != nil {
		t.Fatalf("dial smoke daemon: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	// The source: a fake that prompts, then echoes whatever is typed as "got: ...".
	script := filepath.Join(t.TempDir(), "source.txt")
	if err := os.WriteFile(script, []byte("ask > \nidle 60s\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sourceID, _, err := c.Launch(protocol.LaunchReq{
		Agent: "fake", Cwd: t.TempDir(),
		Options: map[string]string{"script": script},
		Env:     []string{"PATH=" + os.Getenv("PATH")},
		Cols:    80, Rows: 24,
	})
	if err != nil {
		t.Fatalf("launch source: %v", err)
	}
	_, sourceLocal, _ := protocol.ParseID(sourceID)

	// The real CLI, from "inside" the source: SWARM_SESSION_ID names the source.
	ctx := filepath.Join(t.TempDir(), "handoff.md")
	if err := os.WriteFile(ctx, []byte("# Goal\nsmoke\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) (string, error) {
		cmd := exec.Command(swarmBin, args...)
		cmd.Env = append(append(os.Environ(), env...), hookclient.EnvSessionID+"="+sourceLocal)
		out, err := cmd.Output()
		return strings.TrimSpace(string(out)), err
	}
	childID, err := run("handoff", "--cli", "fake", "--supervision", "passive", "--context-file", ctx)
	if err != nil || childID == "" {
		t.Fatalf("swarm handoff: %v (stdout %q)", err, childID)
	}

	// The roster shows the mode on the child; the notification lands on the source.
	deadline := time.Now().Add(15 * time.Second)
	var sawMode, sawNotice bool
	for time.Now().Before(deadline) && !(sawMode && sawNotice) {
		if out, err := run("ls", "--json"); err == nil {
			var views []protocol.SessionView
			_ = json.Unmarshal([]byte(out), &views)
			for _, v := range views {
				if v.ID == childID && v.Supervision == protocol.SupervisionPassive {
					sawMode = true
				}
			}
		}
		if out, err := run("peek", sourceID); err == nil && strings.Contains(out, "got: [swarm supervision") {
			sawNotice = true
		}
		time.Sleep(200 * time.Millisecond)
	}
	if !sawMode {
		t.Errorf("child %s never showed supervision=passive in swarm ls --json", childID)
	}
	if !sawNotice {
		out, _ := run("peek", sourceID)
		t.Fatalf("source screen never showed the supervision notification; screen:\n%s", out)
	}
	// The 80-column screen wraps the line, so compare with whitespace removed.
	out, _ := run("peek", sourceID)
	if compact := strings.NewReplacer("\n", "", " ", "").Replace(out); !strings.Contains(compact, "iscompleted-doafinalreview") {
		t.Errorf("notification does not name the completed state; screen:\n%s", out)
	}
}
