package protocol

// FAILING-FIRST (TDD RED, GG-5) tests for the approve op — IS-LIFE-4's wire carriage on the
// DAEMON side. The daemon already holds the half nobody else can define: approveInteraction
// (internal/skeleton/approval.go) validates the ADR-007 D7 binding tuple, the content hash and
// the daemon-authoritative expiry before any effect. It has no route: there is no OpApprove,
// no arm in handleControl, and no backend seam to reach it through, so its only callers are
// tests.
//
// What these assert, and why each is a property rather than bookkeeping:
//
//   - approve rides requireRemoteAuthz like every other remote mutating op (ADR-007 D4: no
//     remote-class mutating op executes without a valid device signature). The decision itself
//     is unsigned by IS-LIFE-4's design; the OP is not.
//   - the signed tuple's ContentHash IS the interaction's own content_hash (ADR-007 D7 spends
//     the one content slot on it, IS-APR-2 makes the phone echo it verbatim), so a gateway or
//     relay that swaps the hash breaks the signature instead of redirecting the approval.
//   - the body may not name a session other than the one signed. Without that check the
//     gateway — the documented D4/D5 owner-uid residual — could point a signature authorized
//     for session A at session B, which is the field-collision hazard remote_devicerevoke_test
//     already records for TargetDeviceID.
//   - a refusal from the daemon's validation reaches the phone with its D10 taxonomy code
//     intact, because the phone's retry policy is driven by the code and not by prose.
//
// RED is undefined-only: OpApprove and InteractionApprover do not exist, so this file fails to
// compile until the implementer defines them and dispatches handleApprove from handleControl.

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sync"
	"testing"
	"time"
)

// approveCall is one ApproveInteraction the Server forwarded.
type approveCall struct {
	machine     string
	operationID string
	req         ApproveReq
}

// approveStub is a remote-capable backend that ALSO answers approve. It records every
// forwarded call so a test can assert what crossed the seam, and can inject the refusal the
// daemon's own validation produces.
type approveStub struct {
	*stubDaemon
	mu    sync.Mutex
	calls []approveCall
	code  ErrorCode
	err   error
}

func (a *approveStub) ApproveInteraction(machine, operationID string, req ApproveReq) (ErrorCode, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls = append(a.calls, approveCall{machine: machine, operationID: operationID, req: req})
	return a.code, a.err
}

func (a *approveStub) forwarded() []approveCall {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]approveCall(nil), a.calls...)
}

// approveHash is one interaction content hash in the shape the item ships it: lowercase hex
// over 32 bytes (internal/skeleton/approval.go's contentHashSlot).
func approveHash(seed string) string {
	sum := sha256.Sum256([]byte(seed))
	return hex.EncodeToString(sum[:])
}

// approveFrame builds a well-formed approve Control against sid.
func approveFrame(endpoint, sid, hash string, exp *time.Time) Control {
	return Control{
		Op: OpApprove, EndpointID: endpoint, SessionID: sid,
		OperationID: "devA:01JAPPROVE00000000000000",
		DeviceID:    "devA", DeviceSig: "sig", ExpiresAt: exp,
		Approve: &ApproveReq{
			Session:       sid,
			AgentInstance: AgentInstanceRef{ShimPID: 4242, ShimStartTime: 1700000000},
			InteractionID: "itm_01JAPPROVE",
			ContentHash:   hash,
			ExpiresAt:     exp,
			Decision:      "accept",
		},
	}
}

