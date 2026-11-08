package main

import (
	"github.com/Nathandela/swarm/internal/persist"
	"testing"
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

func TestManagerShimCodexRequiresExactQualifiedRuntime(t *testing.T) {
	for _, version := range []string{"nil", "", "0.166.0", "0.168.0", "0.167.0"} {
		t.Run(version, func(t *testing.T) {
			lc := shimLaunchConfig{NativeProvider: "codex", BackendRequired: true, BackendProgram: "/trusted/codex", BackendSocketPath: "/private/backend.sock"}
			if version != "nil" {
				lc.CLIIdentity = &persist.CLIIdentity{Path: "/trusted/codex", Version: version}
			}
			if err := validateManagerShimRuntime(lc, true); (err == nil) != (version == "0.167.0") {
				t.Fatalf("runtime %q admitted=%v", version, err == nil)
			}
		})
	}
}
