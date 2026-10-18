package protocol

import "testing"

func (s *nativeDecisionStub) NativeRequests(local, requestID, nativeSessionID string) (NativeRequestsReply, error) {
	return NativeRequestsReply{Session: local, Requests: []NativeRequestSummary{{Kind: "permission", RequestID: "per_native", NativeSessionID: "ses_child", Summary: "bash: safe command"}}}, nil
}

func TestNativeRequestReadIsOwnerOnlyAndBoundedSelectorShape(t *testing.T) {
	s := &nativeDecisionStub{stubDaemon: newStubDaemon()}
	c, err := Dial(serveOwnerAPI(t, s), []string{CapNativeRequests})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	req := NativeRequestsReq{Session: c.EndpointID() + "/sess1"}
	result, err := c.NativeRequests(req)
	if err != nil || len(result.Requests) != 1 || result.Requests[0].NativeSessionID != "ses_child" {
		t.Fatalf("read: %+v %v", result, err)
	}
	req.RequestID = "per_native"
	if _, err := c.NativeRequests(req); err == nil {
		t.Fatal("partial request selector accepted")
	}
	rc := rawDial(t, serveRemoteAPI(t, s))
	hello := rc.hello(Version, []string{CapRemoteGateway, CapNativeRequests})
	rc.writeControl(Control{Op: OpNativeRequests, EndpointID: hello.EndpointID, SessionID: hello.EndpointID + "/sess1", NativeRequests: &NativeRequestsReq{Session: hello.EndpointID + "/sess1"}})
	if result := rc.readControl(); result.Op != OpError || result.ErrorCode != CodeNotAuthorized {
		t.Fatalf("remote owner read: %+v", result)
	}
	if s.calls.Load() != 0 {
		t.Fatal("read caused decision effect")
	}
}