// TestIsLife4_ApproveRidesTheSameSignatureDisciplineAsItsSiblings: an authorized approve
// reaches the daemon's validation with the whole body intact, and the tuple it was authorized
// under names the approve action, the target session and the interaction's OWN content hash.
func TestIsLife4_ApproveRidesTheSameSignatureDisciplineAsItsSiblings(t *testing.T) {
	stub := &approveStub{stubDaemon: newStubDaemon()} // authzFn nil => the signature is accepted
	sock := serveRemoteAPI(t, stub)
	rc := rawDial(t, sock)
	rep := rc.hello(Version, []string{CapRemoteGateway})
	sid := rep.EndpointID + "/sess1"

	exp := time.Now().Add(time.Minute)
	hash := approveHash("the item as shipped")
	rc.writeControl(approveFrame(rep.EndpointID, sid, hash, &exp))

	got := nextControl(t, rc)
	if got.Op != OpOK {
		t.Fatalf("authorized approve = op %q code %q error %q; want ok", got.Op, got.ErrorCode, got.Error)
	}

	tuples := stub.authorizedTuples()
	if len(tuples) != 1 {
		t.Fatalf("authenticator saw %d commands for approve; want 1 (ADR-007 D4: approve is a remote-class mutating op and must pass the choke point)", len(tuples))
	}
	if tuples[0].Action != ActionApprove {
		t.Errorf("approve tuple Action = %q, want %q", tuples[0].Action, ActionApprove)
	}
	if tuples[0].Session != sid {
		t.Errorf("approve tuple Session = %q, want %q", tuples[0].Session, sid)
	}
	want, _ := hex.DecodeString(hash)
	if string(tuples[0].ContentHash) != string(want) {
		t.Errorf("approve tuple ContentHash = %x, want the interaction's own content_hash %x "+
			"(ADR-007 D7 spends the one content slot on the interaction content; IS-APR-2 makes the phone echo it verbatim)",
			tuples[0].ContentHash, want)
	}

	calls := stub.forwarded()
	if len(calls) != 1 {
		t.Fatalf("daemon saw %d approves; want 1", len(calls))
	}
	if calls[0].machine != rep.EndpointID {
		t.Errorf("approve forwarded machine %q, want the endpoint id %q (D7's tuple opens on the machine)", calls[0].machine, rep.EndpointID)
	}
	if calls[0].operationID != "devA:01JAPPROVE00000000000000" {
		t.Errorf("approve forwarded operation_id %q, want the phone's idempotency key", calls[0].operationID)
	}
	if calls[0].req.InteractionID != "itm_01JAPPROVE" || calls[0].req.Decision != "accept" {
		t.Errorf("approve body reached the daemon as %+v; want the interaction id and decision intact", calls[0].req)
	}
	if calls[0].req.ExpiresAt == nil || !calls[0].req.ExpiresAt.Equal(exp) {
		t.Errorf("approve body expires_at = %v, want the daemon-authoritative value echoed verbatim (IS-APR-2)", calls[0].req.ExpiresAt)
	}
}

// TestIsLife4_ApproveRejectedByAuthzAppliesNothing: a forged or expired signature refuses the
// approve not_authorized and the daemon's validation is never reached — an unauthorized
// approve must never become an applied decision.
func TestIsLife4_ApproveRejectedByAuthzAppliesNothing(t *testing.T) {
	stub := &approveStub{stubDaemon: newStubDaemon()}
	stub.authzFn = func(DeviceCommandAuth) error { return errForged }
	sock := serveRemoteAPI(t, stub)
	rc := rawDial(t, sock)
	rep := rc.hello(Version, []string{CapRemoteGateway})
	sid := rep.EndpointID + "/sess1"

	exp := time.Now().Add(time.Minute)
	rc.writeControl(approveFrame(rep.EndpointID, sid, approveHash("x"), &exp))

	got := nextControl(t, rc)
	if got.Op != OpError || got.ErrorCode != CodeNotAuthorized {
		t.Fatalf("rejected approve = op %q code %q; want error/not_authorized", got.Op, got.ErrorCode)
	}
	if n := len(stub.forwarded()); n != 0 {
		t.Fatalf("rejected approve reached the daemon %d times; want 0 (ADR-007 D4)", n)
	}
}

