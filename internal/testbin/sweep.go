package testbin

import (
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"syscall"
)

// KillProcessesOf SIGKILLs every process other than this one whose executable is one of exes.
// A test run calls it last with the binaries it built (and its own test binary, which agents
// re-exec): shims deliberately outlive their daemon, so nothing else ends a session a test
// forgot to kill. Exe paths are unique per run, so no other run's processes match. Linux only:
// without /proc it does nothing.
func KillProcessesOf(exes ...string) {
	entries, _ := os.ReadDir("/proc")
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid == os.Getpid() {
			continue
		}
		if exe, err := os.Readlink(filepath.Join("/proc", e.Name(), "exe")); err == nil && slices.Contains(exes, exe) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	}
}

// Sweep ends a run that used b: it kills every process still running b's binaries and removes
// the directory Build created. Call it from TestMain after m.Run.
func (b *Binaries) Sweep() {
	if b.Swarm == "" {
		return
	}
	KillProcessesOf(b.Swarm, b.FakeAgent)
	_ = os.RemoveAll(filepath.Dir(b.Swarm))
}
