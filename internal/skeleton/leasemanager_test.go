package skeleton

// FAILING-FIRST (TDD RED) for A7 input Slice 4 — the LeaseManager (remotegw.LeaseManager),
// the fan-out that ties a phone's take_control to keystroke routing. Where Slice 3 proved ONE
// lease conn carries a genuinely-authorized take_control plus keystrokes on ONE connection,
// this slice proves the MANAGER over that primitive: Begin opens+leases a conn keyed by
// session, Input routes a frame to THAT session's conn (the same connection the daemon binds
// its input gate to), End tears the conn down (releasing the lease server-side), and Input for
// an ended/unknown session is dropped without a crash or a write to a closed conn.
//
// Same real remote-tier harness as lease_conn_test.go: a REAL assembled daemon over its
// remote.sock, a REAL device keystore + phonecore signer, requireRemoteAuthz NOT stubbed, so a
// green run proves the manager-reconstructed take_control is genuinely authorized end-to-end.
// The keystroke's arrival is proven behaviorally exactly as in Slice 3: the fake agent's `ask`
// BLOCKS reading its PTY, F3 suppresses the controller's echo, so the ONLY way the session
// leaves running is input reaching its PTY over the manager-owned lease conn.
//
// RED: remotegw.LeaseManager is a stub (leasemanager.go) — Begin returns not-implemented — so
// the lease is never established and the test fails at Begin. GREEN dials + awaits + stores +
// routes + tears down.

import (
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/phonecore"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/remote/device"
	"github.com/Nathandela/swarm/internal/remotegw"
)

func TestLeaseManager_TakeControlThenInputSameConn(t *testing.T) {
	sk, rsock := assembleWithRemote(t)
	ks := registerPhone(t, sk, device.CapFull) // real device; also turns the durable kill switch ON

	// A fake session that BLOCKS on stdin (ask) and exits once a line arrives: the only way
	// past the ask is a keystroke reaching the session's PTY, so a later process exit proves
	// the input landed on the manager's lease conn.
	meta := launchFake(t, sk, "ask go?\nexit 0\n")
	session := protocol.NamespacedID(sk.api.endpointID, meta.ID)

	// Phone-authored, genuinely-signed take_control (real Ed25519 over SHA256(gateToken)),
	// wrapped as the opened RemoteCommand the manager reconstructs the lease frame from.
	const gateToken = "gate-lease-slice4"
	cmd, err := phonecore.SignTakeControl(ks, phonecore.TakeControlInput{
		Machine:     sk.api.endpointID,
		Session:     session,
		OperationID: "devLEASE:01JLEASE00000000000004",
		ExpiresAt:   time.Now().Add(time.Minute),
		GateToken:   gateToken,
	})
	if err != nil {
		t.Fatalf("sign take_control: %v", err)
	}
	rcmd := protocol.RemoteCommand{DeviceCommandAuth: cmd, GateToken: gateToken, TTLSeconds: 3600}

	mgr := remotegw.NewLeaseManager(rsock, 10*time.Second)
	defer mgr.Close()

	// Begin dials ONE persistent conn for the session and establishes the lease. A nonzero
	// generation means the lease is granted (attach opened the upstream stream).
	if err := mgr.Begin(rcmd); err != nil {
		t.Fatalf("begin lease: %v", err)
	}
	if gen := mgr.Generation(session); gen == 0 {
		t.Fatalf("lease generation = 0 after Begin; want a nonzero OpLease generation (lease established)")
	}

	// Input{data} routes to the session's lease conn — the SAME connection the gate is bound
	// to — so the keystroke reaches the fake session's PTY and the ask-blocked script exits.
	if err := mgr.Input(session, remotegw.InputFrame{Kind: "data", Data: []byte("ls\n")}); err != nil {
		t.Fatalf("route data_in through the manager: %v", err)
	}
	if !waitSessionExited(t, sk, meta.ID, 10*time.Second) {
		t.Fatal("fake session never left running; the keystroke did not reach the session's PTY over the manager's lease conn")
	}

	// End closes + removes the session's lease conn (the client EOF releases the lease
	// server-side); the manager then holds no conn for the session.
	mgr.End(session)
	if gen := mgr.Generation(session); gen != 0 {
		t.Fatalf("lease conn still present after End(session): generation = %d, want 0", gen)
	}

	// Input after End is DROPPED: a benign no-op (no error surfaced, no write to a closed
	// conn, no panic under -race) because the manager has no conn for the ended session.
	if err := mgr.Input(session, remotegw.InputFrame{Kind: "data", Data: []byte("rm -rf /\n")}); err != nil {
		t.Fatalf("Input after End must be dropped benignly, got error: %v", err)
	}
}
