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
	origin := spec.ClientEnv
	if origin == nil {
		origin = daemonEnviron()
	}
	var origins []string
	for _, entry := range origin {
		key, _, _ := strings.Cut(entry, "=")
		if key == "HERMES_HOME" || key == "HERMES_GATEWAY_LOCK_DIR" || key == "XDG_STATE_HOME" {
			origins = append(origins, entry)
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
	return &hermesinput.SessionConfig{Program: spec.Argv[0], ProviderVersion: spec.CLIIdentity.Version, Profile: profile, Cwd: spec.Cwd, Options: spec.Options, Origins: origins, ResumeID: spec.ConversationID, InitialPrompt: spec.InitialPrompt}, nil
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
