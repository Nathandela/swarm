package agyinput

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/persist"
)

func TestNativeAgyModalAdmission(t *testing.T) {
	if os.Getenv("SWARM_NATIVE_ADMISSION_FIXTURE") != "1" {
		t.Skip("set SWARM_NATIVE_ADMISSION_FIXTURE=1 for installed-agy qualification")
	}
	binary, err := exec.LookPath("agy")
	if err != nil {
		t.Fatal(err)
	}
	if selected := os.Getenv("SWARM_NATIVE_AGY_BINARY"); selected != "" {
		binary, err = filepath.Abs(selected)
		if err != nil {
			t.Fatal(err)
		}
	}
	banner, err := exec.Command(binary, "--version").Output()
	if err != nil {
		t.Fatal(err)
	}
	version := strings.TrimSpace(string(banner))
	dir, err := os.MkdirTemp(os.TempDir(), "ai")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	original, err := os.Open(binary)
	if err != nil {
		t.Fatal(err)
	}
	private := filepath.Join(dir, "agy")
	copyFile, err := os.OpenFile(private, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o700)
	if err != nil {
		_ = original.Close()
		t.Fatal(err)
	}
	_, copyErr := io.Copy(copyFile, original)
	closeErr := copyFile.Close()
	_ = original.Close()
	if copyErr != nil || closeErr != nil {
		t.Fatalf("private provider copy: %v %v", copyErr, closeErr)
	}
	binary = private
	swarm := filepath.Join(dir, "swarm")
	goTool := os.Getenv("SWARM_NATIVE_GO")
	if goTool == "" {
		goTool = "go"
	}
	build := exec.Command(goTool, "build", "-o", swarm, "./cmd/swarm")
	build.Dir = "../.."
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build native registration role: %v %s", err, output)
	}
	fingerprint, err := persist.CLIFingerprint(binary)
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{SessionID: "native-agy-probe", Executable: binary, Version: version, Fingerprint: fingerprint, Cwd: dir, BootstrapCommand: BootstrapCommand(swarm, filepath.Join(dir, DescriptorFile)), Env: []string{"HOME=" + filepath.Join(dir, "home"), "PATH=" + os.Getenv("PATH")}}
	b, err := Start(dir, cfg, os.Getpid(), 1)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = b.Close() }()
	if runtime.GOOS == "darwin" {
		guard, err := filepath.Abs("../../scripts/verification/native_fixture_sandbox.py")
		if err != nil {
			t.Fatal(err)
		}
		b.run = func(ctx context.Context, source Source, args ...string) ([]byte, error) {
			_, port, err := net.SplitHostPort(source.Address)
			if err != nil {
				return nil, err
			}
			profile := filepath.Join(dir, "helper.sbpl")
			if output, err := exec.Command("python3", guard, "--root", dir, "--output", profile, "--port", port).CombinedOutput(); err != nil {
				return nil, fmt.Errorf("fixture guard: %v %s", err, output)
			}
			protected := cfg
			protected.Executable = "/usr/bin/sandbox-exec"
			return runNative(ctx, protected, source, append([]string{"-f", profile, cfg.Executable}, args...)...)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 75*time.Second)
	defer cancel()
	harness, err := filepath.Abs("../../scripts/verification/agy_native_admission.py")
	if err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(ctx, "python3", harness, binary, filepath.Join(dir, DescriptorFile))
	command.Env = append(os.Environ(), "PYTHONDONTWRITEBYTECODE=1")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("native agy admission: %v\n%s", err, output)
	}
	t.Log(string(output))
}
