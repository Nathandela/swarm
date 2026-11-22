package main

import (
	"testing"

	"github.com/Nathandela/swarm/internal/persist"
)

func TestManagerRequiredBackendCannotDecodeAsStandalone(t *testing.T) {
	for _, launch := range []shimLaunchConfig{{BackendRequired: true}, {BackendRequired: true, BackendProgram: "/trusted/codex", BackendSocketPath: "/private/session/codex.sock"}} {
		backend := backendConfigFromLaunch(launch)
		if backend == nil || !backend.Required {
			t.Fatal("required backend silently decoded as legacy standalone session")
		}
	}
	if backendConfigFromLaunch(shimLaunchConfig{}) != nil {
		t.Fatal("ordinary session gained a backend")
	}
}

func TestManagerShimCodexRequiresMinimumRuntime(t *testing.T) {
	for version, want := range map[string]bool{
		"nil": false, "": false, "0.166.0": false, "0.167.0": true, "0.171.1": true, "0.172.0-beta.1": false,
	} {
		t.Run(version, func(t *testing.T) {
			lc := shimLaunchConfig{NativeProvider: "codex", BackendRequired: true, BackendProgram: "/trusted/codex", BackendSocketPath: "/private/backend.sock"}
			if version != "nil" {
				lc.CLIIdentity = &persist.CLIIdentity{Path: "/trusted/codex", Version: version}
			}
			if err := validateManagerShimRuntime(lc, true); (err == nil) != want {
				t.Fatalf("runtime %q admitted=%v", version, err == nil)
			}
		})
	}
}
