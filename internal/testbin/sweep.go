package testbin

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"syscall"
	"time"

	"github.com/Nathandela/swarm/internal/processcontain"
	"github.com/Nathandela/swarm/internal/procstart"
)

type ownedProcess struct {
	pid        int
	start      int64
	executable string
}

// KillProcessesOf stops and waits for processes running these exact executables.
// Executables must be created by this harness run; preexisting external executors
// require fixture-owned custody. It never matches argv, names or old prefixes.
func KillProcessesOf(exes ...string) error {
	exes = slices.Clone(exes)
	for i, exe := range exes {
		if resolved, err := filepath.EvalSymlinks(exe); err == nil {
			exes[i] = resolved
		} else if parent, err := filepath.EvalSymlinks(filepath.Dir(exe)); err == nil {
			exes[i] = filepath.Join(parent, filepath.Base(exe))
		}
	}
	return killCanonicalProcesses(exes...)
}

func killCanonicalProcesses(exes ...string) error {
	return sweepProcesses(exes, processIDs, processExecutable, procstart.StartTime, syscall.Kill)
}

func sweepProcesses(exes []string, enumerate func() ([]int, error), executable func(int) (string, error), startTime func(int) (int64, error), kill func(int, syscall.Signal) error) error {
	var harnessStart int64
	// Linux birth ticks are ordered; Darwin wall-clock identity tokens are not.
	if runtime.GOOS == "linux" {
		if start, err := startTime(os.Getpid()); err == nil && start > 0 {
			harnessStart = start
		}
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		pids, err := enumerate()
		var cleanupErr error
		if err != nil {
			cleanupErr = fmt.Errorf("enumerate owned processes: %w", err)
		}
		var owned []ownedProcess
		for _, pid := range pids {
			if pid == os.Getpid() {
				continue
			}
			exe, err := executable(pid)
			if gone(err) {
				continue
			}
			if err != nil {
				if errors.Is(err, os.ErrPermission) && harnessStart > 0 {
					if start, startErr := startTime(pid); startErr == nil && start > 0 && start < harnessStart {
						continue // This process cannot be a descendant of this harness.
					}
				}
				cleanupErr = errors.Join(cleanupErr, fmt.Errorf("executable for pid %d: %w", pid, err))
				continue
			}
			if exe == "" || !slices.Contains(exes, exe) {
				continue
			}
			start, err := startTime(pid)
			if gone(err) {
				continue
			}
			if err != nil {
				cleanupErr = errors.Join(cleanupErr, fmt.Errorf("start time for pid %d: %w", pid, err))
				continue
			}
			if start <= 0 {
				cleanupErr = errors.Join(cleanupErr, fmt.Errorf("unknown process identity for pid %d", pid))
				continue
			}
			if (processcontain.Identity{PID: pid, StartTime: start}).Alive() {
				owned = append(owned, ownedProcess{pid, start, exe})
			}
		}
		if len(owned) == 0 {
			return cleanupErr
		}
		for _, process := range owned {
			cleanupErr = errors.Join(cleanupErr, signalOwned(process, startTime, executable, kill))
		}
		for _, process := range owned {
			for (processcontain.Identity{PID: process.pid, StartTime: process.start}).Alive() {
				if time.Now().After(deadline) {
					cleanupErr = errors.Join(cleanupErr, fmt.Errorf("owned process %d did not stop", process.pid))
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
		}
		if cleanupErr != nil {
			return cleanupErr
		}
		if time.Now().After(deadline) {
			return errors.New("owned processes kept appearing during cleanup")
		}
	}
}

func gone(err error) bool { return errors.Is(err, syscall.ESRCH) || errors.Is(err, os.ErrNotExist) }

func signalOwned(process ownedProcess, startTime func(int) (int64, error), executable func(int) (string, error), kill func(int, syscall.Signal) error) error {
	start, err := startTime(process.pid)
	if gone(err) {
		return nil
	}
	if err != nil {
		return err
	}
	exe, err := executable(process.pid)
	if gone(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if start != process.start || exe != process.executable {
		return nil
	}
	// The identity check is portable; PID reuse between this check and kill remains possible.
	if err := kill(process.pid, syscall.SIGKILL); err != nil && !gone(err) {
		return fmt.Errorf("kill owned pid %d: %w", process.pid, err)
	}
	return nil
}

// Sweep removes built binaries only after all verified processes have stopped.
func (b *Binaries) Sweep() error {
	if b.Swarm == "" {
		return nil
	}
	if err := KillProcessesOf(b.Swarm, b.FakeAgent); err != nil {
		return err
	}
	return os.RemoveAll(filepath.Dir(b.Swarm))
}
