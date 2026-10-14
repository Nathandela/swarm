package skeleton

import (
	"testing"

	"github.com/Nathandela/swarm/internal/shim"
)

// TestBackendReadyDeadline_OutlastsTheShimsDegradedStart pins the two backend budgets together.
// The shim waits for its backend to serve and then for the daemon's go-ahead; only after both
// (shim.DegradedAgentDelay) does it start the agent degraded. A daemon that stops dialing
// before then abandons a backend the shim may still serve, so its deadline must be the longer.
func TestBackendReadyDeadline_OutlastsTheShimsDegradedStart(t *testing.T) {
	if backendReadyDeadline <= shim.DegradedAgentDelay {
		t.Errorf("backendReadyDeadline = %s, want longer than the shim's degraded start delay %s",
			backendReadyDeadline, shim.DegradedAgentDelay)
	}
}
