//go:build linux

package skeleton

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/accountconfig"
	"github.com/Nathandela/swarm/internal/accounts"
	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/enrollment"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/shim"
)

func nativeRetentionFixture(t *testing.T) (*accountManager, []string, string, string) {
	t.Helper()
	m := accountTestManager(t, accountTestState(t))
	home := filepath.Join(m.stateRoot, "home")
	bin := filepath.Join(home, "bin")
	versions := filepath.Join(home, ".local", "share", "claude", "versions")
	for _, path := range []string{bin, versions} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	source := filepath.Join(versions, accountconfig.CharacterizedClaudeVersion)
	copyRetentionELF(t, source)
	alias := filepath.Join(bin, "claude")
	if err := os.Symlink(source, alias); err != nil {
		t.Fatal(err)
	}
	return m, []string{"HOME=" + home, "PATH=" + bin + ":/usr/bin:/bin"}, source, alias
}

func TestManagedClaudeActualLaunchSelectsRetainedChild(t *testing.T) {
	buildBinaries(t)
	m, env, source, alias := nativeRetentionFixture(t)
	home := strings.TrimPrefix(env[0], "HOME=")
	record := filepath.Join(m.stateRoot, "selected-child.json")
	accountTestPut(t, filepath.Join(home, ".retention-fixture-record"), []byte(record))
	accountTestPut(t, filepath.Join(home, ".claude", "settings.json"), []byte(`{"model":"claude-sonnet-4-6","hooks":{}}`))
	account := accountTestAdmit(t, m, accountTestCandidate(t, m, accounts.ProviderClaude, "retention-actor"))
	if _, err := m.store.SetEnabled(accountTestRegistry(t, m).Revision, accounts.ProviderClaude, true); err != nil {
		t.Fatal(err)
	}
	selected, err := m.managedNative(accounts.ProviderClaude, env, m.stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	newer := filepath.Join(filepath.Dir(source), "2.1.290")
	copyRetentionELF(t, newer)
	if err := os.Remove(alias); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(newer, alias); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(source); err != nil {
		t.Fatal(err)
	}
	// A stale startup entry cannot veto the fresh verified managed selection.
	m.native[accounts.ProviderClaude] = &persist.CLIIdentity{Path: alias, Version: "2.1.290", Fingerprint: "startup-observation"}
	assembly := &Daemon{accounts: m}
	core := accountTestCore(t, m, func(cfg *daemon.Config) {
		cfg.ShimBinary = swarmBin
		cfg.FinalizeLaunch = assembly.prepareAccountLaunch
	})
	api := newCoreAPI(core, "", testEndpoint)
	api.accounts = m
	t.Cleanup(api.close)
	meta, err := api.Launch(daemon.LaunchSpec{AgentType: accounts.ProviderClaude, Cwd: m.stateRoot, ClientEnv: env, Options: map[string]string{}, Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = core.Kill(meta.ID) })
	if meta.CLIIdentity == nil || *meta.CLIIdentity != *selected || meta.AccountBinding == nil || meta.AccountBinding.AccountID != account.ID {
		t.Fatalf("managed launch identity differs: %+v", meta)
	}
	deadline := time.Now().Add(10 * time.Second)
	var child struct {
		Executable, Argv0, Cwd string
		PID                    int
		StartTime              int64
	}
	for {
		raw, readErr := os.ReadFile(record)
		if readErr == nil && json.Unmarshal(raw, &child) == nil && shim.ReadCLIObservation(filepath.Join(m.stateRoot, meta.ID)) != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("retained synthetic child or observation did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	observed := shim.ReadCLIObservation(filepath.Join(m.stateRoot, meta.ID))
	if child.Executable != selected.Path || child.Argv0 != selected.Path || child.Cwd != m.stateRoot || observed == nil || *observed != *selected {
		t.Fatalf("child/observation selected another executable: %+v %+v", child, observed)
	}
	ambient, err := probeCLIIdentity(accounts.ProviderClaude, "", env, m.stateRoot)
	if err != nil || ambient.Version != "2.1.290" || ambient.Path != alias {
		t.Fatalf("owner path changed: %+v %v", ambient, err)
	}
}

func copyRetentionELF(t *testing.T, path string) {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	source, err := os.Open(executable)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = source.Close() }()
	target, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o700)
	if err != nil {
		t.Fatal(err)
	}
	_, copyErr := io.Copy(target, source)
	closeErr := target.Close()
	if copyErr != nil || closeErr != nil {
		t.Fatalf("copy synthetic ELF: %v %v", copyErr, closeErr)
	}
}

func TestManagedClaudeRetentionSurvivesUpdaterGCAndRestart(t *testing.T) {
	m, env, source, alias := nativeRetentionFixture(t)
	selected, err := m.managedNative(accounts.ProviderClaude, env, m.stateRoot)
	if err != nil || !persist.IsCLIContentFingerprint(selected.Fingerprint) || selected.Path == alias {
		t.Fatalf("not retained: %+v %v", selected, err)
	}
	newer := filepath.Join(filepath.Dir(source), "2.1.290")
	copyRetentionELF(t, newer)
	if err := os.Remove(alias); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(newer, alias); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(source); err != nil {
		t.Fatal(err)
	}
	again, err := m.managedNative(accounts.ProviderClaude, env, m.stateRoot)
	if err != nil || *again != *selected {
		t.Fatalf("vendor GC stranded runtime: %+v %v", again, err)
	}
	ambient, err := probeCLIIdentity(accounts.ProviderClaude, "", env, m.stateRoot)
	if err != nil || ambient.Path != alias || ambient.Version != "2.1.290" {
		t.Fatalf("owner CLI changed: %+v %v", ambient, err)
	}
	api := &coreAPI{accounts: m}
	resolved, err := api.accountLaunchResolver(daemon.LaunchSpec{AgentType: accounts.ProviderClaude})("claude", env)
	if err != nil || resolved != alias {
		t.Fatalf("unbound PATH changed: %q %v", resolved, err)
	}
	t.Setenv("HOME", strings.TrimPrefix(env[0], "HOME="))
	t.Setenv("PATH", strings.TrimPrefix(env[1], "PATH="))
	restarted := openAccountManager(m.stateRoot, "", nil)
	t.Cleanup(restarted.close)
	if restarted.unavailable != nil || restarted.nativeSelected == nil || *restarted.nativeSelected != *selected {
		t.Fatalf("restart lost retained selection: %+v %v", restarted.nativeSelected, restarted.unavailable)
	}
	method := restarted.methods()[accounts.ProviderClaude][0]
	if !method.Available || !strings.Contains(method.Reason, "2.1.289") || !strings.Contains(method.Reason, "2.1.290") || !strings.Contains(method.Reason, "Last observed") {
		t.Fatalf("untruthful runtime presentation: %+v", method)
	}
}

func TestManagedClaudeBootstrapAndMissingArtifactHold(t *testing.T) {
	m, env, source, alias := nativeRetentionFixture(t)
	newer := filepath.Join(filepath.Dir(source), "2.1.290")
	copyRetentionELF(t, newer)
	if err := os.Remove(alias); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(newer, alias); err != nil {
		t.Fatal(err)
	}
	selected, err := m.managedNative(accounts.ProviderClaude, env, m.stateRoot)
	if err != nil || selected.Version != accountconfig.CharacterizedClaudeVersion {
		t.Fatalf("qualified sibling bootstrap failed: %+v %v", selected, err)
	}
	before := accountTestRegistry(t, m)
	if err := os.Remove(selected.Path); err != nil {
		t.Fatal(err)
	}
	if _, err := m.managedNative(accounts.ProviderClaude, env, m.stateRoot); err == nil {
		t.Fatal("missing retained artifact fell back")
	}
	if !reflect.DeepEqual(before, accountTestRegistry(t, m)) || m.methods()[accounts.ProviderClaude][0].Available {
		t.Fatal("missing artifact changed authority or remained available")
	}
}

func TestManagedClaudeCustomELFStaysInPlace(t *testing.T) {
	m, env, _, alias := nativeRetentionFixture(t)
	if err := os.Remove(alias); err != nil {
		t.Fatal(err)
	}
	copyRetentionELF(t, alias)
	selected, err := m.managedNative(accounts.ProviderClaude, env, m.stateRoot)
	if err != nil || selected.Path != alias || strings.HasPrefix(selected.Fingerprint, "sha256:") {
		t.Fatalf("custom wrapper falsely relocated: %+v %v", selected, err)
	}
	if _, err := os.Lstat(filepath.Join(m.stateRoot, "accounts", "native")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("custom wrapper created cache: %v", err)
	}
}

func TestManagedClaudeRetentionAtomicPublicationAndRetry(t *testing.T) {
	for _, phase := range []string{"before-publish", "after-publish"} {
		t.Run(phase, func(t *testing.T) {
			m, env, _, _ := nativeRetentionFixture(t)
			failure := errors.New("synthetic publication interruption")
			if phase == "before-publish" {
				m.nativePublish = func(*os.Root, string, string) error { return failure }
			} else {
				m.nativeSync = func(*os.Root) error { return failure }
			}
			if _, err := m.managedNative(accounts.ProviderClaude, env, m.stateRoot); !errors.Is(err, failure) {
				t.Fatalf("fault not exercised: %v", err)
			}
			if phase == "before-publish" {
				if _, err := m.retainedClaude(); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("partial runtime selectable: %v", err)
				}
			}
			m.nativePublish, m.nativeSync = nil, nil
			selected, err := m.managedNative(accounts.ProviderClaude, env, m.stateRoot)
			if err != nil || !persist.IsCLIContentFingerprint(selected.Fingerprint) {
				t.Fatalf("retry failed: %+v %v", selected, err)
			}
		})
	}
}

