package skeleton

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"time"

	"github.com/Nathandela/swarm/internal/accountcheck"
	"github.com/Nathandela/swarm/internal/accountconfig"
	"github.com/Nathandela/swarm/internal/accounts"
	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/adapter/claude"
	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/status"
)

var errAccountLaunch = errors.New("accounts: private account or configuration is unavailable; review Accounts before resuming")

// This seam covers every assembled launch. Existing discussions retain their
// frozen account; enrollment and pool toggles never rebind a running process.
// tried names accounts that already refused this launch; selection skips them.
func (a *coreAPI) bindAccountLaunch(spec *daemon.LaunchSpec, tried ...accountRefusal) error {
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
		// An explicit owner resume passes the existing ended-source/history and
		// duplicate-writer guards before reaching here. Enroll only its new child;
		// the retained legacy source gains neither a binding nor stopped proof.
		registry, err := store.Snapshot()
		if err != nil {
			return errAccountLaunch
		}
		// An automated refresh resume (ExpectedCLIIdentity) stays an owner launch:
		// rotation alone never onboards a discussion (ADR-032 D6). Enrollment
		// selects its destination without resume_from and is unaffected.
		if !registry.Enabled[spec.AgentType] || spec.ExpectedCLIIdentity != nil && spec.Options[protocol.OptionResumeFrom] != "" {
			return nil
		}
		counts := map[string]int{}
		for _, m := range a.core.List() {
			if m.AccountBinding != nil && m.Status.Process == status.ProcessRunning {
				counts[m.AccountBinding.AccountID]++
			}
		}
		for _, refusal := range tried {
			delete(registry.Accounts, refusal.id)
		}
		now := time.Now()
		selection, err := accounts.SelectInitial(registry, accounts.SelectionRequest{Provider: spec.AgentType, Model: spec.Options["model"], ConfigurationGeneration: 1, ActiveCounts: counts}, now)
		if err != nil {
			return noAccountLeft(spec.AgentType, registry, tried, now)
		}
		binding := selection.Binding
		spec.AccountBinding = &binding
	}
	if spec.AccountBinding.Provider != spec.AgentType || store.ValidateBinding(*spec.AccountBinding) != nil {
		return errAccountLaunch
	}
	spec.AccountStateRoot = a.accounts.stateRoot
	spec.AuthIdentity = spec.AccountBinding.Identity
	return nil
}

