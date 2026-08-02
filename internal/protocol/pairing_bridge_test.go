package protocol

// FAILING-FIRST tests for the daemon-hosted PAIRING BRIDGE at the protocol layer
// (remote slice A3.3-bc, ADR-007 amendment "Pairing host: Option A"). This slice
// wires the owner-tier pair_* wire ops (already frozen in types.go) to an optional
// PairingHost the assembled daemon implements, and enforces the anti-MITM SAS gate's
// fail-closed concurrency. It is tested here against a FAKE PairingHost (no real
// crypto/enroll — that is the next sub-slice A3.3-d); the security property under
// test is the WIRING + fail-closed-on-disconnect, not the handshake math.
//
// RED is undefined-only: this file does not compile because the intended PRODUCTION
// symbols below do not exist yet. The GREEN implementer defines exactly these.
//
// FROZEN API these tests pin (the GREEN implements it — match it byte-for-byte):
//
//	// A pairing request, translated by handlePairStart from c.Pairing.
//	type PairStartReq struct {
//	    Capability string
//	    TTLSeconds int
//	}
//
//	// The synchronous rendezvous view handlePairStart replies with (pair_start reply).
//	type PairView struct {
//	    QR           string
//	    RendezvousID string
//	    ExpiresAt    *time.Time
//	}
//
//	// The terminal pairing outcome the host reports via the result callback.
//	type PairResult struct {
//	    DeviceID   string
//	    Name       string
//	    Capability string
//	    Err        error
//	}
//
//	// PairingHost is the OPTIONAL interface the assembled daemon implements so the
//	// owner-tier Server can host a pairing. BeginPairing creates the rendezvous + QR
//	// SYNCHRONOUSLY (returned in PairView) and runs the handshake in a background
//	// goroutine. It calls confirm(sas, deviceName) at the SAS gate (blocking until the
//	// human decides) and result(...) EXACTLY ONCE at the terminal outcome. ctx
//	// cancellation (connection drop / TTL) MUST make an in-flight confirm return a
//	// NON-NIL error — fail closed -> decline.
//	type PairingHost interface {
//	    BeginPairing(ctx context.Context, req PairStartReq,
//	        confirm func(sas []string, deviceName string) (bool, error),
//	        result  func(PairResult)) (PairView, error)
//	}
//
// HANDLER CONTRACT (GREEN wires these into handleControl's switch):
//   - handlePairStart(c Control): OWNER-TIER ONLY — refuse on cc.srv.remoteTier with
//     CodeNotAuthorized (mirrors handleAttach's remote-tier refusal), BEFORE anything
//     else. Then require CapPairing (else error), then type-assert cc.srv.d.(PairingHost)
//     (else error). Build PairStartReq from c.Pairing and call BeginPairing with a ctx
//     derived from the CONNECTION lifetime (a disconnect cancels it). Pass a confirm
//     closure that PUSHES a pair_pending Control (SAS + DeviceName + RendezvousID) to THIS
//     connection then BLOCKS for the matching pair_confirm (or ctx cancel -> non-nil err),
//     and a result closure that PUSHES a pair_result Control. Reply to pair_start with
//     Control{Op: OpPairStart, Pairing: &PairingControl{QR, RendezvousID, ExpiresAt}}.
//     Only ONE pairing in flight per connection.
//   - handlePairConfirm(c Control): route c.Pairing.Allow to the waiting confirm closure
//     for this connection's in-flight pairing (a channel). No pairing in flight -> error.
//
// Every test carries a deadline (readControl's recvTimeout / the recorder waits below);
// nothing may hang.

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// confirmOutcome records exactly what the GREEN's confirm closure returned to the
// host — the crux of the fail-closed assertion (test 3 inspects the err).
type confirmOutcome struct {
	ok  bool
	err error
}

