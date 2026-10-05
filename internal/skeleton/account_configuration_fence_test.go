//go:build linux

package skeleton

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/accountcheck"
	"github.com/Nathandela/swarm/internal/accountconfig"
	"github.com/Nathandela/swarm/internal/accounts"
	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/persist"
)

func configurationFenceSnapshot(t *testing.T, root string) map[string]any {
	t.Helper()
	snapshot := make(map[string]any)
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		snapshot[path] = info.Mode()
		if info.Mode().IsRegular() {
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			snapshot[path] = sha256.Sum256(raw)
		} else if info.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			snapshot[path] = target
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func holdNativeCredentialFence(t *testing.T, root string, binding accounts.Binding) {
	t.Helper()
	entered, release, finished := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		finished <- accountcheck.WithCredentialFence(root, binding, func() error { close(entered); <-release; return nil })
	}()
	select {
	case <-entered:
		t.Cleanup(func() {
			close(release)
			if err := <-finished; err != nil {
				t.Error(err)
			}
		})
	case err := <-finished:
		t.Fatalf("synthetic native worker could not acquire fence: %v", err)
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("synthetic native worker did not acquire fence")
	}
}

func TestAccountConfigurationFenceReusesExactCohortAndExcludesUpdates(t *testing.T) {
	for _, provider := range []string{"codex", "claude"} {
		for _, state := range []string{"installed", "initial", "replacement", "pending-update"} {
			t.Run(provider+"/"+state, func(t *testing.T) {
				m := accountTestManager(t, accountTestState(t))
				candidate := accountTestCandidate(t, m, provider, "configuration-fence")
				account := accountTestAdmit(t, m, candidate)
				binding := accountTestBinding(t, m, account)
				home, cwd, env := accountTestLaunchEnvironment(t, m.stateRoot)
				version, marker, updateMarker := "0.160.0", accountconfig.CodexContextMarker, accountconfig.CodexContextUpdateMarker
				sourceSettings := filepath.Join(home, ".codex", "config.toml")
				argv := []string{"/fixture/codex"}
				if provider == "claude" {
					version, marker, updateMarker = "2.1.289", accountconfig.ClaudeContextMarker, accountconfig.ClaudeContextUpdateMarker
					sourceSettings = filepath.Join(home, ".claude", "settings.json")
					accountTestPut(t, sourceSettings, []byte(`{"model":"owner-first-model"}`))
					argv = []string{"/fixture/claude", "--settings", `{"hooks":{}}`}
				}
				spec := daemon.LaunchSpec{AgentType: provider, Argv: argv, Cwd: cwd, ClientEnv: env, AccountBinding: &binding, CLIIdentity: &persist.CLIIdentity{Path: argv[0], Version: version}}
				assembly := &Daemon{accounts: m}
				if state != "initial" {
					if _, err := assembly.prepareAccountLaunch("first", spec); err != nil {
						t.Fatal(err)
					}
				}
				if state == "replacement" || state == "pending-update" {
					old, err := os.ReadFile(filepath.Join(candidate.ProfilePath, marker))
					if err != nil {
						t.Fatal(err)
					}
					next := []byte("model_reasoning_effort=\"medium\"\n")
					if provider == "claude" {
						next = []byte(`{"model":"owner-next-model"}`)
					}
					accountTestPut(t, sourceSettings, next)
					if state == "pending-update" {
						if _, err := assembly.prepareAccountLaunch("refresh", spec); err != nil {
							t.Fatal(err)
						}
						current, err := os.ReadFile(filepath.Join(candidate.ProfilePath, marker))
						if err != nil {
							t.Fatal(err)
						}
						intent, err := json.Marshal(struct {
							SchemaVersion  int
							Previous, Next json.RawMessage
						}{1, old, current})
						if err != nil {
							t.Fatal(err)
						}
						accountTestPut(t, filepath.Join(candidate.ProfilePath, updateMarker), intent)
					}
				}
				beforeProfile := configurationFenceSnapshot(t, candidate.ProfilePath)
				configurationRoot := filepath.Join(m.stateRoot, "accounts", "configurations")
				beforeProjections := configurationFenceSnapshot(t, configurationRoot)
				holdNativeCredentialFence(t, m.stateRoot, binding)
				prepared, err := assembly.prepareAccountLaunch("concurrent", spec)
				if state == "installed" {
					if err != nil || !prepared.AccountNativeContext || prepared.AccountProjectionRef == "" {
						t.Fatalf("exact installed context blocked by unrelated credential worker: %v", err)
					}
				} else if !errors.Is(err, accountcheck.ErrUnavailable) {
					t.Fatalf("%s update reached install while native credential worker holds fence: %v", state, err)
				}
				if !reflect.DeepEqual(beforeProfile, configurationFenceSnapshot(t, candidate.ProfilePath)) || !reflect.DeepEqual(beforeProjections, configurationFenceSnapshot(t, configurationRoot)) {
					t.Fatal("busy generation fence changed destination configuration or projection")
				}
			})
		}
	}
}
