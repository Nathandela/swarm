package protocol

// FAILING-FIRST tests for the async PAIRING CLIENT API (slice A4, the prerequisite
// for `swarm remote pair` and the TUI pairing modal). The daemon HOSTS pairing
// (ADR-007 amendment "Pairing host: Option A"): an owner-tier pair_start makes the
// daemon run the handshake and PUSH pair_pending (the SAS + device name) then
// pair_result. But Client.request is a strict 1-request/1-response round-trip and
// dispatchControl routes unrecognized ops to the request respCh via `default` — so
// those PUSHES have no home. This slice adds Client.StartPairing + a PairingSession
// handle that routes the pushes to session channels, never colliding with respCh.
//
// RED is undefined-only: this file does not compile because StartPairing /
// PairingSession / PairingPending / PairingResult do not exist yet. The GREEN
// implementer defines exactly these in client.go.
//
// These run against the REAL owner-tier pairing handlers (servePairingHost +
// fakePairingHost from pairing_bridge_test.go), so the client is exercised over the
// genuine pair_start-reply -> pair_pending-push -> pair_confirm -> pair_result-push
// contract, not a hand-scripted fake.

import (
	"testing"
	"time"
)

// recvPending waits for one PairingPending on ch, or fails.
func recvPending(t *testing.T, ch <-chan PairingPending, d time.Duration) PairingPending {
	t.Helper()
	select {
	case p := <-ch:
		return p
	case <-time.After(d):
		t.Fatalf("no PairingPending within %v", d)
		return PairingPending{}
	}
}

// recvResult waits for one PairingResult on ch, or fails.
func recvResult(t *testing.T, ch <-chan PairingResult, d time.Duration) PairingResult {
	t.Helper()
	select {
	case r := <-ch:
		return r
	case <-time.After(d):
		t.Fatalf("no PairingResult within %v", d)
		return PairingResult{}
	}
}

// TestClient_PairingSession_ReceivesSASAndConfirms drives the full async pairing
// client API against the real owner-tier handlers: StartPairing sends pair_start and
// returns the synchronous PairView (QR/rendezvous) on the session handle; the
// daemon's pair_pending PUSH arrives on Pending() (NOT the request respCh);
// Confirm(true) sends pair_confirm; the daemon's pair_result PUSH arrives on Result()
// as a paired outcome. A trailing List() proves the two pushes never polluted respCh.
func TestClient_PairingSession_ReceivesSASAndConfirms(t *testing.T) {
	host := newFakePairingHost()
	sock := servePairingHost(t, host)
	c := dialClient(t, sock, []string{CapPairing})

	sess, err := c.StartPairing(PairStartReq{Capability: "full"})
	if err != nil {
		t.Fatalf("StartPairing: %v", err)
	}

	// The pair_start reply (OpPairStart), routed to the request respCh and NOT
	// swallowed by the pairing push route, carries the synchronous PairView.
	if sess.QR == "" {
		t.Fatalf("StartPairing session QR empty; want the rendezvous QR (pair_start reply lost?)")
	}
	if sess.RendezvousID != "rvz-7" {
		t.Fatalf("StartPairing session RendezvousID = %q; want %q", sess.RendezvousID, "rvz-7")
	}

	// The handler translated the wire request into a PairStartReq for the host.
	if got := host.startedReq(); got.Capability != "full" {
		t.Fatalf("host BeginPairing req.Capability = %q; want %q", got.Capability, "full")
	}

	// The daemon PUSHES pair_pending (the SAS gate): it must arrive on Pending(), not
	// collide with the request respCh.
	pend := recvPending(t, sess.Pending(), recvTimeout)
	if len(pend.SAS) != 6 {
		t.Fatalf("Pending SAS has %d words; want 6", len(pend.SAS))
	}
	if pend.DeviceName != "phone" {
		t.Fatalf("Pending DeviceName = %q; want %q", pend.DeviceName, "phone")
	}

	// Approve at the SAS gate: Confirm sends pair_confirm(Allow=true).
	if err := sess.Confirm(true); err != nil {
		t.Fatalf("Confirm: %v", err)
	}

	// The daemon PUSHES pair_result: it must arrive on Result() as a paired outcome.
	res := recvResult(t, sess.Result(), recvTimeout)
	if !res.Paired {
		t.Fatalf("Result Paired = false; want true (approved pairing)")
	}
	if res.DeviceID != "devX" {
		t.Fatalf("Result DeviceID = %q; want %q", res.DeviceID, "devX")
	}

	// The host saw an approving confirm (the SAS gate returned true), i.e. Confirm
	// reached it over the wire.
	if o := host.confirmReturned(t, recvTimeout); !o.ok || o.err != nil {
		t.Fatalf("host confirm outcome = %+v; want ok=true err=nil", o)
	}

	// No respCh collision: a normal request/reply still works after the pushes.
	if _, err := c.List(); err != nil {
		t.Fatalf("List after pairing: %v (pushes polluted the request respCh?)", err)
	}
}

// TestClient_PairingSession_DisconnectBeforeConfirmFailsClosed: dropping the
// connection at the SAS gate (before Confirm) ends the session fail-closed — the
// client's Result() yields a non-paired outcome, and the daemon's connection-derived
// ctx cancels its confirm (non-nil error), so nothing is enrolled.
func TestClient_PairingSession_DisconnectBeforeConfirmFailsClosed(t *testing.T) {
	host := newFakePairingHost()
	sock := servePairingHost(t, host)
	c := dialClient(t, sock, []string{CapPairing})

	sess, err := c.StartPairing(PairStartReq{Capability: "full"})
	if err != nil {
		t.Fatalf("StartPairing: %v", err)
	}

	// Reach the SAS gate: pair_pending delivered means the host's confirm is blocking.
	_ = recvPending(t, sess.Pending(), recvTimeout)

	// Drop the connection WITHOUT confirming.
	_ = c.Close()

	// Fail closed, client side: the session ends with a non-paired result.
	res := recvResult(t, sess.Result(), recvTimeout)
	if res.Paired {
		t.Fatalf("Result Paired = true after disconnect; want false (fail closed)")
	}

	// Fail closed, daemon side: the connection-derived ctx cancelled confirm (non-nil
	// error), so nothing was enrolled.
	if o := host.confirmReturned(t, recvTimeout); o.err == nil || o.ok {
		t.Fatalf("host confirm outcome = %+v; want ok=false err!=nil (fail closed)", o)
	}
}