// TestIsLife4_ApproveIsRefusedWhileTheKillSwitchIsOff: the switch is the FIRST gate, ahead of
// the authenticator, exactly as it is for kill and take_control.
func TestIsLife4_ApproveIsRefusedWhileTheKillSwitchIsOff(t *testing.T) {
	stub := &approveStub{stubDaemon: newStubDaemon()}
	sock := serveRemoteAPI(t, approveKillSwitchOff{approveStub: stub})
	rc := rawDial(t, sock)
	rep := rc.hello(Version, []string{CapRemoteGateway})
	sid := rep.EndpointID + "/sess1"

	exp := time.Now().Add(time.Minute)
	rc.writeControl(approveFrame(rep.EndpointID, sid, approveHash("x"), &exp))

	got := nextControl(t, rc)
	if got.Op != OpError || got.ErrorCode != CodeKillSwitch {
		t.Fatalf("approve with the kill switch off = op %q code %q; want error/kill_switch", got.Op, got.ErrorCode)
	}
	if n := len(stub.authorizedTuples()); n != 0 {
		t.Errorf("the authenticator was consulted %d times with the switch off; want 0 (the switch precedes authz)", n)
	}
	if n := len(stub.forwarded()); n != 0 {
		t.Errorf("approve reached the daemon %d times with the switch off; want 0", n)
	}
}

// approveKillSwitchOff is approveStub with the remote-control master switch DISABLED.
type approveKillSwitchOff struct{ *approveStub }

func (approveKillSwitchOff) RemoteControlEnabled() bool { return false }

// TestIsLife4_ApproveWithoutItsBodyIsRefused: the ApproveReq body is the whole of what the
// daemon validates against. A frame that carries none is refused invalid_field and applies
// nothing — never forwarded with a zero body, which would be an approve naming no interaction.
func TestIsLife4_ApproveWithoutItsBodyIsRefused(t *testing.T) {
	stub := &approveStub{stubDaemon: newStubDaemon()}
	sock := serveRemoteAPI(t, stub)
	rc := rawDial(t, sock)
	rep := rc.hello(Version, []string{CapRemoteGateway})
	sid := rep.EndpointID + "/sess1"

	exp := time.Now().Add(time.Minute)
	c := approveFrame(rep.EndpointID, sid, approveHash("x"), &exp)
	c.Approve = nil
	rc.writeControl(c)

	got := nextControl(t, rc)
	if got.Op != OpError || got.ErrorCode != CodeInvalidField {
		t.Fatalf("bodyless approve = op %q code %q; want error/invalid_field", got.Op, got.ErrorCode)
	}
	if n := len(stub.forwarded()); n != 0 {
		t.Fatalf("bodyless approve reached the daemon %d times; want 0", n)
	}
}

// TestIsLife4_ApproveBodyMayNotNameASessionTheSignatureDoesNot: the signature binds Session,
// and the body carries its own copy. If the two may differ, the gateway — which opens the
// sealed frame and is the documented D4/D5 owner-uid residual — can point a signature
// authorized for one session at another. They must be the same session or the frame is refused
// before authorization is even attempted.
func TestIsLife4_ApproveBodyMayNotNameASessionTheSignatureDoesNot(t *testing.T) {
	stub := &approveStub{stubDaemon: newStubDaemon()}
	sock := serveRemoteAPI(t, stub)
	rc := rawDial(t, sock)
	rep := rc.hello(Version, []string{CapRemoteGateway})
	sid := rep.EndpointID + "/sess1"

	exp := time.Now().Add(time.Minute)
	c := approveFrame(rep.EndpointID, sid, approveHash("x"), &exp)
	c.Approve.Session = rep.EndpointID + "/sess2" // a session the signature never covered
	rc.writeControl(c)

	got := nextControl(t, rc)
	if got.Op != OpError || got.ErrorCode != CodeInvalidField {
		t.Fatalf("approve whose body names another session = op %q code %q; want error/invalid_field", got.Op, got.ErrorCode)
	}
	if n := len(stub.authorizedTuples()); n != 0 {
		t.Errorf("the authenticator was consulted %d times for a mismatched body; want 0", n)
	}
	if n := len(stub.forwarded()); n != 0 {
		t.Errorf("a mismatched approve reached the daemon %d times; want 0", n)
	}
}

