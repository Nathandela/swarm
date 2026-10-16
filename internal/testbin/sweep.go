package testbin

import (
	"os"
	"path/filepath"
	"slices"
	"syscall"
)

// KillProcessesOf SIGKILLs every process other than this one whose executable is one of exes.
// A test run calls it last with the binaries it built (and its own test binary, which agents
// re-exec): shims deliberately outlive their daemon, so nothing else ends a session a test
// forgot to kill. Kernel executable paths are unique per run, so no other run's processes match.
func KillProcessesOf(exes ...string) {
	exes = slices.Clone(exes)
	for i, exe := range exes {
		if resolved, err := filepath.EvalSymlinks(exe); err == nil {
			exes[i] = resolved
		}
	}
	for _, pid := range processIDs() {
		if pid == os.Getpid() {
			continue
		}
		if exe, err := processExecutable(pid); err == nil && exe != "" && slices.Contains(exes, exe) {
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
