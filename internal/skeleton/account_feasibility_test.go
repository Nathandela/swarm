package skeleton

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/accountconfig"
	"github.com/Nathandela/swarm/internal/accounts"
	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/adapter/registry"
	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/protocol"
)

type feasibleAccountFixture struct {
	m                      *accountRotationManager
	f                      *authFake
	source                 persist.Meta
	root, native, original string
	prepared               []daemon.LaunchSpec
}

func (f *feasibleAccountFixture) install(t *testing.T, version string) {
	t.Helper()
	// This synthetic executable only prints a version; no auth/model command runs.
	if err := os.WriteFile(f.native, []byte("#!/bin/sh\nprintf '%s\\n' 'codex-cli "+version+"'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
}

func feasibleAccountTest(t *testing.T, count int) *feasibleAccountFixture {
	t.Helper()
	store, root, bindings := accountTestStore(t, count)
	fixture := &feasibleAccountFixture{root: root, native: filepath.Join(root, "bin", "codex"), original: filepath.Join(root, "home", ".codex")}
	for _, path := range []string{filepath.Dir(fixture.native), fixture.original, filepath.Join(root, "requested"), filepath.Join(root, "checkout")} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	fixture.install(t, "0.160.0")
	if err := os.WriteFile(filepath.Join(fixture.original, "config.toml"), []byte("model = \"gpt-first-model\"\nsandbox_mode = \"workspace-write\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	source := accountTestSource(t, filepath.Join(root, "requested"), bindings[0])
	source.AgentCwd = filepath.Join(root, "checkout")
	source.Env = []string{"HOME=" + filepath.Dir(fixture.original), "PATH=" + filepath.Dir(fixture.native)}
	source.LaunchOptions = map[string]string{"model": "gpt-first-model", "sandbox": "workspace-write", protocol.OptionWorktree: "true"}
	identity, err := probeCLIIdentity(source.AgentType, "", source.Env, source.AgentCwd)
	if err != nil {
		t.Fatal(err)
	}
	source.CLIIdentity = identity
	ad, _ := registry.New(accounts.ProviderCodex)
	argv, err := ad.Command(adapter.LaunchSpec{Cwd: source.AgentCwd, Options: source.LaunchOptions})
	if err != nil {
		t.Fatal(err)
	}
	argv[0] = fixture.native
	profile, err := store.ProfilePath(bindings[0])
	if err != nil {
		t.Fatal(err)
	}
	projection, err := accountconfig.PrepareWithModel(root, source.AgentType, profile, source.AgentCwd, source.Env, argv, "", "")
	if err != nil {
		t.Fatal(err)
	}
	bound := *source.AccountBinding
	bound.ConfigurationGeneration = projection.Generation
	source.AccountBinding = &bound
	source.AccountProjectionRef = projection.Ref
	accountTestRollout(t, store, bound, source.AgentCwd, `{"type":"turn_context","payload":{"model":"gpt-first-model"}}`+"\n")
	fixture.source = source
	fixture.m, fixture.f = rotationTestManager(t, store, root, source)
	fixture.m.w.resolve, fixture.m.w.cliProbe = lookPathIn, probeCLIIdentity
	assembly := &Daemon{accounts: &accountManager{store: store, stateRoot: root}}
	fixture.m.prepareLaunch = func(spec daemon.LaunchSpec) (daemon.LaunchSpec, error) {
		return assembly.prepareAccountLaunch("preview", spec)
	}
	launch := fixture.m.w.launch
	fixture.m.w.launch = func(spec daemon.LaunchSpec) (persist.Meta, error) {
		if spec.Options["sandbox"] != "" || spec.Options[protocol.OptionWorktree] != "" {
			t.Fatal("inherited policy was passed as a new request")
		}
		compiled, err := composeLaunchSpec(spec, fixture.m.w.endpointID, "", fixture.f.get, lookPathIn)
		if err != nil {
			return persist.Meta{}, err
		}
		actual, err := probeCLIIdentity(compiled.AgentType, compiled.Argv[0], compiled.ClientEnv, compiled.Cwd)
		if err != nil || spec.ExpectedCLIIdentity == nil || *actual != *spec.ExpectedCLIIdentity {
			return persist.Meta{}, errAccountSuccessorUnavailable
		}
		compiled.CLIIdentity = actual
		prepared, err := assembly.prepareAccountLaunch("actual", compiled)
		if err != nil {
			return persist.Meta{}, err
		}
		fixture.prepared = append(fixture.prepared, prepared)
		meta, err := launch(spec)
		meta.AgentCwd, meta.Env, meta.LaunchOptions, meta.CLIIdentity = prepared.Cwd, prepared.ClientEnv, compiled.Options, actual
		fixture.f.add(meta)
		return meta, err
	}
	return fixture
}

func TestAccountActualSuccessorPreparationPrecedesClaimAndUsesMinimalRequest(t *testing.T) {
	x := feasibleAccountTest(t, 3)
	if err := x.m.reportFailure(x.m.w, x.source.ID, "quota", "", "native-failure"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 6; i++ {
		x.m.step()
	}
	rec := x.m.w.state.AccountRotations[x.source.ID]
	if len(x.f.killed) != 1 || len(x.f.launched) != 1 || len(x.prepared) != 1 || rec.State != accountCommitted || rec.ExpectedCLIIdentity == nil || *rec.ExpectedCLIIdentity != *x.source.CLIIdentity {
		t.Fatalf("valid preparation did not complete: %+v", rec)
	}
	request, prepared := x.f.launched[0], x.prepared[0]
	if request.Cwd != x.source.AgentCwd || request.Options["sandbox"] != "" || request.Options[protocol.OptionWorktree] != "" || prepared.Options["sandbox"] != "workspace-write" || prepared.Options[protocol.OptionWorktree] != "" || prepared.AccountProjectionRef != x.source.AccountProjectionRef {
		t.Fatalf("resume changed checkout/policy: request=%+v prepared=%+v", request, prepared)
	}
	// Exercise real composition/preparation on the next failure after /model.
	child := *x.f.sessions[rec.CandidateID]
	accountTestRollout(t, x.m.store, *child.AccountBinding, child.AgentCwd, `{"type":"turn_context","payload":{"model":"gpt-new-owner-model"}}`+"\n")
	if err := x.m.reportFailure(x.m.w, child.ID, "quota", "gpt-stale-hint", "repeat-failure"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 6; i++ {
		x.m.step()
	}
	if len(x.prepared) != 2 {
		t.Fatalf("native model change cannot continue through real preparation: %+v", x.m.w.state.AccountRotations[x.source.ID])
	}
	last := x.prepared[1]
	for _, args := range [][]string{last.Argv, last.AccountBackendArgs} {
		if !strings.Contains(strings.Join(args, " "), `model="gpt-new-owner-model"`) || strings.Contains(strings.Join(args, " "), `model="gpt-first-model"`) {
			t.Fatalf("native processes disagree on proven model: %v", args)
		}
	}
	if last.AccountProjectionRef != x.source.AccountProjectionRef || last.AccountBinding.ConfigurationGeneration != x.source.AccountBinding.ConfigurationGeneration {
		t.Fatal("model invocation rewrote frozen projection")
	}
}

func TestAccountImpossibleSuccessorNeverClaimsRunningSource(t *testing.T) {
	for _, kind := range []string{"unsupported-before-reservation", "unsupported-after-reservation", "source-config", "destination-cohort", "checkout-gone", "model-changed-after-reservation"} {
		t.Run(kind, func(t *testing.T) {
			x := feasibleAccountTest(t, 2)
			if err := x.m.reportFailure(x.m.w, x.source.ID, "quota", "", "native-failure"); err != nil {
				t.Fatal(err)
			}
			if kind == "unsupported-before-reservation" {
				x.install(t, "0.161.0")
			} else {
				x.m.step()
			}
			rec := x.m.w.state.AccountRotations[x.source.ID]
			switch kind {
			case "unsupported-after-reservation":
				x.install(t, "0.161.0")
			case "source-config":
				if err := os.WriteFile(filepath.Join(x.original, "config.toml"), []byte("model = \"changed-local-source\"\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "destination-cohort":
				profile, err := x.m.store.ProfilePath(*rec.Destination)
				if err != nil {
					t.Fatal(err)
				}
				if err = os.WriteFile(filepath.Join(profile, "AGENTS.md"), []byte("different native instructions"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "checkout-gone":
				if err := os.RemoveAll(x.source.AgentCwd); err != nil {
					t.Fatal(err)
				}
			case "model-changed-after-reservation":
				accountTestRollout(t, x.m.store, *x.source.AccountBinding, x.source.AgentCwd, `{"type":"turn_context","payload":{"model":"gpt-later-model"}}`+"\n")
			}
			for i := 0; i < 3; i++ {
				x.m.step()
			}
			loaded, err := loadAuthWatchStateChecked(x.root)
			if err != nil {
				t.Fatal(err)
			}
			if len(x.f.killed) != 0 || len(x.f.launched) != 0 || loaded.Killed[x.source.ID] {
				t.Fatalf("impossible successor stopped source: %+v", loaded.AccountRotations[x.source.ID])
			}
		})
	}
}

func TestAccountClaimedRedriveRetainsAuthorityWhenSuccessorBecomesImpossible(t *testing.T) {
	x := feasibleAccountTest(t, 2)
	x.f.killErr = errors.New("ambiguous signal delivery")
	if err := x.m.reportFailure(x.m.w, x.source.ID, "quota", "", "native-failure"); err != nil {
		t.Fatal(err)
	}
	x.m.step()
	x.m.step()
	x.f.killErr = nil
	x.install(t, "0.161.0")
	x.m.step()
	loaded, err := loadAuthWatchStateChecked(x.root)
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.Killed[x.source.ID] || loaded.AccountRotations[x.source.ID].State != accountClaimed || len(x.f.killed) != 0 {
		t.Fatal("impossible redrive killed source or dropped issued claim")
	}
	x.m.endTarget = func(string, string) error { return nil }
	if err := x.m.ownerEnd(x.m.w, x.source.ID, "kill"); err != nil {
		t.Fatal(err)
	}
	x.m.step()
	if x.m.w.state.AccountRotations[x.source.ID].State != accountOwnerCanceled || len(x.f.launched) != 0 {
		t.Fatal("owner cancellation resurrected recovery")
	}
}

func TestManagedCLIRefreshUnsupportedDiscoveryAndFinalClaimHold(t *testing.T) {
	t.Run("discovery", func(t *testing.T) {
		x := feasibleAccountTest(t, 1)
		x.source.CLIIdentity.Version = "0.159.0"
		x.f.add(x.source)
		x.m.w.cliObserve = func(string) *persist.CLIIdentity { return x.source.CLIIdentity }
		x.install(t, "0.161.0")
		for i := 0; i < 4; i++ {
			x.m.w.tickCLIRefresh(true)
		}
		if len(x.f.killed) != 0 || x.m.w.state.Killed[x.source.ID] || len(x.m.w.state.CLICandidates) != 0 {
			t.Fatal("unsupported managed target was admitted")
		}
	})
	t.Run("final-claim", func(t *testing.T) {
		x := feasibleAccountTest(t, 1)
		x.m.w.ensureStateMaps()
		x.m.w.state.CLI[x.source.ID] = cliRefreshRecord{AgentType: x.source.AgentType, Target: *x.source.CLIIdentity, State: cliRefreshPending}
		if err := x.m.w.saveState(); err != nil {
			t.Fatal(err)
		}
		if !x.m.w.prepareCLIRefreshTarget(x.source) {
			t.Fatal("supported target could not prepare")
		}
		x.m.w.withRecycleFence = func(_ string, attempt func() error) error { x.install(t, "0.161.0"); return attempt() }
		x.m.w.recycle(x.source.AgentType, x.source)
		loaded, err := loadAuthWatchStateChecked(x.root)
		if err != nil {
			t.Fatal(err)
		}
		if len(x.f.killed) != 0 || loaded.Killed[x.source.ID] {
			t.Fatal("final refresh claim stopped unsupported source")
		}
	})
}