// prepareAccountLaunch runs after worktree resolution and before the first
// persisted reservation or child spawn, so both processes share the real cwd.
func (d *Daemon) prepareAccountLaunch(id string, spec daemon.LaunchSpec) (daemon.LaunchSpec, error) {
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
	if strings.HasPrefix(spec.CLIIdentity.Fingerprint, "sha256:") && !persist.MatchCLIFingerprint(spec.CLIIdentity.Path, spec.CLIIdentity.Fingerprint) {
		return spec, errAccountLaunch
	}
	if spec.AgentType == accounts.ProviderClaude && !accountconfig.ClaudeNativeContextVersion(spec.CLIIdentity.Version) {
		if spec.AccountProjectionRef == "" {
			return spec, accountconfig.Conflict("native-version-not-characterized")
		}
		native, err := accountconfig.HasNativeContext(d.accounts.stateRoot, spec.AccountProjectionRef)
		if err != nil || native {
			return spec, accountconfig.Conflict("native-version-not-characterized")
		}
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
	lease := func(globalGeneration string, install func() error) error {
		if accountconfig.MatchingInstalledContext(profile, spec.AgentType, globalGeneration) {
			return install()
		}
		// A refresh rewrites ordinary configuration the way the owner's own CLIs
		// do; other discussions on the account, live or not, never block it.
		return accountcheck.WithCredentialFence(d.accounts.stateRoot, *spec.AccountBinding, install)
	}
	var projection accountconfig.Projection
	originalEnv := append(append([]string(nil), spec.ClientEnv...), spec.AccountOriginalConfigurationEnv...)
	err = accountcheck.WithConfigurationFence(d.accounts.stateRoot, *spec.AccountBinding, func() error {
		var prepareErr error
		projection, prepareErr = accountconfig.PrepareNative(d.accounts.stateRoot, spec.AgentType, profile, spec.Cwd, originalEnv, spec.Argv, spec.AccountProjectionRef, spec.AccountNativeModel, lease, id != "")
		return prepareErr
	})
	if err != nil {
		return spec, err
	}

	binding := *spec.AccountBinding
	binding.ConfigurationGeneration = projection.Generation
	spec.AccountBinding = &binding
	spec.AccountProjectionRef = projection.Ref
	spec.AccountNativeContext = projection.NativeContext
	spec.AccountBackendArgs = projection.BackendArgs
	spec.Argv = projection.CLIArgs
	spec.ClientEnv = projection.HarmlessEnv
	if spec.AgentType == "claude" && projection.NativeContext {
		if id != "" {
			spec.ClientEnv = accountLaunchEnvValue(spec.ClientEnv, "CLAUDE_CODE_DIAGNOSTICS_FILE", filepath.Join(d.accounts.stateRoot, id, claudeDiagnosticsFile))
		}
		policy := &persist.ClaudeFallbackPolicy{RecoveryPinned: spec.AccountNativeModel != ""}
		for _, item := range originalEnv {
			if value, found := strings.CutPrefix(item, "CLAUDE_CODE_NO_MODEL_FALLBACK="); found {
				policy.OwnerValue = &value
			}
		}
		if policy.RecoveryPinned || policy.OwnerValue != nil {
			spec.AccountClaudeFallback = policy
		}
		if policy.RecoveryPinned {
			spec.ClientEnv = accountLaunchEnvValue(spec.ClientEnv, "CLAUDE_CODE_NO_MODEL_FALLBACK", "true")
		}
	}
	return spec, nil
}

func restoreClaudeOwnerFallback(source persist.Meta, env []string) []string {
	if source.AccountClaudeFallback == nil {
		return env
	}
	out := make([]string, 0, len(env))
	for _, item := range env {
		if !strings.HasPrefix(item, "CLAUDE_CODE_NO_MODEL_FALLBACK=") {
			out = append(out, item)
		}
	}
	if source.AccountClaudeFallback.OwnerValue != nil {
		out = append(out, "CLAUDE_CODE_NO_MODEL_FALLBACK="+*source.AccountClaudeFallback.OwnerValue)
	}
	return out
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

// The owner's first managed resume must not certify an inherited launch model
// as the current model of an existing native conversation.
func (a *coreAPI) prepareLegacyAccountResume(spec daemon.LaunchSpec) (daemon.LaunchSpec, error) {
	if spec.AccountBinding != nil || a.accounts == nil || a.accounts.store == nil || spec.Options[protocol.OptionResumeFrom] == "" {
		return spec, nil
	}
	_, source, err := validateResumeSource(spec.Options[protocol.OptionResumeFrom], spec.AgentType, a.endpointID, a.core.Get)
	if err != nil {
		return spec, err
	}
	return a.prepareLegacyAccountResumeSource(spec, source)
}

func (a *coreAPI) prepareLegacyAccountResumeSource(spec daemon.LaunchSpec, source persist.Meta) (daemon.LaunchSpec, error) {
	var err error
	managedNative := false
	if source.AccountBinding != nil {
		if source.AccountProjectionRef == "" {
			return spec, errAccountLaunch
		}
		managedNative, err = accountconfig.HasNativeContext(a.accounts.stateRoot, source.AccountProjectionRef)
		if err != nil {
			return spec, err
		}
		if !managedNative {
			return spec, nil
		}
	}
	registry, err := a.accounts.store.Snapshot()
	if err != nil {
		return spec, errAccountLaunch
	}
	if !managedNative && !registry.Enabled[spec.AgentType] || spec.Options["model"] != "" {
		return spec, nil
	}
	options := make(map[string]string, len(spec.Options)+1)
	for k, v := range spec.Options {
		options[k] = v
	}
	spec.Options = options
	if spec.AgentType == "claude" {
		// An explicit empty request wins the inherited option merge. Native session
		// restore selects its model; recovery waits for authenticated exact evidence.
		spec.Options["model"] = ""
		return spec, nil
	}
	if spec.AgentType != "codex" {
		return spec, nil
	}
	var profile string
	if managedNative {
		profile, err = accountconfig.NativeHistoryAuthority(a.accounts.stateRoot, source.AccountProjectionRef, source.AgentType, source.ProviderCwd(), source.AccountBinding.ConfigurationGeneration)
	} else {
		profile, err = accountconfig.NativeConfigurationOrigin(spec.AgentType, spec.ClientEnv)
	}
	if err != nil || profile == "" {
		return spec, accountconfig.Conflict("native-resume-model-unavailable")
	}
	resolver := newFilesystemResumeHistoryResolver(profile, defaultResumeHistoryLimits)
	resolver.privateProvider = spec.AgentType
	path, outcome := resolver.LocateTranscript(source, source.ConversationID)
	if outcome != resumeHistoryFound {
		return spec, accountconfig.Conflict("native-resume-model-unavailable")
	}
	model, seen, err := readCodexTranscriptModel(profile, path)
	if err != nil || !seen || !exactAccountModel(model) {
		return spec, accountconfig.Conflict("native-resume-model-unavailable")
	}
	spec.Options["model"] = model
	return spec, nil
}

func accountLaunchEnvValue(env []string, key, value string) []string {
	out := make([]string, 0, len(env)+1)
	for _, item := range env {
		if !strings.HasPrefix(item, key+"=") {
			out = append(out, item)
		}
	}
	return append(out, key+"="+value)
}
