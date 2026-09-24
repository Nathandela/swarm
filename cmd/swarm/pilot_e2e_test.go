package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
)

func TestPilotRealCLIControlsTwoWorkersWithoutChangingOrdinaryCLI(t *testing.T) {
	if testing.Short() {
		t.Skip("builds binaries and starts an isolated daemon with fake workers")
	}
	swarmBin, fakeAgentBin := buildRoleBinaries(t)
	env := startSmokeDaemon(t, swarmBin, fakeAgentBin)
	cc := ccFromSmokeEnv(env, swarmBin)
	c, err := protocol.Dial(cc.SocketPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	script := filepath.Join(t.TempDir(), "worker.txt")
	if err := os.WriteFile(script, []byte("ask > \nidle 60s\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var ids []string
	for range 2 {
		id, _, err := c.Launch(protocol.LaunchReq{Agent: "fake", Cwd: t.TempDir(),
			Options: map[string]string{"script": script}, Env: []string{"PATH=" + os.Getenv("PATH")}, Cols: 80, Rows: 24})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	tmp := t.TempDir()
	run := func(args ...string) (string, error) {
		cmd := exec.Command(swarmBin, args...)
		cmd.Env = append(append(os.Environ(), env...), "TMPDIR="+tmp)
		var stderr strings.Builder
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			return "", fmt.Errorf("%w: %s", err, stderr.String())
		}
		return string(out), nil
	}
	entry, err := run("--pilot")
	if err != nil {
		t.Fatal(err)
	}
	var start struct {
		Context         string          `json:"context"`
		TrustedGuidance string          `json:"trusted_guidance"`
		WorkerData      json.RawMessage `json:"worker_data"`
	}
	if err := json.Unmarshal([]byte(entry), &start); err != nil || start.Context == "" || start.TrustedGuidance == "" {
		t.Fatalf("pilot entry %q: %v", entry, err)
	}
	for _, id := range ids {
		if !strings.Contains(string(start.WorkerData), id) {
			t.Fatalf("entry roster omits %s: %s", id, start.WorkerData)
		}
	}
	ordinary, err := run("ls")
	if err != nil || strings.Contains(ordinary, "Trusted pilot") {
		t.Fatalf("ordinary ls changed by pilot: out=%q err=%v", ordinary, err)
	}
	payload := "Is AMI still probing?"
	sent, err := run("pilot", "--context", start.Context, "send", ids[0], "--text", payload)
	if err != nil {
		t.Fatal(err)
	}
	var receipt struct {
		Receipt string `json:"receipt"`
	}
	if err := json.Unmarshal([]byte(sent), &receipt); err != nil || receipt.Receipt != "sent" {
		t.Fatalf("send receipt %q: %v", sent, err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		out, err := run("peek", ids[0])
		gotExact := false
		for _, line := range strings.Split(out, "\n") {
			gotExact = gotExact || strings.TrimRight(line, " ") == "got: "+payload
		}
		if err == nil && gotExact {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("worker did not receive exact payload; screen=%q err=%v", out, err)
		}
		time.Sleep(100 * time.Millisecond)
	}
	if out, err := run("peek", ids[1]); err != nil || strings.Contains(out, payload) {
		t.Fatalf("message crossed worker boundary: screen=%q err=%v", out, err)
	}
	for _, id := range ids {
		out, err := run("peek", id)
		if err != nil || strings.Contains(out, "Trusted pilot") {
			t.Fatalf("pilot guidance entered worker screen %s: err=%v", id, err)
		}
		_, local, ok := protocol.ParseID(id)
		if !ok {
			t.Fatalf("invalid worker id %q", id)
		}
		log, err := os.ReadFile(filepath.Join(cc.StateDir, local, "transcript.log"))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(log), "Trusted pilot") {
			t.Fatalf("pilot guidance entered worker transcript %s", id)
		}
	}
	if _, err := run("pilot", "--context", start.Context, "exit"); err != nil {
		t.Fatal(err)
	}
}
