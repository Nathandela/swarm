package shim

import (
	"errors"
	"path/filepath"

	"github.com/Nathandela/swarm/internal/logins"
	"github.com/Nathandela/swarm/internal/persist"
)

// ErrClaudeNativeControlDormant prevents birth before durable ingress is wired.
var ErrClaudeNativeControlDormant = errors.New("shim: Claude native control ingress is not enabled")

// ValidateClaudeNativeControlConfig validates carriage, never launch authority.
func ValidateClaudeNativeControlConfig(cfg Config) error {
	if cfg.RequiredRuntime != persist.RequiredRuntimeClaudeNativeControl {
		return nil
	}
	cli := cfg.CLIIdentity
	if !persist.ValidID(cfg.SessionID) || !filepath.IsAbs(cfg.LoginStateRoot) || filepath.Clean(cfg.LoginStateRoot) != cfg.LoginStateRoot || cfg.SessionDir != filepath.Join(cfg.LoginStateRoot, cfg.SessionID) || cfg.LoginRuntime == nil || !logins.IsClaudeProfileRuntime(cfg.LoginStateRoot, *cfg.LoginRuntime) || cfg.NativeProvider != logins.ProviderClaude || cli == nil || cli.Version != "2.1.310" || !filepath.IsAbs(cli.Path) || filepath.Clean(cli.Path) != cli.Path || !logins.IsClaudeCLIFingerprint(cli.Fingerprint) || len(cfg.Argv) == 0 || cfg.Argv[0] != cli.Path || !filepath.IsAbs(cfg.Cwd) || filepath.Clean(cfg.Cwd) != cfg.Cwd || cfg.ClaudeChatFixture != "" || cfg.Backend != nil || cfg.OpenCode != nil || cfg.HermesInput != nil || cfg.AgyInput != nil || cfg.InputEmbargo != "" || cfg.InputEmbargoToken != "" || cfg.LoginSwitchID != "" || cfg.LoginSwitchSource != "" {
		return errors.New("shim: invalid Claude native control configuration")
	}
	return nil
}
