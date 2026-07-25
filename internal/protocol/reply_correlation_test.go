package protocol

// FAILING-FIRST (TDD RED, GG-5) tests for the DAEMON half of PB-SYNC-7's reply
// correlation (6.6): replyOK/replyError omit OperationID although Control already
// carries the tag (server.go:2296-2307). The gateway seals the daemon's reply verbatim
// (remotegw.SealControlReply), so an untagged reply reaches the phone untagged, and
// PB-SYNC-2 (repair the command-reply stream "via the durable operation outcome"),
// PB-STATE-1 ("pending idempotent ops and their outcomes"), PB-INPUT-4 (retry keyed on
// the reply's error code) and PB-APP-9 all lose their attribution at the source.
//
// The refusal path matters as much as the success path: a phone with two ops in flight
// that receives one refusal cannot tell which op was refused, so it can neither retry
// the right one nor mark the right one failed.
//
// RED is behavior-only: these tests compile against today's tree and fail on the
// assertion, because the daemon replies with an empty operation_id.

import (
	"testing"
	"time"
)

// TestProtocol_RemoteReplyEchoesOperationID: the OK reply to a signed remote mutating op
// carries back the op's operation_id.
func TestProtocol_RemoteReplyEchoesOperationID(t *testing.T) {
	stub := newStubDaemon()
	sock := serveRemoteAPI(t, stub)
	rc := rawDial(t, sock)
	rep := rc.hello(Version, []string{CapRemoteGateway})
	sid := rep.EndpointID + "/sess1"

	const opID = "devA:01JKILLCORRELATE00000000"
	exp := time.Now().Add(time.Minute)
	rc.writeControl(Control{
		Op: OpKill, EndpointID: rep.EndpointID, SessionID: sid,
		OperationID: opID,
		DeviceID:    "devA", DeviceSig: "sig", ExpiresAt: &exp,
	})
	got := rc.readControl()
	if got.Op != OpOK {
		t.Fatalf("kill reply = op %q code %q; want ok", got.Op, got.ErrorCode)
	}
	if got.OperationID != opID {
		t.Fatalf("kill reply operation_id = %q; want %q (a reply the phone cannot attribute is unusable)", got.OperationID, opID)
	}
}

// TestProtocol_RemoteRefusalEchoesOperationID: a REFUSED remote mutating op is tagged
// too. Here the device fields are absent, so the op is refused invalid_field before it
// reaches the daemon -- the operation_id is on the request Control regardless, so there
// is no excuse for dropping it.
func TestProtocol_RemoteRefusalEchoesOperationID(t *testing.T) {
	stub := newStubDaemon()
	sock := serveRemoteAPI(t, stub)
	rc := rawDial(t, sock)
	rep := rc.hello(Version, []string{CapRemoteGateway})
	sid := rep.EndpointID + "/sess1"

	const opID = "devA:01JKILLREFUSED0000000000"
	rc.writeControl(Control{Op: OpKill, EndpointID: rep.EndpointID, SessionID: sid, OperationID: opID})
	got := rc.readControl()
	if got.Op != OpError || got.ErrorCode != CodeInvalidField {
		t.Fatalf("refusal = op %q code %q; want error/invalid_field", got.Op, got.ErrorCode)
	}
	if got.OperationID != opID {
		t.Fatalf("refusal operation_id = %q; want %q (a refusal must name the op it refused)", got.OperationID, opID)
	}
}
