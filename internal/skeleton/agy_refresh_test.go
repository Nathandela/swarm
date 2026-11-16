package skeleton

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/status"
)

func installAgyRefreshMock(t *testing.T, path, version string) {
	t.Helper()
	body := fmt.Sprintf("#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then printf '%%s\\n' '%s'; exit 0; fi\nprintf 'fixture-version=%%s\\n' '%s'\nfor arg do printf 'arg=%%s\\n' \"$arg\"; done\nprintf 'fixture-ready\\n'\nexec /bin/sleep 60\n", version, version)
	if err := os.WriteFile(path+".new", []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path+".new", path); err != nil {
		t.Fatal(err)
	}
}

func TestCLIRefreshInstalledUpgradeAgyRealResume(t *testing.T) {
	buildBinaries(t)

	dir, err := os.MkdirTemp("", "swagyref")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	root := t.TempDir()
	binDir := filepath.Join(root, "bin")
	project := filepath.Join(root, "project")
	homeDir := filepath.Join(root, "home")
	for _, d := range []string{binDir, project, homeDir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}

	agyBin := filepath.Join(binDir, "agy")
	installAgyRefreshMock(t, agyBin, "1.4.7")

	core, err := daemon.Open(daemon.Config{
		StateDir:    dir,
		SocketPath:  filepath.Join(dir, "d.sock"),
		LockPath:    filepath.Join(dir, "d.lock"),
		LogPath:     filepath.Join(dir, "d.log"),
		ShimBinary:  swarmBin,
		MaxSessions: 8,
	})
	if err != nil {
		t.Fatalf("daemon.Open: %v", err)
	}
	t.Cleanup(func() {
		for _, m := range core.List() {
			if m.Status.Process == status.ProcessRunning {
				_ = core.Kill(m.ID)
			}
		}
		_ = core.Close()
	})

	api := &coreAPI{core: core, endpointID: testEndpoint}
	env := []string{
		"PATH=" + binDir + string(os.PathListSeparator) + os.Getenv("PATH"),
		"HOME=" + homeDir,
	}

	src, err := api.Launch(daemon.LaunchSpec{
		AgentType:   "agy",
		Name:        "agy-work",
		Tag:         "team-a",
		Cwd:         project,
		ClientEnv:   env,
		Cols:        80,
		Rows:        24,
		SpawnedFrom: "parent-01",
		SpawnIntent: "delegate",
		Supervision: "passive",
		Options: map[string]string{
			"model":  "Gemini 4 Carbon (Medium)",
			"mode":   "auto",
			"effort": "high",
		},
	})
	if err != nil {
		t.Fatalf("initial Launch: %v", err)
	}

	refreshAwaitTranscript(t, dir, src.ID, "fixture-ready")
	refreshAwaitCLIObservation(t, dir, src)

	const convID = "50000000-0000-4000-8000-000000000055"
	if err := core.SetConversationID(src.ID, convID); err != nil {
		t.Fatalf("SetConversationID: %v", err)
	}
	if err := core.SetStatus(src.ID, status.Status{Process: status.ProcessRunning, Turn: status.TurnIdle, Interaction: status.InteractionNone}); err != nil {
		t.Fatalf("SetStatus: %v", err)
	}

	installAgyRefreshMock(t, agyBin, "1.5.9")

	w := testWatcher(t, newAuthFake(""))
	w.stateDir = dir
	w.state = emptyAuthWatchState()
	w.agents = []string{"agy"}
	w.list = core.List
	w.get = core.Get
	w.kill = core.Kill
	w.launch = api.Launch
	w.remove = core.Delete
	w.resolve = lookPathIn
	w.cliProbe = probeCLIIdentity
	w.cliObserve = func(id string) *persist.CLIIdentity { return readCLIObservation(dir, id) }
	w.exitWait = 5 * time.Second
	w.exitPoll = 10 * time.Millisecond

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		w.tick()
		rec := w.state.CLI[src.ID]
		if rec.State == cliRefreshComplete {
			break
		}
		if rec.State == cliRefreshBlocked {
			t.Fatalf("refresh blocked: %s", rec.LastError)
		}
		time.Sleep(25 * time.Millisecond)
	}

	rec := w.state.CLI[src.ID]
	if rec.State != cliRefreshComplete || rec.ReplacementID == "" {
		t.Fatalf("refresh did not complete: %+v", rec)
	}

	fresh, ok := core.Get(rec.ReplacementID)
	if !ok {
		t.Fatalf("replacement %s missing from daemon", rec.ReplacementID)
	}
	t.Cleanup(func() { _ = core.Kill(fresh.ID) })

	if fresh.ResumedFrom != src.ID {
		t.Errorf("ResumedFrom = %q, want %q", fresh.ResumedFrom, src.ID)
	}
	if fresh.ConversationID != convID {
		t.Errorf("ConversationID = %q, want %q", fresh.ConversationID, convID)
	}
	if fresh.Name != src.Name || fresh.Tag != src.Tag {
		t.Errorf("Name/Tag = (%q, %q), want (%q, %q)", fresh.Name, fresh.Tag, src.Name, src.Tag)
	}
	if fresh.SpawnedFrom != src.SpawnedFrom || fresh.SpawnIntent != src.SpawnIntent || fresh.Supervision != src.Supervision {
		t.Errorf("lineage = (%q, %q, %q), want (%q, %q, %q)", fresh.SpawnedFrom, fresh.SpawnIntent, fresh.Supervision, src.SpawnedFrom, src.SpawnIntent, src.Supervision)
	}
	if fresh.CLIIdentity == nil || fresh.CLIIdentity.Version != "1.5.9" {
		t.Fatalf("fresh.CLIIdentity = %+v, want version 1.5.9", fresh.CLIIdentity)
	}

	out := refreshAwaitTranscript(t, dir, fresh.ID, "fixture-ready")
	norm := strings.ReplaceAll(out, "\r\n", "\n")
	if !strings.Contains(norm, "fixture-version=1.5.9\n") {
		t.Fatalf("replacement transcript missing 1.5.9 version banner:\n%s", norm)
	}
	var gotArgs []string
	for _, line := range strings.Split(norm, "\n") {
		if val, ok := strings.CutPrefix(line, "arg="); ok {
			gotArgs = append(gotArgs, val)
		}
	}

	hasArgPair := func(flag, want string) bool {
		for i := 0; i+1 < len(gotArgs); i++ {
			if gotArgs[i] == flag && gotArgs[i+1] == want {
				return true
			}
		}
		return false
	}
	hasSettings := false
	for i := 0; i+1 < len(gotArgs); i++ {
		if gotArgs[i] == "--settings" && strings.Contains(gotArgs[i+1], "AgyStatusLine") {
			hasSettings = true
		}
	}
	if !hasSettings {
		t.Errorf("replacement argv missing --settings AgyStatusLine: %v", gotArgs)
	}
	if !hasArgPair("--model", "Gemini 4 Carbon (Medium)") {
		t.Errorf("replacement argv missing --model: %v", gotArgs)
	}
	if !hasArgPair("--mode", "auto") {
		t.Errorf("replacement argv missing --mode auto: %v", gotArgs)
	}
	if !hasArgPair("--effort", "high") {
		t.Errorf("replacement argv missing --effort high: %v", gotArgs)
	}
	if !hasArgPair("--conversation", convID) {
		t.Errorf("replacement argv missing --conversation %s: %v", convID, gotArgs)
	}
}

