package skeleton

// FAILING-FIRST end-to-end test for committee finding C2a at the ASSEMBLY layer: `swarm remote off`
// (SetRemoteControl(false)) must PROACTIVELY sever a live take_control lease on the remote Server,
// not merely pause per-keystroke input. Otherwise turning `on` again before the signed expiry would
// silently resume control without a fresh take_control (a new biometric gate). The assembly wires
// the coreAPI kill-switch setter to the remote Server's SeverAllRemoteControl seam.
//
// RED today (behavioral): SetRemoteControl(false) sets the manual override but never signals the
// remote Server, so a live lease survives and the phone is never sent OpDetach. This test
// establishes a real signed lease over the remote socket, flips the switch off, and asserts the
// phone's lease connection receives OpDetach (the lease was released at the daemon).

import (
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/remote/device"
)

func TestKillSwitch_ManualOffSeversLiveTakeControlLease(t *testing.T) {
	sk, rsock := assembleWithRemote(t)
	ks := registerPhone(t, sk, device.CapFull) // pairs a device: the kill switch is ON
	meta := launchFake(t, sk, "print HELLO\nidle 60s\n")
	session := protocol.NamespacedID(sk.api.endpointID, meta.ID)

	// Establish a REAL signed take_control lease over the dedicated remote socket.
	rc := dialRemote(t, rsock, protocol.CapRemoteGateway)
	const gateToken = "gate-oneshot-c2a"
	cmd := signedTakeControl(t, ks, sk.api.endpointID, session, "devTC:01JC2AOFF00000000000000", gateToken, time.Now().Add(time.Minute))
	sendTakeControl(rc, rc.endpointID, session, cmd, gateToken)
	if got := rc.read(10 * time.Second); got.Op != protocol.OpLease || got.Generation == 0 {
		t.Fatalf("take_control = op %q code %q gen %d; want an OpLease grant (lease established)", got.Op, got.ErrorCode, got.Generation)
	}

	// `swarm remote off`: must sever the live lease on the remote Server via the wired observer.
	if err := remoteControlSetterOf(t, sk).SetRemoteControl(false); err != nil {
		t.Fatalf("SetRemoteControl(false): %v", err)
	}

	// The phone's lease connection is sent OpDetach: the lease was PROACTIVELY released at the
	// daemon (not left live to resume on `on`). Scan for it within a bound.
	deadline := time.Now().Add(10 * time.Second)
	for {
		if time.Now().After(deadline) {
			t.Fatal("SetRemoteControl(false) did not sever the live take_control lease: no OpDetach within bound. " +
				"The manual kill switch must proactively tear down the lease (assembly wires the coreAPI setter to " +
				"the remote Server's SeverAllRemoteControl), else the lease survives `off` and resumes on `on` " +
				"without a fresh take_control.")
		}
		got, err := rc.readTry(2 * time.Second)
		if err != nil {
			continue
		}
		if got.Op == protocol.OpDetach {
			break // the lease was severed at the daemon
		}
	}
}