// fakePairingHost is a DaemonAPI (via the embedded *stubDaemon) that ALSO implements
// the intended PairingHost, so an owner-tier Server serves pair_* over it. Its
// BeginPairing returns a canned PairView immediately, then drives the SAS gate from a
// background goroutine exactly as the contract prescribes, recording what confirm
// returned so a test can inspect fail-closed behavior after a disconnect.
type fakePairingHost struct {
	*stubDaemon

	view PairView // returned synchronously by BeginPairing

	mu       sync.Mutex
	beginReq PairStartReq // the PairStartReq handlePairStart translated from c.Pairing
	outcome  confirmOutcome

	// confirmDone fires (buffered, cap 1) the instant confirm returns, carrying its
	// (ok, err); resultDone fires when result is invoked. Buffered so the host
	// goroutine never blocks on a test that does not drain them (happy/deny paths).
	confirmDone chan confirmOutcome
	resultDone  chan PairResult
}

func newFakePairingHost() *fakePairingHost {
	exp := time.Now().Add(2 * time.Minute)
	return &fakePairingHost{
		stubDaemon:  newStubDaemon(),
		view:        PairView{QR: "otpauth://swarm-pair/abc123", RendezvousID: "rvz-7", ExpiresAt: &exp},
		confirmDone: make(chan confirmOutcome, 1),
		resultDone:  make(chan PairResult, 1),
	}
}

// BeginPairing returns the canned view synchronously, then runs the "handshake" in a
// goroutine: it calls confirm with a canned 6-word SAS and device name "phone",
// records what confirm returned, and calls result exactly once — success on (true,
// nil), failure (Err set) on (false, nil) or (_, err).
func (h *fakePairingHost) BeginPairing(_ context.Context, req PairStartReq,
	confirm func(sas []string, deviceName string) (bool, error),
	result func(PairResult)) (PairView, error) {

	h.mu.Lock()
	h.beginReq = req
	h.mu.Unlock()

	go func() {
		sas := []string{"a", "b", "c", "d", "e", "f"}
		ok, err := confirm(sas, "phone")

		h.mu.Lock()
		h.outcome = confirmOutcome{ok: ok, err: err}
		h.mu.Unlock()
		h.confirmDone <- confirmOutcome{ok: ok, err: err}

		var res PairResult
		if ok && err == nil {
			res = PairResult{DeviceID: "devX", Name: "phone", Capability: "full"}
		} else {
			reason := err
			if reason == nil {
				reason = errors.New("pairing declined")
			}
			res = PairResult{Err: reason}
		}
		result(res)
		h.resultDone <- res
	}()

	return h.view, nil
}

// confirmReturned returns what confirm reported to the host, or fails if it did not
// return within d (a hang would mean the fail-closed ctx-cancel wiring is missing).
func (h *fakePairingHost) confirmReturned(t *testing.T, d time.Duration) confirmOutcome {
	t.Helper()
	select {
	case o := <-h.confirmDone:
		return o
	case <-time.After(d):
		t.Fatalf("host.confirm never returned within %v (fail-closed ctx cancel not wired?)", d)
		return confirmOutcome{}
	}
}

// startedReq returns the PairStartReq handlePairStart translated from the wire.
func (h *fakePairingHost) startedReq() PairStartReq {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.beginReq
}

// Compile-time proof the fake satisfies both surfaces (undefined until GREEN lands).
var (
	_ DaemonAPI   = (*fakePairingHost)(nil)
	_ PairingHost = (*fakePairingHost)(nil)
)

