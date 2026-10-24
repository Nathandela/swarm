package shim

import (
	"errors"
	"strings"

	"github.com/Nathandela/swarm/internal/logins"
	"github.com/Nathandela/swarm/internal/persist"
)

var errLoginResolution = errors.New("shim: provider login runtime is unavailable or changing")

func resolveLoginEnvironment(cfg *Config) error {
	if cfg.LoginRuntime == nil {
		if cfg.LoginStateRoot != "" || cfg.LoginSwitchID != "" || cfg.LoginSwitchSource != "" {
			return errLoginResolution
		}
		return nil
	}
	if cfg.LoginStateRoot == "" || cfg.NativeProvider != cfg.LoginRuntime.Provider || len(cfg.Argv) == 0 {
		return errLoginResolution
	}
	if cfg.CLIIdentity != nil && persist.IsCLIContentFingerprint(cfg.CLIIdentity.Fingerprint) && (!persist.MatchCLIFingerprint(cfg.CLIIdentity.Path, cfg.CLIIdentity.Fingerprint) || cfg.CLIIdentity.Path != cfg.Argv[0]) {
		return errLoginResolution
	}
	store, err := logins.OpenReadOnly(cfg.LoginStateRoot)
	if err != nil {
		return errLoginResolution
	}
	defer func() { _ = store.Close() }()
	externalBirth := cfg.LoginRuntime.ExternalAuth && cfg.LoginSwitchID == ""
	if externalBirth {
		_, err = store.PrepareCodexExternalBirth(*cfg.LoginRuntime, nil)
	} else {
		err = store.ValidateRuntime(*cfg.LoginRuntime)
	}
	if err != nil && cfg.LoginSwitchID != "" {
		err = store.ValidateSwitchRestart(cfg.LoginSwitchID, cfg.LoginSwitchSource, *cfg.LoginRuntime)
	}

	if err != nil {
		return errLoginResolution
	}
	if logins.ValidateProjectAuthEnv(cfg.LoginRuntime.Provider, cfg.Cwd, cfg.Env) != nil || logins.ValidateArgsAuth(cfg.LoginRuntime.Provider, cfg.Argv) != nil {
		return errLoginResolution
	}
	if cfg.Backend != nil && logins.ValidateArgsAuth(cfg.LoginRuntime.Provider, cfg.Backend.Args) != nil {
		return errLoginResolution
	}
	if externalBirth {
		cfg.Env, err = store.PrepareCodexExternalBirth(*cfg.LoginRuntime, cfg.Env)
	} else if cfg.LoginSwitchID != "" && store.ValidateRuntime(*cfg.LoginRuntime) != nil {
		cfg.Env, err = store.PrepareSwitchEnvironment(cfg.LoginSwitchID, cfg.LoginSwitchSource, *cfg.LoginRuntime, cfg.Env)
	} else {
		cfg.Env, err = store.PrepareEnvironment(*cfg.LoginRuntime, cfg.Env)
	}
	if err != nil {
		return errLoginResolution
	}
	if cfg.Backend != nil {
		base := cfg.Backend.Env
		if len(base) == 0 {
			base = cfg.Env
		}
		if externalBirth {
			cfg.Backend.Env, err = store.PrepareCodexExternalBirth(*cfg.LoginRuntime, base)
		} else if cfg.LoginSwitchID != "" && store.ValidateRuntime(*cfg.LoginRuntime) != nil {
			cfg.Backend.Env, err = store.PrepareSwitchEnvironment(cfg.LoginSwitchID, cfg.LoginSwitchSource, *cfg.LoginRuntime, base)
		} else {
			cfg.Backend.Env, err = store.PrepareEnvironment(*cfg.LoginRuntime, base)
		}
		if err != nil {
			return errLoginResolution
		}
	}
	// Runtime routing must not be shadowed by a provider home in argv. Native
	// config validation belongs to the store's characterized credential adapter.
	for _, arg := range cfg.Argv[1:] {
		if strings.ContainsRune(arg, '\x00') {
			return errLoginResolution
		}
	}
	return nil
}
