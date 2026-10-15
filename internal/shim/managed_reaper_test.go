//go:build linux

package shim

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/processcontain"
)

// procState returns the /proc state letter and ppid of pid, or "" once the pid is gone.
func procState(pid int) (string, int) {
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return "", 0
	}
	fields := strings.Fields(string(raw)[strings.LastIndexByte(string(raw), ')')+1:])
	var ppid int
	_, _ = fmt.Sscan(fields[1], &ppid)
	return fields[0], ppid
}

// TestManagedShimReapsAnAdoptedOrphanWhileTheSessionRuns pins zombie reaping during a managed
// session's life. The managed shim is a child subreaper, so every double-forked or setsid
// descendant whose parent exits is re-parented to it; nothing waits for them until the shim's
// final ContainAndReap. Live: about 48 000 zombies, 41 818 under one week-old shim.
func TestManagedShimReapsAnAdoptedOrphanWhileTheSessionRuns(t *testing.T) {
	cfg, cmd, done, children := managedFixture(t, false)
	writer := children[0]
	// The intermediate fork exits asynchronously; wait for the re-parenting.
	for end := time.Now().Add(10 * time.Second); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		if _, ppid := procState(writer.PID); ppid == cmd.Process.Pid {
			break
		}
	}
	if state, ppid := procState(writer.PID); state == "" || ppid != cmd.Process.Pid {
		t.Fatalf("fixture writer %d: state %q ppid %d, want adopted by the managed shim %d", writer.PID, state, ppid, cmd.Process.Pid)
	}
	if err := processcontain.SignalIdentity(writer, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	state := ""
	for time.Now().Before(deadline) {
		if state, _ = procState(writer.PID); state == "" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	// Let the session end normally so cleanup proves the agent was not reaped out from under Run.
	if err := os.WriteFile(filepath.Join(cfg.AccountStateRoot, "..", "native", "finish"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("managed shim exited %v after reaping; the agent's own exit status must survive", err)
		}
	case <-time.After(8 * time.Second):
		t.Error("managed shim did not finish")
	}
	if state != "" {
		t.Errorf("adopted orphan %d still in state %q 10 s after it died; the running managed shim never reaps", writer.PID, state)
	}
}
