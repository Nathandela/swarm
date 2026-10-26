package hermesinput

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestOwnerFailureStopsOnlyItsOwnProcessGroup(t *testing.T) {
	dir := t.TempDir()
	unrelated := exec.Command("/bin/sh", "-c", "sleep 30")
	unrelated.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := unrelated.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = syscall.Kill(-unrelated.Process.Pid, syscall.SIGKILL); _ = unrelated.Wait() }()
	home := filepath.Join(dir, "home")
	if err := os.Mkdir(home, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "config.yaml"), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	pidFile := filepath.Join(dir, "owner.pid")
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	_, err := Start(ctx, OwnerConfig{Home: home, Cwd: dir, Argv: []string{"/bin/sh", "-c", `echo $$ > "$1"; trap '' TERM; sleep 30`, "owner", pidFile}, Env: os.Environ(), Grace: 20 * time.Millisecond})
	if err == nil {
		t.Fatal("owner without native readiness accepted")
	}
	raw, readErr := os.ReadFile(pidFile)
	if readErr != nil || len(raw) == 0 {
		t.Fatalf("fixture did not start: %q %v", raw, readErr)
	}
	if err := unrelated.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatalf("unrelated native owner stopped: %v", err)
	}
}

func TestNativeCommandNeverReplacesOrStopsGlobalOwner(t *testing.T) {
	argv := GatewayArgv("/opt/hermes")
	if len(argv) != 6 || argv[0] != "/opt/hermes" || argv[1] != "gateway" || argv[2] != "run" || argv[3] != "--no-supervise" || argv[4] != "--force" || argv[5] != "--quiet" {
		t.Fatalf("argv = %v", argv)
	}
}
