package skeleton

// FAILING-FIRST (TDD RED) for A7 input Slice 3 — the gateway's PERSISTENT lease-holding
// connection (remotegw.LeaseConn), the security-critical keystroke data-plane foundation.
//
// Today the gateway opens a FRESH daemon connection per command (remotegw.ForwardCommand)
// and does not handle take_control. But take_control establishes cc.control on ONE
// connection and BINDS the input gate to that connection's lease (cc.attSession/attGen), so
// every subsequent keystroke must ride the SAME connection. This slice is that connection
// primitive: dial ONE persistent remote-daemon connection, establish the lease with a
// genuinely-signed take_control, then forward wire.TDataIn keystrokes on it, and surface
// lease-death when the daemon sends OpDetach.
//
// This runs against the REAL assembled remote-tier daemon over its dedicated remote.sock,
// with a REAL device keystore + registry + phonecore signer (assembleWithRemote /
// registerPhone / phonecore.SignTakeControl — the takecontrol_gatetoken_test.go pattern).
// requireRemoteAuthz is NOT stubbed: the daemon verifies the device's Ed25519 signature over
// the canonical take_control tuple (content_hash = SHA256(GateToken)) and only then opens the
// lease, so a green run proves the leaseConn's reconstructed take_control is genuinely
// authorized end-to-end.
//
// Observing that the keystroke REACHED the fake session, lease-free: the fake agent's `ask`
// BLOCKS reading its PTY stdin, and the script exits only after a line arrives. The single
// controller lease is held by this leaseConn (F3 suppresses its output), so a competing
// attach cannot observe echo; instead the ONLY way past `ask` is input reaching the session's
// PTY, so the daemon-side session leaving RUNNING is a faithful "the keystroke arrived"
// signal (the phonesim_e2e_test.go kill-observed pattern). That same session end closes the
// upstream stream, so the daemon sends OpDetach on the lease conn, which the readLoop must
// surface as lease-dead.
//
// RED: remotegw.LeaseConn is a stub (lease.go) — AwaitLease returns not-implemented — so the
// lease is never established and the test fails at the OpLease grant. GREEN implements the
// real dial + take_control + writeDataIn + readLoop.

import (
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/phonecore"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/remote/device"
	"github.com/Nathandela/swarm/internal/remotegw"
	"github.com/Nathandela/swarm/internal/status"
)

func TestLeaseConn_WriteDataInAndDrainOutput(t *testing.T) {
	sk, rsock := assembleWithRemote(t)
	ks := registerPhone(t, sk, device.CapFull) // real device; also turns the durable kill switch ON

	// A fake session that BLOCKS on stdin (ask), then exits once a line arrives. The ONLY
	// way past the ask is a keystroke reaching the session's PTY, so a subsequent process
	// exit proves the input landed.
	meta := launchFake(t, sk, "ask go?\nexit 0\n")
	session := protocol.NamespacedID(sk.api.endpointID, meta.ID)

	// Phone-authored, genuinely-signed take_control (real Ed25519 over SHA256(gateToken)),
	// wrapped as the opened RemoteCommand the gateway reconstructs the frame from.
	const gateToken = "gate-lease-slice3"
	cmd, err := phonecore.SignTakeControl(ks, phonecore.TakeControlInput{
		Machine:     sk.api.endpointID,
		Session:     session,
		OperationID: "devLEASE:01JLEASE00000000000000",
		ExpiresAt:   time.Now().Add(time.Minute),
		GateToken:   gateToken,
	})
	if err != nil {
		t.Fatalf("sign take_control: %v", err)
	}
	rcmd := protocol.RemoteCommand{DeviceCommandAuth: cmd, GateToken: gateToken, TTLSeconds: 3600}

	// The leaseConn dials ONE persistent connection and forwards the take_control on it.
	lc, err := remotegw.DialLease(rsock, rcmd)
	if err != nil {
		t.Fatalf("dial lease: %v", err)
	}
	defer lc.Close()

	// The readLoop captures the OpLease grant: a nonzero generation means the lease is
	// established (attach opened an upstream stream).
	gen, err := lc.AwaitLease(10 * time.Second)
	if err != nil {
		t.Fatalf("await lease grant: %v", err)
	}
	if gen == 0 {
		t.Fatalf("lease generation = 0; want a nonzero OpLease generation (lease established)")
	}

	// writeDataIn writes wire.TDataIn on the SAME connection; it must reach the fake
	// session's PTY. The agent's `ask` consumes the line and the script exits, so the
	// daemon-side session leaving running is the faithful "keystroke reached the session"
	// signal.
	if err := lc.WriteDataIn([]byte("ls\n")); err != nil {
		t.Fatalf("write data_in on the lease: %v", err)
	}
	if !waitSessionExited(t, sk, meta.ID, 10*time.Second) {
		t.Fatal("fake session never left running after writeDataIn; the keystroke did not reach the session's PTY")
	}

	// The session end closes the upstream stream, so the daemon sends OpDetach on the lease
	// conn; the readLoop must surface that as lease-dead.
	select {
	case <-lc.Dead():
	case <-time.After(10 * time.Second):
		t.Fatal("leaseConn never signalled lease-dead after OpDetach/close")
	}
}

// waitSessionExited polls the daemon core until the session's process leaves running (or
// the deadline elapses), returning whether it exited.
func waitSessionExited(t *testing.T, sk *Daemon, id string, within time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if m, ok := sk.Core().Get(id); ok && m.Status.Process != status.ProcessRunning {
			return true
		}
		time.Sleep(30 * time.Millisecond)
	}
	return false
}
