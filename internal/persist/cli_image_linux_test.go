//go:build linux

package persist

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/procstart"
)

func TestCLIImageChild(t *testing.T) {
	if os.Getenv("SWARM_CLI_IMAGE_CHILD") != "1" {
		return
	}
	fmt.Println("ready")
	if os.Getenv("SWARM_CLI_IMAGE_REEXEC") == "1" {
		_, _ = io.Copy(io.Discard, os.Stdin)
		os.Exit(0)
	}
	var input [1]byte
	if _, err := io.ReadFull(os.Stdin, input[:]); err != nil {
		os.Exit(2)
	}
	if err := syscall.Exec(os.Args[0], os.Args, append(os.Environ(), "SWARM_CLI_IMAGE_REEXEC=1")); err != nil {
		os.Exit(3)
	}
}

func TestCLIImageSurvivesReplacementButRejectsSamePathExec(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "native")
	copyBinary := func(destination string) {
		t.Helper()
		input, err := os.Open(executable)
		if err != nil {
			t.Fatal(err)
		}
		defer input.Close()
		output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0700)
		if err != nil {
			t.Fatal(err)
		}
		_, copyErr := io.Copy(output, input)
		closeErr := output.Close()
		if copyErr != nil || closeErr != nil {
			t.Fatalf("copy native fixture: %v %v", copyErr, closeErr)
		}
	}
	copyBinary(path)
	fingerprint, err := CLIFingerprint(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "-test.run=^TestCLIImageChild$")
	cmd.Env = append(os.Environ(), "SWARM_CLI_IMAGE_CHILD=1")
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = input.Close(); _ = cmd.Process.Kill(); _ = cmd.Wait() })
	reader := bufio.NewReader(output)
	ready := func() {
		t.Helper()
		if line, err := reader.ReadString('\n'); err != nil || line != "ready\n" {
			t.Fatalf("native fixture readiness: %q %v", line, err)
		}
	}
	ready()
	pid := cmd.Process.Pid
	start, err := procstart.StartTime(pid)
	if err != nil {
		t.Fatal(err)
	}
	if !MatchCLIProcessImage(pid, start, path, fingerprint) {
		t.Fatal("initial loaded native image did not match")
	}
	for _, mismatch := range []struct {
		pid               int
		start             int64
		path, fingerprint string
	}{{pid, start + 1, path, fingerprint}, {pid, start, path + "-other", fingerprint}, {pid, start, path, "bad"}, {-1, start, path, fingerprint}} {
		if MatchCLIProcessImage(mismatch.pid, mismatch.start, mismatch.path, mismatch.fingerprint) {
			t.Fatal("mismatched native image accepted")
		}
	}
	replacement := filepath.Join(root, "replacement")
	copyBinary(replacement)
	if err := os.Rename(replacement, path); err != nil {
		t.Fatal(err)
	}
	if MatchCLIFingerprint(path, fingerprint) || !MatchCLIProcessImage(pid, start, path, fingerprint) {
		t.Fatal("installer replacement lost the original loaded image")
	}
	if _, err := input.Write([]byte{'x'}); err != nil {
		t.Fatal(err)
	}
	ready()
	if after, err := procstart.StartTime(pid); err != nil || after != start {
		t.Fatal("fixture exec changed process identity")
	}
	if MatchCLIProcessImage(pid, start, path, fingerprint) {
		t.Fatal("same-path exec accepted the replacement image under the old fingerprint")
	}
}
