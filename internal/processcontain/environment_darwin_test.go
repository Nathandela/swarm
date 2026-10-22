//go:build darwin

package processcontain

import (
	"encoding/binary"
	"errors"
	"golang.org/x/sys/unix"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestDarwinAuthEnvironmentIgnoresArgumentsAndMatchesExactHomes(t *testing.T) {
	raw := make([]byte, 4)
	binary.LittleEndian.PutUint32(raw, 2)
	raw = append(raw, []byte("/synthetic/codex\x00\x00codex\x00CODEX_HOME=/protected\x00CODEX_HOME=/unrelated\x00SWARM_SESSION_ID=other\x00\x00")...)
	got, err := darwinAuthEnvironmentMatches(raw, []string{"/protected"}, nil)
	if err != nil || got {
		t.Fatalf("matched argv instead of environment: %v %v", got, err)
	}
	got, err = darwinAuthEnvironmentMatches(raw, []string{"/unrelated"}, nil)
	if err != nil || !got {
		t.Fatalf("did not match auth-home env: %v %v", got, err)
	}
	got, err = darwinAuthEnvironmentMatches(raw, nil, []string{"other"})
	if err != nil || !got {
		t.Fatalf("did not match initial-owner detached session: %v %v", got, err)
	}
	for _, invalid := range [][]byte{nil, {1, 0, 0, 0}, append([]byte{255, 255, 255, 127}, []byte("x\x00")...)} {
		if _, err = darwinAuthEnvironmentMatches(invalid, nil, nil); err == nil {
			t.Fatal("malformed native environment accepted")
		}
	}
}
func TestDarwinWriterInventoryFindsEscapedSameProviderChild(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	pidPath := filepath.Join(t.TempDir(), "escaped.pid")
	session := "synthetic-native-writer-" + strconv.Itoa(os.Getpid())
	launcher := exec.Command(executable, "-test.run=^TestDarwinLoginWriterHelper$")
	launcher.Env = append(os.Environ(), "SWARM_WRITER_TEST=launcher", "SWARM_WRITER_PID_FILE="+pidPath, "CODEX_HOME="+home, "SWARM_SESSION_ID="+session)
	if out, err := launcher.CombinedOutput(); err != nil {
		t.Fatalf("launch isolated helper: %v %s", err, out)
	}
	raw, err := os.ReadFile(pidPath)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || pid <= 0 {
		t.Fatal("invalid owned child PID")
	}
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	// The launcher has exited; its child is in another session/process group.
	if writers, err := EnvironmentWriters([]string{home}, nil); err != nil || len(writers) == 0 {
		raw, readErr := unix.SysctlRaw("kern.procargs2", pid)
		proc, procErr := unix.SysctlKinfoProc("kern.proc.pid", pid)
		matches, parseErr := darwinAuthEnvironmentMatches(raw, []string{home}, nil)
		uid := uint32(0)
		state := int8(0)
		if proc != nil {
			uid = proc.Eproc.Ucred.Uid
			state = proc.Proc.P_stat
		}
		t.Fatalf("escaped native auth writer was invisible: uid=%d state=%d readErr=%v procErr=%v bytes=%d matched=%v parseErr=%v inventoryErr=%v", uid, state, readErr, procErr, len(raw), matches, parseErr, err)
	}
	if writers, err := EnvironmentWriters(nil, []string{session}); err != nil || len(writers) == 0 {
		t.Fatal("owner-mode escaped writer lost its session provenance")
	}
	if err = syscall.Kill(pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		writers, scanErr := EnvironmentWriters([]string{home}, []string{session})
		if scanErr == nil && len(writers) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("remaining native writers: %+v error: %v", writers, scanErr)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
func TestDarwinLoginWriterHelper(t *testing.T) {
	switch os.Getenv("SWARM_WRITER_TEST") {
	case "launcher":
		exe, err := os.Executable()
		if err != nil {
			os.Exit(2)
		}
		cmd := exec.Command(exe, "-test.run=^TestDarwinLoginWriterHelper$")
		cmd.Env = append(os.Environ(), "SWARM_WRITER_TEST=escaped")
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if err = cmd.Start(); err != nil {
			os.Exit(3)
		}
		if err = os.WriteFile(os.Getenv("SWARM_WRITER_PID_FILE"), []byte(strconv.Itoa(cmd.Process.Pid)), 0600); err != nil {
			os.Exit(4)
		}
		os.Exit(0)
	case "escaped":
		time.Sleep(time.Minute)
		os.Exit(0)
	}
}
