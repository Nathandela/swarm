package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestReplayBinaryWaitsForExplicitStartGate(t *testing.T) {
	dir := t.TempDir()
	fixture := filepath.Join(dir, "fixture.json")
	if err := os.WriteFile(fixture, []byte(`{"pty_capture":"aGVsbG8="}`), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	gate := filepath.Join(dir, "start")
	source := filepath.Join(dir, "replay.go")
	if err := os.WriteFile(source, []byte(replaySource(fixture, []replaySegment{{End: 5}}, gate)), 0o600); err != nil {
		t.Fatalf("write replay source: %v", err)
	}
	binary := filepath.Join(dir, "replay")
	if out, err := exec.Command("go", "build", "-o", binary, source).CombinedOutput(); err != nil {
		t.Fatalf("build replay: %v\n%s", err, out)
	}

	done := make(chan struct{})
	var out []byte
	var runErr error
	go func() {
		out, runErr = exec.Command(binary).Output()
		close(done)
	}()
	select {
	case <-done:
		t.Fatalf("replay exited before its start gate was released: err=%v output=%q", runErr, out)
	case <-time.After(200 * time.Millisecond):
	}
	if err := os.WriteFile(gate, []byte("start\n"), 0o600); err != nil {
		t.Fatalf("release start gate: %v", err)
	}
	select {
	case <-done:
		if runErr != nil {
			t.Fatalf("replay after gate release: %v", runErr)
		}
		if string(out) != "hello" {
			t.Fatalf("replay output = %q, want hello", out)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("replay did not start within 5s after gate release")
	}
}
