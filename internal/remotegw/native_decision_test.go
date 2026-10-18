package remotegw

import (
	"reflect"
	"testing"

	"github.com/Nathandela/swarm/internal/protocol"
)

func TestNativeDecisionRoutesAndForwardsTheSignedBody(t *testing.T) {
	rc := protocol.RemoteCommand{DeviceCommandAuth: protocol.DeviceCommandAuth{Action: protocol.ActionNativeDecide, Session: "m/s", OperationID: "decision-operation", Sig: "unchanged-signature"}, BodyVersion: 1}
	if _, err := opForAction(rc); err == nil {
		t.Fatal("stripped native decision was forwarded")
	}
	rc.NativeDecision = &protocol.NativeDecisionReq{Session: "m/s", RequestID: "que_exact", Kind: "question_reply", Answers: [][]string{{"one", "two"}}}
	op, err := opForAction(rc)
	if err != nil || op != protocol.OpNativeDecide {
		t.Fatalf("native decision route: %q %v", op, err)
	}
	got := forwardControl("m", op, rc)
	if got.OperationID != rc.OperationID || got.DeviceSig != rc.Sig || got.BodyVersion != rc.BodyVersion || !reflect.DeepEqual(got.NativeDecision, rc.NativeDecision) {
		t.Fatal("gateway lost or changed signed native decision contents")
	}
}
