package protocol

// FAILING-FIRST security tests for committee finding C2a: the kill switch must SEVER, not merely
// PAUSE. Today `off` (RemoteControlEnabled()==false) makes controlGateOpen clause 1 DROP input
// per keystroke, but the live take_control LEASE (cc.control) is never torn down and the remote
// connection stays open — so turning `on` again before the signed expiry REACTIVATES the existing
// lease WITHOUT a fresh take_control (no new biometric gate). And journal_subscribe is NOT
// kill-switch-gated (unlike the terminal peek), so a still-open phone keeps receiving session
// lifecycle events after `off`. This contradicts the DoD "off severs the gateway".
//
// The fix (BOTH layers exercised here):
//   - PROACTIVE teardown: an exported (*Server).SeverAllRemoteControl force-releases EVERY remote
//     control lease (clearing cc.control + closing the upstream stream) and cancels EVERY active
//     terminal peek — reusing the C1 severance machinery (severControl). The assembly's coreAPI
//     kill-switch setter invokes it when remote control transitions to DISABLED, so `off` severs
//     and a subsequent `on` requires a FRESH take_control to resume.
//   - JOURNAL gate: handleJournalRead / handleJournalSubscribe refuse CodeKillSwitch while the
//     switch is off (and the fan-out stops streaming), so `off` blanks the journal too — mirroring
//     the terminal peek's kill-switch gate.
//
// FROZEN API these tests expect (the GREEN implementer adds it):
//   - func (s *Server) SeverAllRemoteControl() — proactive teardown of all remote control leases +
//     peeks (the seam the coreAPI kill-switch setter calls; exported for the cross-package signal).
//   - handleJournalRead / handleJournalSubscribe gate on the kill switch (CodeKillSwitch when off).

import (
	"bytes"
	"testing"

	"github.com/Nathandela/swarm/internal/wire"
)

