package testbin

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/procstart"
	"golang.org/x/sys/unix"
)

func TestLinuxOpaqueProcess(t *testing.T) {
	if os.Getenv("TESTBIN_OPAQUE") != "1" {
		return
	}
	if err := unix.Prctl(unix.PR_SET_DUMPABLE, 0, 0, 0, 0); err != nil {
		t.Fatal(err)
	}
	fmt.Println("ready")
	for {
		time.Sleep(time.Hour)
	}
}

func TestLinuxOpaqueUnrelatedDoesNotBlockOwnedCleanup(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("requires a nonroot reader to exercise protected proc executable metadata")
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	opaque := exec.Command(self, "-test.run=^TestLinuxOpaqueProcess$")
	// The generic scanner cannot recover this deliberately opaque fixture after a timeout.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	opaque.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
	opaque.Env = append(os.Environ(), "TESTBIN_OPAQUE=1")
	stdout, err := opaque.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := opaque.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan struct{})
	go func() { _ = opaque.Wait(); close(exited) }()
	t.Cleanup(func() { _ = opaque.Process.Kill(); <-exited })
	ready := make(chan bool, 1)
	go func() {
		line, err := bufio.NewReader(stdout).ReadString('\n')
		ready <- err == nil && line == "ready\n"
	}()
	select {
	case ok := <-ready:
		if !ok {
			t.Fatal("opaque process did not become ready")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("opaque process readiness timed out")
	}
	path := filepath.Join("/proc", strconv.Itoa(opaque.Process.Pid), "exe")
	if _, err := os.Readlink(path); !os.IsPermission(err) {
		t.Fatalf("fixture executable must deny introspection: %v", err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid == uint32(os.Geteuid()) {
		t.Fatal("fixture executable metadata did not enter privileged custody")
	}
	// Reuse the real normal/panic/timeout and registered-survivor checks while
	// this unrelated same-UID process stays alive and refuses executable reads.
	cmd := exec.Command(self, "-test.run=^TestSuperviseCleansAfterChildExit$", "-test.timeout=30s")
	for _, env := range os.Environ() {
		if !strings.HasPrefix(env, runRootEnv+"=") {
			cmd.Env = append(cmd.Env, env)
		}
	}
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("opaque unrelated process blocked owned cleanup: %v\n%s", err, output)
	}
	select {
	case <-exited:
		t.Fatal("supervisor killed unrelated opaque process")
	default:
	}
}

func TestLinuxOlderDeniedProcessDoesNotPreventOwnedChildCleanup(t *testing.T) {
	exe := filepath.Join(t.TempDir(), "owned")
	exited := detachedBinary(t, exe, exe)
	canonical, err := filepath.EvalSymlinks(exe)
	if err != nil {
		t.Fatal(err)
	}
	const opaquePID = 1 << 29
	err = sweepProcesses([]string{canonical}, func() ([]int, error) { pids, e := processIDs(); return append(pids, opaquePID), e }, func(pid int) (string, error) {
		if pid == opaquePID {
			return "", syscall.EACCES
		}
		return processExecutable(pid)
	}, func(pid int) (int64, error) {
		if pid == opaquePID {
			return 1, nil
		}
		return procstart.StartTime(pid)
	}, func(pid int, sig syscall.Signal) error {
		if pid == opaquePID {
			t.Fatal("older unrelated process signaled")
		}
		return syscall.Kill(pid, sig)
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-exited:
	case <-time.After(5 * time.Second):
		t.Fatal("verified owned child survived")
	}
}
