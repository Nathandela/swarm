package protocol

import (
	"bytes"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLaunchRefusal_IsWrittenToTheDaemonLog pins observability of launch and handoff
// refusals (A4). replyError only wrote the refusal to the client, so a TUI-surfaced
// refusal left no trace in daemon.log and no handoff failure text could be recovered.
func TestLaunchRefusal_IsWrittenToTheDaemonLog(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	stub := newStubDaemon()
	stub.launchErr = errors.New(`accounts: no codex account left: "Work" configuration-writer-active`)
	c := dialClient(t, serveStub(t, stub), nil)
	cwd := t.TempDir()
	if _, _, err := c.Launch(LaunchReq{Agent: "codex", Name: "fix-login", Cwd: cwd, Cols: 80, Rows: 24}); err == nil {
		t.Fatal("launch succeeded, want the daemon refusal")
	}
	if _, _, err := c.Launch(LaunchReq{Agent: "claude", Name: "no-such-dir", Cwd: filepath.Join(cwd, "missing"), Cols: 80, Rows: 24}); err == nil {
		t.Fatal("launch into a missing directory succeeded")
	}
	got := buf.String()
	for _, want := range []string{
		`agent=codex name="fix-login"`, `"Work" configuration-writer-active`,
		`agent=claude name="no-such-dir"`, "cwd is not an existing directory",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("daemon log lacks %q; a refused launch must be logged with its session and reason:\n%s", want, got)
		}
	}
}
