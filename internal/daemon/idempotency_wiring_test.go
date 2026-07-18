package daemon

// FAILING-FIRST daemon-integration test for two-phase launch idempotency (plan
// R-IDP.2/.3, amendment D.0-A3). RED is undefined-only: `go test
// ./internal/daemon/` fails to compile because LaunchSpec has no OperationID field
// yet.
//
// Expected seam (the implementer's deliverable):
//
//	type LaunchSpec struct { ...; OperationID string } // remote launch idempotency key (<device_id>:<client-ULID>); local launches leave it "".
//
// A3 pins the mechanism: the operation_id is persisted AS PART OF the existing
// two-phase session reservation (launch.go phaseReserved, same fsync), so a replay
// of the same operation_id REUSES the reserved session rather than spawning a
// second. The store-level fsync-before-side-effect + crash semantics are covered by
// internal/idempotency; this asserts the real-daemon "no second SESSION" property.

import (
	"path/filepath"
	"testing"
)

// TestIdempotency_ReplayLaunchNoSecondSession (R-IDP.3): two Launch calls carrying
// the SAME operation_id yield exactly one session — the replay returns the cached
// reservation and spawns nothing.
func TestIdempotency_ReplayLaunchNoSecondSession(t *testing.T) {
	d := openDaemon(t, daemonConfig(t))

	pidFile := filepath.Join(t.TempDir(), "agent.pid")
	spec := announceSpec(t, pidFile)
	spec.OperationID = "devA:01JLAUNCHDUP0000000000000" // remote idempotency key

	m1, err := d.Launch(spec)
	if err != nil {
		t.Fatalf("first Launch: %v", err)
	}
	t.Cleanup(func() { _ = d.Kill(m1.ID) })

	m2, err := d.Launch(spec) // replay: same operation_id
	if err != nil {
		t.Fatalf("replay Launch: %v", err)
	}

	if m1.ID != m2.ID {
		t.Fatalf("replayed launch produced a DIFFERENT session: %q then %q; want the cached reservation", m1.ID, m2.ID)
	}
	if n := len(d.List()); n != 1 {
		t.Fatalf("registry holds %d sessions after a replayed launch; want exactly 1 (no second spawn)", n)
	}
}
