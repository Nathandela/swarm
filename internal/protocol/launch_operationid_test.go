package protocol

// FAILING-FIRST test for audit finding C3 [BLOCKER]: a signed remote launch's operation_id
// must reach the daemon LaunchSpec so the daemon's launch idempotency engages — else a
// REPLAYED signed launch spawns a DUPLICATE session. Today daemonLaunchSpec never sets
// LaunchSpec.OperationID, so remote launch idempotency is dead and a replay double-spawns.
//
// GREEN: daemonLaunchSpec threads c.OperationID into the spec (on the remote tier, where the
// launch already carries a signed operation_id via requireRemoteAuthz).

import (
	"sync"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/persist"
)

// idempotentLaunchStub is a remote-tier backend that dedups Launch by OperationID, exactly as
// the production daemon does (one non-empty key -> one session, R-IDP.2/.3), so a
// protocol-level test can prove the signed operation_id is threaded into the daemon LaunchSpec.
// With an EMPTY OperationID (the pre-fix behavior) it CANNOT dedup, so a replay double-spawns.
type idempotentLaunchStub struct {
	*stubDaemon
	mu     sync.Mutex
	byOpID map[string]persist.Meta
}

func newIdempotentLaunchStub() *idempotentLaunchStub {
	return &idempotentLaunchStub{stubDaemon: newStubDaemon(), byOpID: map[string]persist.Meta{}}
}

// Launch reuses the reserved session for a repeated non-empty OperationID (spawning nothing),
// else records the spec + creates a fresh session through the embedded stub.
func (s *idempotentLaunchStub) Launch(spec daemon.LaunchSpec) (persist.Meta, error) {
	s.mu.Lock()
	if spec.OperationID != "" {
		if m, ok := s.byOpID[spec.OperationID]; ok {
			s.mu.Unlock()
			return m, nil // replay: reuse the reserved session, spawn nothing
		}
	}
	s.mu.Unlock()
	m, err := s.stubDaemon.Launch(spec) // records the spec + creates a fresh session
	if err == nil && spec.OperationID != "" {
		s.mu.Lock()
		s.byOpID[spec.OperationID] = m
		s.mu.Unlock()
	}
	return m, err
}

// Compile-time proof the override keeps the stub a full DaemonAPI.
var _ DaemonAPI = (*idempotentLaunchStub)(nil)

// TestProtocol_RemoteLaunchOperationIDEngagesIdempotency: a signed remote launch carrying an
// operation_id reaches the daemon LaunchSpec, and a REPLAY of the same signed launch is deduped
// (exactly one session spawned, not two).
func TestProtocol_RemoteLaunchOperationIDEngagesIdempotency(t *testing.T) {
	stub := newIdempotentLaunchStub()
	// This test is about the operation_id reaching the daemon LaunchSpec (C3), not R-POL.3
	// (cwd confinement), so wire a permissive LaunchPolicy — otherwise the F4
	// fail-closed-absent guard would refuse the launch before OperationID is ever inspected.
	sock := serveRemoteAPI(t, allowAllLaunchPolicy{stub})
	rc := rawDial(t, sock)
	rep := rc.hello(Version, []string{CapRemoteGateway})

	dir := t.TempDir()
	opID := "devA:01JLAUNCHDUP0000000000000"
	launch := func() Control {
		exp := time.Now().Add(time.Minute)
		rc.writeControl(Control{
			Op:          OpLaunch,
			EndpointID:  rep.EndpointID,
			Launch:      &LaunchReq{Agent: "claude", Cwd: dir, Cols: 80, Rows: 24},
			OperationID: opID,
			DeviceID:    "devA", DeviceSig: "sig", ExpiresAt: &exp,
		})
		return rc.readControl()
	}

	if got := launch(); got.Op == OpError {
		t.Fatalf("first remote launch refused: %q / %q", got.Error, got.ErrorCode)
	}
	// Replay the SAME signed launch (same operation_id): idempotency must dedupe it.
	if got := launch(); got.Op == OpError {
		t.Fatalf("replayed remote launch refused: %q / %q", got.Error, got.ErrorCode)
	}

	specs := stub.launchSpecs()
	if len(specs) == 0 {
		t.Fatalf("no launch reached the daemon")
	}
	// The signed operation_id reached the daemon LaunchSpec (C3) ...
	if specs[0].OperationID != opID {
		t.Fatalf("daemon LaunchSpec.OperationID = %q; want the signed operation_id %q reaching the daemon (C3)", specs[0].OperationID, opID)
	}
	// ... and the replay was DEDUPED: exactly one session spawned, not two.
	if len(specs) != 1 {
		t.Fatalf("replayed signed launch spawned %d sessions; want exactly 1 (remote launch idempotency engaged)", len(specs))
	}
}
