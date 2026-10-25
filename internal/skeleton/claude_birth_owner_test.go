package skeleton

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/logins"
	"github.com/Nathandela/swarm/internal/processcontain"
)

// This fixture substitutes only an already-settled private auth receipt. It uses
// public profile/selection/admission APIs and never executes an installed CLI.
// Linux avoids the production Darwin credential store entirely.
func claudeOwnerBirthFixture(t *testing.T, root, cwd string) (*logins.Store, logins.Login, logins.Runtime) {
	t.Helper()
	s, err := logins.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	expires := time.UnixMilli(time.Now().Add(time.Hour).UnixMilli()).UTC()
	grant, _ := json.Marshal(map[string]any{"claudeAiOauth": map[string]any{"accessToken": "synthetic-access", "refreshToken": "synthetic-refresh", "expiresAt": expires.UnixMilli(), "scopes": []string{"user:profile", "user:inference"}}})
	r, err := s.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	r, login, err := s.Add(r.Revision, logins.ProviderClaude, "held-birth", logins.Credential{Auth: grant, Identity: json.RawMessage(`{"oauthAccount":{"accountUuid":"synthetic-account","organizationUuid":"synthetic-org"},"userID":"synthetic-account"}`)})
	if err != nil {
		t.Fatal(err)
	}
	r, err = s.ProvisionClaudeProfile(r.Revision, login.ID, []string{cwd})
	if err != nil {
		t.Fatal(err)
	}
	ref, _, err := s.BeginClaudeAuth(r.Revision, login.ID)
	if err != nil {
		t.Fatal(err)
	}
	r, err = s.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := os.ReadFile(filepath.Join(root, "logins", "saved", login.ID, ".credentials.json"))
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(canonical)
	started := time.Now().Add(-time.Second).UTC()
	receipt, _ := json.Marshal(map[string]any{"version": 1, "ref": ref, "selection": r.Providers[logins.ProviderClaude], "deadline": started.Add(45 * time.Second), "attempt": 1, "phase": "ready", "started_at": started, "generation": hex.EncodeToString(digest[:]), "worker": processcontain.Identity{PID: 999999998, StartTime: 1}, "native": processcontain.Identity{PID: 999999999, StartTime: 1}, "stop_proof": "native-tree", "expires_at": expires})
	if err := os.WriteFile(filepath.Join(root, "logins", "claude-auth", login.ID+".json"), receipt, 0600); err != nil {
		t.Fatal(err)
	}
	r, err = s.SelectClaudeProfile(r.Revision, login.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ValidateClaudeAuthReceipt(ref, r.Providers[logins.ProviderClaude].Epoch); err != nil {
		t.Fatal(err)
	}
	home, err := s.ClaudeProfileHome(login.Identity)
	if err != nil {
		t.Fatal(err)
	}
	return s, login, logins.Runtime{Provider: logins.ProviderClaude, Epoch: r.Providers[logins.ProviderClaude].Epoch, Identity: login.Identity, Home: home}
}

func TestClaudeBirthHeldSelectionDoesNotBlockInstalledOwnerKill(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("synthetic Claude saved authority is Linux-only; never use Darwin Keychain")
	}
	buildBinaries(t)
	base, err := filepath.EvalSymlinks("/tmp")
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp(base, "swbirth-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	cwd, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	selected, login, rt := claudeOwnerBirthFixture(t, root, cwd)
	birth, err := logins.OpenExisting(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = birth.Close() })
	core, err := daemon.Open(daemon.Config{StateDir: root, SocketPath: filepath.Join(root, "d.sock"), LockPath: filepath.Join(root, "d.lock"), LogPath: filepath.Join(root, "d.log"), ShimBinary: swarmBin, MaxSessions: 4})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = core.Close() })
	d := &Daemon{core: core, stateDir: root}
	d.logins = &loginManager{d: d, store: selected}
	d.api = &coreAPI{core: core, stateDir: root, killFn: d.ownerKill}
	// A real shim/PTY remains independent from the held registry guard. The file
	// barrier makes output after the guard positive drain evidence.
	marker := filepath.Join(root, "guard-held")
	session, err := core.Launch(daemon.LaunchSpec{AgentType: "fake", Argv: []string{"/bin/sh", "-c", `printf 'before-guard\n'; while [ ! -f "$1" ]; do /bin/sleep 0.01; done; printf 'during-guard\n'; exec /bin/sleep 60`, "fixture", marker}, Cwd: cwd, ClientEnv: []string{"PATH=/usr/bin:/bin", "HOME=" + root}, Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = core.Kill(session.ID) })
	refreshAwaitTranscript(t, root, session.ID, "before-guard")
	r, err := selected.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	releaseBirth := func() { once.Do(func() { close(release) }) }
	held := make(chan error, 1)
	admitDone := make(chan struct{})
	go func() {
		defer close(admitDone)
		held <- birth.AdmitClaudeProfileBirth(rt, cwd, func() error { close(entered); <-release; return nil })
	}()
	defer func() { releaseBirth(); <-admitDone }()
	select {
	case <-entered:
	case err := <-held:
		t.Fatalf("actual admission refused: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("actual birth did not enter guard")
	}
	selectionDone := make(chan error, 1)
	go func() { _, err := selected.SelectClaudeProfile(r.Revision, login.ID); selectionDone <- err }()
	var joined sync.Once
	finishGuard := func() {
		joined.Do(func() {
			releaseBirth()
			if err := <-held; err != nil {
				t.Error(err)
			}
			if err := <-selectionDone; err != nil {
				t.Error(err)
			}
		})
	}
	t.Cleanup(finishGuard)

	// Require positive stack evidence of the actual selection waiting on flock,
	// rather than assuming a launched goroutine has acquired Store.mu already.
	deadline := time.Now().Add(3 * time.Second)
	waiting := false
	for time.Now().Before(deadline) {
		stack := make([]byte, 1<<20)
		n := runtime.Stack(stack, true)
		for _, g := range strings.Split(string(stack[:n]), "\n\n") {
			if strings.Contains(g, "SelectClaudeProfile") && strings.Contains(g, "syscall.Flock") {
				waiting = true
			}
		}
		if waiting {
			break
		}
		runtime.Gosched()
	}
	if !waiting {
		finishGuard()
		t.Fatal("real selection never waited behind held birth")
	}
	if err := os.WriteFile(marker, []byte("held"), 0600); err != nil {
		finishGuard()
		t.Fatal(err)
	}
	refreshAwaitTranscript(t, root, session.ID, "during-guard")
	killed := make(chan error, 1)
	go func() { killed <- d.api.Kill(session.ID) }()
	blocked := false
	select {
	case err := <-killed:
		if err != nil {
			t.Error(err)
		}
	case <-time.After(time.Second):
		blocked = true
	}
	finishGuard()
	if blocked {
		if err := <-killed; err != nil {
			t.Error(err)
		}
		t.Fatal("installed owner Kill blocked behind a queued real selection while native birth held registry flock")
	}
	refreshAwaitExited(t, core, session.ID)
}

