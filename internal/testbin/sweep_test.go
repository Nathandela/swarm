package testbin

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestSweepProcess(t *testing.T) {
	if os.Args[len(os.Args)-1] != "testbin-survivor" {
		return
	}
	signal.Ignore(syscall.SIGTERM)
	fmt.Println("ready")
	for {
		time.Sleep(time.Hour)
	}
}

func detachedBinary(t *testing.T, exe, argv0 string) <-chan struct{} {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if err := RegisterExecutable(exe); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(exe, raw, 0o700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "-test.run=^TestSweepProcess$", "--", "testbin-survivor")
	cmd.Args[0] = argv0
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} // detached, as a shim is
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan struct{})
	go func() { _ = cmd.Wait(); close(exited) }()
	t.Cleanup(func() { _ = cmd.Process.Kill(); <-exited })
	ready := make(chan bool, 1)
	go func() {
		line, err := bufio.NewReader(stdout).ReadString('\n')
		ready <- err == nil && line == "ready\n"
	}()
	select {
	case ok := <-ready:
		if !ok {
			t.Fatal("test-owned process exited before its ready signal")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("test-owned process did not become ready")
	}
	return exited
}

func TestKillProcessesOf_KillsADetachedSurvivorOfATestBinary(t *testing.T) {
	exe := filepath.Join(t.TempDir(), "swarm")
	exited := detachedBinary(t, exe, exe)
	KillProcessesOf(exe)

	select {
	case <-exited:
	case <-time.After(5 * time.Second):
		t.Fatal("a process running a test-built binary survived the sweep")
	}
}

// TestKillProcessesOf_MatchesABinaryBuiltUnderASymlinkedDir pins the sweep when TMPDIR is a
// symlink: the kernel names the resolved path, so an unresolved build path matched nothing
// and the sweep was a silent no-op.
func TestKillProcessesOf_MatchesABinaryBuiltUnderASymlinkedDir(t *testing.T) {
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "tmp")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(link, "swarm")
	exited := detachedBinary(t, exe, exe)
	KillProcessesOf(exe)

	select {
	case <-exited:
	case <-time.After(5 * time.Second):
		t.Fatal("a binary built under a symlinked dir survived the sweep")
	}
}

func TestKillProcessesOf_PreservesAnotherExecutableWithTheSameNameAndArgv0(t *testing.T) {
	exe := filepath.Join(t.TempDir(), "swarm")
	exited := detachedBinary(t, exe, exe)
	unrelated := filepath.Join(t.TempDir(), "swarm")
	unrelatedExited := detachedBinary(t, unrelated, exe)

	KillProcessesOf(exe, "")

	select {
	case <-exited:
	case <-time.After(5 * time.Second):
		t.Fatal("the test-owned process survived the sweep")
	}
	select {
	case <-unrelatedExited:
		t.Fatal("the sweep killed an unrelated executable with matching argv0")
	case <-time.After(100 * time.Millisecond):
	}
}

func TestSweep_EndsOwnedProcessesAndRemovesTheirDirectory(t *testing.T) {
	dir := t.TempDir()
	binaries := Binaries{Swarm: filepath.Join(dir, "swarm"), FakeAgent: filepath.Join(dir, "swarm-fake-agent")}
	shimExited := detachedBinary(t, binaries.Swarm, binaries.Swarm)
	agentExited := detachedBinary(t, binaries.FakeAgent, binaries.FakeAgent)

	binaries.Sweep()

	for _, exited := range []<-chan struct{}{shimExited, agentExited} {
		select {
		case <-exited:
		case <-time.After(5 * time.Second):
			t.Fatal("a test-owned process survived Sweep")
		}
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("test binary directory remains after Sweep: %v", err)
	}
}

func TestSweepStopsOwnedChildrenBeforeReturningObservationErrors(t *testing.T) {
	exe := filepath.Join(t.TempDir(), "owned")
	exited := detachedBinary(t, exe, exe)
	canonical, err := filepath.EvalSymlinks(exe)
	if err != nil {
		t.Fatal(err)
	}
	const opaquePID = 1 << 29
	denied := fmt.Errorf("unclassified own-UID executable metadata: %w", syscall.EACCES)
	err = sweepProcesses([]string{canonical}, func() ([]int, error) { pids, err := processIDs(); return append([]int{opaquePID}, pids...), err }, func(pid int) (string, error) {
		if pid == opaquePID {
			return "", denied
		}
		return processExecutable(pid)
	}, syscall.Kill)
	if !errors.Is(err, syscall.EACCES) {
		t.Fatalf("unknown metadata failure hidden: %v", err)
	}
	select {
	case <-exited:
	case <-time.After(5 * time.Second):
		t.Fatal("unknown metadata error prevented verified child cleanup")
	}
}

func TestSignalOwnedPreservesKillFailure(t *testing.T) {
	failure := syscall.EPERM
	err := signalOwned(ownedProcess{pid: 123, start: 456, executable: "/owned/test"}, func(int) (int64, error) { return 456, nil }, func(int) (string, error) { return "/owned/test", nil }, func(int, syscall.Signal) error { return failure })
	if !errors.Is(err, failure) {
		t.Fatalf("owned kill failure hidden: %v", err)
	}
}
