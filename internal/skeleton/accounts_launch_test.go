//go:build linux

package skeleton

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/status"
)

func TestAccountLaunchPoolEnrollsOwnerLaunchesAndPreservesFrozenResume(t *testing.T) {
	m := accountTestManager(t, accountTestState(t))
	candidate := accountTestCandidate(t, m, "codex", "bound-resume")
	account := accountTestAdmit(t, m, candidate)
	binding := accountTestBinding(t, m, account)
	projection := strings.Repeat("a", 64)
	store, err := persist.NewStore(m.stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(persist.Meta{ID: "bound-source", AgentType: "codex", ConversationID: migratedConversationID, Cwd: m.stateRoot, AccountBinding: &binding, AccountProjectionRef: projection, CreatedAt: time.Now(), Status: status.Status{Process: status.ProcessExited}}); err != nil {
		t.Fatal(err)
	}
	core := accountTestCore(t, m, nil)
	api := &coreAPI{core: core, accounts: m, endpointID: testEndpoint}
	fresh := daemon.LaunchSpec{AgentType: "codex", Options: map[string]string{"model": "gpt-fixed"}}
	if err := api.bindAccountLaunch(&fresh); err != nil || fresh.AccountBinding != nil {
		t.Fatal("disabled pool changed ambient fresh launch")
	}
	if _, err := m.store.SetEnabled(accountTestRegistry(t, m).Revision, "codex", true); err != nil {
		t.Fatal(err)
	}
	fresh = daemon.LaunchSpec{AgentType: "codex", Options: map[string]string{"model": "gpt-fixed"}}
	if err := api.bindAccountLaunch(&fresh); err != nil || fresh.AccountBinding == nil || fresh.AccountBinding.AccountID != account.ID || fresh.AuthIdentity != binding.Identity {
		t.Fatalf("enabled new discussion was not bound: %v", err)
	}
	if fresh.Options["model"] != "gpt-fixed" || fresh.AccountStateRoot != m.stateRoot {
		t.Fatal("account assignment changed requested model or private state anchor")
	}
	defaultModel := daemon.LaunchSpec{AgentType: "codex"}
	if err := api.bindAccountLaunch(&defaultModel); err != nil || defaultModel.AccountBinding == nil || defaultModel.AccountBinding.AccountID != account.ID {
		t.Fatalf("ordinary launch without --model was not assigned: %v", err)
	}
	if defaultModel.Options["model"] != "" || defaultModel.AccountNativeModel != "" {
		t.Fatal("initial assignment invented a resolved native model")
	}
	if _, err := m.store.SetEnabled(accountTestRegistry(t, m).Revision, "codex", false); err != nil {
		t.Fatal(err)
	}
	resume := daemon.LaunchSpec{AgentType: "codex", Options: map[string]string{protocol.OptionResumeFrom: protocol.NamespacedID(testEndpoint, "bound-source")}}
	if err := api.bindAccountLaunch(&resume); err != nil || resume.AccountBinding == nil || *resume.AccountBinding != binding || resume.AccountProjectionRef != projection {
		t.Fatalf("disabled pool lost frozen resume binding: %v", err)
	}
	resume.AccountBinding.ConfigurationGeneration++
	source, _ := core.Get("bound-source")
	if source.AccountBinding == nil || *source.AccountBinding != binding {
		t.Fatal("resume mutated source's immutable binding pointer")
	}
	if _, err := m.store.SetEnabled(accountTestRegistry(t, m).Revision, "codex", true); err != nil {
		t.Fatal(err)
	}
	external := daemon.LaunchSpec{AgentType: "codex", Options: map[string]string{protocol.OptionResumeConversationID: migratedConversationID}}
	if err := api.bindAccountLaunch(&external); err != nil || external.AccountBinding == nil {
		t.Fatal("enabled pool did not enroll owner imported discussion")
	}
	wrongProvider := daemon.LaunchSpec{AgentType: "claude", AccountBinding: &binding}
	if err := api.bindAccountLaunch(&wrongProvider); !errors.Is(err, errAccountLaunch) {
		t.Fatal("cross-provider binding was accepted")
	}
}

func TestAccountLaunchFinalizesBothConfigurationsAfterActualCwd(t *testing.T) {
	m := accountTestManager(t, accountTestState(t))
	candidate := accountTestCandidate(t, m, "codex", "projected-account")
	account := accountTestAdmit(t, m, candidate)
	binding := accountTestBinding(t, m, account)
	home, _, env := accountTestLaunchEnvironment(t, m.stateRoot)
	actual := filepath.Join(m.stateRoot, "actual-worktree")
	if err := os.Mkdir(actual, 0o700); err != nil {
		t.Fatal(err)
	}
	spec := daemon.LaunchSpec{AgentType: "codex", Argv: []string{"/fixture/codex", "--model", "gpt-fixed", "--sandbox", "workspace-write"}, Cwd: actual, ClientEnv: env, AccountBinding: &binding, CLIIdentity: &persist.CLIIdentity{Path: "/fixture/codex", Version: "0.160.0"}}
	d := &Daemon{accounts: m}
	prepared, err := d.prepareAccountLaunch("fixture", spec)
	if err != nil {
		t.Fatal(err)
	}
	if prepared.AccountBinding == nil || prepared.AccountBinding.ConfigurationGeneration == 0 || prepared.AccountProjectionRef == "" || binding.ConfigurationGeneration != 1 {
		t.Fatal("resolved configuration was not frozen independently")
	}
	for _, expected := range []string{`cli_auth_credentials_store="file"`} {
		if !argvContains(prepared.Argv, expected) || !argvContains(prepared.AccountBackendArgs, expected) {
			t.Fatalf("native CLI/backend configuration diverged for %s", expected)
		}
	}
	if !argvContains(prepared.Argv, "gpt-fixed") || !argvContains(prepared.Argv, "workspace-write") || argvContains(prepared.AccountBackendArgs, `model_reasoning_effort="high"`) {
		t.Fatal("native settings tier or explicit launch permission changed")
	}
	if !argvContains(prepared.ClientEnv, "HOME="+home) {
		t.Fatal("configuration projection lost ordinary HOME")
	}
	for _, entry := range prepared.ClientEnv {
		if strings.HasPrefix(entry, "OPENAI_API_KEY=") || strings.HasPrefix(entry, "CODEX_HOME=") {
			t.Fatal("ambient authentication persisted in harmless launch environment")
		}
	}
	raw, err := os.ReadFile(filepath.Join(m.stateRoot, "accounts", "configurations", prepared.AccountProjectionRef, "projection.json"))
	if err != nil || !strings.Contains(string(raw), actual) || strings.Contains(string(raw), "synthetic-ambient-key") {
		t.Fatal("configuration manifest omitted actual cwd or included ambient credentials")
	}
}

func TestAccountLaunchInvalidBindingOrConfigurationStartsZeroChildren(t *testing.T) {
	for _, failure := range []string{"changed-identity", "unsupported-auth-route", "missing-authority", "missing-cli-identity", "unsupported-cli-version"} {
		t.Run(failure, func(t *testing.T) {
			m := accountTestManager(t, accountTestState(t))
			candidate := accountTestCandidate(t, m, "codex", "invalid-launch")
			account := accountTestAdmit(t, m, candidate)
			binding := accountTestBinding(t, m, account)
			home, requested, env := accountTestLaunchEnvironment(t, m.stateRoot)
			actual := filepath.Join(m.stateRoot, "actual-worktree")
			if err := os.Mkdir(actual, 0o700); err != nil {
				t.Fatal(err)
			}
			marker := filepath.Join(m.stateRoot, "shim-was-started")
			spyShim := filepath.Join(m.stateRoot, "spy-shim")
			accountTestPut(t, spyShim, []byte(fmt.Sprintf("#!/bin/sh\nprintf started > %q\nexit 0\n", marker)))
			if err := os.Chmod(spyShim, 0o700); err != nil {
				t.Fatal(err)
			}
			assembly := &Daemon{accounts: m}
			identity := &persist.CLIIdentity{Path: "/fixture/codex", Version: "0.160.0"}
			switch failure {
			case "changed-identity":
				accountTestPut(t, filepath.Join(candidate.ProfilePath, "auth.json"), accountTestCodexRaw("changed-account", "fixture@example.test"))
			case "unsupported-auth-route":
				accountTestPut(t, filepath.Join(home, ".codex", "config.toml"), []byte("model_provider = \"custom\"\n"))
			case "missing-authority":
				assembly.accounts = nil
			case "missing-cli-identity":
				identity = nil
			case "unsupported-cli-version":
				identity.Version = "0.161.0"
			}
			planned, rolledBack := 0, false
			core := accountTestCore(t, m, func(cfg *daemon.Config) {
				cfg.ShimBinary = spyShim
				cfg.PreLaunch = func(_ string, _ daemon.LaunchSpec) (string, error) { return actual, nil }
				cfg.FinalizeLaunch = assembly.prepareAccountLaunch
				cfg.PreDelete = func(meta persist.Meta) error { rolledBack = meta.AgentCwd == actual; return nil }
				cfg.BackendPlanner = func(_, _, _ string, _, _ []string) (*daemon.BackendSpec, error) { planned++; return nil, nil }
			})
			_, err := core.Launch(daemon.LaunchSpec{AgentType: "codex", Argv: []string{"/fixture/codex", "--model", "gpt-fixed"}, Cwd: requested, ClientEnv: env, AccountBinding: &binding, AccountStateRoot: m.stateRoot, CLIIdentity: identity, Cols: 80, Rows: 24})
			if err == nil || !rolledBack || planned != 0 || len(core.List()) != 0 {
				t.Fatal("failed account finalization crossed spawn/reservation boundary")
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatal("invalid account launch executed a shim child")
			}
		})
	}
}

type accountExecRecord struct {
	Profile           string   `json:"profile"`
	Home              string   `json:"home"`
	Cwd               string   `json:"cwd"`
	Args              []string `json:"args"`
	AmbientKeyPresent bool     `json:"ambient_key_present"`
}

func TestAccountLaunchActualCLIAndBackendShareBoundProfileAndWorktree(t *testing.T) {
	buildBinaries(t)
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("local execution fixture requires Python 3")
	}
	m := accountTestManager(t, accountTestState(t))
	candidate := accountTestCandidate(t, m, "codex", "exec-account")
	account := accountTestAdmit(t, m, candidate)
	binding := accountTestBinding(t, m, account)
	home, requested, env := accountTestLaunchEnvironment(t, m.stateRoot)
	actual := filepath.Join(m.stateRoot, "actual-worktree")
	if err := os.Mkdir(actual, 0o700); err != nil {
		t.Fatal(err)
	}
	fixture := filepath.Join(m.stateRoot, "exec-fixture.py")
	cliRecord, backendRecord := filepath.Join(m.stateRoot, "cli-exec.json"), filepath.Join(m.stateRoot, "backend-exec.json")
	code := fmt.Sprintf("import json,os,socket,sys,time\nbackend = len(sys.argv)>1 and sys.argv[1]=='fixture-backend'\nargs=sys.argv[3:] if backend else sys.argv[1:]\nrecord={'profile':os.environ.get('CODEX_HOME',''),'home':os.environ.get('HOME',''),'cwd':os.getcwd(),'args':args,'ambient_key_present':bool(os.environ.get('OPENAI_API_KEY'))}\npath=%q if backend else %q\nfd=os.open(path,os.O_CREAT|os.O_WRONLY|os.O_TRUNC,0o600)\nwith os.fdopen(fd,'w') as f: json.dump(record,f)\nif backend:\n s=socket.socket(socket.AF_UNIX);s.bind(sys.argv[2]);s.listen()\n while True:\n  c,_=s.accept();c.close()\nelse:\n while True: time.sleep(1)\n", backendRecord, cliRecord)
	accountTestPut(t, fixture, []byte(code))
	native := filepath.Join(m.stateRoot, "fixture-codex")
	accountTestPut(t, native, []byte(fmt.Sprintf("#!/bin/sh\nexec %q %q \"$@\"\n", python, fixture)))
	if err := os.Chmod(native, 0o700); err != nil {
		t.Fatal(err)
	}
	fingerprint, err := persist.CLIFingerprint(native)
	if err != nil {
		t.Fatal(err)
	}
	identity := &persist.CLIIdentity{Path: native, Version: "0.160.0", Fingerprint: fingerprint}
	assembly := &Daemon{accounts: m}
	core := accountTestCore(t, m, func(cfg *daemon.Config) {
		cfg.ShimBinary = swarmBin
		cfg.PreLaunch = func(_ string, _ daemon.LaunchSpec) (string, error) { return actual, nil }
		cfg.FinalizeLaunch = assembly.prepareAccountLaunch
		cfg.BackendPlanner = func(_, _, socket string, env, _ []string) (*daemon.BackendSpec, error) {
			return &daemon.BackendSpec{Program: native, Args: []string{"fixture-backend", socket}, Env: env}, nil
		}
	})
	meta, err := core.Launch(daemon.LaunchSpec{AgentType: "codex", Argv: []string{native, "--model", "gpt-fixed", "--sandbox", "workspace-write"}, Cwd: requested, ClientEnv: env, AccountBinding: &binding, AccountStateRoot: m.stateRoot, CLIIdentity: identity, Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = core.Kill(meta.ID) })
	if err := core.SendBackendAttach(meta.ID, nil); err != nil {
		t.Fatal(err)
	}
	read := func(path string) accountExecRecord {
		deadline := time.Now().Add(8 * time.Second)
		for time.Now().Before(deadline) {
			raw, err := os.ReadFile(path)
			var record accountExecRecord
			if err == nil && json.Unmarshal(raw, &record) == nil {
				return record
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatal("managed native fixture did not execute")
		return accountExecRecord{}
	}
	cli, backend := read(cliRecord), read(backendRecord)
	for _, record := range []accountExecRecord{cli, backend} {
		if record.Profile != candidate.ProfilePath || record.Home != home || record.Cwd != actual || record.AmbientKeyPresent {
			t.Fatal("native CLI/backend inherited divergent profile, HOME, cwd or ambient credentials")
		}
		if !argvContains(record.Args, `cli_auth_credentials_store="file"`) || argvContains(record.Args, `sandbox_workspace_write.network_access=true`) || argvContains(record.Args, `model_reasoning_effort="high"`) {
			t.Fatal("native user settings were promoted to CLI or network policy was broadened")
		}

	}
	current, ok := core.Get(meta.ID)
	if !ok || current.AccountBinding == nil || current.AccountBinding.ConfigurationGeneration == 1 || current.AccountProjectionRef == "" || current.AgentCwd != actual {
		t.Fatal("actual worktree projection was not persisted before native spawn")
	}
	if !reflect.DeepEqual(current.AccountBinding, meta.AccountBinding) {
		t.Fatal("returned and persisted account binding disagree")
	}
	raw, err := os.ReadFile(filepath.Join(m.stateRoot, meta.ID, "shim-launch.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{accountFixtureBearer, accountFixtureRefresh, "synthetic-ambient-key"} {
		if strings.Contains(string(raw), secret) {
			t.Fatal("provider credential persisted in managed shim launch configuration")
		}
	}
}