func TestLoginOwnershipSnapshotPreservesSwitchNamesAndFailureRefusal(t *testing.T) {
	t.Run("switch-source-and-child", func(t *testing.T) {
		f := newLoginFixture(t)
		op, err := f.m.BeginSwitch(logins.ProviderCodex, f.second.ID, "ownership-reader")
		if err != nil {
			t.Fatal(err)
		}
		op.Sessions[0].ChildID = "retained-child"
		if err := f.m.store.SaveSwitch(op); err != nil {
			t.Fatal(err)
		}
		for _, id := range []string{op.Sessions[0].SourceID, op.Sessions[0].ChildID} {
			if !f.m.owns(id) {
				t.Fatalf("switch-owned name escaped arbitration: %s", id)
			}
		}
		if f.m.owns("unrelated") {
			t.Fatal("healthy unrelated row became switch-owned")
		}
	})
	for _, kind := range []string{"registry-read", "reader-open"} {
		t.Run(kind, func(t *testing.T) {
			f := newLoginFixture(t)
			if kind == "registry-read" {
				if err := os.WriteFile(filepath.Join(f.m.store.StateRoot(), "logins", "registry.json"), []byte("not json"), 0600); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.Remove(filepath.Join(f.m.store.StateRoot(), "logins", ".registry-lock")); err != nil {
					t.Fatal(err)
				}
			}
			if !f.m.owns("unrelated") {
				t.Fatal("ownership snapshot error became not-owned")
			}
		})
	}
}
