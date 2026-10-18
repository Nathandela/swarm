package protocol

import (
	"errors"
	"sync/atomic"
	"testing"
)

type nativeDecisionStatusStub struct {
	*nativeDecisionStub
	result atomic.Value
}

func (s *nativeDecisionStatusStub) NativeDecisionStatus(_, id string) (NativeDecisionReceipt, error) {
	value, ok := s.result.Load().(NativeDecisionReceipt)
	if !ok || value.OperationID != id {
		return NativeDecisionReceipt{}, errors.New("missing")
	}
	return value, nil
}

func TestNativeDecisionStatusReadsWithoutAnotherDecisionAndRefusesRemote(t *testing.T) {
	s := &nativeDecisionStatusStub{nativeDecisionStub: &nativeDecisionStub{stubDaemon: newStubDaemon()}}
	c, err := Dial(serveOwnerAPI(t, s), []string{CapNativeRequests})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	body := nativeDecisionBody(c.EndpointID() + "/sess1")
	receipt, err := c.NativeDecide("native-read-status", body)
	if err != nil {
		t.Fatal(err)
	}
	s.result.Store(receipt)
	if status, err := c.NativeDecisionStatus(body.Session, receipt.OperationID); err != nil || status.State != "admitted" || s.calls.Load() != 1 {
		t.Fatalf("status: %+v %v", status, err)
	}
	if _, err := c.NativeDecisionStatus(body.Session, "missing"); err == nil || s.calls.Load() != 1 {
		t.Fatal("missing receipt caused a decision")
	}
	rc := rawDial(t, serveRemoteAPI(t, s))
	hello := rc.hello(Version, []string{CapRemoteGateway, CapNativeRequests})
	rc.writeControl(Control{Op: OpNativeDecisionStatus, EndpointID: hello.EndpointID, SessionID: hello.EndpointID + "/sess1", OperationID: "native-read-status"})
	if response := rc.readControl(); response.Op != OpError || response.ErrorCode != CodeNotAuthorized {
		t.Fatalf("remote status: %+v", response)
	}
}
