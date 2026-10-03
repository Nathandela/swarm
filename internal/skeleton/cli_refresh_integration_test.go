package skeleton

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/status"
)

// These are actual shim/PTY launches. The provider is a local shell fixture:
// no credentials, network, or installed Claude/Codex executable is used.
func TestCLIRefreshInstalledUpgradeRealResume(t *testing.T) {
	buildBinaries(t)
	shimBinary := swarmBin
	if installed := os.Getenv("SWARM_REFRESH_TEST_BINARY"); installed != "" {
		shimBinary = installed
	}
	dir, err := os.MkdirTemp("/tmp", "swcli-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	bin := filepath.Join(dir, "claude")
	installRefreshMock(t, bin, "2.0.1")
	core, err := daemon.Open(daemon.Config{StateDir: dir, SocketPath: filepath.Join(dir, "d.sock"), LockPath: filepath.Join(dir, "d.lock"), LogPath: filepath.Join(dir, "d.log"), ShimBinary: shimBinary, MaxSessions: 8})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = core.Close() })
	api := &coreAPI{core: core, endpointID: testEndpoint}
	env := []string{"PATH=" + dir + ":/usr/bin:/bin", "HOME=" + dir}
	old, err := api.Launch(daemon.LaunchSpec{AgentType: "claude", Name: "upgrade-me", Tag: "refresh", Cwd: dir, ClientEnv: env, ConversationID: migratedConversationID, Cols: 80, Rows: 24, SpawnedFrom: "parent", SpawnIntent: "delegate", Supervision: "manual", Options: map[string]string{"model": "sonnet"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Kill(old.ShimPID, syscall.SIGTERM) })
	before := refreshAwaitTranscript(t, dir, old.ID, "fixture-ready")
	if old.CLIIdentity == nil || old.CLIIdentity.Version != "2.0.1" {
		t.Fatalf("initial CLI identity = %+v", old.CLIIdentity)
	}
	installRefreshMock(t, bin, "2.0.2")
	candidate, err := probeCLIIdentity("claude", "", env, dir)
	if err != nil {
		t.Fatal(err)
	}
	if !cliIdentityNewer(candidate, old.CLIIdentity) {
		t.Fatalf("upgrade not detected: %+v -> %+v", old.CLIIdentity, candidate)
	}
	if err := syscall.Kill(old.ShimPID, 0); err != nil {
		t.Fatalf("installer interrupted existing shim: %v", err)
	}
	after, _ := os.ReadFile(filepath.Join(dir, old.ID, "transcript.log"))
	if strings.Contains(string(after), "fixture-version=2.0.2") || !strings.Contains(before, "fixture-version=2.0.1") {
		t.Fatal("installation replacement changed existing process")
	}
	if err := core.SetStatus(old.ID, status.Status{Process: status.ProcessRunning, Turn: status.TurnIdle, Interaction: status.InteractionNone}); err != nil {
		t.Fatal(err)
	}
	w := testWatcher(t, newAuthFake(""))
	w.stateDir, w.state = dir, emptyAuthWatchState()
	w.agents = []string{"claude"}
	w.list, w.get, w.kill, w.launch, w.remove = core.List, core.Get, core.Kill, api.Launch, core.Delete
	w.cliProbe = probeCLIIdentity
	w.cliObserve = func(id string) *persist.CLIIdentity { return readCLIObservation(dir, id) }
	var fresh persist.Meta
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		w.tick()
		for _, m := range core.List() {
			if m.ResumedFrom == old.ID {
				fresh = m
			}
		}
		if fresh.ID != "" && w.state.CLI[old.ID].State == cliRefreshComplete {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if fresh.ID == "" || w.state.CLI[old.ID].State != cliRefreshComplete {
		t.Fatalf("watcher did not finish installed upgrade: %+v", w.state.CLI[old.ID])
	}
	t.Cleanup(func() { _ = syscall.Kill(fresh.ShimPID, syscall.SIGTERM) })
	transcript := refreshAwaitTranscript(t, dir, fresh.ID, "fixture-ready")
	for _, want := range []string{"fixture-version=2.0.2", "arg=--resume", "arg=" + migratedConversationID, "arg=--model", "arg=sonnet", "fixture-home=" + dir} {
		if !strings.Contains(transcript, want) {
			t.Errorf("replacement output missing %q: %s", want, transcript)
		}
	}
	if fresh.CLIIdentity == nil || fresh.CLIIdentity.Version != "2.0.2" || fresh.CLIIdentity.Fingerprint != candidate.Fingerprint {
		t.Errorf("replacement identity = %+v; target = %+v", fresh.CLIIdentity, candidate)
	}
	if fresh.ResumedFrom != old.ID || fresh.ConversationID != old.ConversationID || fresh.SpawnedFrom != old.SpawnedFrom || fresh.SpawnIntent != old.SpawnIntent || fresh.Supervision != old.Supervision || fresh.Tag != old.Tag || fresh.Name != old.Name {
		t.Errorf("replacement lost identity/settings: %+v", fresh)
	}
	assembly := &Daemon{core: core, detectProviderVersion: func(string) string {
		t.Error("session capability version consulted daemon installation")
		return "9.9.9"
	}}
	if got := assembly.sessionProviderVersion(old.ID, "claude"); got != "2.0.1" {
		t.Errorf("old session capability version = %q", got)
	}
	if got := assembly.sessionProviderVersion(fresh.ID, "claude"); got != "2.0.2" {
		t.Errorf("new session capability version = %q", got)
	}
	if _, ok := core.Get(old.ID); !ok {
		t.Fatal("source recovery row disappeared")
	}
	// A second installer update after observation must refuse the expected target
	// before creating another session, even though it is also a newer version.
	if err := core.Kill(fresh.ID); err != nil {
		t.Fatal(err)
	}
	refreshAwaitExited(t, core, fresh.ID)
	installRefreshMock(t, bin, "2.0.3")
	count := len(core.List())
	_, err = api.Launch(daemon.LaunchSpec{AgentType: "claude", Cwd: dir, ClientEnv: env, ExpectedCLIIdentity: candidate, Options: map[string]string{protocol.OptionResumeFrom: testEndpoint + "/" + fresh.ID}})
	if err == nil {
		t.Fatal("installer race accepted stale expected target")
	}
	if len(core.List()) != count {
		t.Fatal("refused expected target created a session")
	}
}

func installRefreshMock(t *testing.T, path, version string) {
	t.Helper()
	body := fmt.Sprintf("#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then printf '%%s (Claude Code)\\n' '%s'; exit 0; fi\nprintf 'fixture-version=%%s\\n' '%s'\nprintf 'fixture-home=%%s\\n' \"$HOME\"\nfor arg do printf 'arg=%%s\\n' \"$arg\"; done\nprintf 'fixture-ready\\n'\nexec /bin/sleep 60\n", version, version)
	if err := os.WriteFile(path+".new", []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path+".new", path); err != nil {
		t.Fatal(err)
	}
}

func refreshAwaitTranscript(t *testing.T, dir, id, want string) string {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	var b []byte
	for time.Now().Before(deadline) {
		b, _ = os.ReadFile(filepath.Join(dir, id, "transcript.log"))
		if strings.Contains(string(b), want) {
			return string(b)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("session %s never printed %q: %s", id, want, b)
	return ""
}

func refreshAwaitExited(t *testing.T, core *daemon.Daemon, id string) persist.Meta {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if m, ok := core.Get(id); ok && m.Status.Process != status.ProcessRunning {
			return m
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("session %s did not exit", id)
	return persist.Meta{}
}
