// VM-local credential custody for the real provider-login qualification. This
// command prints no credentials or account identity values.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/Nathandela/swarm/internal/logins"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/processcontain"
)

type source struct {
	Provider string `json:"provider"`
	ID       string `json:"id"`
	Home     string `json:"home"`
}

func main() {
	action := flag.String("action", "seed", "seed or restore")
	state := flag.String("state", "", "private qualification state")
	provider := flag.String("provider", "", "claude or codex")
	a := flag.String("source-a", "", "first exclusive native credential home")
	b := flag.String("source-b", "", "second exclusive native credential home")
	flag.Parse()
	if err := run(*action, *state, *provider, []string{*a, *b}); err != nil {
		fmt.Fprintln(os.Stderr, "qualification credential custody:", err)
		os.Exit(1)
	}
}

func authName(provider string) string {
	if provider == logins.ProviderClaude {
		return ".credentials.json"
	}
	return "auth.json"
}
func requireNoWriters(homes []string) error {
	writers, err := processcontain.EnvironmentWriters(homes, nil)
	if err != nil {
		return err
	}
	if len(writers) != 0 {
		return errors.New("credential home still has native writers")
	}
	return nil
}
func writePrivate(path string, body []byte) error {
	f, err := os.OpenFile(path+".qualification-tmp", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	if _, err = f.Write(body); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(path+".qualification-tmp", path)
}
func run(action, state, provider string, homes []string) error {
	if provider != logins.ProviderClaude && provider != logins.ProviderCodex {
		return errors.New("explicit provider required")
	}
	if !filepath.IsAbs(state) || filepath.Clean(state) != state {
		return errors.New("canonical absolute state required")
	}
	store, err := logins.Open(state)
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()
	if action == "diagnose" {
		paths, err := filepath.Glob(filepath.Join(state, "*", "shim-launch.json"))
		if err != nil {
			return err
		}
		for _, path := range paths {
			var cfg struct {
				Runtime     *logins.Runtime      `json:"login_runtime"`
				Op          string               `json:"login_switch_id"`
				Source      string               `json:"login_switch_source"`
				Cwd         string               `json:"cwd"`
				Argv        []string             `json:"argv"`
				Env         []string             `json:"env"`
				BackendArgs []string             `json:"backend_args"`
				BackendEnv  []string             `json:"backend_env"`
				CLI         *persist.CLIIdentity `json:"cli_identity"`
			}
			raw, e := os.ReadFile(path)
			if e != nil {
				return e
			}
			if json.Unmarshal(raw, &cfg) != nil {
				return errors.New("invalid launch config")
			}
			if cfg.Op == "" || cfg.Runtime == nil {
				continue
			}
			_, envErr := store.PrepareSwitchEnvironment(cfg.Op, cfg.Source, *cfg.Runtime, cfg.Env)
			_, backendEnvErr := store.PrepareSwitchEnvironment(cfg.Op, cfg.Source, *cfg.Runtime, cfg.BackendEnv)
			flags := map[string]bool{"runtime_valid": store.ValidateRuntime(*cfg.Runtime) == nil, "switch_valid": store.ValidateSwitchRestart(cfg.Op, cfg.Source, *cfg.Runtime) == nil, "project_valid": logins.ValidateProjectAuthEnv(provider, cfg.Cwd, cfg.Env) == nil, "argv_valid": logins.ValidateArgsAuth(provider, cfg.Argv) == nil, "backend_args_valid": logins.ValidateArgsAuth(provider, cfg.BackendArgs) == nil, "switch_environment_valid": envErr == nil, "switch_backend_environment_valid": backendEnvErr == nil, "cli_matches": cfg.CLI != nil && persist.MatchCLIFingerprint(cfg.CLI.Path, cfg.CLI.Fingerprint)}
			body, _ := json.Marshal(flags)
			fmt.Println(string(body))
		}
		return nil
	}
	intent := filepath.Join(state, "qualification-sources-"+provider+".json")
	if action == "seed" {
		if _, err = os.Lstat(intent); !errors.Is(err, os.ErrNotExist) {
			return errors.New("source custody intent already exists")
		}
		if err = requireNoWriters(homes); err != nil {
			return err
		}
		sources := []source{}
		identities := map[string]bool{}
		for index, home := range homes {
			credential, e := logins.ReadNativeCredential(provider, home)
			if e != nil {
				return e
			}
			r, e := store.Snapshot()
			if e != nil {
				return e
			}
			_, login, e := store.Add(r.Revision, provider, fmt.Sprintf("Qualification %c", 'A'+index), credential)
			if e != nil {
				return e
			}
			if identities[login.Identity] {
				return errors.New("two distinct native identities required")
			}
			identities[login.Identity] = true
			sources = append(sources, source{Provider: provider, ID: login.ID, Home: home})
		}
		body, _ := json.Marshal(sources)
		if err = writePrivate(intent, body); err != nil {
			return err
		}
		// Native authority is now the private saved login. Park the originals so
		// another native invocation cannot silently refresh a stale second copy.
		for _, s := range sources {
			path := filepath.Join(s.Home, authName(provider))
			if _, e := os.Lstat(path + ".qualification-held"); !errors.Is(e, os.ErrNotExist) {
				return errors.New("source already held")
			}
			if err = os.Rename(path, path+".qualification-held"); err != nil {
				return err
			}
		}
		fmt.Println(`{"seeded":true,"distinct_logins":2,"source_credentials_parked":true}`)
		return nil
	}
	if action != "restore" {
		return errors.New("unknown action")
	}
	body, err := os.ReadFile(intent)
	if err != nil {
		return err
	}
	var sources []source
	if json.Unmarshal(body, &sources) != nil || len(sources) != 2 {
		return errors.New("invalid source intent")
	}
	homes = nil
	for _, s := range sources {
		homes = append(homes, s.Home)
	}
	protected, err := store.ProtectedHomes(provider)
	if err != nil {
		return err
	}
	homes = append(homes, protected...)
	if err = requireNoWriters(homes); err != nil {
		return err
	}
	if err = store.Reconcile(); err != nil {
		return err
	}
	r, err := store.Snapshot()
	if err != nil {
		return err
	}
	for _, s := range sources {
		var saved struct {
			Credential logins.Credential `json:"credential"`
		}
		raw, e := os.ReadFile(filepath.Join(state, "logins", "saved", s.ID, "credential.json"))
		if e != nil {
			return e
		}
		if json.Unmarshal(raw, &saved) != nil {
			return errors.New("invalid saved credential")
		}
		credential := saved.Credential
		if provider == logins.ProviderClaude {
			credential.Auth, e = os.ReadFile(filepath.Join(state, "logins", "saved", s.ID, ".credentials.json"))
			if e != nil {
				return e
			}
		} else if native, e := logins.ReadNativeCredential(provider, store.Home(provider)); e == nil {
			id, e := logins.CodexIdentity(native.Auth)
			if e != nil {
				return e
			}
			for _, l := range r.Logins {
				if l.ID == s.ID && l.Identity == id {
					credential = native
				}
			}
		}
		path := filepath.Join(s.Home, authName(provider))
		held := path + ".qualification-held"
		original, e := os.ReadFile(held)
		if errors.Is(e, os.ErrNotExist) {
			original, e = os.ReadFile(path)
		}
		if e != nil {
			return e
		}
		oldID, newID := "", ""
		if provider == logins.ProviderCodex {
			oldID, e = logins.CodexIdentity(original)
			if e == nil {
				newID, e = logins.CodexIdentity(credential.Auth)
			}
		} else {
			identity, readErr := os.ReadFile(filepath.Join(s.Home, ".claude.json"))
			if readErr != nil {
				return readErr
			}
			oldID, e = logins.ClaudeIdentity(identity)
			if e == nil {
				newID, e = logins.ClaudeIdentity(credential.Identity)
			}
		}
		if e != nil || oldID == "" || oldID != newID {
			return errors.New("restoration identity mismatch")
		}
		var oldFields, newFields map[string]json.RawMessage
		if json.Unmarshal(original, &oldFields) != nil || json.Unmarshal(credential.Auth, &newFields) != nil {
			return errors.New("invalid restoration auth")
		}
		for k, v := range newFields {
			oldFields[k] = v
		}
		restored, _ := json.Marshal(oldFields)
		if err = writePrivate(path, restored); err != nil {
			return err
		}
		if err = os.Remove(held); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	fmt.Println(`{"restored":true,"latest_credentials_remain_vm_local":true,"source_writers_stopped":true}`)
	return nil
}
