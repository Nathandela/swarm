package protocol

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// E6.6 — the daemon RE-VALIDATES every client-supplied field server-side (P-6),
// regardless of client checks. Each negative asserts BOTH that the op is refused
// AND that the bad value never reached the DaemonAPI (the server rejected before
// forwarding). The frozen surface is the low-reversibility one; hostile input
// must not cross it.

// validLaunch returns a launch request that would succeed, for one-field mutation.
func validLaunch(t *testing.T) LaunchReq {
	t.Helper()
	return LaunchReq{Agent: "claude", Cwd: t.TempDir(), Cols: 80, Rows: 24}
}

func TestRevalidate_LaunchRejectsNonexistentCwd(t *testing.T) {
	stub := newStubDaemon()
	c := dialClient(t, serveStub(t, stub), nil)

	req := validLaunch(t)
	req.Cwd = filepath.Join(t.TempDir(), "does", "not", "exist")
	if _, err := c.Launch(req); err == nil {
		t.Fatalf("Launch with nonexistent cwd: err = nil, want rejection (L-3/P-6)")
	}
	if len(stub.launchSpecs()) != 0 {
		t.Fatalf("nonexistent-cwd launch was forwarded to the daemon: %v", stub.launchSpecs())
	}
}

func TestRevalidate_LaunchRejectsCwdThatIsNotADirectory(t *testing.T) {
	stub := newStubDaemon()
	c := dialClient(t, serveStub(t, stub), nil)

	file := filepath.Join(t.TempDir(), "afile")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	req := validLaunch(t)
	req.Cwd = file
	if _, err := c.Launch(req); err == nil {
		t.Fatalf("Launch with a file (not dir) cwd: err = nil, want rejection")
	}
	if len(stub.launchSpecs()) != 0 {
		t.Fatalf("not-a-directory cwd launch was forwarded: %v", stub.launchSpecs())
	}
}

func TestRevalidate_LaunchRejectsUnknownOrEmptyAgent(t *testing.T) {
	for _, agent := range []string{"", strings.Repeat("x", 10<<10)} {
		stub := newStubDaemon()
		c := dialClient(t, serveStub(t, stub), nil)
		req := validLaunch(t)
		req.Agent = agent
		if _, err := c.Launch(req); err == nil {
			t.Fatalf("Launch with invalid agent (len=%d): err = nil, want rejection", len(agent))
		}
		if len(stub.launchSpecs()) != 0 {
			t.Fatalf("invalid-agent launch was forwarded: %v", stub.launchSpecs())
		}
	}
}

func TestRevalidate_LaunchRejectsOversizedOptions(t *testing.T) {
	stub := newStubDaemon()
	c := dialClient(t, serveStub(t, stub), nil)

	req := validLaunch(t)
	// A single option value far larger than any real launch option. Fits within a
	// wire frame (so this exercises SERVER policy, not the envelope cap), but must
	// exceed the server's per-field option bound.
	req.Options = map[string]string{"blob": strings.Repeat("A", 128<<10)}
	if _, err := c.Launch(req); err == nil {
		t.Fatalf("Launch with oversized options: err = nil, want rejection (P-6)")
	}
	if len(stub.launchSpecs()) != 0 {
		t.Fatalf("oversized-options launch was forwarded: %v", stub.launchSpecs())
	}
}

func TestRevalidate_LaunchRejectsHugeDimensions(t *testing.T) {
	stub := newStubDaemon()
	c := dialClient(t, serveStub(t, stub), nil)

	req := validLaunch(t)
	req.Cols, req.Rows = 100000, 100000
	if _, err := c.Launch(req); err == nil {
		t.Fatalf("Launch with absurd cols/rows: err = nil, want rejection (panic/OOM guard, P-6)")
	}
	if len(stub.launchSpecs()) != 0 {
		t.Fatalf("huge-dimensions launch was forwarded: %v", stub.launchSpecs())
	}
}

func TestRevalidate_SessionOpsRejectBadIDs(t *testing.T) {
	// Each bad id, sent to a session-scoped op, must be refused before the daemon
	// is touched. Traversal, empty, and control-char ids are all path hazards.
	bad := []string{
		"",             // no namespace at all
		"/",            // empty endpoint and local
		"ep/",          // empty local
		"ep/..",        // traversal
		"ep/../../etc", // traversal
		"ep/a\x00b",    // NUL byte
	}
	for _, id := range bad {
		stub := oneRunningSession()
		c := dialClient(t, serveStub(t, stub), nil)

		if err := c.Kill(id); err == nil {
			t.Errorf("Kill(%q): err = nil, want rejection", id)
		}
		if err := c.Delete(id); err == nil {
			t.Errorf("Delete(%q): err = nil, want rejection", id)
		}
		if _, err := c.Attach(id); err == nil {
			t.Errorf("Attach(%q): err = nil, want rejection", id)
		}
		if k := stub.killedIDs(); len(k) != 0 {
			t.Errorf("Kill(%q) forwarded a bad id to the daemon: %v", id, k)
		}
		if d := stub.deletedIDs(); len(d) != 0 {
			t.Errorf("Delete(%q) forwarded a bad id to the daemon: %v", id, d)
		}
		if stub.streamCount() != 0 {
			t.Errorf("Attach(%q) opened a stream for a bad id", id)
		}
	}
}

func TestRevalidate_ResizeOutOfRangeNotForwarded(t *testing.T) {
	stub := oneRunningSession()
	c := dialClient(t, serveStub(t, stub), nil)

	a, err := c.Attach(onlyViewID(t, c))
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	// A resize far outside the shim's accepted range must be dropped, never
	// forwarded (the shim would otherwise reject it, but P-6 says re-validate).
	_ = a.Resize(100000, 100000)

	st := stub.lastStream()
	// Give the server a beat to (not) forward it, then assert nothing arrived.
	if waitResize(st, 300) {
		for _, rz := range st.resizesCopy() {
			if rz[0] >= 100000 || rz[1] >= 100000 {
				t.Fatalf("out-of-range resize %v was forwarded to the shim — violates P-6", rz)
			}
		}
	}
}

// waitResize reports whether any resize is recorded within ms milliseconds.
func waitResize(st *stubStream, ms int) bool {
	for i := 0; i < ms/10; i++ {
		if st.resizeCount() > 0 {
			return true
		}
		sleepMS(10)
	}
	return st.resizeCount() > 0
}
