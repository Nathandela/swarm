package shim

import (
	"encoding/binary"
	"encoding/hex"
	"errors"
	"strings"

	"github.com/Nathandela/swarm/internal/accountconfig"
	"github.com/Nathandela/swarm/internal/accounts"
	"github.com/Nathandela/swarm/internal/persist"
)

var errAccountResolution = errors.New("shim: private account binding could not be resolved")

func resolveAccountEnvironment(cfg *Config) error {
	if cfg.AccountBinding == nil {
		if cfg.AccountStateRoot != "" {
			return errAccountResolution
		}
		return nil
	}
	if cfg.AccountStateRoot == "" {
		return errAccountResolution
	}
	if cfg.CLIIdentity == nil || !accountconfig.SupportedNativeVersion(cfg.AccountBinding.Provider, cfg.CLIIdentity.Version) || len(cfg.Argv) == 0 || cfg.CLIIdentity.Path != cfg.Argv[0] {
		return errAccountResolution
	}
	if !persist.MatchCLIFingerprint(cfg.CLIIdentity.Path, cfg.CLIIdentity.Fingerprint) {
		return errAccountResolution
	}
	resolved, err := accounts.ResolveBoundEnvironment(cfg.AccountStateRoot, *cfg.AccountBinding, cfg.Env)
	if err != nil {
		return errAccountResolution
	}
	refBytes, err := hex.DecodeString(cfg.AccountProjectionRef)
	if err != nil || len(refBytes) != 32 {
		return errAccountResolution
	}
	generation := binary.BigEndian.Uint64(refBytes[:8])
	if generation == 0 {
		generation = 1
	}
	if generation != cfg.AccountBinding.ConfigurationGeneration {
		return errAccountResolution
	}
	profile := ""
	for _, value := range resolved {
		if cfg.AccountBinding.Provider == accounts.ProviderCodex && strings.HasPrefix(value, "CODEX_HOME=") {
			profile = strings.TrimPrefix(value, "CODEX_HOME=")
		}
		if cfg.AccountBinding.Provider == accounts.ProviderClaude && strings.HasPrefix(value, "CLAUDE_CONFIG_DIR=") {
			profile = strings.TrimPrefix(value, "CLAUDE_CONFIG_DIR=")
		}
	}
	if profile == "" || accountconfig.Revalidate(cfg.AccountStateRoot, cfg.AccountProjectionRef, cfg.AccountBinding.Provider, profile, cfg.Cwd) != nil {
		return errAccountResolution
	}
	if cfg.Backend != nil {
		// Preserve backend-specific harmless environment while sharing the one
		// resolved provider selection. Credentials are read exactly once above.
		base := cfg.Backend.Env
		if len(base) == 0 {
			base = cfg.Env
		}
		base, err = accounts.ScrubEnvironment(base)
		if err != nil {
			return errAccountResolution
		}
		for _, value := range resolved {
			key, _, _ := strings.Cut(value, "=")
			switch key {
			case "CODEX_HOME", "CLAUDE_CONFIG_DIR", "CLAUDE_SECURESTORAGE_CONFIG_DIR", "CLAUDE_CODE_OAUTH_TOKEN":
				base = append(base, value)
			}
		}
		cfg.Backend.Env = base
	}
	cfg.Env = resolved
	return nil
}
