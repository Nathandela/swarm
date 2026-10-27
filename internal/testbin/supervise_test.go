package testbin

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/processcontain"
	"github.com/Nathandela/swarm/internal/procstart"
)

func TestMain(m *testing.M) {
	if code, parent := Supervise(); parent {
		os.Exit(code)
	}
	os.Exit(m.Run())
}

type survivorReport struct {
	Root  string
	PID   int
	Start int64
}

func TestSupervisorFixture(t *testing.T) {
	mode := os.Getenv("TESTBIN_SCENARIO")
	if mode == "" {
		return
	}
	exe, _ := os.Executable()
	if mode == "registered" {
		path := filepath.Join(os.TempDir(), "registered")
		if err := RegisterExecutable(path); err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(exe)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, raw, 0700); err != nil {
			t.Fatal(err)
		}
		exe = path
	}
	cmd := exec.Command(exe, "-test.run=^TestSweepProcess$", "--", "testbin-survivor")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	if line, err := bufio.NewReader(pipe).ReadString('\n'); err != nil || line != "ready\n" {
		t.Fatalf("readiness: %q %v", line, err)
	}
	_ = pipe.Close()
	start, err := procstart.StartTime(cmd.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	report, _ := json.Marshal(survivorReport{os.TempDir(), cmd.Process.Pid, start})
	if err := os.WriteFile(os.Getenv("TESTBIN_REPORT"), report, 0600); err != nil {
		t.Fatal(err)
	}
	if mode == "cleanupfail" || mode == "paniccleanupfail" {
		retained := filepath.Join(os.TempDir(), "retained")
		if err := os.Mkdir(retained, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(retained, "evidence"), []byte("keep"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(retained, 0500); err != nil {
			t.Fatal(err)
		}
	}
	switch mode {
	case "panic", "paniccleanupfail":
		panic("deliberate child panic")
	case "timeout":
		time.Sleep(time.Hour)
	}
}

func TestSuperviseCleansAfterChildExit(t *testing.T) {
	for _, mode := range []string{"normal", "registered", "panic", "timeout", "cleanupfail", "paniccleanupfail"} {
		t.Run(mode, func(t *testing.T) {
			if os.Geteuid() == 0 && strings.Contains(mode, "cleanupfail") {
				t.Skip("root bypasses directory permission denial; injected cleanup failures are tested separately")
			}
			reportPath := filepath.Join(t.TempDir(), "report")
			exe, _ := os.Executable()
			// Go's watchdog includes fixture setup and child readiness; only the
			// timeout scenario should intentionally exhaust it after reporting ownership.
			timeout := "30s"
			if mode == "timeout" {
				timeout = "5s"
			}
			cmd := exec.Command(exe, "-test.run=^TestSupervisorFixture$", "-test.timeout="+timeout)
			for _, env := range os.Environ() {
				if !strings.HasPrefix(env, runRootEnv+"=") && !strings.HasPrefix(env, "TESTBIN_SCENARIO=") {
					cmd.Env = append(cmd.Env, env)
				}
			}
			cmd.Env = append(cmd.Env, "TESTBIN_SCENARIO="+mode, "TESTBIN_REPORT="+reportPath)
			output, err := cmd.CombinedOutput()
			raw, readErr := os.ReadFile(reportPath)
			if readErr != nil {
				t.Fatalf("report: %v, command: %v\n%s", readErr, err, output)
			}
			var report survivorReport
			if err := json.Unmarshal(raw, &report); err != nil {
				t.Fatal(err)
			}
			parent, pathErr := filepath.EvalSymlinks(filepath.Dir(report.Root))
			if pathErr != nil || report.Root != filepath.Join(parent, filepath.Base(report.Root)) {
				t.Errorf("scratch root uses an alias rejected by private native storage: %s (%v)", report.Root, pathErr)
			}
			identity := processcontain.Identity{PID: report.PID, StartTime: report.Start}
			t.Cleanup(func() {
				if identity.Alive() {
					_ = syscall.Kill(report.PID, syscall.SIGKILL)
				}
				_ = os.Chmod(filepath.Join(report.Root, "retained"), 0700)
				_ = os.RemoveAll(report.Root)
			})
			success := mode == "normal" || mode == "registered"
			if success && err != nil {
				t.Fatalf("normal status: %v\n%s", err, output)
			}
			if !success && err == nil {
				t.Fatal("lost failing child exit status")
			}
			if mode == "timeout" && !strings.Contains(string(output), "panic: test timed out after "+timeout) {
				t.Errorf("child did not fail through Go's timeout watchdog: %v\n%s", err, output)
			}
			if identity.Alive() {
				t.Errorf("%s left detached owned process %d alive", mode, report.PID)
			}
			if strings.Contains(mode, "cleanupfail") {
				if _, err := os.Stat(report.Root); err != nil {
					t.Fatalf("lost failed cleanup evidence: %v", err)
				}
				if !strings.Contains(string(output), "retained scratch: "+report.Root) {
					t.Errorf("cleanup failure hidden: %s", output)
				}
			} else if _, err := os.Stat(report.Root); !os.IsNotExist(err) {
				t.Errorf("%s retained successful cleanup root %s: %v", mode, report.Root, err)
			}
			if !success {
				expected := 2
				if mode == "cleanupfail" {
					expected = 1
				}
				exit, ok := err.(*exec.ExitError)
				if !ok || exit.ExitCode() != expected {
					t.Errorf("child status: %v; wanted %d", err, expected)
				}
			}
		})
	}
}

func TestSignalOwnedRejectsChangedIdentity(t *testing.T) {
	for _, changed := range []string{"start", "executable"} {
		t.Run(changed, func(t *testing.T) {
			process := ownedProcess{pid: 123, start: 456, executable: "/owned/test"}
			start := process.start
			exe := process.executable
			if changed == "start" {
				start++
			} else {
				exe = "/unrelated/test"
			}
			err := signalOwned(process, func(int) (int64, error) { return start, nil }, func(int) (string, error) { return exe, nil }, func(int, syscall.Signal) error { t.Fatal("signaled changed identity"); return nil })
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCleanupFailureRetainsScratch(t *testing.T) {
	for _, stage := range []string{"enumerate", "kill", "remove"} {
		t.Run(stage, func(t *testing.T) {
			root := t.TempDir()
			marker := filepath.Join(root, "evidence")
			if err := os.WriteFile(marker, []byte("keep"), 0600); err != nil {
				t.Fatal(err)
			}
			injected := errors.New(stage + " failed")
			removed := false
			err := cleanupRun(root, nil, func(...string) error {
				if stage != "remove" {
					return injected
				}
				return nil
			}, func(string) error { removed = true; return injected })
			if !errors.Is(err, injected) {
				t.Fatalf("failure hidden: %v", err)
			}
			if stage != "remove" && removed {
				t.Fatal("removed root before process cleanup succeeded")
			}
			if _, err := os.Stat(marker); err != nil {
				t.Fatalf("lost cleanup evidence: %v", err)
			}
		})
	}
}

func TestRegisterExecutableRejectsOutsideRun(t *testing.T) {
	if os.Getenv(runRootEnv) == "" {
		t.Fatal("test not supervised")
	}
	if err := RegisterExecutable("/bin/sh"); err == nil {
		t.Fatal("accepted unrelated executable")
	}
	path := filepath.Join(t.TempDir(), "owned")
	if err := RegisterExecutable(path); err != nil {
		t.Fatal(err)
	}
	paths, err := registeredExecutables(os.Getenv(runRootEnv))
	if err != nil {
		t.Fatal(err)
	}
	realParent, err := filepath.EvalSymlinks(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(realParent, filepath.Base(path))
	found := false
	for _, got := range paths {
		if got == want {
			found = true
		}
	}
	if !found {
		t.Fatalf("executable absent from parent manifest: %s", want)
	}
}

func TestManifestRejectsSymlinkAndNonregularFiles(t *testing.T) {
	for _, kind := range []string{"symlink", "hardlink", "fifo", "oversized"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, executableManifest)
			switch kind {
			case "symlink", "hardlink":
				target := filepath.Join(t.TempDir(), "empty")
				if err := os.WriteFile(target, nil, 0600); err != nil {
					t.Fatal(err)
				}
				var err error
				if kind == "symlink" {
					err = os.Symlink(target, path)
				} else {
					err = os.Link(target, path)
				}
				if err != nil {
					t.Fatal(err)
				}
			case "fifo":
				if err := syscall.Mkfifo(path, 0600); err != nil {
					t.Fatal(err)
				}
			case "oversized":
				f, err := os.Create(path)
				if err != nil {
					t.Fatal(err)
				}
				if err = f.Truncate(1<<20 + 1); err != nil {
					_ = f.Close()
					t.Fatal(err)
				}
				if err = f.Close(); err != nil {
					t.Fatal(err)
				}
			}
			done := make(chan error, 1)
			go func() { _, err := registeredExecutables(root); done <- err }()
			select {
			case err := <-done:
				if err == nil {
					t.Fatalf("accepted unsafe %s manifest", kind)
				}
			case <-time.After(time.Second):
				if kind == "fifo" {
					fd, err := syscall.Open(path, syscall.O_WRONLY|syscall.O_NONBLOCK, 0)
					if err == nil {
						_ = syscall.Close(fd)
					}
				}
				t.Fatalf("blocked on %s manifest", kind)
			}
		})
	}
}

func TestRegisterExecutableNeverWritesThroughManifestAlias(t *testing.T) {
	root := t.TempDir()
	t.Setenv(runRootEnv, root)
	target := filepath.Join(t.TempDir(), "unrelated")
	if err := os.WriteFile(target, []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(root, executableManifest)); err != nil {
		t.Fatal(err)
	}
	if err := RegisterExecutable(filepath.Join(root, "test-helper")); err == nil {
		t.Error("registration accepted aliased manifest")
	}
	got, err := os.ReadFile(target)
	if err != nil || string(got) != "preserve" {
		t.Fatalf("registration modified outside file: %q %v", got, err)
	}
}

func TestManifestRejectsExternalExecutable(t *testing.T) {
	for _, mode := range []string{"absolute", "symlink"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			path := "/bin/sh"
			if mode == "symlink" {
				path = filepath.Join(root, "owned")
				if err := os.Symlink("/bin/sh", path); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(filepath.Join(root, executableManifest), []byte(path+"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := registeredExecutables(root); err == nil {
				t.Fatal("trusted executable outside run root")
			}
		})
	}
}

func TestSupervisedChildRequiresOwnedRootAndExecutable(t *testing.T) {
	if !supervisedChild(os.Getenv(runRootEnv)) {
		t.Fatal("current copied executable did not retain supervision")
	}
	borrowed := t.TempDir()
	if supervisedChild(borrowed) {
		t.Fatal("root marker alone disabled supervision")
	}
}

func TestSupervisorParentTerminationStillCleans(t *testing.T) {
	reportPath := filepath.Join(t.TempDir(), "report")
	exe, _ := os.Executable()
	cmd := exec.Command(exe, "-test.run=^TestSupervisorFixture$", "-test.timeout=30s")
	for _, env := range os.Environ() {
		if !strings.HasPrefix(env, runRootEnv+"=") {
			cmd.Env = append(cmd.Env, env)
		}
	}
	cmd.Env = append(cmd.Env, "TESTBIN_SCENARIO=timeout", "TESTBIN_REPORT="+reportPath)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	waitDone := make(chan struct{})
	go func() { defer close(waitDone); exited <- cmd.Wait() }()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		select {
		case <-waitDone:
		case <-time.After(5 * time.Second):
		}
	})
	var report survivorReport
	deadline := time.Now().Add(5 * time.Second)
	for {
		raw, err := os.ReadFile(reportPath)
		if err == nil && json.Unmarshal(raw, &report) == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("child did not report readiness")
		}
		time.Sleep(10 * time.Millisecond)
	}
	identity := processcontain.Identity{PID: report.PID, StartTime: report.Start}
	t.Cleanup(func() {
		if identity.Alive() {
			_ = syscall.Kill(report.PID, syscall.SIGKILL)
		}
		_ = os.RemoveAll(report.Root)
	})
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-exited:
		if err == nil {
			t.Fatal("lost terminated child status")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("supervisor did not finish")
	}
	if identity.Alive() {
		t.Error("parent TERM left owned detached survivor")
	}
	if _, err := os.Stat(report.Root); !os.IsNotExist(err) {
		t.Errorf("parent TERM retained root: %v", err)
	}
}