func TestManagedClaudeRetentionPreservesAbandonedStageAndRejectsTamper(t *testing.T) {
	m, env, _, _ := nativeRetentionFixture(t)
	stage := filepath.Join(m.stateRoot, "accounts", "native", ".stage-"+newItemID())
	if err := os.MkdirAll(stage, 0o700); err != nil {
		t.Fatal(err)
	}
	partial := []byte("interrupted executable copy")
	if err := os.WriteFile(filepath.Join(stage, "claude"), partial, 0o600); err != nil {
		t.Fatal(err)
	}
	selected, err := m.managedNative(accounts.ProviderClaude, env, m.stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	retained, err := os.ReadFile(filepath.Join(stage, "claude"))
	if err != nil || !reflect.DeepEqual(retained, partial) {
		t.Fatal("abandoned stage was changed or removed")
	}
	if _, err := m.retainedClaude(); err != nil {
		t.Fatalf("published runtime did not survive stage: %v", err)
	}
	info, err := os.Stat(selected.Path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(selected.Path, 0o700); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(selected.Path, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteAt([]byte("X"), 0); err != nil {
		t.Fatal(err)
	}
	_ = file.Close()
	if err := os.Chmod(selected.Path, 0o500); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(selected.Path, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	if _, err := m.managedNative(accounts.ProviderClaude, env, m.stateRoot); err == nil {
		t.Fatal("known content tamper accepted")
	}
	if m.methods()[accounts.ProviderClaude][0].Available {
		t.Fatal("known digest failure still advertised available")
	}
}

func TestManagedClaudeRetentionConcurrentManagersAndPresentation(t *testing.T) {
	m, env, _, _ := nativeRetentionFixture(t)
	t.Setenv("PATH", "/no-retention-provider")
	other := openAccountManager(m.stateRoot, "", nil)
	t.Cleanup(other.close)
	var wg sync.WaitGroup
	identities := make([]*persist.CLIIdentity, 2)
	errors := make([]error, 2)
	for index, manager := range []*accountManager{m, other} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			identities[index], errors[index] = manager.managedNative(accounts.ProviderClaude, env, m.stateRoot)
		}()
	}
	wg.Wait()
	if errors[0] != nil || errors[1] != nil || *identities[0] != *identities[1] {
		t.Fatalf("publishers replaced winner: %+v %+v", identities, errors)
	}
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			for n := 0; n < 3; n++ {
				if index == 0 {
					_, _ = m.managedNative(accounts.ProviderClaude, env, m.stateRoot)
				} else {
					_, _ = m.Accounts(protocol.AccountsReq{Action: "list"})
				}
			}
		}(i)
	}
	wg.Wait()
}

