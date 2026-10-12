package e2e

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/status"
)

// Exercise the real client, daemon, shim and PTY using a non-billable provider
// fixture. The actual process cwd and persisted successor must both relocate.
func TestE2E_ResumeRelocatesMissingDirectory(t *testing.T) {
	buildBinaries(t)
	env := newDaemonEnv(t)
	startDaemon(t, env)
	c, err := protocol.Dial(env.sock, []string{"attach", protocol.CapResumeRelocate})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	const convID = "conv-REF-relocated"
	binDir := fakeReferenceBinDir(t, convID)
	script := "#!/bin/sh\nprintf 'conv-id=conv-REF-relocated\\n'\nprintf 'cwd=%s\\n' \"$PWD\"\nsleep 30\n"
	if err := os.WriteFile(filepath.Join(binDir, "reference-cli"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	sourceDir := t.TempDir()
	srcID, _, err := c.Launch(protocol.LaunchReq{
		Agent: "reference", Cwd: sourceDir, Name: "Retained work", Tag: "WIP",
		Env: []string{"PATH=" + binDir + ":" + os.Getenv("PATH")}, Cols: 80, Rows: 24,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Delete(srcID) })
	srcLocal := localOf(t, srcID)
	waitForConversationID(t, env.stateDir, srcLocal, convID)
	if err := c.Kill(srcID); err != nil {
		t.Fatal(err)
	}
	if _, ok := waitForStatus(t, c, srcID, l1Bound, func(s status.Status) bool { return s.Process != status.ProcessRunning }); !ok {
		t.Fatal("source did not end")
	}
	source := readMeta(t, env.stateDir, srcLocal)
	if err := os.Remove(sourceDir); err != nil {
		t.Fatal(err)
	}
	destination := t.TempDir()
	newID, _, err := c.Launch(protocol.LaunchReq{
		Agent: "reference", Cwd: destination,
		Options: map[string]string{protocol.OptionResumeFrom: srcID, protocol.OptionResumeRelocate: "true"},
		// Resume must use the saved provider environment even from another terminal.
		Env: []string{"PATH=/no-provider-here"}, Cols: 120, Rows: 24,
	})
	if err != nil {
		t.Fatalf("relocate and resume: %v", err)
	}
	t.Cleanup(func() { _ = c.Delete(newID) })
	attachment, _ := attachWhenGridHas(t, c, newID, "cwd="+destination)
	if err := attachment.Detach(); err != nil {
		t.Fatal(err)
	}
	newLocal := localOf(t, newID)
	resumed := readMeta(t, env.stateDir, newLocal)
	if resumed.Cwd != destination || resumed.ResumedFrom != srcLocal || resumed.ConversationID != source.ConversationID || resumed.Name != source.Name || resumed.Tag != source.Tag || !reflect.DeepEqual(resumed.Env, source.Env) {
		t.Fatalf("relocation lost saved context: %+v", resumed)
	}
	if argv := readShimArgv(t, env.stateDir, newLocal); !argvHas(argv, "--resume") || !argvHas(argv, convID) {
		t.Fatalf("relocation launched a fresh conversation: %v", argv)
	}
	if retained := readMeta(t, env.stateDir, srcLocal); retained.Cwd != source.Cwd || retained.AgentCwd != source.AgentCwd || !reflect.DeepEqual(retained.LaunchOptions, source.LaunchOptions) {
		t.Fatal("relocation rewrote the source metadata")
	}
	if _, err := os.Stat(sourceDir); !os.IsNotExist(err) {
		t.Fatal("relocation recreated the deleted directory")
	}
}
