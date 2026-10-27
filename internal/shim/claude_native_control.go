package shim

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"

	"github.com/Nathandela/swarm/internal/claudeinput"
	"github.com/Nathandela/swarm/internal/hookclient"

	"github.com/Nathandela/swarm/internal/logins"
	"github.com/Nathandela/swarm/internal/persist"
)

// ErrClaudeNativeControlDormant prevents birth before durable ingress is wired.
var ErrClaudeNativeControlDormant = errors.New("shim: Claude native control ingress is not enabled")

func attachClaudeControlSpool(cfg Config, broker *claudeinput.Broker, hooks *hookServer) error {
	if cfg.RequiredRuntime != persist.RequiredRuntimeClaudeNativeControl || broker == nil || hooks == nil || hooks.spool == nil {
		return errors.New("shim: native control spool unavailable")
	}
	values := map[string]string{}
	for _, entry := range cfg.Env {
		key, value, ok := strings.Cut(entry, "=")
		if ok && (key == hookclient.EnvSessionID || key == hookclient.EnvToken) {
			if _, duplicate := values[key]; duplicate {
				return errors.New("shim: ambiguous native control hook identity")
			}
			values[key] = value
		}
	}
	if values[hookclient.EnvSessionID] != cfg.SessionID || len(values[hookclient.EnvToken]) != 64 || broker.Descriptor().SessionID != cfg.SessionID {
		return errors.New("shim: native control hook identity unavailable")
	}
	return broker.SetControlSink(func(envelope claudeinput.ControlEnvelope) (uint64, error) {
		envelope.Token = values[hookclient.EnvToken]
		raw, err := json.Marshal(envelope)
		if err != nil {
			return 0, err
		}
		return hooks.spool.Append(raw)
	})
}

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
