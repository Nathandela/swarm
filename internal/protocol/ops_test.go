package protocol

import (
	"testing"

	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/status"
)

// Happy-path control ops: List/Launch/Kill/Delete forward to the DaemonAPI with
// the client's fields, and the server applies persist.FilterEnv to the launch
// env (S-6). These pin the positive contract that the E6.6 negatives guard.

func TestOps_ListReturnsStampedViews(t *testing.T) {
	stub := newStubDaemon()
	stub.setMetas(
		persist.Meta{ID: "a", AgentType: "claude", Cwd: "/x", Status: status.Status{Process: status.ProcessRunning, Turn: status.TurnActive}},
		persist.Meta{ID: "b", AgentType: "codex", Cwd: "/y", Status: status.Status{Process: status.ProcessExited}},
	)
	sock := serveStub(t, stub)
	c := dialClient(t, sock, nil)

	views, err := c.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(views) != 2 {
		t.Fatalf("List returned %d views, want 2", len(views))
	}
	for _, v := range views {
		if v.EndpointID != c.EndpointID() {
			t.Errorf("view %q endpoint id = %q, want %q", v.ID, v.EndpointID, c.EndpointID())
		}
		ep, local, ok := ParseID(v.ID)
		if !ok || ep != c.EndpointID() || local == "" {
			t.Errorf("view id %q is not namespaced as <endpoint>/<local> for this endpoint", v.ID)
		}
	}
}

func TestOps_LaunchForwardsAndFiltersEnv(t *testing.T) {
	stub := newStubDaemon()
	sock := serveStub(t, stub)
	c := dialClient(t, sock, nil)

	id, _, err := c.Launch(LaunchReq{
		Agent:   "claude",
		Cwd:     t.TempDir(),
		Options: map[string]string{"model": "opus"},
		// A poisoned env: an injection vector plus an allowlisted credential.
		Env:  []string{"LD_PRELOAD=/evil.so", "ANTHROPIC_API_KEY=sk-test", "PATH=/usr/bin"},
		Cols: 80, Rows: 24,
	})
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	if id == "" {
		t.Fatalf("Launch returned an empty session id")
	}

	specs := stub.launchSpecs()
	if len(specs) != 1 {
		t.Fatalf("DaemonAPI.Launch called %d times, want 1", len(specs))
	}
	got := envKeys(specs[0].ClientEnv)
	if got["LD_PRELOAD"] {
		t.Errorf("launch forwarded a non-allowlisted env var LD_PRELOAD: %v — server must FilterEnv (S-6)", specs[0].ClientEnv)
	}
	if !got["ANTHROPIC_API_KEY"] || !got["PATH"] {
		t.Errorf("launch dropped allowlisted env vars: %v", specs[0].ClientEnv)
	}
	if specs[0].AgentType != "claude" {
		t.Errorf("launch agent type = %q, want claude", specs[0].AgentType)
	}
}

func TestOps_KillAndDeleteForwardLocalID(t *testing.T) {
	stub := oneRunningSession()
	sock := serveStub(t, stub)
	c := dialClient(t, sock, nil)
	id := onlyViewID(t, c)

	if err := c.Kill(id); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	if got := stub.killedIDs(); len(got) != 1 || got[0] != "sess1" {
		t.Fatalf("DaemonAPI.Kill received %v, want [sess1] (the de-namespaced local id)", got)
	}
	if err := c.Delete(id); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if got := stub.deletedIDs(); len(got) != 1 || got[0] != "sess1" {
		t.Fatalf("DaemonAPI.Delete received %v, want [sess1]", got)
	}
}

// envKeys returns the set of variable names present in a KEY=VALUE slice.
func envKeys(env []string) map[string]bool {
	out := map[string]bool{}
	for _, kv := range env {
		if i := indexByte(kv, '='); i >= 0 {
			out[kv[:i]] = true
		} else {
			out[kv] = true
		}
	}
	return out
}

func indexByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}
