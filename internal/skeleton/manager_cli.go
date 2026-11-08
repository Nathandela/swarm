package skeleton

import (
	"errors"

	"github.com/Nathandela/swarm/internal/adapter/codex"
	"github.com/Nathandela/swarm/internal/claudeinput"
	"github.com/Nathandela/swarm/internal/daemon"
)

// Manager qualification precedes managed-account environment preparation.
// The later ordinary identity probe still catches installation drift before spawn.
func qualifyManagerLaunchCLI(spec *daemon.LaunchSpec) error {
	if spec.Manager == nil {
		return nil
	}
	if spec.Manager.UnsupportedOrigin || !daemon.ManagerNativeEnvironmentSupported(spec.AgentType, spec.ClientEnv) {
		return errors.New("manager native account configuration is unsupported; select its native CLI account without credential or backend overrides")
	}
	refuse := errors.New("manager requires a qualified native-turn runtime before native account preparation")
	if len(spec.Argv) == 0 {
		return refuse
	}
	observed, err := probeCLIIdentity(spec.AgentType, spec.Argv[0], spec.ClientEnv, spec.Cwd)
	if err != nil || observed == nil {
		return refuse
	}
	qualified := false
	switch spec.AgentType {
	case "codex":
		qualified = codex.SupportsNativeTurns(observed.Version)
	case "claude":
		qualified = claudeinput.SupportsNativeTurns(observed.Version)
	}
	if !qualified {
		return refuse
	}
	if expected := spec.ExpectedCLIIdentity; expected != nil && *expected != *observed {
		return errors.New("launch: CLI installation changed before refresh")
	}
	spec.CLIIdentity = observed
	return nil
}