// serveRemoteAPISrv is serveRemoteAPI returning the *Server too, so a test can invoke the exported
// SeverAllRemoteControl seam directly — the proactive teardown the coreAPI kill-switch setter calls
// when remote control transitions to disabled.
func serveRemoteAPISrv(t *testing.T, d DaemonAPI) (string, *Server) {
	t.Helper()
	sock := tmpSock(t)
	srv, err := ServeRemote(d, sock)
	if err != nil {
		t.Fatalf("ServeRemote: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	return sock, srv
}

// TestProtocol_OffSeversLiveLeaseRequiresFreshTakeControl: a live take_control lease is established
// with the switch ON; the switch flips OFF and the sever seam runs; the lease must be RELEASED (its
// upstream stream closed, cc.control cleared) — not merely paused. Flipping the switch back ON must
// NOT silently resume the OLD lease: a data_in on it is DROPPED (clause 2 fail-closed, cc.control
// nil), so resuming control requires a FRESH take_control. Contrast today: the lease survives and
// `on` reactivates it without a new take_control.
func TestProtocol_OffSeversLiveLeaseRequiresFreshTakeControl(t *testing.T) {
	stub := newStubDaemon()
	ks := &toggleKillSwitchStub{stubDaemon: stub}
	ks.enabled.Store(true) // ON: take_control may establish the lease
	sock, srv := serveRemoteAPISrv(t, ks)

	rc := rawDial(t, sock)
	rep := rc.hello(Version, []string{CapRemoteGateway})
	sid := rep.EndpointID + "/sess1"
	takeControl(t, rc, rep.EndpointID, sid, 3600)

	// The lease is LIVE: the controller's own keystroke reaches the shim.
	warm := []byte("echo hi\r")
	rc.writeFrame(wire.TDataIn, warm)
	rc.writeControl(Control{Op: OpList, EndpointID: rep.EndpointID})
	syncControlOp(t, rc, OpList)
	st := stub.lastStream()
	if st == nil || !bytes.Contains(st.inputBytes(), warm) {
		t.Fatalf("precondition: controller's own input did not reach the shim; the lease is not live")
	}

	// OFF SEVERS: the switch flips off and the coreAPI seam invokes the proactive teardown.
	ks.enabled.Store(false)
	srv.SeverAllRemoteControl()

	// The live lease was PROACTIVELY released: its single upstream stream is CLOSED (not merely
	// paused). A released lease cannot be silently resumed when the switch turns back on.
	if !st.waitClosed(recvTimeout) {
		t.Fatalf("off did not sever the live control lease (upstream stream never closed); a still-signed lease survives `off` and would resume on `on` without a fresh take_control")
	}

	// FRESH take_control REQUIRED: flip the switch back ON and replay a data_in on the OLD lease.
	// It must be DROPPED (cc.control was cleared by the sever) — resuming control needs a new
	// take_control, not a silent resume of the old lease.
	ks.enabled.Store(true)
	after := []byte("rm -rf ~\r")
	rc.writeFrame(wire.TDataIn, after)
	rc.writeControl(Control{Op: OpList, EndpointID: rep.EndpointID})
	syncControlOp(t, rc, OpList)
	if bytes.Contains(st.inputBytes(), after) {
		t.Fatalf("after off+on the OLD lease silently resumed: a keystroke reached the shim without a fresh take_control: %q", st.inputBytes())
	}
}

// TestProtocol_OffSeversLivePeek: a live terminal peek is severed proactively when the switch flips
// off, via the sever seam — even when the peek is IDLE (no further tap output). This proves the
// teardown terminates an idle peek PROMPTLY (through the seam / cancelPeek), not only on the next
// frame's per-emission recheck.
func TestProtocol_OffSeversLivePeek(t *testing.T) {
	stub := newTerminalTapStub()
	stub.ks.Store(true) // ON: the peek may open
	sock, srv := serveRemoteAPISrv(t, stub)

	rc := rawDial(t, sock)
	rep := rc.hello(Version, []string{CapRemoteGateway})
	psid := rep.EndpointID + "/sess1"
	rc.writeControl(Control{Op: OpTerminalSubscribe, EndpointID: rep.EndpointID, SessionID: psid})
	if ack := nextControl(t, rc); ack.Op != OpOK {
		t.Fatalf("terminal_subscribe = op %q code %q; want OpOK", ack.Op, ack.ErrorCode)
	}
	tap := stub.lastTap()
	if tap == nil {
		t.Fatalf("peek opened no tap")
	}
	tap.frames <- []byte("PEEK")
	_ = readTerminalSnapshot(t, rc) // the peek is live

	// The peek is now IDLE (no further tap output). OFF must terminate it PROMPTLY via the seam,
	// not linger until the next frame or a connection drop.
	stub.ks.Store(false)
	srv.SeverAllRemoteControl()

	// The idle peek terminated at the daemon: its read-only tap is released ...
	if !tap.waitClosed(recvTimeout) {
		t.Fatalf("off did not sever the live (idle) terminal peek: its read-only tap was never released (the seam must cancel an idle peek promptly, not wait for the next frame)")
	}
	// ... and the peeker is signaled the peek ended (OpError), so its gateway stops reading.
	got := nextControl(t, rc)
	if got.Op != OpError {
		t.Fatalf("after off the peek conn got op %q; want OpError (the peek-ended signal)", got.Op)
	}
}

// offJournalStub is a remote backend (DaemonAPI + DeviceAuthenticator + OperationClaimer via the
// embedded *stubDaemon) that ALSO implements JournalBackend, with the kill switch OFF — so
// journal_read / journal_subscribe must be refused CodeKillSwitch (a still-open phone must not keep
// reading session lifecycle events after `off`).
type offJournalStub struct {
	*stubDaemon
	source chan JournalRecord
}

func newOffJournalStub() *offJournalStub {
	return &offJournalStub{stubDaemon: newStubDaemon(), source: make(chan JournalRecord, 16)}
}

// RemoteControlEnabled makes offJournalStub a KillSwitch reporting OFF (remote control disabled).
func (o *offJournalStub) RemoteControlEnabled() bool { return false }

func (o *offJournalStub) JournalReadFrom(from uint64) (JournalResume, error) {
	return JournalResume{}, nil
}

func (o *offJournalStub) JournalSubscribe() (<-chan JournalRecord, func()) {
	return o.source, func() {}
}

var (
	_ DaemonAPI      = (*offJournalStub)(nil)
	_ JournalBackend = (*offJournalStub)(nil)
	_ KillSwitch     = (*offJournalStub)(nil)
)

// TestProtocol_OffGatesJournalSubscribe: with the kill switch OFF, journal_subscribe (and
// journal_read) are refused CodeKillSwitch — `off` blanks the journal stream too, consistent with
// the terminal peek's kill-switch gate. Today journal is not kill-switch-gated, so an OK/register
// (or a journal_read result) is returned instead of the refusal.
func TestProtocol_OffGatesJournalSubscribe(t *testing.T) {
	stub := newOffJournalStub()
	sock := serveRemoteAPI(t, stub)
	rc := rawDial(t, sock)
	rep := rc.hello(Version, []string{CapJournal})

	// journal_subscribe with the switch OFF must be refused (the phone is blanked), not registered.
	rc.writeControl(Control{Op: OpJournalSubscribe, EndpointID: rep.EndpointID})
	got := rc.readControl()
	if got.Op != OpError || got.ErrorCode != CodeKillSwitch {
		t.Fatalf("journal_subscribe with the kill switch off = op %q code %q; want error/kill_switch (off must blank the journal stream)", got.Op, got.ErrorCode)
	}

	// journal_read is likewise refused: a read is also a leak of session lifecycle metadata.
	rc.writeControl(Control{Op: OpJournalRead, EndpointID: rep.EndpointID, Cursor: 0})
	got = rc.readControl()
	if got.Op != OpError || got.ErrorCode != CodeKillSwitch {
		t.Fatalf("journal_read with the kill switch off = op %q code %q; want error/kill_switch", got.Op, got.ErrorCode)
	}
}