// TestIsLife4_ApproveWithAnUnhashableContentHashIsRefused: content_hash is the signed tuple's
// content slot, so it must be decodable to the 32 bytes the signature covers. A value that is
// not is refused invalid_field rather than silently signed over an empty hash — SHA256("") is
// a valid 32-byte digest, which is the same trap handleTakeControl's empty-gate-token check
// exists for.
func TestIsLife4_ApproveWithAnUnhashableContentHashIsRefused(t *testing.T) {
	stub := &approveStub{stubDaemon: newStubDaemon()}
	sock := serveRemoteAPI(t, stub)
	rc := rawDial(t, sock)
	rep := rc.hello(Version, []string{CapRemoteGateway})
	sid := rep.EndpointID + "/sess1"

	for _, bad := range []string{"", "not-hex", approveHash("x")[:32]} {
		exp := time.Now().Add(time.Minute)
		c := approveFrame(rep.EndpointID, sid, bad, &exp)
		rc.writeControl(c)
		got := nextControl(t, rc)
		if got.Op != OpError || got.ErrorCode != CodeInvalidField {
			t.Errorf("approve carrying content_hash %q = op %q code %q; want error/invalid_field", bad, got.Op, got.ErrorCode)
		}
	}
	if n := len(stub.forwarded()); n != 0 {
		t.Errorf("an unhashable approve reached the daemon %d times; want 0", n)
	}
}

// TestIsLife4_TheDaemonsRefusalReachesThePhoneWithItsCode: approveInteraction answers a stale
// card with CodeStaleApproval from D10's taxonomy. The phone drives retry policy off the code,
// so a refusal that arrives as prose alone is a refusal the phone cannot classify.
func TestIsLife4_TheDaemonsRefusalReachesThePhoneWithItsCode(t *testing.T) {
	stub := &approveStub{stubDaemon: newStubDaemon()}
	stub.code = CodeStaleApproval
	stub.err = errors.New("no approval is pending for this session")
	sock := serveRemoteAPI(t, stub)
	rc := rawDial(t, sock)
	rep := rc.hello(Version, []string{CapRemoteGateway})
	sid := rep.EndpointID + "/sess1"

	exp := time.Now().Add(time.Minute)
	rc.writeControl(approveFrame(rep.EndpointID, sid, approveHash("x"), &exp))

	got := nextControl(t, rc)
	if got.Op != OpError || got.ErrorCode != CodeStaleApproval {
		t.Fatalf("a stale approve = op %q code %q; want error/stale_approval", got.Op, got.ErrorCode)
	}
	if got.Error == "" {
		t.Errorf("a refused approve carried no prose beside its code; the operator needs both")
	}
}

// TestIsLife4_ApproveIsRefusedByABackendThatCannotAnswerIt: a daemon assembled without the
// interaction producer implements no approver. It must refuse rather than reply OK to a
// decision nothing applied — an approve answered by silence leaves the CLI blocked while the
// card disappears from the phone.
func TestIsLife4_ApproveIsRefusedByABackendThatCannotAnswerIt(t *testing.T) {
	sock := serveRemote(t, newStubDaemon()) // a plain stub: no InteractionApprover
	rc := rawDial(t, sock)
	rep := rc.hello(Version, []string{CapRemoteGateway})
	sid := rep.EndpointID + "/sess1"

	exp := time.Now().Add(time.Minute)
	rc.writeControl(approveFrame(rep.EndpointID, sid, approveHash("x"), &exp))

	got := nextControl(t, rc)
	if got.Op != OpError {
		t.Fatalf("approve on a backend with no approver = op %q; want an error", got.Op)
	}
}