// servePairingHost stands up an OWNER-tier Server over a PairingHost-capable
// DaemonAPI (mirrors serveDeviceLister/serveJournal — the Server exposes pair_* when
// its backend implements PairingHost and the `pairing` cap was negotiated).
func servePairingHost(t *testing.T, backend DaemonAPI) string {
	t.Helper()
	sock := tmpSock(t)
	srv, err := Serve(backend, sock)
	if err != nil {
		t.Fatalf("Serve(pairingHost): %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	return sock
}

// readReplyAndPending reads the two frames a pair_start produces — the pair_start
// REPLY (handler goroutine) and the pushed pair_pending (background handshake
// goroutine) — in whichever order they land on the wire. Their relative order is a
// genuine race (two goroutines writing the one connection, serialized only by
// writeMu), so the test classifies by Op rather than assuming an order.
func readReplyAndPending(t *testing.T, rc *rawConn) (reply, pending Control) {
	t.Helper()
	for i := 0; i < 2; i++ {
		c := rc.readControl()
		switch c.Op {
		case OpPairStart:
			reply = c
		case OpPairPending:
			pending = c
		default:
			t.Fatalf("unexpected op %q while reading pair_start reply/pending", c.Op)
		}
	}
	if reply.Op != OpPairStart {
		t.Fatalf("no pair_start reply among the first two frames")
	}
	if pending.Op != OpPairPending {
		t.Fatalf("no pair_pending push among the first two frames")
	}
	return reply, pending
}

// awaitPairResult reads control frames until the pushed pair_result, skipping any
// interleaved ack, within a small frame budget (readControl is deadline-bounded).
func awaitPairResult(t *testing.T, rc *rawConn) Control {
	t.Helper()
	for i := 0; i < 8; i++ {
		c := rc.readControl()
		if c.Op == OpPairResult {
			return c
		}
	}
	t.Fatalf("no pair_result push within frame budget")
	return Control{}
}

// TestPairing_StartPendingConfirmResult_HappyPath: the full owner-tier bridge flow.
// pair_start returns a PairView; the SAS gate is pushed as pair_pending; an approving
// pair_confirm drives the host to a successful pair_result; the host observed
// confirm==true.
func TestPairing_StartPendingConfirmResult_HappyPath(t *testing.T) {
	host := newFakePairingHost()
	sock := servePairingHost(t, host)
	rc := rawDial(t, sock)
	rep := rc.hello(Version, []string{CapPairing})

	rc.writeControl(Control{Op: OpPairStart, EndpointID: rep.EndpointID, Pairing: &PairingControl{Capability: "full"}})

	reply, pending := readReplyAndPending(t, rc)

	// pair_start reply carries the synchronous PairView.
	if reply.Pairing == nil {
		t.Fatalf("pair_start reply has nil Pairing; want a PairView")
	}
	if reply.Pairing.QR == "" {
		t.Fatalf("pair_start reply QR empty; want the rendezvous QR")
	}
	if reply.Pairing.RendezvousID == "" {
		t.Fatalf("pair_start reply RendezvousID empty; want it set")
	}

	// The handler translated the wire request into a PairStartReq for the host.
	if got := host.startedReq(); got.Capability != "full" {
		t.Fatalf("host BeginPairing req.Capability = %q; want %q", got.Capability, "full")
	}

	// pair_pending carries the SAS gate: a 6-word SAS + the device name.
	if pending.Pairing == nil {
		t.Fatalf("pair_pending has nil Pairing; want SAS + DeviceName")
	}
	if len(pending.Pairing.SAS) != 6 {
		t.Fatalf("pair_pending SAS has %d words; want 6", len(pending.Pairing.SAS))
	}
	if pending.Pairing.DeviceName != "phone" {
		t.Fatalf("pair_pending DeviceName = %q; want %q", pending.Pairing.DeviceName, "phone")
	}

	// Approve at the SAS gate.
	rc.writeControl(Control{Op: OpPairConfirm, EndpointID: rep.EndpointID, Pairing: &PairingControl{Allow: true}})

	res := awaitPairResult(t, rc)
	if res.Pairing == nil || res.Pairing.DeviceID != "devX" {
		t.Fatalf("pair_result = %+v; want success with DeviceID %q", res.Pairing, "devX")
	}

	// The host saw an approving confirm (the SAS gate returned true).
	if o := host.confirmReturned(t, recvTimeout); !o.ok || o.err != nil {
		t.Fatalf("host confirm outcome = %+v; want ok=true err=nil", o)
	}
}

// TestPairing_DenyFailsClosed: a declining pair_confirm (Allow=false) drives the host
// to a FAILURE pair_result (no DeviceID), and the host saw confirm==false.
func TestPairing_DenyFailsClosed(t *testing.T) {
	host := newFakePairingHost()
	sock := servePairingHost(t, host)
	rc := rawDial(t, sock)
	rep := rc.hello(Version, []string{CapPairing})

	rc.writeControl(Control{Op: OpPairStart, EndpointID: rep.EndpointID, Pairing: &PairingControl{Capability: "full"}})
	_, pending := readReplyAndPending(t, rc)
	if pending.Pairing == nil || len(pending.Pairing.SAS) != 6 {
		t.Fatalf("pair_pending missing the 6-word SAS gate; got %+v", pending.Pairing)
	}

	// Decline at the SAS gate.
	rc.writeControl(Control{Op: OpPairConfirm, EndpointID: rep.EndpointID, Pairing: &PairingControl{Allow: false}})

	res := awaitPairResult(t, rc)
	if res.Pairing != nil && res.Pairing.DeviceID != "" {
		t.Fatalf("pair_result reported DeviceID %q on a declined pairing; want failure (no device)", res.Pairing.DeviceID)
	}

	// The host saw a declining confirm (the SAS gate returned false).
	if o := host.confirmReturned(t, recvTimeout); o.ok {
		t.Fatalf("host confirm outcome = %+v; want ok=false on a declined pairing", o)
	}
}

// TestPairing_DisconnectBeforeConfirmFailsClosed: dropping the connection at the SAS
// gate (before any pair_confirm) MUST cancel the connection-derived ctx, so the
// in-flight confirm returns a NON-NIL error — the anti-MITM fail-closed property. The
// test observes the cancelled confirm via the host's confirmDone recorder (populated
// the instant confirm returns), so it can inspect the outcome AFTER closing the conn.
func TestPairing_DisconnectBeforeConfirmFailsClosed(t *testing.T) {
	host := newFakePairingHost()
	sock := servePairingHost(t, host)
	rc := rawDial(t, sock)
	rep := rc.hello(Version, []string{CapPairing})

	rc.writeControl(Control{Op: OpPairStart, EndpointID: rep.EndpointID, Pairing: &PairingControl{Capability: "full"}})
	// Reading pair_pending proves confirm was called and is now BLOCKING on the SAS
	// gate — so closing the connection next exercises the ctx-cancel path deterministically.
	_, pending := readReplyAndPending(t, rc)
	if pending.Op != OpPairPending {
		t.Fatalf("did not reach the SAS gate before disconnect; got op %q", pending.Op)
	}

	// Drop the connection WITHOUT sending pair_confirm.
	_ = rc.conn.Close()

	// Fail closed: the connection-derived ctx is cancelled, so confirm returns a
	// non-nil error (a decline) rather than hanging.
	o := host.confirmReturned(t, recvTimeout)
	if o.err == nil {
		t.Fatalf("confirm returned err=nil after disconnect; want a non-nil error (fail closed: a dropped connection declines the SAS gate)")
	}
	if o.ok {
		t.Fatalf("confirm returned ok=true after disconnect; want ok=false (fail closed)")
	}
}

// TestPairing_RemoteTierRefused: pair_start on the REMOTE tier is refused
// not_authorized BEFORE the host is ever consulted — pairing is owner-tier only
// (mirrors handleAttach's remote-tier fail-closed refusal).
func TestPairing_RemoteTierRefused(t *testing.T) {
	host := newFakePairingHost()
	sock := serveRemoteAPI(t, host) // remote-tier Server over the same host
	rc := rawDial(t, sock)
	rep := rc.hello(Version, []string{CapPairing})

	rc.writeControl(Control{Op: OpPairStart, EndpointID: rep.EndpointID, Pairing: &PairingControl{Capability: "full"}})
	got := rc.readControl()
	if got.Op != OpError || got.ErrorCode != CodeNotAuthorized {
		t.Fatalf("remote pair_start = op %q code %q; want error/not_authorized (pairing is owner-tier only)", got.Op, got.ErrorCode)
	}
}

// TestPairing_RequiresCapPairing: on the owner tier, pair_start without the negotiated
// `pairing` capability is refused (mirrors journalBackend()/deviceLister()'s cap gate).
func TestPairing_RequiresCapPairing(t *testing.T) {
	host := newFakePairingHost()
	sock := servePairingHost(t, host)
	rc := rawDial(t, sock)
	rep := rc.hello(Version, nil) // no capabilities offered

	rc.writeControl(Control{Op: OpPairStart, EndpointID: rep.EndpointID, Pairing: &PairingControl{Capability: "full"}})
	if got := rc.readControl(); got.Op != OpError {
		t.Fatalf("pair_start without pairing cap = op %q; want error", got.Op)
	}
}
