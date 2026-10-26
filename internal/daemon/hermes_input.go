package daemon

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Nathandela/swarm/internal/hermesinput"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/status"
)

func (d *Daemon) hermesLaunchConfig(spec LaunchSpec) (*hermesinput.SessionConfig, error) {
	if spec.AgentType != "hermes" {
		return nil, nil
	}
	source, hasSource := d.Get(spec.ResumedFrom)
	nativeResume := hasSource && source.HermesNative
	enabled := d.cfg.NativeInputEnabled != nil && d.cfg.NativeInputEnabled()
	if !nativeResume && !enabled {
		return nil, nil
	}
	if spec.CLIIdentity == nil || !hermesinput.SupportedVersion(spec.CLIIdentity.Version) || len(spec.Argv) == 0 {
		if nativeResume {
			return nil, errors.New("hermes native resume requires a qualified current runtime")
		}
		return nil, nil
	}
	origins := spec.HermesOrigins
	if origins == nil {
		origins = d.HermesConfigurationEnvironment(spec.ClientEnv)
	}
	for _, entry := range origins {
		if entry == "SWARM_HERMES_UNSUPPORTED_ORIGIN=1" {
			return nil, errors.New("hermes native input requires a local provider home; custom remote transports are unsupported")
		}
	}
	profile := spec.Options["profile"]
	if nativeResume {
		desc, err := d.hermesDescriptor(source.ID, false)
		if err != nil || source.ConversationID == "" || spec.ConversationID != source.ConversationID {
			return nil, errors.New("hermes native resume binding is unproven; retained source was not changed")
		}
		if profile != "" && profile != source.LaunchOptions["profile"] {
			return nil, errors.New("hermes native resume cannot change its profile")
		}
		profile = source.LaunchOptions["profile"]
		origins = append(origins, "HERMES_HOME="+desc.Binding.Home)
	}
	cwd := spec.Cwd
	if nativeResume {
		desc, err := d.hermesDescriptor(source.ID, false)
		if err != nil || desc.Binding.Directory == "" {
			return nil, errors.New("hermes native resume cwd is unproven")
		}
		if spec.Cwd != source.Cwd && spec.Cwd != source.ProviderCwd() {
			return nil, errors.New("hermes native resume cannot change its working directory")
		}
		cwd = desc.Binding.Directory
	}
	return &hermesinput.SessionConfig{Program: spec.Argv[0], ProviderVersion: spec.CLIIdentity.Version, Profile: profile, Cwd: cwd, Options: spec.Options, Origins: origins, ResumeID: spec.ConversationID, InitialPrompt: spec.InitialPrompt}, nil
}

func (d *Daemon) HermesNativeRequested(spec LaunchSpec) bool {
	if spec.AgentType != "hermes" {
		return false
	}
	if source, ok := d.Get(spec.ResumedFrom); ok && source.HermesNative {
		return true
	}
	return d.cfg.NativeInputEnabled != nil && d.cfg.NativeInputEnabled()
}

func HermesConfigurationEnvironment(env []string) []string {
	out := make([]string, 0)
	for _, entry := range env {
		key, value, _ := strings.Cut(entry, "=")
		switch key {
		case "HERMES_HOME", "HERMES_GATEWAY_LOCK_DIR", "XDG_STATE_HOME":
			out = append(out, entry)
		case "HERMES_TUI_GATEWAY_URL", "HERMES_TUI_SIDECAR_URL":
			if value != "" {
				out = append(out, "SWARM_HERMES_UNSUPPORTED_ORIGIN=1")
			}
		case "SWARM_HERMES_UNSUPPORTED_ORIGIN":
			out = append(out, "SWARM_HERMES_UNSUPPORTED_ORIGIN=1")
		}
	}
	return out
}

func (d *Daemon) HermesConfigurationEnvironment(env []string) []string {
	if env == nil {
		env = d.savedEnv
		if len(env) == 0 {
			env = daemonEnviron()
		}
	}
	return HermesConfigurationEnvironment(env)
}

func (d *Daemon) hermesDescriptor(id string, live bool) (hermesinput.Descriptor, error) {
	meta, ok := d.Get(id)
	if !ok || !meta.HermesNative || meta.AgentType != "hermes" || meta.CLIIdentity == nil {
		return hermesinput.Descriptor{}, errors.New("hermes native input requires an explicitly refreshed session")
	}
	desc, err := hermesinput.ReadDescriptor(d.sessionDir(id))
	if err != nil || desc.WorkerID != id || desc.ShimPID != meta.ShimPID || desc.ShimStartTime != meta.ShimStartTime || desc.ProviderVersion != meta.CLIIdentity.Version || meta.ConversationID != "" && desc.Binding.SessionID != meta.ConversationID || live && !d.shimIdentityMatches(meta) {
		return hermesinput.Descriptor{}, errors.New("hermes native input binding is unproven; refresh this session")
	}
	return desc, nil
}

func (d *Daemon) SessionHermesInput(id string) (hermesinput.Descriptor, error) {
	return d.hermesDescriptor(id, true)
}

func (d *Daemon) joinHermes(id string) error {
	desc, err := d.SessionHermesInput(id)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err = hermesinput.Validate(ctx, desc.Binding); err != nil {
		return err
	}
	return d.SetConversationID(id, desc.Binding.SessionID)
}

func (d *Daemon) stopHermes(id string) error {
	desc, err := d.hermesDescriptor(id, false)
	if err != nil {
		return err
	}
	if err = hermesinput.WriteStopIntent(d.sessionDir(id), desc); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err = hermesinput.Stop(ctx, desc.Binding); err != nil {
		return fmt.Errorf("hermes native session stop is unconfirmed: %w", err)
	}
	return nil
}

func (d *Daemon) SetHermesStatus(id, generation string, s status.Status) error {
	desc, err := d.SessionHermesInput(id)
	if err != nil || desc.Generation != generation {
		return errors.New("daemon: stale Hermes native status")
	}
	return d.setStatus(id, s, "hermes:"+generation)
}

func protectHermesNative(m persist.Meta) persist.Meta {
	if m.HermesNative && m.Status.Process != status.ProcessRunning {
		m.Status = status.Status{Process: status.ProcessLost, Turn: status.TurnUnknown, Interaction: status.InteractionError, Attention: true, NativeWorkUnknown: true}
		m.ResumeError = "Hermes viewer is unavailable; native job completion is unproven and the workspace is retained"
	}
	return m
}
