package daemon

import (
	"errors"
	"path/filepath"
	"slices"
	"strings"

	"github.com/Nathandela/swarm/internal/logins"
)

// This proof cannot be supplied through an owner launch request. It binds only
// selectors produced by the currently validated managed native account.
type managerLoginBinding struct {
	store                          *logins.Store
	runtime                        logins.Runtime
	stateRoot                      string
	environment, nativeEnvironment []string
}

// BindManagerLoginEnvironment seals the store-generated selectors for core admission.
func (spec *LaunchSpec) BindManagerLoginEnvironment(store *logins.Store) error {
	if spec.Manager == nil || spec.LoginRuntime == nil {
		return nil
	}
	refuse := errors.New("manager managed native account environment is unconfirmed")
	if store == nil || spec.LoginRuntime.Provider != spec.AgentType || spec.AuthIdentity != spec.LoginRuntime.Identity || spec.LoginStateRoot == "" || spec.LoginRuntime.Home != filepath.Join(spec.LoginStateRoot, "logins", "provider", spec.AgentType, "native") || store.ValidateRuntime(*spec.LoginRuntime) != nil {
		return refuse
	}
	generated, err := store.PrepareEnvironment(*spec.LoginRuntime, nil)
	if err != nil {
		return refuse
	}
	expected := map[string]string{}
	for _, entry := range generated {
		key, value, ok := strings.Cut(entry, "=")
		if !ok {
			return refuse
		}
		expected[key] = value
	}
	seen := map[string]bool{}
	native := make([]string, 0, len(spec.ClientEnv))
	for _, entry := range spec.ClientEnv {
		key, value, ok := strings.Cut(entry, "=")
		if want, generated := expected[key]; generated {
			if !ok || seen[key] || value != want {
				return refuse
			}
			seen[key] = true
			continue
		}
		native = append(native, entry)
	}
	if len(seen) != len(expected) || !ManagerNativeEnvironmentSupported(spec.AgentType, native) {
		return refuse
	}
	spec.managerLogin = &managerLoginBinding{store: store, runtime: *spec.LoginRuntime, stateRoot: spec.LoginStateRoot, environment: append([]string(nil), spec.ClientEnv...), nativeEnvironment: native}
	return nil
}

func (d *Daemon) checkedManagerLaunchEnvironment(spec LaunchSpec) ([]string, error) {
	refuse := errors.New("manager native account configuration is unsupported; select its native CLI account without credential or backend overrides")
	if spec.Manager.UnsupportedOrigin {
		return nil, refuse
	}
	env := spec.ClientEnv
	if spec.LoginRuntime != nil {
		proof := spec.managerLogin
		if proof == nil || proof.runtime != *spec.LoginRuntime || proof.runtime.Provider != spec.AgentType || spec.AuthIdentity != proof.runtime.Identity || proof.stateRoot != spec.LoginStateRoot || !slices.Equal(proof.environment, env) || proof.store.ValidateRuntime(proof.runtime) != nil {
			return nil, refuse
		}
		env = proof.nativeEnvironment
	}
	env = d.ManagerEnvironment(spec.AgentType, env)
	if !ManagerNativeEnvironmentSupported(spec.AgentType, env) {
		return nil, refuse
	}
	return env, nil
}
