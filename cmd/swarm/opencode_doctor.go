package main

import (
	"context"
	"path/filepath"

	"github.com/Nathandela/swarm/internal/opencodebridge"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/shim"
)

func doctorOpenCodeCheck(ctx context.Context, stateDir string, m persist.Meta) doctorFinding {
	finding := doctorFinding{Check: "native-session", Status: "warn", Fix: "inspect the discussion in Swarm; end and resume it explicitly if its native binding cannot recover"}
	if !persist.ValidID(m.ID) || m.SchemaVersion != persist.OpenCodeSchemaVersion {
		finding.Detail = "saved native session identity or schema is unsupported"
		return finding
	}
	finding.Check += ":" + m.ID
	if m.OpenCodeBinding == nil || m.OpenCodeBinding.Phase != persist.OpenCodePhaseReady || m.OpenCodeBinding.NativeCreatedAtMS <= 0 {
		finding.Detail = "native session binding is not confirmed; input may still be held"
		return finding
	}
	if initial := m.OpenCodeBinding.Initial; initial != nil && initial.Phase != persist.OpenCodeInitialAdmitted {
		finding.Detail = "initial native admission is unconfirmed; no automatic resubmission is safe"
		if initial.Phase == persist.OpenCodeInitialRejected {
			finding.Detail = "initial native input was rejected before submission: " + initial.Reason
			finding.Fix = "inspect and correct the requested model or agent; end this worker and use a new launch"
		}
		return finding
	}
	if ctx.Err() != nil {
		finding.Detail = "native diagnostic deadline reached; channel status is unconfirmed"
		return finding
	}
	sessionDir, err := filepath.EvalSymlinks(filepath.Join(stateDir, m.ID))
	if err != nil {
		finding.Detail = "private native session directory is unavailable"
		return finding
	}
	b, err := opencodebridge.ReadBinding(filepath.Join(sessionDir, shim.OpenCodeBindingFile))
	if err != nil || b.WorkerID != m.ID || b.RootID != m.ConversationID || b.Directory != m.ProviderCwd() || b.BackendGeneration != m.OpenCodeBinding.Generation || b.SessionInstance != m.OpenCodeBinding.Generation || b.Mode != m.OpenCodeBinding.Mode || b.InitialPrompt != (m.OpenCodeBinding.Initial != nil) || b.NativeCreatedAtMS != 0 && b.NativeCreatedAtMS != m.OpenCodeBinding.NativeCreatedAtMS {
		finding.Detail = "private native channel custody is unavailable or differs from the saved binding"
		return finding
	}
	client, err := opencodebridge.NewClient(b.SocketPath, b.Token)
	if err != nil {
		finding.Detail = "private native channel is unavailable"
		return finding
	}
	defer client.Close()
	ready, err := client.Ready(ctx)
	want := opencodebridge.Identity{Version: b.Version, WorkerID: b.WorkerID, SessionInstance: b.SessionInstance, RootID: b.RootID, Directory: b.Directory, BackendGeneration: b.BackendGeneration, NativeCreatedAtMS: m.OpenCodeBinding.NativeCreatedAtMS}
	if err != nil || ready.Identity != want || !ready.Bound || !ready.Acknowledged {
		finding.Detail = "native channel did not confirm the saved session identity"
		return finding
	}
	if !opencodebridge.SupportedNativeVersion(ready.NativeVersion) {
		finding.Detail = "native runtime version must be a stable release at or above " + opencodebridge.MinimumNativeVersion
		return finding
	}
	if !ready.Ready {
		finding.Detail = "native view is not ready; semantic actions remain withheld"
		return finding
	}
	finding.Status, finding.Detail, finding.Fix = "ok", "native runtime version, exact binding and private channel match; actions remain capability gated", ""
	return finding
}
