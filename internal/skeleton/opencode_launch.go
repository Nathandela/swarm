package skeleton

import (
	"errors"
	"path/filepath"
	"strings"

	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/protocol"
)

func (a *coreAPI) captureOpenCodeOrigin(spec *daemon.LaunchSpec) error {
	if spec.AgentType != "opencode" || a.fakeAgentBin != "" {
		return nil
	}
	var err error
	if sourceID := spec.Options[protocol.OptionResumeFrom]; sourceID != "" {
		local, source, e := validateResumeSource(sourceID, spec.AgentType, a.endpointID, a.core.Get)
		if e != nil {
			return e
		}
		if source.RequiredRuntime != persist.RequiredRuntimeOpenCode || source.OpenCodeBinding == nil || source.OpenCodeBinding.NativeCreatedAtMS <= 0 {
			return errors.New("OpenCode runtime: resume requires a durable native binding")
		}
		cwd, e := filepath.EvalSymlinks(source.ProviderCwd())
		if e != nil {
			return errors.New("OpenCode runtime: saved native directory unavailable")
		}
		requested, e := filepath.EvalSymlinks(spec.Cwd)
		if spec.Cwd != "" && (e != nil || requested != cwd) {
			return errors.New("OpenCode runtime: resume cannot change the native working directory")
		}
		spec.Cwd = cwd
		spec.OpenCodeBinding = persist.CloneOpenCodeBinding(source.OpenCodeBinding)
		spec.OpenCodeEnv, err = a.core.OpenCodeResumeEnvironment(local)
	} else {
		origin := spec.OpenCodeEnv
		if origin == nil {
			origin = spec.ClientEnv
		}
		spec.OpenCodeEnv, err = a.core.OpenCodeEnvironment(origin)
	}
	return err
}

func (a *coreAPI) qualifyOpenCodeLaunch(spec *daemon.LaunchSpec) error {
	if spec.AgentType != "opencode" || a.fakeAgentBin != "" {
		return nil
	}
	if spec.ConversationID != "" && (spec.OpenCodeBinding == nil || spec.OpenCodeBinding.NativeCreatedAtMS <= 0) {
		return errors.New("OpenCode runtime: resume requires the exact durable native identity")
	}
	if len(spec.Argv) == 0 {
		return errors.New("OpenCode runtime: native executable unavailable")
	}
	env := append([]string(nil), spec.ClientEnv...)
	for _, entry := range spec.OpenCodeEnv {
		key, _, _ := strings.Cut(entry, "=")
		filtered := env[:0]
		for _, old := range env {
			if !strings.HasPrefix(old, key+"=") {
				filtered = append(filtered, old)
			}
		}
		env = append(filtered, entry)
	}
	identity, err := probeCLIIdentity("opencode", spec.Argv[0], env, spec.Cwd)
	if err != nil || identity == nil || identity.Version != "1.19.3" {
		return errors.New("OpenCode runtime: native version 1.19.3 is required")
	}
	if expected := spec.ExpectedCLIIdentity; expected != nil && *expected != *identity {
		return errors.New("OpenCode runtime: native installation changed")
	}
	spec.RequiredRuntime = persist.RequiredRuntimeOpenCode
	spec.CLIIdentity, spec.ExpectedCLIIdentity = identity, identity
	return nil
}
