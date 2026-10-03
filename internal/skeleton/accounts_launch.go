package skeleton

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/Nathandela/swarm/internal/accountconfig"
	"github.com/Nathandela/swarm/internal/accounts"
	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/adapter/claude"
	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/status"
)

var errAccountLaunch = errors.New("accounts: private account or configuration is unavailable; review Accounts before resuming")

// This seam covers every assembled launch. Existing discussions retain their
// frozen account; enrollment and pool toggles never rebind a running process.
func (a *coreAPI) bindAccountLaunch(spec *daemon.LaunchSpec) error {
	if spec.AgentType != "codex" && spec.AgentType != "claude" {
		if spec.AccountBinding != nil {
			return errAccountLaunch
		}
		return nil
	}
	if spec.AccountBinding == nil && spec.Options[protocol.OptionResumeFrom] != "" {
		_, source, err := validateResumeSource(spec.Options[protocol.OptionResumeFrom], spec.AgentType, a.endpointID, a.core.Get)
		if err != nil {
			return err
		}
		if source.AccountBinding != nil {
			binding := *source.AccountBinding
			spec.AccountBinding = &binding
			if spec.AccountProjectionRef == "" {
				spec.AccountProjectionRef = source.AccountProjectionRef
			}
		}
	}
	if a.accounts == nil || a.accounts.store == nil {
		if spec.AccountBinding != nil {
			return errAccountLaunch
		}
		return nil
	}
	store := a.accounts.store
	if spec.AccountBinding == nil {
		// Native imports and unmanaged resumes remain tied to their original history.
		if spec.Options[protocol.OptionResumeFrom] != "" || spec.Options[protocol.OptionResumeConversationID] != "" {
			return nil
		}
		registry, err := store.Snapshot()
		if err != nil {
			return errAccountLaunch
		}
		if !registry.Enabled[spec.AgentType] {
			return nil
		}
		counts := map[string]int{}
		for _, m := range a.core.List() {
			if m.AccountBinding != nil && m.Status.Process == status.ProcessRunning {
				counts[m.AccountBinding.AccountID]++
			}
		}
		selection, err := accounts.SelectInitial(registry, accounts.SelectionRequest{Provider: spec.AgentType, Model: spec.Options["model"], ConfigurationGeneration: 1, ActiveCounts: counts}, time.Now())
		if err != nil {
			return errAccountLaunch
		}
		binding := selection.Binding
		spec.AccountBinding = &binding
	}
	if spec.AccountBinding.Provider != spec.AgentType || store.ValidateBinding(*spec.AccountBinding) != nil {
		return errAccountLaunch
	}
	native := a.accounts.native[spec.AgentType]
	if native == nil || (spec.AgentType == "codex" && native.Version != "0.160.0") || (spec.AgentType == "claude" && native.Version != "2.1.288") {
		return errAccountLaunch
	}
	spec.AccountStateRoot = a.accounts.stateRoot
	spec.AuthIdentity = spec.AccountBinding.Identity
	return nil
}

// prepareAccountLaunch runs after worktree resolution and before the first
// persisted reservation or child spawn, so both processes share the real cwd.
func (d *Daemon) prepareAccountLaunch(_ string, spec daemon.LaunchSpec) (daemon.LaunchSpec, error) {
	if spec.AccountBinding == nil {
		return spec, nil
	}
	// The startup probe advertises enrollment methods, while this identity is
	// the binary resolved for this exact launch. A changed PATH or upgraded CLI
	// cannot inherit an old version's authentication/history guarantees.
	if spec.CLIIdentity == nil || !accountconfig.SupportedNativeVersion(spec.AgentType, spec.CLIIdentity.Version) {
		return spec, errAccountLaunch
	}
	if d.accounts == nil || d.accounts.store == nil {
		return spec, errAccountLaunch
	}
	profile, err := d.accounts.store.ProfilePath(*spec.AccountBinding)
	if err != nil {
		return spec, errAccountLaunch
	}
	if spec.AgentType == "claude" {
		spec.Argv, err = managedClaudeObserverArgs(spec.Argv)
		if err != nil {
			return spec, err
		}
		for _, event := range managedClaudeObserverEvents {
			found := false
			for _, existing := range spec.CaptureEvents {
				found = found || existing == event
			}
			if !found {
				spec.CaptureEvents = append(spec.CaptureEvents, event)
			}
		}
	}
	projection, err := accountconfig.PrepareWithModel(d.accounts.stateRoot, spec.AgentType, profile, spec.Cwd, spec.ClientEnv, spec.Argv, spec.AccountProjectionRef, spec.AccountNativeModel)
	if err != nil {
		return spec, err
	}
	binding := *spec.AccountBinding
	binding.ConfigurationGeneration = projection.Generation
	spec.AccountBinding = &binding
	spec.AccountProjectionRef = projection.Ref
	spec.AccountBackendArgs = projection.BackendArgs
	spec.Argv = projection.CLIArgs
	spec.ClientEnv = projection.HarmlessEnv
	return spec, nil
}

var managedClaudeObserverEvents = []string{"SessionStart", "StopFailure"}

func managedClaudeObserverSources() []adapter.SignalSource {
	sources := make([]adapter.SignalSource, 0, len(managedClaudeObserverEvents))
	for _, source := range claude.ManagedObservations().SignalSources() {
		for _, event := range managedClaudeObserverEvents {
			if source.Descriptor["event"] == event {
				sources = append(sources, source)
			}
		}
	}
	return sources
}

// The managed observations do not change the adapter's status vocabulary.
// They carry native conversation and failure evidence through the same
// authenticated hook channel, without changing unmanaged invocations.
func managedClaudeObserverArgs(argv []string) ([]string, error) {
	out := append([]string(nil), argv...)
	for i := 1; i+1 < len(out); i++ {
		if out[i] != "--settings" {
			continue
		}
		var settings map[string]json.RawMessage
		if json.Unmarshal([]byte(out[i+1]), &settings) != nil || settings == nil {
			return nil, errAccountLaunch
		}
		var hooks map[string]json.RawMessage
		if json.Unmarshal(settings["hooks"], &hooks) != nil || hooks == nil {
			return nil, errAccountLaunch
		}
		for _, event := range managedClaudeObserverEvents {
			entry, err := json.Marshal([]map[string]any{{"hooks": []map[string]string{{"type": "command", "command": "swarm hook " + event}}}})
			if err != nil {
				return nil, errAccountLaunch
			}
			hooks[event] = entry
		}
		settings["hooks"], _ = json.Marshal(hooks)
		data, err := json.Marshal(settings)
		if err != nil {
			return nil, errAccountLaunch
		}
		out[i+1] = string(data)
		return out, nil
	}
	return nil, errAccountLaunch
}
