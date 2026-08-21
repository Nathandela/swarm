package protocol

// WAVE R8 / CLOSING ROUND 2 -- T8's KILL/DELETE SEVERANCE, FENCED AT THE CALL SITE
// (closing review, finding 8, second pass).
//
// THE HOLE. Round 4 fenced session-kill and session-delete severance by calling
// `severTerminalControlForSession` DIRECTLY (r8r4_severance_test.go:61) and mutating the
// HELPER'S BODY. That proves the helper severs; it proves nothing about whether any handler
// calls it. Replacing all four production call sites in handleKill/handleDelete with `_ = local`
// left `go test -run TestR8R4Sever ./internal/protocol/` PASS, exit 0 -- a fence that survives
// the removal of the behaviour it fences. The wave raised exactly that class as its own finding 7
// and claimed to have closed it.
//
// WHAT THIS ADDS: the ops driven END TO END over a real remote-tier `ServeRemote` -- hello,
// a signed kill and a signed delete, read to their replies -- with the generation registry read
// on the same Server the handler ran on. Both daemon shapes are covered because handleKill and
// handleDelete each have TWO severance call sites: the IdempotentExecutor branch a remote-tier
// backend with durable operation claims takes, and the plain branch every other backend takes.
//
// THE CONTROL PLANE IS PARKED (ADR-017 amendment C0/C1) and has no production sink, so this
// escape is inert in the shipped product. It is fenced anyway because the ADR's T8 trigger table
// is a claim about this code, and a claim no test can lose is not evidence.

import (
	"testing"
	"time"
)

// r8r5SignedOp is one remote-tier control carrying everything requireRemoteAuthz wants: a
// device id, a signature the stub authenticator accepts, an operation id and an expiry.
func r8r5SignedOp(op, endpoint, session, operationID string) Control {
	exp := time.Now().Add(time.Minute)
	return Control{
		Op: op, EndpointID: endpoint, SessionID: session,
		OperationID: operationID,
		DeviceID:    "devA", DeviceSig: "sig", ExpiresAt: &exp,
	}
}

// TestR8R5Sever_TheKillHandlerSeversOverTheRealServer drives OpKill and OpDelete to their
// replies against a backend that is NOT an IdempotentExecutor -- the plain branch of each
// handler.
func TestR8R5Sever_TheKillHandlerSeversOverTheRealServer(t *testing.T) {
	stub := newStubDaemon()
	sock, srv := serveRemoteAPISrv(t, stub)
	rc := rawDial(t, sock)
	rep := rc.hello(Version, []string{CapRemoteGateway})

	killed := r8r4Gen(t, srv, "sess1", "devA", "inst-1")
	deleted := r8r4Gen(t, srv, "sess2", "devA", "inst-2")
	bystander := r8r4Gen(t, srv, "sess3", "devA", "inst-3")

	rc.writeControl(r8r5SignedOp(OpKill, rep.EndpointID, rep.EndpointID+"/sess1", "devA:01JR5KILL000000000000000"))
	if got := rc.readControl(); got.Op != OpOK {
		t.Fatalf("kill = op %q code %q %q; want ok", got.Op, got.ErrorCode, got.Error)
	}
	rc.writeControl(r8r5SignedOp(OpDelete, rep.EndpointID, rep.EndpointID+"/sess2", "devA:01JR5DEL0000000000000000"))
	if got := rc.readControl(); got.Op != OpOK {
		t.Fatalf("delete = op %q code %q %q; want ok", got.Op, got.ErrorCode, got.Error)
	}

	// The ops really executed, so the assertions below are about a handler that RAN and not
	// about one that refused on its way in.
	if k, d := stub.killedIDs(), stub.deletedIDs(); len(k) != 1 || len(d) != 1 {
		t.Fatalf("the plain branch executed kills %v deletes %v; want exactly one of each", k, d)
	}

	assertSeveredByHandler(t, srv, killed, deleted, bystander)
}

// TestR8R5Sever_TheIdempotentBranchSeversToo covers the OTHER two call sites: a remote-tier
// backend with durable operation claims takes a different path through both handlers, and a
// fence that only drives one branch leaves the other free to lose the call.
func TestR8R5Sever_TheIdempotentBranchSeversToo(t *testing.T) {
	stub := newIdempotentStub()
	sock, srv := serveRemoteAPISrv(t, stub)
	rc := rawDial(t, sock)
	rep := rc.hello(Version, []string{CapRemoteGateway})

	killed := r8r4Gen(t, srv, "sess1", "devA", "inst-1")
	deleted := r8r4Gen(t, srv, "sess2", "devA", "inst-2")
	bystander := r8r4Gen(t, srv, "sess3", "devA", "inst-3")

	rc.writeControl(r8r5SignedOp(OpKill, rep.EndpointID, rep.EndpointID+"/sess1", "devA:01JR5KILLIDEM0000000000"))
	if got := rc.readControl(); got.Op != OpOK {
		t.Fatalf("kill = op %q code %q %q; want ok", got.Op, got.ErrorCode, got.Error)
	}
	rc.writeControl(r8r5SignedOp(OpDelete, rep.EndpointID, rep.EndpointID+"/sess2", "devA:01JR5DELIDEM00000000000"))
	if got := rc.readControl(); got.Op != OpOK {
		t.Fatalf("delete = op %q code %q %q; want ok", got.Op, got.ErrorCode, got.Error)
	}
	if n := len(stub.killedIDs()); n != 1 {
		t.Fatalf("the idempotent branch executed %d kills; want 1 (the test is not driving the branch it names)", n)
	}

	assertSeveredByHandler(t, srv, killed, deleted, bystander)
}

// assertSeveredByHandler reads the generation registry on the Server the handlers ran on.
func assertSeveredByHandler(t *testing.T, srv *Server, killed, deleted, bystander string) {
	t.Helper()
	if _, ok := srv.terminalGenerationByID(killed); ok {
		t.Errorf("ADR-017 T8: the KILL handler replied ok and left the killed session's control " +
			"generation live in the server registry. `Refused on the next frame` is not severance -- " +
			"the phone may send nothing at all, and a generation that outlives its session is one " +
			"the next incarnation under the same id inherits.")
	}
	if _, ok := srv.terminalGenerationByID(deleted); ok {
		t.Errorf("ADR-017 T8: the DELETE handler replied ok and left the deleted session's control " +
			"generation live. The control plane is separate from the lease plane (OPEN-C4), so " +
			"dropping the lease does not drop the generation.")
	}
	if _, ok := srv.terminalGenerationByID(bystander); !ok {
		t.Errorf("killing one session severed an UNRELATED session's generation; severance is per " +
			"SESSION, and a handler that wipes the registry would pass every assertion above.")
	}
}
