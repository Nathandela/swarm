package skeleton

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/adapter/claude"
	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/logins"
	"github.com/Nathandela/swarm/internal/persist"
)

func (a *coreAPI) bindLoginLaunch(spec *daemon.LaunchSpec) error {
	if !managedLoginProvider(spec.AgentType) {
		if spec.LoginRuntime != nil {
			return logins.ErrIneligible
		}
		return nil
	}
	if a.logins == nil {
		return nil
	}
	var runtime logins.Runtime
	var err error
	if spec.LoginSwitchID != "" {
		runtime, err = a.logins.store.RuntimeForSwitch(spec.LoginSwitchID, spec.LoginSwitchSource)
		if err == nil {
			err = a.logins.store.ValidateSwitchRestart(spec.LoginSwitchID, spec.LoginSwitchSource, runtime)
		}
	} else {
		r, e := a.logins.store.Snapshot()
		if e != nil {
			return e
		}
		if r.MigrationPending {
			return logins.ErrPending
		}
		if _, ok := r.Switches[spec.AgentType]; ok {
			return logins.ErrPending
		}
		if r.Providers[spec.AgentType].ActiveID == "" {
			spec.LoginRuntime = nil
			return nil
		}
		for _, login := range r.Logins {
			if login.ID == r.Providers[spec.AgentType].ActiveID && login.LimitKnown && (login.ExhaustedUntil.IsZero() || time.Now().Before(login.ExhaustedUntil)) {
				return logins.ErrNoEligible
			}
		}
		runtime, err = a.logins.store.Runtime(spec.AgentType)
	}
	if err != nil {
		return err
	}
	spec.LoginRuntime = &runtime
	spec.LoginStateRoot = a.logins.d.stateDir
	spec.AuthIdentity = runtime.Identity
	if spec.LoginSwitchID != "" {
		spec.ClientEnv, err = a.logins.store.PrepareSwitchEnvironment(spec.LoginSwitchID, spec.LoginSwitchSource, runtime, spec.ClientEnv)
	} else {
		spec.ClientEnv, err = a.logins.store.PrepareEnvironment(runtime, spec.ClientEnv)
	}
	return err
}
func (d *Daemon) prepareLoginLaunch(_ string, spec daemon.LaunchSpec) (daemon.LaunchSpec, error) {
	if spec.LoginRuntime == nil {
		return spec, nil
	}
	if d.logins == nil {
		return spec, logins.ErrIneligible
	}
	var err error
	if spec.LoginSwitchID != "" {
		err = d.logins.store.ValidateSwitchRestart(spec.LoginSwitchID, spec.LoginSwitchSource, *spec.LoginRuntime)
	} else {
		err = d.logins.store.ValidateRuntime(*spec.LoginRuntime)
	}
	if err != nil {
		return spec, err
	}
	if err = logins.ValidateProjectAuthEnv(spec.AgentType, spec.Cwd, spec.ClientEnv); err != nil {
		return spec, err
	}
	if err = logins.ValidateArgsAuth(spec.AgentType, spec.Argv); err != nil {
		return spec, err
	}
	if spec.AgentType == logins.ProviderClaude {
		spec.Argv, err = loginClaudeObserverArgs(spec.Argv)
		if err != nil {
			return spec, err
		}
		for _, event := range loginClaudeObserverEvents {
			seen := false
			for _, present := range spec.CaptureEvents {
				seen = seen || present == event
			}
			if !seen {
				spec.CaptureEvents = append(spec.CaptureEvents, event)
			}
		}
	}
	return spec, nil
}

var loginClaudeObserverEvents = []string{"SessionStart", "StopFailure"}

func loginClaudeObserverSources() []adapter.SignalSource {
	var out []adapter.SignalSource
	for _, source := range claude.LoginObservations().SignalSources() {
		for _, event := range loginClaudeObserverEvents {
			if source.Descriptor["event"] == event {
				out = append(out, source)
			}
		}
	}
	return out
}
func loginClaudeObserverArgs(argv []string) ([]string, error) {
	out := append([]string(nil), argv...)
	for i := 1; i+1 < len(out); i++ {
		if out[i] != "--settings" {
			continue
		}
		var settings map[string]json.RawMessage
		if json.Unmarshal([]byte(out[i+1]), &settings) != nil || settings == nil {
			return nil, errors.New("login: invalid native observer settings")
		}
		var hooks map[string]json.RawMessage
		if json.Unmarshal(settings["hooks"], &hooks) != nil || hooks == nil {
			return nil, errors.New("login: missing native observer hooks")
		}
		for _, event := range loginClaudeObserverEvents {
			entry, err := json.Marshal([]map[string]any{{"hooks": []map[string]string{{"type": "command", "command": "swarm hook " + event}}}})
			if err != nil {
				return nil, err
			}
			hooks[event] = entry
		}
		settings["hooks"], _ = json.Marshal(hooks)
		raw, err := json.Marshal(settings)
		if err != nil {
			return nil, err
		}
		out[i+1] = string(raw)
		return out, nil
	}
	return nil, fmt.Errorf("login: native observer settings unavailable")
}

type loginResumeHistoryResolver struct {
	fallback resumeHistoryResolver
	stateDir string
}

func (r *loginResumeHistoryResolver) selected(meta persist.Meta) resumeHistoryResolver {
	if meta.LoginRuntime == nil {
		return r.fallback
	}
	resolver := newFilesystemResumeHistoryResolver(meta.LoginRuntime.Home, defaultResumeHistoryLimits)
	resolver.privateProvider = meta.AgentType
	resolver.stateDir = r.stateDir
	return resolver
}
func (r *loginResumeHistoryResolver) Resolve(meta persist.Meta) resumeHistoryResult {
	return r.selected(meta).Resolve(meta)
}
func (r *loginResumeHistoryResolver) LocateTranscript(meta persist.Meta, id string) (string, resumeHistoryOutcome) {
	return r.selected(meta).LocateTranscript(meta, id)
}
