package main

import (
	"bytes"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/daemon"
)

// TestDispatchAgentVerb_APermissionRefusedDialDoesNotBlameAMissingDaemon pins the hint for the
// sandboxed case. Inside a Codex workspace-write sandbox without network access, connect() to a
// RUNNING daemon fails "operation not permitted", and the old hint told the agent to start a
// daemon that was already running. A socket the caller may not connect to stands in for it.
func TestDispatchAgentVerb_APermissionRefusedDialDoesNotBlameAMissingDaemon(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores socket permissions")
	}
	dir, err := os.MkdirTemp("", "swdh")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "daemon.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	if err := os.Chmod(sock, 0); err != nil {
		t.Fatal(err)
	}
	t.Setenv(daemon.EnvStateDir, dir)
	t.Setenv(daemon.EnvSocket, sock)

	var stderr bytes.Buffer
	ran := false
	code := dispatchAgentVerb(func([]string, agentClient, io.Writer, io.Writer) int { ran = true; return 0 }, nil, nil, io.Discard, &stderr)
	if code == 0 || ran {
		t.Fatalf("dispatch succeeded against an unconnectable socket (code %d)", code)
	}
	msg := stderr.String()
	if strings.Contains(msg, "no daemon running") || !strings.Contains(msg, "sandbox") {
		t.Errorf("stderr = %q; a permission-refused dial must point at the sandbox/permissions, not at a missing daemon", msg)
	}
}
