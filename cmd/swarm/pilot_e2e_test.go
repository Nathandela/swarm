package main

import (
	"bufio"
	"context"
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
	t.Run("watch contexts are independent and receive real journal events", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		type watched struct {
			path  string
			lines chan json.RawMessage
			done  chan error
		}
		startWatch := func(id string) watched {
			t.Helper()
			entry, err := run("--pilot")
			if err != nil {
				t.Fatal(err)
			}
			var info struct {
				Context string `json:"context"`
			}
			if err := json.Unmarshal([]byte(entry), &info); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _, _ = run("pilot", "--context", info.Context, "exit") })
			cmd := exec.CommandContext(ctx, swarmBin, "pilot", "--context", info.Context, "watch", id)
			cmd.Env = append(append(os.Environ(), env...), "TMPDIR="+tmp)
			pipe, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			w := watched{info.Context, make(chan json.RawMessage, 64), make(chan error, 1)}
			go func() {
				scan := bufio.NewScanner(pipe)
				scan.Buffer(make([]byte, 4096), 2<<20)
				for scan.Scan() {
					select {
					case w.lines <- append(json.RawMessage(nil), scan.Bytes()...):
					case <-ctx.Done():
					}
				}
				close(w.lines)
				w.done <- cmd.Wait()
			}()
			return w
		}
		next := func(w watched) map[string]json.RawMessage {
			t.Helper()
			select {
			case line, ok := <-w.lines:
				if !ok {
					t.Fatal("watch ended before expected event")
				}
				var data map[string]json.RawMessage
				if err := json.Unmarshal(line, &data); err != nil {
					t.Fatalf("watch did not emit NDJSON: %s: %v", line, err)
				}
				return data
			case <-ctx.Done():
				t.Fatal("timed out waiting for watch event")
			}
			return nil
		}
		a, b := startWatch(ids[0]), startWatch(ids[1])
		for i, w := range []watched{a, b} {
			data := next(w)
			if string(data["receipt"]) != `"snapshot"` || !strings.Contains(string(data["worker_data"]), ids[i]) || strings.Contains(string(data["worker_data"]), ids[1-i]) {
				t.Fatalf("wrong watch snapshot: %s", data)
			}
		}
		if _, err := run("pilot", "--context", a.path, "exit"); err != nil {
			t.Fatal(err)
		}
		select {
		case <-a.done:
		case <-ctx.Done():
			t.Fatal("exit did not stop its watch")
		}
		select {
		case err := <-b.done:
			t.Fatalf("other pilot stopped: %v", err)
		default:
		}
		if err := c.Kill(ids[1]); err != nil {
			t.Fatal(err)
		}
		for {
			data := next(b)
			if string(data["receipt"]) == `"event"` && strings.Contains(string(data["worker_data"]), `"type":"exited"`) {
				break
			}
		}
	})
	if _, err := run("pilot", "--context", start.Context, "exit"); err != nil {
		t.Fatal(err)
	}
}