func TestCLIRefreshAgyDefersWhenBusyReviewingOrUncaptured(t *testing.T) {
	oldID := &persist.CLIIdentity{
		Path:        "/usr/local/bin/agy",
		Version:     "1.4.7",
		Fingerprint: "sha256:old",
	}
	newID := &persist.CLIIdentity{
		Path:        "/usr/local/bin/agy",
		Version:     "1.5.9",
		Fingerprint: "sha256:new",
	}

	f := newAuthFake("")
	f.add(persist.Meta{
		ID:             "busy",
		AgentType:      "agy",
		Cwd:            "/work/busy",
		Env:            []string{"PATH=/usr/local/bin"},
		ConversationID: "50000000-0000-4000-8000-000000000001",
		CLIIdentity:    oldID,
		Status:         status.Status{Process: status.ProcessRunning, Turn: status.TurnActive, Interaction: status.InteractionNone},
	})
	f.add(persist.Meta{
		ID:             "review",
		AgentType:      "agy",
		Cwd:            "/work/review",
		Env:            []string{"PATH=/usr/local/bin"},
		ConversationID: "50000000-0000-4000-8000-000000000002",
		CLIIdentity:    oldID,
		Status:         status.Status{Process: status.ProcessRunning, Turn: status.TurnIdle, Interaction: status.InteractionPermission},
	})
	f.add(persist.Meta{
		ID:          "nocid",
		AgentType:   "agy",
		Cwd:         "/work/nocid",
		Env:         []string{"PATH=/usr/local/bin"},
		CLIIdentity: oldID,
		Status:      status.Status{Process: status.ProcessRunning, Turn: status.TurnIdle, Interaction: status.InteractionNone},
	})

	w := testWatcher(t, f)
	w.agents = []string{"agy"}
	w.resolve = func(string, []string) (string, error) { return "/usr/local/bin/agy", nil }
	w.cliObserve = func(id string) *persist.CLIIdentity {
		if id == "busy" || id == "review" || id == "nocid" {
			c := *oldID
			return &c
		}
		c := *newID
		return &c
	}
	w.launch = func(spec daemon.LaunchSpec) (persist.Meta, error) {
		m, err := f.launch(spec)
		if err != nil {
			return m, err
		}
		if spec.ExpectedCLIIdentity != nil {
			c := *spec.ExpectedCLIIdentity
			m.CLIIdentity = &c
			f.sessions[m.ID].CLIIdentity = &c
		}
		return m, nil
	}
	w.cliProbe = func(agentType, _ string, _ []string, _ string) (*persist.CLIIdentity, error) {
		if agentType != "agy" {
			return nil, fmt.Errorf("unexpected agent %q", agentType)
		}
		copyID := *newID
		return &copyID, nil
	}

	for i := 0; i < 5; i++ {
		w.tick()
	}
	if len(f.killed) != 0 || len(f.launched) != 0 {
		t.Fatalf("agy refresh acted on busy/reviewing/uncaptured sessions: killed=%v launched=%d", f.killed, len(f.launched))
	}

	// Once the busy session finishes its turn and returns to idle/none, it refreshes
	// while reviewing and uncaptured sessions remain untouched.
	f.sessions["busy"].Status = status.Status{Process: status.ProcessRunning, Turn: status.TurnIdle, Interaction: status.InteractionNone}
	for i := 0; i < 3; i++ {
		w.tick()
	}

	if len(f.killed) != 1 || f.killed[0] != "busy" {
		t.Fatalf("killed = %v, want [busy]", f.killed)
	}
	if len(f.launched) != 1 || f.launched[0].Options[protocol.OptionResumeFrom] != "ep-test01/busy" {
		t.Fatalf("launched = %+v, want single resume of busy", f.launched)
	}
	if got := f.sessions["review"].Status.Process; got != status.ProcessRunning {
		t.Errorf("reviewing session process = %s, want running", got)
	}
	if got := f.sessions["nocid"].Status.Process; got != status.ProcessRunning {
		t.Errorf("uncaptured session process = %s, want running", got)
	}
}
