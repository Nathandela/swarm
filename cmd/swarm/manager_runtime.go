package main

import (
	"fmt"
	"github.com/Nathandela/swarm/internal/adapter/codex"

	"github.com/Nathandela/swarm/internal/claudeinput"
)

func validateManagerShimRuntime(lc shimLaunchConfig, required bool) error {
	if !required {
		if lc.BackendRequired {
			return fmt.Errorf("manager launch requires matching --require-manager and runtime configuration")
		}
		return nil
	}
	switch lc.NativeProvider {
	case "codex":
		if !lc.BackendRequired || lc.BackendProgram == "" || lc.BackendSocketPath == "" || lc.CLIIdentity == nil || !codex.SupportsNativeTurns(lc.CLIIdentity.Version) {
			return fmt.Errorf("manager launch requires its native Codex backend")
		}
	case "claude":
		if !lc.ClaudeInput || lc.CLIIdentity == nil || !claudeinput.SupportsNativeTurns(lc.CLIIdentity.Version) {
			return fmt.Errorf("manager launch requires a qualified Claude native-turn broker")
		}
	default:
		return fmt.Errorf("manager launch requires a supported native runtime")
	}
	return nil
}
