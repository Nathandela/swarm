package main

// End-to-end through the REAL binaries (ADR-010 Amendment 3): a real `swarm daemon`, a
// scripted fake SOURCE session, the real `swarm handoff --supervision passive` CLI run
// with the source's SWARM_SESSION_ID (exactly what a source agent does), and the
// supervisor's notification observed on the source's own screen through `swarm peek`.
// The child is a fake that prints, idles a second and exits: the attention event under
// test is `completed`, delivered as soon as the idle source is safe.

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/hookclient"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/status"
)

func TestHandoffSupervision_PassiveChildWakesSourceThroughRealBinaries(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns a real daemon + sessions")
	}
	swarmBin, fakeAgentBin := buildRoleBinaries(t)
	// `swarm handoff` carries no script option, so the child fake would get "" and die
	// before its shim is ready (a slow launch under load); a wrapper gives an argument-less
	// launch a short, well-formed script instead, and passes an explicit one through.
	childScript := filepath.Join(t.TempDir(), "child.txt")
	if err := os.WriteFile(childScript, []byte("print child up\nidle 1s\nexit 0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	wrapper := filepath.Join(t.TempDir(), "fake-agent")
	if err := os.WriteFile(wrapper, []byte("#!/bin/sh\ns=\"$1\"; [ -n \"$s\" ] || s='"+childScript+"'\nexec '"+fakeAgentBin+"' \"$s\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	env := startSmokeDaemon(t, swarmBin, wrapper)
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
	// The real `swarm handoff` copies the document into its own os.TempDir; point THAT
	// process's TMPDIR at a per-test root so the copy never lands in the real /tmp. Only
	// the subprocess env changes: the daemon's socket paths were minted above and must
	// stay short.
	copyRoot := t.TempDir()
	run := func(args ...string) (string, error) {
		cmd := exec.Command(swarmBin, args...)
		cmd.Env = append(append(os.Environ(), env...), hookclient.EnvSessionID+"="+sourceLocal, "TMPDIR="+copyRoot)
		var stderr strings.Builder
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			err = fmt.Errorf("%w; stderr: %s", err, strings.TrimSpace(stderr.String()))
		}
		return strings.TrimSpace(string(out)), err
	}
	// The source must be a RUNNING roster row before the CLI can hand off from it.
	roster := func() []protocol.SessionView {
		out, _ := run("ls", "--json")
		var views []protocol.SessionView
		_ = json.Unmarshal([]byte(out), &views)
		return views
	}
	deadline := time.Now().Add(10 * time.Second)
	for running := false; !running; time.Sleep(100 * time.Millisecond) {
		for _, v := range roster() {
			running = running || (v.ID == sourceID && v.Status.Process == status.ProcessRunning)
		}
		if !running && time.Now().After(deadline) {
			t.Fatalf("source %s never became a running roster row: %+v", sourceID, roster())
		}
	}
	childID, err := run("handoff", "--cli", "fake", "--supervision", "passive", "--context-file", ctx)
	if err != nil || childID == "" {
		dlog, _ := os.ReadFile(cc.LogPath)
		t.Fatalf("swarm handoff: %v (stdout %q)\nroster: %+v\ndaemon log tail:\n%s", err, childID, roster(), tail(string(dlog), 40))
	}

	// The roster shows the mode on the child; the notification lands on the source.
	deadline = time.Now().Add(15 * time.Second)
	var sawMode, sawNotice bool
	for time.Now().Before(deadline) && (!sawMode || !sawNotice) {
		for _, v := range roster() {
			if v.ID == childID && v.Supervision == protocol.SupervisionPassive {
				sawMode = true
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

// tail returns the last n lines of s.
func tail(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
