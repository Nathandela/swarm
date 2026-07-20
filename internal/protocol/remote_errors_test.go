package protocol

// FAILING-FIRST protocol tests for the machine-readable error taxonomy (plan
// R-PROT.7 / amendment D.0-A11) and the remote-mutating-op request_id requirement
// (plan R-IDP.1 / amendment D.0-A4). RED is undefined-only.
//
// FROZEN API these tests expect:
//
//	// A stable machine-readable refusal-reason taxonomy every R-POL/R-KS/R-IDP/R-REL
//	// refusal carries so the phone can drive retry policy (string-only errors cannot).
//	type ErrorCode string
//	const (
//	    CodePolicy        ErrorCode = "policy"
//	    CodeKillSwitch    ErrorCode = "kill_switch"
//	    CodeRateLimit     ErrorCode = "rate_limit"
//	    CodeStaleApproval ErrorCode = "stale_approval"
//	    CodeNotAuthorized ErrorCode = "not_authorized"
//	    CodeInvalidField  ErrorCode = "invalid_field"
//	)
//	func (ErrorCode) Transient() bool // rate_limit is transient; policy/not_authorized/... are permanent
//	// Control gains: ErrorCode ErrorCode `json:"error_code,omitempty"`
//
//	// A remote-tier Server enforces the remote origin (R-GW.8 owns the dedicated
//	// remote.sock; this is the protocol-enforcement seam). Every remote MUTATING op
//	// (interrupt/kill/launch/approve) MUST carry operation_id; input is exempt.
//	func ServeRemote(d DaemonAPI, socketPath string) (*Server, error)

import (
	"testing"
	"time"
)

// serveRemote stands up a remote-tier Server (every connection is remote origin).
func serveRemote(t *testing.T, stub *stubDaemon) string {
	t.Helper()
	sock := tmpSock(t)
	srv, err := ServeRemote(stub, sock)
	if err != nil {
		t.Fatalf("ServeRemote: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	return sock
}

// TestProtocol_ErrorCodeTaxonomy (R-PROT.7): the refusal codes are stable, distinct,
// and classified transient-vs-permanent; a real refusal carries its machine-readable
// code, not only prose.
func TestProtocol_ErrorCodeTaxonomy(t *testing.T) {
	codes := []ErrorCode{
		CodePolicy, CodeKillSwitch, CodeRateLimit,
		CodeStaleApproval, CodeNotAuthorized, CodeInvalidField,
	}
	seen := map[ErrorCode]bool{}
	for _, c := range codes {
		if c == "" {
			t.Fatalf("error code is empty; every refusal reason must be a stable non-empty token")
		}
		if seen[c] {
			t.Fatalf("duplicate error code %q; the taxonomy must be distinct", c)
		}
		seen[c] = true
	}

	// Transient-vs-permanent drives client retry policy.
	if !CodeRateLimit.Transient() {
		t.Fatalf("rate_limit must classify as transient (retryable)")
	}
	if CodeNotAuthorized.Transient() {
		t.Fatalf("not_authorized must classify as permanent (do not retry)")
	}
	if CodeInvalidField.Transient() {
		t.Fatalf("invalid_field must classify as permanent (do not retry)")
	}

	// A real refusal carries the code on the wire (not just Error prose): a remote
	// kill without operation_id is an invalid-field refusal.
	sock := serveRemote(t, newStubDaemon())
	rc := rawDial(t, sock)
	rep := rc.hello(Version, []string{CapRemoteGateway})
	rc.writeControl(Control{Op: OpKill, EndpointID: rep.EndpointID, SessionID: rep.EndpointID + "/sess1"})
	got := rc.readControl()
	if got.Op != OpError {
		t.Fatalf("remote kill without operation_id got op %q; want an error", got.Op)
	}
	if got.ErrorCode != CodeInvalidField {
		t.Fatalf("refusal error_code = %q; want %q (machine-readable, not string-only)", got.ErrorCode, CodeInvalidField)
	}
}

// TestServer_RemoteMutatingOpRequiresRequestID (R-IDP.1): on the remote tier, a
// mutating op lacking operation_id is refused and NOT actioned; the same op WITH a
// operation_id is forwarded to the daemon.
func TestServer_RemoteMutatingOpRequiresRequestID(t *testing.T) {
	stub := newStubDaemon()
	sock := serveRemote(t, stub)
	rc := rawDial(t, sock)
	rep := rc.hello(Version, []string{CapRemoteGateway})
	sid := rep.EndpointID + "/sess1"

	// Without operation_id: refused, and the daemon never saw a kill.
	rc.writeControl(Control{Op: OpKill, EndpointID: rep.EndpointID, SessionID: sid})
	if got := rc.readControl(); got.Op != OpError {
		t.Fatalf("remote kill without operation_id got op %q; want an error refusal", got.Op)
	}
	if n := len(stub.killedIDs()); n != 0 {
		t.Fatalf("daemon executed %d kills for an op lacking operation_id; want 0 (refused before action)", n)
	}

	// With operation_id AND device authorization: forwarded. (R-POL.9 layered a device
	// signature + capability gate on top of the R-IDP.1 operation_id gate; the stub
	// daemon's authenticator accepts by default, so a well-formed authorized op with all
	// the identity fields present is forwarded. The operation_id-gate behavior asserted
	// above is unchanged — a missing operation_id still fails first, with invalid_field.)
	exp := time.Now().Add(time.Minute)
	rc.writeControl(Control{
		Op: OpKill, EndpointID: rep.EndpointID, SessionID: sid,
		OperationID: "devA:01JKILLOK000000000000000",
		DeviceID:    "devA", DeviceSig: "sig", ExpiresAt: &exp,
	})
	if got := rc.readControl(); got.Op == OpError {
		t.Fatalf("remote kill WITH operation_id and device auth was refused: %q / %q", got.Error, got.ErrorCode)
	}
	if n := len(stub.killedIDs()); n != 1 {
		t.Fatalf("daemon executed %d kills for a well-formed remote op; want 1", n)
	}
}