func TestManagedClaudeEmptyStartupDefersCopyUntilFirstEnrollment(t *testing.T) {
	m, env, source, alias := nativeRetentionFixture(t)
	newer := filepath.Join(filepath.Dir(source), "2.1.290")
	copyRetentionELF(t, newer)
	if err := os.Remove(alias); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(newer, alias); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", strings.TrimPrefix(env[0], "HOME="))
	t.Setenv("PATH", strings.TrimPrefix(env[1], "PATH="))
	cold := openAccountManager(m.stateRoot, "/must-not-execute", nil)
	t.Cleanup(cold.close)
	if _, err := os.Lstat(filepath.Join(m.stateRoot, "accounts", "native")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("empty startup copied native executable: %v", err)
	}
	method := cold.methods()[accounts.ProviderClaude][0]
	if !method.Available || !strings.Contains(method.Reason, "2.1.289") || !strings.Contains(method.Reason, "2.1.290") {
		t.Fatalf("first enrollment unavailable under newer ambient: %+v", method)
	}
	if err := os.Chmod(source, 0o500); err != nil {
		t.Fatal(err)
	}
	if cold.methods()[accounts.ProviderClaude][0].Available {
		t.Fatal("changed qualified source metadata remained available")
	}
	if err := os.Chmod(source, 0o700); err != nil {
		t.Fatal(err)
	}
	reply, err := cold.Accounts(protocol.AccountsReq{Action: "start", Provider: accounts.ProviderClaude, Method: enrollment.MethodNativeLogin, ExpectedRevision: accountTestRegistry(t, cold).Revision})
	if err != nil || reply.Job == nil || reply.Job.ErrorCode != "worker-start-failed" {
		t.Fatalf("first enrollment was not admitted to synthetic spawn boundary: %+v %v", reply.Job, err)
	}
	selected, err := cold.retainedClaude()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(m.stateRoot, "accounts", "jobs", reply.Job.ID, enrollment.ConfigFile))
	if err != nil {
		t.Fatal(err)
	}
	var cfg enrollment.Config
	if json.Unmarshal(raw, &cfg) != nil || cfg.SchemaVersion != enrollment.RetainedNativeConfigSchemaVersion || cfg.NativePath != selected.Path || cfg.NativeFingerprint != selected.Fingerprint {
		t.Fatalf("enrollment lost retained custody: %+v", cfg)
	}
}
