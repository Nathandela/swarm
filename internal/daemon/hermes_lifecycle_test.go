package daemon

import (
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/shim"
	"github.com/Nathandela/swarm/internal/status"
	"testing"
)

func TestHermesViewerExitDoesNotCompleteNativeWork(t *testing.T) {
	m := mergeExit(persist.Meta{HermesNative: true, Status: status.Status{Process: status.ProcessRunning}}, shim.ExitInfo{ExitCode: 0})
	if m.Status.Process != status.ProcessLost || !m.Status.NativeWorkUnknown || status.Derive(m.Status) != status.GroupNeedsInput {
		t.Fatalf("viewer exit became native completion: %+v", m)
	}
}
