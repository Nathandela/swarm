package protocol

// FAILING-FIRST test for audit finding F5 [HARDENING]: a device-signed command's ExpiresAt
// must be capped server-side. requireRemoteAuthz only checks ExpiresAt is present + not past;
// a command signed with ExpiresAt = now + years stays cryptographically valid indefinitely,
// widening the replay window without bound and outrunning the idempotency GC TTL. The daemon
// must REJECT an ExpiresAt beyond now + maxCommandValidity (1h) — in requireRemoteAuthz, NOT
// the frozen crypto layer — while accepting one inside the window.

import (
	"testing"
	"time"
)

// TestProtocol_CommandExpiresAtCapped: a remote mutating op signed with ExpiresAt = now+2h is
// REFUSED (beyond the 1h cap) with NO side effect; the same op with ExpiresAt = now+30m is
// accepted and reaches the daemon.
func TestProtocol_CommandExpiresAtCapped(t *testing.T) {
	// Beyond the cap: refused, no kill reaches the daemon.
	t.Run("beyond_cap_refused", func(t *testing.T) {
		stub := newStubDaemon()
		sock := serveRemote(t, stub)
		rc := rawDial(t, sock)
		rep := rc.hello(Version, []string{CapRemoteGateway})
		sid := rep.EndpointID + "/sess1"

		exp := time.Now().Add(2 * time.Hour) // far beyond maxCommandValidity (1h)
		rc.writeControl(Control{
			Op: OpKill, EndpointID: rep.EndpointID, SessionID: sid,
			OperationID: "devA:01JKILLCAP00000000000FAR",
			DeviceID:    "devA", DeviceSig: "sig", ExpiresAt: &exp,
		})
		got := rc.readControl()
		if got.Op != OpError {
			t.Fatalf("over-cap command = op %q; want error (ExpiresAt beyond the max validity window)", got.Op)
		}
		if n := len(stub.killedIDs()); n != 0 {
			t.Fatalf("over-cap command executed %d kills; want 0 (refused before any side effect)", n)
		}
	})

	// Inside the cap: accepted, reaches the daemon.
	t.Run("within_cap_accepted", func(t *testing.T) {
		stub := newStubDaemon()
		sock := serveRemote(t, stub)
		rc := rawDial(t, sock)
		rep := rc.hello(Version, []string{CapRemoteGateway})
		sid := rep.EndpointID + "/sess1"

		exp := time.Now().Add(30 * time.Minute) // within maxCommandValidity
		rc.writeControl(Control{
			Op: OpKill, EndpointID: rep.EndpointID, SessionID: sid,
			OperationID: "devA:01JKILLCAP0000000000NEAR",
			DeviceID:    "devA", DeviceSig: "sig", ExpiresAt: &exp,
		})
		got := rc.readControl()
		if got.Op == OpError {
			t.Fatalf("within-cap command refused: %q / %q; a fresh command inside the validity window must be accepted", got.Error, got.ErrorCode)
		}
		if n := len(stub.killedIDs()); n != 1 {
			t.Fatalf("within-cap command executed %d kills; want 1 (accepted, reached the daemon)", n)
		}
	})
}
